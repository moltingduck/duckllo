// Package workspace abstracts the place where executor tools run. Phase 1
// runs them on the host filesystem (HostExecutor). Phase 2 runs them inside
// a per-run Docker container (DockerExecutor). Same interface either way,
// so the orchestrator and tool registry don't care which.
package workspace

import (
	"context"
	"errors"
)

// Executor is the small set of file + exec primitives the executor agent
// reaches for. Implementations enforce path containment on their side
// (host: chroot-relative; docker: container scope).
type Executor interface {
	// Kind reports "host" or "docker". Surfaced in workspace_meta for
	// observability.
	Kind() string

	// ReadFile returns up to MaxBytes from the file at rel.
	ReadFile(ctx context.Context, rel string) ([]byte, error)

	// WriteFile creates parent directories as needed and replaces the
	// file at rel.
	WriteFile(ctx context.Context, rel string, body []byte) error

	// ListDir returns immediate children of rel; directories are
	// suffixed with '/'.
	ListDir(ctx context.Context, rel string) ([]string, error)

	// Exec runs argv with cwd = workspace root, captures combined
	// stdout+stderr, enforces a timeout. Output may be truncated; the
	// caller decides what to feed back to the model.
	Exec(ctx context.Context, argv []string) (output []byte, exitErr error)

	// Close releases any resources (host: no-op; docker: stop+remove
	// container). Idempotent.
	Close(ctx context.Context) error
}

// Meta is what the runner posts to the server's /runs/{rid}/workspace
// endpoint after provisioning. Server stores it as runs.workspace_meta.
type Meta struct {
	Kind          string `json:"kind"`             // host | docker
	ContainerID   string `json:"container_id,omitempty"`
	NetworkID     string `json:"network_id,omitempty"`
	WorktreePath  string `json:"worktree_path,omitempty"`
	DevURL        string `json:"dev_url,omitempty"`
	TailscaleNode string `json:"tailscale_node,omitempty"`
}

// MaxOutputBytes is the truncation cap shared by every executor — the
// orchestrator relies on this to bound prompt growth.
const MaxOutputBytes = 256 * 1024

// ErrPathEscapesWorkspace is returned when a callsite passes a path that
// resolves outside the workspace root.
var ErrPathEscapesWorkspace = errors.New("path escapes workspace")
