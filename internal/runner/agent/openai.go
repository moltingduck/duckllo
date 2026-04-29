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

// OpenAI implements Provider against /v1/chat/completions. The chat-
// completions schema (rather than the newer Responses API) is the most
// stable surface for tool use across the GPT-4 family and gives us a
// shape close enough to Anthropic's that the orchestrator code stays
// model-agnostic.
type OpenAI struct {
	APIKey  string
	Model   string
	BaseURL string
	HTTP    *http.Client
}

const defaultOpenAIModel = "gpt-4.1"

func NewOpenAI(apiKey, model string) *OpenAI {
	if model == "" {
		model = defaultOpenAIModel
	}
	return &OpenAI{
		APIKey: apiKey, Model: model,
		BaseURL: "https://api.openai.com",
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
	}
}

func (a *OpenAI) Name() string         { return "openai" }
func (a *OpenAI) DefaultModel() string { return a.Model }

type openaiReq struct {
	Model     string          `json:"model"`
	Messages  []openaiMessage `json:"messages"`
	Tools     []openaiTool    `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

type openaiMessage struct {
	Role       string             `json:"role"`
	Content    any                `json:"content,omitempty"`
	ToolCalls  []openaiToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openaiToolFunction `json:"function"`
}

type openaiToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string
}

type openaiTool struct {
	Type     string             `json:"type"`
	Function openaiToolDef      `json:"function"`
}

type openaiToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type openaiResp struct {
	Choices []struct {
		Message      openaiMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Model string `json:"model"`
}

func (a *OpenAI) Complete(ctx context.Context, req Request) (*Response, error) {
	if a.APIKey == "" {
		return nil, errors.New("openai: OPENAI_API_KEY not set")
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	msgs := []openaiMessage{}
	if req.System != "" {
		msgs = append(msgs, openaiMessage{Role: "system", Content: req.System})
	}
	msgs = append(msgs, toOpenAIMessages(req.Messages)...)

	body := openaiReq{Model: a.Model, Messages: msgs, MaxTokens: maxTokens}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, openaiTool{
			Type:     "function",
			Function: openaiToolDef{Name: t.Name, Description: t.Description, Parameters: t.InputSchema},
		})
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.BaseURL+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := a.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("openai %d: %s", res.StatusCode, string(rb))
	}

	var oResp openaiResp
	if err := json.Unmarshal(rb, &oResp); err != nil {
		return nil, err
	}
	out := &Response{Model: oResp.Model, PromptTokens: oResp.Usage.PromptTokens, CompletionTokens: oResp.Usage.CompletionTokens}
	if len(oResp.Choices) == 0 {
		return out, errors.New("openai: empty choices")
	}
	choice := oResp.Choices[0]
	out.StopReason = choice.FinishReason
	if s, ok := choice.Message.Content.(string); ok {
		out.Text = s
	}
	for _, tc := range choice.Message.ToolCalls {
		var input map[string]any
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Input: input,
		})
	}
	return out, nil
}

// toOpenAIMessages translates duckllo's provider-neutral Message shape
// into chat-completions wire format. Tool roles map to role=tool with
// tool_call_id; assistant tool_uses map to message.tool_calls.
func toOpenAIMessages(in []Message) []openaiMessage {
	out := make([]openaiMessage, 0, len(in))
	for _, m := range in {
		switch m.Role {
		case "tool":
			out = append(out, openaiMessage{
				Role: "tool", ToolCallID: m.ToolID, Content: m.Content,
			})
		case "assistant":
			om := openaiMessage{Role: "assistant"}
			if m.Content != "" {
				om.Content = m.Content
			}
			for _, tc := range m.Tools {
				args, _ := json.Marshal(tc.Input)
				om.ToolCalls = append(om.ToolCalls, openaiToolCall{
					ID: tc.ID, Type: "function",
					Function: openaiToolFunction{Name: tc.Name, Arguments: string(args)},
				})
			}
			out = append(out, om)
		default:
			out = append(out, openaiMessage{Role: m.Role, Content: m.Content})
		}
	}
	return out
}
