package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Anthropic implements Provider against the official Messages API. We talk
// to the HTTP endpoint directly rather than depending on the Go SDK so we
// don't have to track its API churn — the wire format is stable and
// versioned via the anthropic-version header.
type Anthropic struct {
	APIKey  string
	Model   string
	BaseURL string
	HTTP    *http.Client
}

const (
	defaultAnthropicModel = "claude-sonnet-4-6"
	anthropicVersion      = "2023-06-01"
)

func NewAnthropic(apiKey, model string) *Anthropic {
	if model == "" {
		model = defaultAnthropicModel
	}
	return &Anthropic{
		APIKey: apiKey, Model: model,
		BaseURL: "https://api.anthropic.com",
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
	}
}

func (a *Anthropic) Name() string         { return "anthropic" }
func (a *Anthropic) DefaultModel() string { return a.Model }

// wire types matching POST /v1/messages.

type anthropicReq struct {
	Model     string                 `json:"model"`
	MaxTokens int                    `json:"max_tokens"`
	System    string                 `json:"system,omitempty"`
	Messages  []anthropicMessage     `json:"messages"`
	Tools     []anthropicTool        `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   any            `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicResp struct {
	StopReason string             `json:"stop_reason"`
	Content    []anthropicContent `json:"content"`
	Model      string             `json:"model"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (a *Anthropic) Complete(ctx context.Context, req Request) (*Response, error) {
	if a.APIKey == "" {
		return nil, errors.New("anthropic: ANTHROPIC_API_KEY not set")
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	body := anthropicReq{
		Model:     a.Model,
		MaxTokens: maxTokens,
		System:    req.System,
		Messages:  toAnthropicMessages(req.Messages),
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthropicTool{
			Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
		})
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.BaseURL+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := a.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("anthropic %d: %s", res.StatusCode, string(rb))
	}

	var aResp anthropicResp
	if err := json.Unmarshal(rb, &aResp); err != nil {
		return nil, err
	}
	out := &Response{
		StopReason:       aResp.StopReason,
		Model:            aResp.Model,
		PromptTokens:     aResp.Usage.InputTokens,
		CompletionTokens: aResp.Usage.OutputTokens,
	}
	for _, c := range aResp.Content {
		switch c.Type {
		case "text":
			out.Text += c.Text
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID: c.ID, Name: c.Name, Input: c.Input,
			})
		}
	}
	return out, nil
}

func toAnthropicMessages(msgs []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		am := anthropicMessage{Role: m.Role}
		switch m.Role {
		case "tool":
			// Tool result must be wrapped as a user-role tool_result.
			am.Role = "user"
			am.Content = []anthropicContent{{
				Type: "tool_result", ToolUseID: m.ToolID, Content: m.Content,
			}}
		case "assistant":
			if m.Content != "" {
				am.Content = append(am.Content, anthropicContent{Type: "text", Text: m.Content})
			}
			for _, tc := range m.Tools {
				am.Content = append(am.Content, anthropicContent{
					Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: tc.Input,
				})
			}
		default:
			am.Content = []anthropicContent{{Type: "text", Text: m.Content}}
		}
		out = append(out, am)
	}
	return out
}
