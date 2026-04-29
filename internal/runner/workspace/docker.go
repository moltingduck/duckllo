package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DockerExecutor runs tools inside a per-run Docker container. The runner
// shells out to the `docker` CLI rather than depending on the Docker
// Engine SDK — keeps the dependency tree small and works against any
// daemon (Docker Desktop / OrbStack / Podman docker-compat) without a
// runtime version check.
//
// Lifecycle: Provision → many Exec/ReadFile/WriteFile → Close.
// Provision pulls the image (docker run handles that), starts a long-
// lived container running `sleep infinity`, and remembers its ID. Tools
// run via `docker exec`. Files move via `docker exec ... cat` / piped
// stdin to avoid the tar-stream complexity of `docker cp`.
type DockerExecutor struct {
	Image          string
	ContainerName  string  // duckllo-<short-run-id> by convention
	Root           string  // workspace path *inside* the container; default /workspace
	Env            []string
	ExtraDockerArgs []string // e.g. --network duckllo-spec-<id>, mounts, etc.
	ExecTimeout    time.Duration

	containerID string
}

// NewDocker is the constructor. Callers fill the fields and call
// Provision; ID is recorded internally.
func NewDocker(image, name string, env, extra []string) *DockerExecutor {
	root := "/workspace"
	return &DockerExecutor{
		Image:           image,
		ContainerName:   name,
		Root:            root,
		Env:             env,
		ExtraDockerArgs: extra,
		ExecTimeout:     5 * time.Minute,
	}
}

func (d *DockerExecutor) Kind() string        { return "docker" }
func (d *DockerExecutor) ID() string          { return d.containerID }
func (d *DockerExecutor) WorkspacePath() string { return d.Root }

// Provision creates and starts the container. Idempotent: if a container
// of the same name already exists and is running, we adopt it (the runner
// may have restarted mid-run); if it exists but is stopped, we start it.
func (d *DockerExecutor) Provision(ctx context.Context) error {
	if d.Image == "" {
		return errors.New("docker workspace: image not set")
	}
	if d.ContainerName == "" {
		return errors.New("docker workspace: container name not set")
	}

	// Try to adopt an existing container by name.
	id, err := d.lookupByName(ctx)
	if err != nil {
		return err
	}
	if id != "" {
		state, err := d.inspectState(ctx, id)
		if err != nil {
			return err
		}
		d.containerID = id
		if state != "running" {
			if _, err := d.docker(ctx, "start", id); err != nil {
				return fmt.Errorf("start existing container: %w", err)
			}
		}
		return nil
	}

	args := []string{
		"run", "-d",
		"--name", d.ContainerName,
		"--label", "duckllo.workspace=true",
		"--workdir", d.Root,
	}
	for _, e := range d.Env {
		args = append(args, "-e", e)
	}
	args = append(args, d.ExtraDockerArgs...)
	args = append(args, d.Image, "sh", "-c", fmt.Sprintf("mkdir -p %s && exec sleep infinity", d.Root))

	out, err := d.docker(ctx, args...)
	if err != nil {
		return fmt.Errorf("docker run: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	d.containerID = strings.TrimSpace(string(out))
	return nil
}

// lookupByName returns "" if no container with the given name exists.
func (d *DockerExecutor) lookupByName(ctx context.Context) (string, error) {
	out, err := d.docker(ctx, "ps", "-a", "--filter", "name=^/"+d.ContainerName+"$", "--format", "{{.ID}}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (d *DockerExecutor) inspectState(ctx context.Context, id string) (string, error) {
	out, err := d.docker(ctx, "inspect", "-f", "{{.State.Status}}", id)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (d *DockerExecutor) Close(ctx context.Context) error {
	if d.containerID == "" {
		return nil
	}
	_, err := d.docker(ctx, "rm", "-f", d.containerID)
	d.containerID = ""
	return err
}

// safeRel verifies the requested rel doesn't escape the workspace root.
func (d *DockerExecutor) safeRel(rel string) (string, error) {
	if rel == "" || rel == "." {
		return d.Root, nil
	}
	clean := strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") {
		return "", ErrPathEscapesWorkspace
	}
	return d.Root + "/" + clean, nil
}

func (d *DockerExecutor) ReadFile(ctx context.Context, rel string) ([]byte, error) {
	if d.containerID == "" {
		return nil, errors.New("docker workspace: not provisioned")
	}
	abs, err := d.safeRel(rel)
	if err != nil {
		return nil, err
	}
	out, err := d.docker(ctx, "exec", d.containerID, "cat", abs)
	if err != nil {
		return out, err
	}
	if len(out) > MaxOutputBytes {
		return append(out[:MaxOutputBytes], []byte("\n[truncated]")...), nil
	}
	return out, nil
}

func (d *DockerExecutor) WriteFile(ctx context.Context, rel string, body []byte) error {
	if d.containerID == "" {
		return errors.New("docker workspace: not provisioned")
	}
	abs, err := d.safeRel(rel)
	if err != nil {
		return err
	}
	dir := abs
	if i := strings.LastIndex(abs, "/"); i > 0 {
		dir = abs[:i]
	}
	cctx, cancel := context.WithTimeout(ctx, d.ExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", "exec", "-i", d.containerID,
		"sh", "-c", fmt.Sprintf("mkdir -p %q && cat > %q", dir, abs))
	cmd.Stdin = bytes.NewReader(body)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker exec write: %w (%s)", err, strings.TrimSpace(buf.String()))
	}
	return nil
}

func (d *DockerExecutor) ListDir(ctx context.Context, rel string) ([]string, error) {
	if d.containerID == "" {
		return nil, errors.New("docker workspace: not provisioned")
	}
	abs, err := d.safeRel(rel)
	if err != nil {
		return nil, err
	}
	out, err := d.docker(ctx, "exec", d.containerID, "sh", "-c",
		fmt.Sprintf("ls -p %q 2>/dev/null", abs))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}, nil
	}
	return lines, nil
}

func (d *DockerExecutor) Exec(ctx context.Context, argv []string) ([]byte, error) {
	if d.containerID == "" {
		return nil, errors.New("docker workspace: not provisioned")
	}
	if len(argv) == 0 {
		return nil, errors.New("argv required")
	}
	args := []string{"exec", "-w", d.Root, d.containerID}
	args = append(args, argv...)

	cctx, cancel := context.WithTimeout(ctx, d.ExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.Bytes()
	if len(out) > MaxOutputBytes {
		out = append(out[:MaxOutputBytes], []byte("\n[truncated]")...)
	}
	if cctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("timed out after %s", d.ExecTimeout)
	}
	return out, err
}

// docker is the workhorse for short, fully-buffered docker CLI calls.
// On non-zero exit it returns stderr as the error message *and* whatever
// hit stdout — some docker subcommands print error context to stdout.
func (d *DockerExecutor) docker(ctx context.Context, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return stdout.Bytes(), fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.Bytes(), nil
}
