package workspace

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// requireDocker skips the test unless `docker version` returns cleanly
// in a short window. We don't gate on platform — Mac (OrbStack), Linux,
// and Windows all expose the docker CLI.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not in PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "version").Run(); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
}

// uniqueName returns a docker-safe name unlikely to collide with other
// running tests. The DockerExecutor's Provision is idempotent on name
// collision, but we still want clean isolation.
func uniqueName(prefix string) string {
	return prefix + "-" + time.Now().Format("20060102T150405.000")
}

func TestDockerExecutor_LifecycleAgainstRealDaemon(t *testing.T) {
	requireDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	name := uniqueName("duckllo-test")
	d := NewDocker("alpine:latest", name, nil, nil)
	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close(context.Background())
	})

	if d.ID() == "" {
		t.Fatal("ID() empty after Provision")
	}

	// Write + read round-trip inside the container.
	if err := d.WriteFile(ctx, "hello.txt", []byte("greetings")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := d.ReadFile(ctx, "hello.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "greetings" {
		t.Errorf("ReadFile: got %q want greetings", got)
	}

	// ListDir surfaces the file we just wrote.
	entries, err := d.ListDir(ctx, ".")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	found := false
	for _, e := range entries {
		if e == "hello.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListDir didn't surface hello.txt: %v", entries)
	}

	// Exec produces stdout that includes the file we wrote.
	out, err := d.Exec(ctx, []string{"ls", "-1"})
	if err != nil {
		t.Fatalf("Exec ls: %v", err)
	}
	if !strings.Contains(string(out), "hello.txt") {
		t.Errorf("ls output: %q", out)
	}

	// Path containment still holds against the docker exec shell.
	if _, err := d.ReadFile(ctx, "../../../etc/hostname"); err == nil {
		t.Error("ReadFile should refuse to escape the workspace root")
	}

	// Idempotent Provision adopts the existing container.
	d2 := NewDocker("alpine:latest", name, nil, nil)
	if err := d2.Provision(ctx); err != nil {
		t.Fatalf("Provision (adopt): %v", err)
	}
	if d2.ID() != d.ID() {
		t.Errorf("adopt: got id %s want %s", d2.ID(), d.ID())
	}

	if err := d.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if d.ID() != "" {
		t.Error("ID should be cleared after Close")
	}

	// After teardown, lookup-by-name returns empty.
	d3 := NewDocker("alpine:latest", name, nil, nil)
	id, err := d3.lookupByName(ctx)
	if err != nil {
		t.Fatalf("lookupByName: %v", err)
	}
	if id != "" {
		t.Errorf("lookupByName after Close: got %s want empty", id)
	}
}

func TestDockerExecutor_RejectsMissingImage(t *testing.T) {
	d := NewDocker("", "ignored", nil, nil)
	if err := d.Provision(context.Background()); err == nil {
		t.Fatal("Provision should reject empty image")
	}
}

func TestDockerExecutor_RejectsCallsBeforeProvision(t *testing.T) {
	d := NewDocker("alpine:latest", "ignored", nil, nil)
	ctx := context.Background()
	if _, err := d.ReadFile(ctx, "x"); err == nil {
		t.Error("ReadFile should fail before Provision")
	}
	if err := d.WriteFile(ctx, "x", []byte("y")); err == nil {
		t.Error("WriteFile should fail before Provision")
	}
	if _, err := d.Exec(ctx, []string{"ls"}); err == nil {
		t.Error("Exec should fail before Provision")
	}
}
