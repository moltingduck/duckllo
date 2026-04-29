package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ClaudeCode shells out to the `claude` CLI on the local host instead
// of calling a model HTTP API. Unlike the API providers, Claude Code
// has its own tool harness — it does file ops, git, web fetch, etc.
// on its own. From duckllo's perspective each phase completes in a
// single turn: we hand `claude` a prompt and read back text.
//
// This is the right driver when the duckllo client and an interactive
// developer share a host: the spec arrives at the server, the client
// turns it into a prompt for Claude Code, Claude Code does the work
// inside the developer's checkout, the result lands back as an
// iteration. The runner's tool whitelist is bypassed because Claude
// Code is its own sandbox — only use this driver in trusted local
// environments.
type ClaudeCode struct {
	Binary  string        // default "claude"
	Args    []string      // default ["-p"]; override for testing or alt CLIs
	Model   string        // optional; threaded as --model
	Cwd     string        // working directory the CLI runs in
	Timeout time.Duration // hard cap so a wedged subprocess can't hang the runner
}

func NewClaudeCode(binary, model, cwd string) *ClaudeCode {
	if binary == "" {
		binary = "claude"
	}
	return &ClaudeCode{
		Binary: binary, Args: []string{"-p"},
		Model: model, Cwd: cwd,
		Timeout: 30 * time.Minute,
	}
}

func (c *ClaudeCode) Name() string         { return "claude-code" }
func (c *ClaudeCode) DefaultModel() string { return c.Model }

func (c *ClaudeCode) Complete(ctx context.Context, req Request) (*Response, error) {
	// Fold the messages into a single transcript string. Claude Code's
	// `-p` flag accepts the prompt either via argv or stdin; we use
	// stdin so we don't have to worry about argv length limits or
	// shell escaping when prompts get large.
	prompt := flattenPrompt(req)

	args := append([]string{}, c.Args...)
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}

	cctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, c.Binary, args...)
	cmd.Stdin = bytes.NewReader([]byte(prompt))
	if c.Cwd != "" {
		cmd.Dir = c.Cwd
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("claude-code: timed out after %s", c.Timeout)
	}
	if err != nil {
		errCtx := strings.TrimSpace(stderr.String())
		if errCtx == "" {
			errCtx = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("claude-code (%s): %w: %s", c.Binary, err, errCtx)
	}

	return &Response{
		Text:       stdout.String(),
		Model:      c.Model,
		StopReason: "end_turn",
		// Token counts aren't surfaced by the CLI in -p mode; leave 0.
	}, nil
}

// flattenPrompt is the wire format Claude Code consumes on stdin. We
// assemble a clearly-marked transcript: each message gets a heading,
// system goes first, tool results are embedded as the previous turn's
// trailer. Claude Code's own context-window management takes it from
// there.
func flattenPrompt(req Request) string {
	var b strings.Builder
	if req.System != "" {
		b.WriteString("# System\n")
		b.WriteString(req.System)
		b.WriteString("\n\n")
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "tool":
			b.WriteString("# Tool result (")
			b.WriteString(m.ToolID)
			b.WriteString(")\n")
			b.WriteString(m.Content)
			b.WriteString("\n\n")
		default:
			b.WriteString("# ")
			b.WriteString(m.Role)
			b.WriteString("\n")
			if m.Content != "" {
				b.WriteString(m.Content)
				b.WriteString("\n")
			}
			for _, tc := range m.Tools {
				fmt.Fprintf(&b, "[tool_call %s %s %v]\n", tc.ID, tc.Name, tc.Input)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// keep error import referenced even on the Go versions where some
// helpers above don't use errors directly.
var _ = errors.New
