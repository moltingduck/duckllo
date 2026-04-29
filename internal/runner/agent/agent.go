// Package agent owns the model-provider boundary the runner reasons over.
// Switching providers (OpenAI / local / etc) is a matter of adding another
// implementation; the orchestrator only sees Provider.
package agent

import "context"

// Message is the simplified chat-message shape duckllo runners exchange
// with a provider. Tool calls and tool results are first-class because the
// PEVC executor needs them on every turn.
type Message struct {
	Role    string       `json:"role"`    // user | assistant | tool
	Content string       `json:"content,omitempty"`
	Tools   []ToolCall   `json:"tools,omitempty"`    // when role=assistant
	ToolID  string       `json:"tool_id,omitempty"`  // when role=tool
}

type ToolCall struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type Request struct {
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
}

type Response struct {
	Text             string
	ToolCalls        []ToolCall
	StopReason       string
	PromptTokens     int
	CompletionTokens int
	Model            string
}

type Provider interface {
	Name() string                    // "anthropic"
	DefaultModel() string            // e.g. "claude-sonnet-4-6"
	Complete(ctx context.Context, req Request) (*Response, error)
}
