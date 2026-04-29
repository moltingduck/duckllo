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

func mockOllama(t *testing.T, response string) (*Ollama, *capturedRequest) {
	t.Helper()
	cap := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.body = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	o := NewOllama(srv.URL, "llama-test")
	return o, cap
}

func TestOllama_BasicChat(t *testing.T) {
	o, cap := mockOllama(t, `{
		"model":"llama-test",
		"message":{"role":"assistant","content":"hi"},
		"done":true,
		"prompt_eval_count":4,
		"eval_count":1
	}`)
	resp, err := o.Complete(context.Background(), Request{
		System:   "be brief",
		Messages: []Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.method != "POST" || cap.path != "/api/chat" {
		t.Errorf("hit %s %s", cap.method, cap.path)
	}
	if resp.Text != "hi" || resp.PromptTokens != 4 || resp.CompletionTokens != 1 {
		t.Errorf("decoded wrong: %+v", resp)
	}
	var sent map[string]any
	_ = json.Unmarshal(cap.body, &sent)
	if sent["stream"] != false {
		t.Error("ollama stream should be false (we want one synchronous response)")
	}
}

func TestOllama_ToolCallsDecoded(t *testing.T) {
	o, _ := mockOllama(t, `{
		"model":"x","done":true,
		"message":{
			"role":"assistant",
			"content":"",
			"tool_calls":[{"function":{"name":"read_file","arguments":{"path":"a.go"}}}]
		},
		"prompt_eval_count":0,"eval_count":0
	}`)
	resp, err := o.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read_file" {
		t.Fatalf("tool call: %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Input["path"] != "a.go" {
		t.Errorf("input: %+v", resp.ToolCalls[0].Input)
	}
}

func TestOllama_ToolResultFoldsIntoUser(t *testing.T) {
	o, cap := mockOllama(t, `{"model":"x","done":true,"message":{"role":"assistant","content":"ok"},"prompt_eval_count":0,"eval_count":0}`)
	_, err := o.Complete(context.Background(), Request{
		Messages: []Message{{Role: "tool", ToolID: "call_1", Content: "result body"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Ollama's chat schema lacks a tool role; we fold into user with a marker.
	if !strings.Contains(string(cap.body), `"role":"user"`) {
		t.Error("tool result should fold into a user turn")
	}
	if !strings.Contains(string(cap.body), "tool result") {
		t.Errorf("tool result body should be visible to the model; got %s", cap.body)
	}
}

func TestOllama_NotDoneIsAnError(t *testing.T) {
	o, _ := mockOllama(t, `{"model":"x","done":false,"message":{"role":"assistant","content":"partial"}}`)
	_, err := o.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Error("expected error when ollama reports done=false")
	}
}

func TestProviderFactoryDefaults(t *testing.T) {
	tests := []struct {
		cfg  Config
		want string
	}{
		{Config{}, "anthropic"},
		{Config{Provider: "anthropic"}, "anthropic"},
		{Config{Provider: "openai"}, "openai"},
		{Config{Provider: "ollama"}, "ollama"},
	}
	for _, tt := range tests {
		p, err := New(tt.cfg)
		if err != nil {
			t.Errorf("New(%+v): %v", tt.cfg, err)
			continue
		}
		if p.Name() != tt.want {
			t.Errorf("New(%+v).Name() = %q want %q", tt.cfg, p.Name(), tt.want)
		}
	}
	if _, err := New(Config{Provider: "bogus"}); err == nil {
		t.Error("expected error for unknown provider")
	}
}
