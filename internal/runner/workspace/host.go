package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HostExecutor runs tools directly on the runner's host filesystem. This
// is Phase 1 default behaviour. Phase 2 will swap to DockerExecutor when
// DUCKLLO_CONTAINER_IMAGE is configured.
type HostExecutor struct {
	Root            string
	AllowedCommands map[string]bool
	ExecTimeout     time.Duration
}

func NewHost(root string) *HostExecutor {
	return &HostExecutor{
		Root: root,
		AllowedCommands: map[string]bool{
			"go": true, "gofmt": true, "go-vet": true, "golangci-lint": true,
			"git": true, "ls": true, "cat": true, "grep": true, "head": true, "tail": true,
			"node": true, "npm": true, "npx": true, "python": true, "python3": true,
			"make": true, "test": true, "echo": true, "pwd": true,
		},
		ExecTimeout: 2 * time.Minute,
	}
}

func (h *HostExecutor) Kind() string                     { return "host" }
func (h *HostExecutor) Close(_ context.Context) error    { return nil }

func (h *HostExecutor) safe(rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", ErrPathEscapesWorkspace
	}
	return filepath.Join(h.Root, clean), nil
}

func (h *HostExecutor) ReadFile(_ context.Context, rel string) ([]byte, error) {
	abs, err := h.safe(rel)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxOutputBytes {
		return append(body[:MaxOutputBytes], []byte("\n[truncated]")...), nil
	}
	return body, nil
}

func (h *HostExecutor) WriteFile(_ context.Context, rel string, body []byte) error {
	abs, err := h.safe(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, body, 0o644)
}

func (h *HostExecutor) ListDir(_ context.Context, rel string) ([]string, error) {
	abs, err := h.safe(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func (h *HostExecutor) Exec(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("argv required")
	}
	if !h.AllowedCommands[argv[0]] {
		return nil, fmt.Errorf("command %q not on allow-list", argv[0])
	}
	cctx, cancel := context.WithTimeout(ctx, h.ExecTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = h.Root
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()

	out := buf.Bytes()
	if len(out) > MaxOutputBytes {
		out = append(out[:MaxOutputBytes], []byte("\n[truncated]")...)
	}
	if cctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("timed out after %s", h.ExecTimeout)
	}
	return out, err
}
