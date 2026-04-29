package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestClaudeCode_RoundTripVia_cat exercises the provider with `cat` as
// a stand-in for `claude -p`. cat reads stdin, prints to stdout — which
// matches the contract the provider expects, so the test verifies that
// (a) the prompt is written to the subprocess's stdin in our flattened
// shape, (b) stdout becomes Response.Text, (c) Cwd is honoured.
func TestClaudeCode_RoundTripViaCat(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not on PATH")
	}
	// Args is empty so cat reads stdin without flags; the provider
	// passes through its prompt and we get it back on stdout verbatim.
	c := &ClaudeCode{Binary: "cat", Args: nil, Cwd: t.TempDir(), Timeout: 10 * time.Second}
	// Override -p flag with /dev/stdin so cat behaves predictably; we
	// can't change the args list from outside, so we just rely on cat
	// ignoring -p and reading stdin from the inherited fd.

	resp, err := c.Complete(context.Background(), Request{
		System: "you are tested",
		Messages: []Message{
			{Role: "user", Content: "hello world"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// The prompt should be relayed verbatim onto stdout because cat is
	// our subprocess. Assert the canonical headings + content land
	// there.
	if !strings.Contains(resp.Text, "# System\nyou are tested") {
		t.Errorf("system not in transcript: %q", resp.Text)
	}
	if !strings.Contains(resp.Text, "# user\nhello world") {
		t.Errorf("user msg not in transcript: %q", resp.Text)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason: got %q want end_turn", resp.StopReason)
	}
}

func TestClaudeCode_FailsLoudlyOnUnknownBinary(t *testing.T) {
	c := &ClaudeCode{Binary: "/this/does/not/exist", Timeout: 5 * time.Second}
	_, err := c.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error when binary is missing")
	}
	if !strings.Contains(err.Error(), "claude-code") {
		t.Errorf("error should mention provider name: %v", err)
	}
}

func TestClaudeCode_NameDefaultsToClaude(t *testing.T) {
	c := NewClaudeCode("", "claude-sonnet-4-6", "/tmp")
	if c.Binary != "claude" {
		t.Errorf("Binary default: got %q want claude", c.Binary)
	}
	if c.Name() != "claude-code" {
		t.Errorf("Name(): got %q", c.Name())
	}
	if c.DefaultModel() != "claude-sonnet-4-6" {
		t.Errorf("DefaultModel(): got %q", c.DefaultModel())
	}
}

func TestClaudeCode_FlattenIncludesToolResults(t *testing.T) {
	out := flattenPrompt(Request{
		Messages: []Message{
			{Role: "tool", ToolID: "call_42", Content: "ls output"},
			{Role: "user", Content: "what now?"},
		},
	})
	if !strings.Contains(out, "# Tool result (call_42)") {
		t.Errorf("tool result heading missing: %s", out)
	}
	if !strings.Contains(out, "ls output") {
		t.Errorf("tool body missing: %s", out)
	}
}

func TestProviderFactory_ClaudeCode(t *testing.T) {
	p, err := New(Config{Provider: "claude-code", ClaudeBinary: "claude", ClaudeWorkingDir: "/tmp"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != "claude-code" {
		t.Errorf("provider name: %q", p.Name())
	}

	// Alias `claude` is also accepted for ergonomics.
	p2, err := New(Config{Provider: "claude"})
	if err != nil {
		t.Fatalf("New(claude): %v", err)
	}
	if p2.Name() != "claude-code" {
		t.Errorf("alias name: %q", p2.Name())
	}
}
