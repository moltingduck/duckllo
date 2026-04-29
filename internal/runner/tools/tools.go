// Package tools is the small whitelist of tools the executor agent can
// call: read_file, write_file, list_dir, exec. The actual file IO and
// process execution are delegated to a workspace.Executor — Phase 1 uses
// HostExecutor; Phase 2 swaps to DockerExecutor — so the tool definitions
// stay identical regardless of where work runs.
package tools

import (
	"context"
	"fmt"
	"os"

	"github.com/moltingduck/duckllo/internal/runner/agent"
	"github.com/moltingduck/duckllo/internal/runner/workspace"
)

type Sandbox struct {
	Workspace workspace.Executor
}

// NewSandbox is kept for compatibility — it returns a host-backed sandbox
// rooted at `root`. Callers that need a Docker workspace should call
// NewSandboxWith(executor) directly.
func NewSandbox(root string) *Sandbox {
	return &Sandbox{Workspace: workspace.NewHost(root)}
}

func NewSandboxWith(exec workspace.Executor) *Sandbox {
	return &Sandbox{Workspace: exec}
}

// Defs returns the JSON-Schema tool definitions the executor agent sees.
// Identical regardless of where the workspace runs.
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
			Description: "Run a whitelisted command in the workspace. argv[0] must be on the host's exec allow-list (host workspace) or the container's PATH (docker workspace).",
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

// Execute runs the requested tool through the workspace. Errors are
// returned as strings (not Go errors) so the runner can feed them back to
// the model as a tool_result without abandoning the loop.
func (s *Sandbox) Execute(ctx context.Context, call agent.ToolCall) string {
	switch call.Name {
	case "read_file":
		path, _ := call.Input["path"].(string)
		body, err := s.Workspace.ReadFile(ctx, path)
		if err != nil {
			return "error: " + err.Error()
		}
		return string(body)
	case "write_file":
		path, _ := call.Input["path"].(string)
		content, _ := call.Input["content"].(string)
		if err := s.Workspace.WriteFile(ctx, path, []byte(content)); err != nil {
			return "error: " + err.Error()
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(content), path)
	case "list_dir":
		path, _ := call.Input["path"].(string)
		entries, err := s.Workspace.ListDir(ctx, path)
		if err != nil {
			return "error: " + err.Error()
		}
		out := ""
		for _, e := range entries {
			out += e + "\n"
		}
		if len(out) > 0 {
			out = out[:len(out)-1]
		}
		return out
	case "exec":
		argvAny, _ := call.Input["argv"].([]any)
		argv := make([]string, 0, len(argvAny))
		for _, v := range argvAny {
			if s, ok := v.(string); ok {
				argv = append(argv, s)
			}
		}
		out, err := s.Workspace.Exec(ctx, argv)
		if err != nil {
			return fmt.Sprintf("%s\nerror: %s", out, err.Error())
		}
		return string(out)
	default:
		return fmt.Sprintf("error: unknown tool %q", call.Name)
	}
}

// EnsureRoot used to make a directory; still needed by main.go for the
// host workspace fallback.
func EnsureRoot(root string) error {
	return os.MkdirAll(root, 0o755)
}
