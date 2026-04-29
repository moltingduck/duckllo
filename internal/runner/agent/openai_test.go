package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mockOpenAI(t *testing.T, response string) (*OpenAI, *capturedRequest) {
	t.Helper()
	cap := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.headers = r.Header.Clone()
		cap.body = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	o := NewOpenAI("test-key", "gpt-test")
	o.BaseURL = srv.URL
	return o, cap
}

func TestOpenAI_TextOnly(t *testing.T) {
	o, cap := mockOpenAI(t, `{
		"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"hi"}}],
		"usage":{"prompt_tokens":3,"completion_tokens":1},
		"model":"gpt-test"
	}`)

	resp, err := o.Complete(context.Background(), Request{
		System: "you are a tester",
		Messages: []Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if cap.method != "POST" || cap.path != "/v1/chat/completions" {
		t.Errorf("hit %s %s", cap.method, cap.path)
	}
	if cap.headers.Get("Authorization") != "Bearer test-key" {
		t.Error("missing/incorrect Bearer header")
	}
	var sent map[string]any
	_ = json.Unmarshal(cap.body, &sent)
	msgs := sent["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected system+user => 2 messages, got %d", len(msgs))
	}
	if (msgs[0].(map[string]any))["role"] != "system" {
		t.Error("system message should come first")
	}
	if resp.Text != "hi" {
		t.Errorf("text: %q", resp.Text)
	}
	if resp.PromptTokens != 3 || resp.CompletionTokens != 1 {
		t.Errorf("usage: %+v", resp)
	}
}

func TestOpenAI_ToolCallShapeDecoded(t *testing.T) {
	o, _ := mockOpenAI(t, `{
		"choices":[{"finish_reason":"tool_calls","message":{
			"role":"assistant","content":null,
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]
		}}],
		"usage":{"prompt_tokens":0,"completion_tokens":0},
		"model":"gpt-test"
	}`)
	resp, err := o.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "x"}},
		Tools:    []ToolDef{{Name: "read_file", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read_file" {
		t.Fatalf("tool call decode: %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Input["path"] != "main.go" {
		t.Errorf("tool args decode: %+v", resp.ToolCalls[0].Input)
	}
}

func TestOpenAI_ToolResultMappedToToolRole(t *testing.T) {
	o, cap := mockOpenAI(t, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`)
	_, err := o.Complete(context.Background(), Request{
		Messages: []Message{{Role: "tool", ToolID: "call_1", Content: "result"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cap.body), `"role":"tool"`) {
		t.Errorf("expected role=tool message in wire body; got %s", cap.body)
	}
	if !strings.Contains(string(cap.body), `"tool_call_id":"call_1"`) {
		t.Errorf("expected tool_call_id wired through; got %s", cap.body)
	}
}
