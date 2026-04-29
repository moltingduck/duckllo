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

// mockAnthropic spins up a fake /v1/messages endpoint that records the
// request body and returns the canned response. The provider's BaseURL
// field points at the test server so we don't need real credentials.
func mockAnthropic(t *testing.T, response string) (*Anthropic, *capturedRequest) {
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

	a := NewAnthropic("test-key", "claude-test")
	a.BaseURL = srv.URL
	return a, cap
}

type capturedRequest struct {
	method  string
	path    string
	headers http.Header
	body    []byte
}

func TestAnthropic_TextOnly(t *testing.T) {
	a, cap := mockAnthropic(t, `{
		"stop_reason":"end_turn",
		"model":"claude-test",
		"content":[{"type":"text","text":"hello"}],
		"usage":{"input_tokens":10,"output_tokens":2}
	}`)

	resp, err := a.Complete(context.Background(), Request{
		System:   "you are a helper",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Wire-format assertions.
	if cap.method != "POST" || cap.path != "/v1/messages" {
		t.Errorf("hit %s %s; want POST /v1/messages", cap.method, cap.path)
	}
	if cap.headers.Get("x-api-key") != "test-key" {
		t.Errorf("missing x-api-key header")
	}
	if cap.headers.Get("anthropic-version") == "" {
		t.Errorf("missing anthropic-version header")
	}
	var sent map[string]any
	if err := json.Unmarshal(cap.body, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent["system"] != "you are a helper" {
		t.Errorf("system field missing or wrong: %v", sent["system"])
	}

	// Response decoding.
	if resp.Text != "hello" {
		t.Errorf("Text: got %q want hello", resp.Text)
	}
	if resp.PromptTokens != 10 || resp.CompletionTokens != 2 {
		t.Errorf("usage decoded wrong: %+v", resp)
	}
}

func TestAnthropic_ToolCallDecoded(t *testing.T) {
	a, _ := mockAnthropic(t, `{
		"stop_reason":"tool_use",
		"model":"claude-test",
		"content":[
			{"type":"text","text":"using read_file"},
			{"type":"tool_use","id":"tool_1","name":"read_file","input":{"path":"a.go"}}
		],
		"usage":{"input_tokens":1,"output_tokens":1}
	}`)
	resp, err := a.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "look"}},
		Tools:    []ToolDef{{Name: "read_file", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "using read_file" {
		t.Errorf("text: %q", resp.Text)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "tool_1" || tc.Name != "read_file" || tc.Input["path"] != "a.go" {
		t.Errorf("tool call decoded wrong: %+v", tc)
	}
}

func TestAnthropic_ToolResultRoleMappedToUser(t *testing.T) {
	a, cap := mockAnthropic(t, `{"stop_reason":"end_turn","model":"x","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":0,"output_tokens":0}}`)
	_, err := a.Complete(context.Background(), Request{
		Messages: []Message{
			{Role: "tool", ToolID: "t1", Content: "result body"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cap.body), `"tool_result"`) {
		t.Errorf("expected wire body to contain tool_result; got %s", cap.body)
	}
	if !strings.Contains(string(cap.body), `"role":"user"`) {
		t.Errorf("tool message must be wrapped in role=user; got %s", cap.body)
	}
}

func TestAnthropic_HTTPErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	defer srv.Close()
	a := NewAnthropic("k", "m")
	a.BaseURL = srv.URL
	_, err := a.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should surface HTTP status: %v", err)
	}
}
