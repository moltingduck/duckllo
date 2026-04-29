// Package tools implements the small whitelist of tools the executor agent
// can call: read_file, write_file, list_dir, exec. Phase 1 runs these
// against the runner's working directory on the host. Phase 2 will do the
// same calls inside a per-spec Docker container.
//
// The exec tool only allows commands whose argv[0] is on a small allow-list,
// preventing a confused agent from running rm -rf or curl piping a script.
package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moltingduck/duckllo/internal/runner/agent"
)

type Sandbox struct {
	Root             string        // working directory for this run
	AllowedCommands  map[string]bool
	MaxOutputBytes   int
	ExecTimeout      time.Duration
}

func NewSandbox(root string) *Sandbox {
	return &Sandbox{
		Root: root,
		AllowedCommands: map[string]bool{
			"go": true, "gofmt": true, "go-vet": true, "golangci-lint": true,
			"git": true, "ls": true, "cat": true, "grep": true, "head": true, "tail": true,
			"node": true, "npm": true, "npx": true, "python": true, "python3": true,
			"make": true, "test": true, "echo": true, "pwd": true,
		},
		MaxOutputBytes: 256 * 1024,
		ExecTimeout:    2 * time.Minute,
	}
}

// Defs returns the JSON-Schema tool definitions the executor agent sees.
func (s *Sandbox) Defs() []agent.ToolDef {
	return []agent.ToolDef{
		{
			Name:        "read_file",
			Description: "Read a UTF-8 file from the workspace. Path is relative to the workspace root.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "write_file",
			Description: "Write a UTF-8 file in the workspace, creating parent directories as needed. Overwrites existing files.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "list_dir",
			Description: "List entries of a directory in the workspace.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name:        "exec",
			Description: "Run a whitelisted command in the workspace. argv[0] must be one of: go, gofmt, golangci-lint, git, ls, cat, grep, head, tail, node, npm, npx, python, python3, make.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"argv": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"argv"},
			},
		},
	}
}

// Execute runs the tool the model requested and returns a stringified
// result. Errors are returned as strings (not Go errors) so the runner can
// feed them back to the model as a tool_result without abandoning the loop.
func (s *Sandbox) Execute(ctx context.Context, call agent.ToolCall) string {
	switch call.Name {
	case "read_file":
		return s.readFile(call.Input)
	case "write_file":
		return s.writeFile(call.Input)
	case "list_dir":
		return s.listDir(call.Input)
	case "exec":
		return s.exec(ctx, call.Input)
	default:
		return fmt.Sprintf("error: unknown tool %q", call.Name)
	}
}

func (s *Sandbox) safe(rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", errors.New("path escapes workspace")
	}
	return filepath.Join(s.Root, clean), nil
}

func (s *Sandbox) readFile(in map[string]any) string {
	p, _ := in["path"].(string)
	abs, err := s.safe(p)
	if err != nil {
		return "error: " + err.Error()
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(body) > s.MaxOutputBytes {
		return string(body[:s.MaxOutputBytes]) + "\n[truncated]"
	}
	return string(body)
}

func (s *Sandbox) writeFile(in map[string]any) string {
	p, _ := in["path"].(string)
	content, _ := in["content"].(string)
	abs, err := s.safe(p)
	if err != nil {
		return "error: " + err.Error()
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "error: " + err.Error()
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "error: " + err.Error()
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), p)
}

func (s *Sandbox) listDir(in map[string]any) string {
	p, _ := in["path"].(string)
	abs, err := s.safe(p)
	if err != nil {
		return "error: " + err.Error()
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "error: " + err.Error()
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "\n")
}

func (s *Sandbox) exec(ctx context.Context, in map[string]any) string {
	argvAny, _ := in["argv"].([]any)
	if len(argvAny) == 0 {
		return "error: argv required"
	}
	argv := make([]string, len(argvAny))
	for i, v := range argvAny {
		str, ok := v.(string)
		if !ok {
			return "error: argv must be strings"
		}
		argv[i] = str
	}
	cmd0 := argv[0]
	if !s.AllowedCommands[cmd0] {
		return fmt.Sprintf("error: command %q not on allow-list", cmd0)
	}

	cctx, cancel := context.WithTimeout(ctx, s.ExecTimeout)
	defer cancel()
	c := exec.CommandContext(cctx, argv[0], argv[1:]...)
	c.Dir = s.Root

	out, err := c.CombinedOutput()
	if len(out) > s.MaxOutputBytes {
		out = append(out[:s.MaxOutputBytes], []byte("\n[truncated]")...)
	}
	if cctx.Err() == context.DeadlineExceeded {
		return string(out) + "\nerror: timed out after " + s.ExecTimeout.String()
	}
	if err != nil {
		return fmt.Sprintf("%s\nerror: %s", out, err.Error())
	}
	return string(out)
}

// EnsureRoot makes sure the workspace directory exists.
func EnsureRoot(root string) error {
	return os.MkdirAll(root, 0o755)
}

// rooted reports whether p is within root (after Clean). Used by tests.
func rooted(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..")
}

var _ = fs.ErrNotExist
var _ = rooted
