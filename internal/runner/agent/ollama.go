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

// Ollama implements Provider against a local Ollama daemon's /api/chat
// endpoint. Tool-use support depends on the chosen model — recent
// Llama 3.x and Qwen3 weights handle the OpenAI-style tool schema; older
// models will simply not produce tool_calls.
type Ollama struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

const defaultOllamaURL = "http://localhost:11434"
const defaultOllamaModel = "llama3.2"

func NewOllama(baseURL, model string) *Ollama {
	if baseURL == "" {
		baseURL = defaultOllamaURL
	}
	if model == "" {
		model = defaultOllamaModel
	}
	return &Ollama{
		BaseURL: baseURL, Model: model,
		HTTP: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (o *Ollama) Name() string         { return "ollama" }
func (o *Ollama) DefaultModel() string { return o.Model }

type ollamaReq struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaToolFunc `json:"function"`
}
type ollamaToolFunc struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ollamaTool struct {
	Type     string         `json:"type"`
	Function ollamaToolDef  `json:"function"`
}
type ollamaToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type ollamaResp struct {
	Model           string         `json:"model"`
	Message         ollamaMessage  `json:"message"`
	Done            bool           `json:"done"`
	PromptEvalCount int            `json:"prompt_eval_count"`
	EvalCount       int            `json:"eval_count"`
}

func (o *Ollama) Complete(ctx context.Context, req Request) (*Response, error) {
	msgs := []ollamaMessage{}
	if req.System != "" {
		msgs = append(msgs, ollamaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "tool":
			// Ollama (and the open-weight chat models behind it) doesn't
			// have a first-class tool message format yet. Best we can do
			// is fold the tool result into a user turn so the next
			// assistant pass sees it.
			msgs = append(msgs, ollamaMessage{
				Role: "user", Content: fmt.Sprintf("[tool result %s]\n%s", m.ToolID, m.Content),
			})
		case "assistant":
			am := ollamaMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.Tools {
				am.ToolCalls = append(am.ToolCalls, ollamaToolCall{
					Function: ollamaToolFunc{Name: tc.Name, Arguments: tc.Input},
				})
			}
			msgs = append(msgs, am)
		default:
			msgs = append(msgs, ollamaMessage{Role: m.Role, Content: m.Content})
		}
	}

	body := ollamaReq{Model: o.Model, Messages: msgs, Stream: false}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, ollamaTool{
			Type:     "function",
			Function: ollamaToolDef{Name: t.Name, Description: t.Description, Parameters: t.InputSchema},
		})
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.BaseURL+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := o.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama %d: %s", res.StatusCode, string(rb))
	}
	var oResp ollamaResp
	if err := json.Unmarshal(rb, &oResp); err != nil {
		return nil, err
	}
	if !oResp.Done {
		return nil, errors.New("ollama: stream didn't complete")
	}
	out := &Response{
		Model: oResp.Model, Text: oResp.Message.Content,
		PromptTokens: oResp.PromptEvalCount, CompletionTokens: oResp.EvalCount,
	}
	for i, tc := range oResp.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:    fmt.Sprintf("call_%d", i),
			Name:  tc.Function.Name,
			Input: tc.Function.Arguments,
		})
	}
	return out, nil
}
