package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/runner/client"
)

// mcpClient drives an mcpServer via an io.Pipe pair so we can exercise
// the request/response loop in-memory. Each test starts its own server
// goroutine and synthesizes JSON-RPC requests.
type mcpClient struct {
	t      *testing.T
	stdin  *io.PipeWriter
	stdout *bufio.Scanner
}

func newMCP(t *testing.T, backendURL string, projectID uuid.UUID) *mcpClient {
	t.Helper()
	srv := &mcpServer{client: client.New(backendURL, "test-key", projectID)}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	go func() {
		_ = srv.serve(inR, outW)
		_ = outW.Close()
	}()

	t.Cleanup(func() {
		_ = inW.Close()
	})

	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	return &mcpClient{t: t, stdin: inW, stdout: sc}
}

func (m *mcpClient) call(method string, id int, params any) map[string]any {
	m.t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	body, _ := json.Marshal(req)
	if _, err := m.stdin.Write(append(body, '\n')); err != nil {
		m.t.Fatalf("write: %v", err)
	}
	if !m.stdout.Scan() {
		m.t.Fatalf("no response: %v", m.stdout.Err())
	}
	var resp map[string]any
	if err := json.Unmarshal(m.stdout.Bytes(), &resp); err != nil {
		m.t.Fatalf("decode: %v body=%s", err, m.stdout.Text())
	}
	return resp
}

func TestMCP_Initialize(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	c := newMCP(t, srv.URL, uuid.New())
	resp := c.call("initialize", 1, map[string]any{})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %+v", resp)
	}
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("protocolVersion: got %v want %v", result["protocolVersion"], mcpProtocolVersion)
	}
	server := result["serverInfo"].(map[string]any)
	if server["name"] != serverName {
		t.Errorf("server name: got %v want %v", server["name"], serverName)
	}
}

func TestMCP_ToolsList(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	c := newMCP(t, srv.URL, uuid.New())
	resp := c.call("tools/list", 1, map[string]any{})
	tools := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools advertised")
	}

	// Spot-check key tools and their required-fields contract.
	want := map[string][]string{
		"duckllo_create_spec":      {"title"},
		"duckllo_add_criterion":    {"spec_id", "text", "sensor_kind"},
		"duckllo_post_annotation":  {"verification_id", "bbox", "body", "verdict"},
	}
	got := map[string][]string{}
	for _, raw := range tools {
		t := raw.(map[string]any)
		schema := t["inputSchema"].(map[string]any)
		var req []string
		if r, ok := schema["required"].([]any); ok {
			for _, rr := range r {
				req = append(req, rr.(string))
			}
		}
		got[t["name"].(string)] = req
	}
	for name, fields := range want {
		gotFields, ok := got[name]
		if !ok {
			t.Errorf("missing tool %q", name)
			continue
		}
		if !sameStrings(gotFields, fields) {
			t.Errorf("%s required: got %v want %v", name, gotFields, fields)
		}
	}
}

func TestMCP_ToolsCall_ListSpecs(t *testing.T) {
	pid := uuid.New()
	canned := `[{"id":"spec-1","title":"first"},{"id":"spec-2","title":"second"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate the URL path the MCP adapter is hitting.
		expected := "/api/projects/" + pid.String() + "/specs"
		if r.URL.Path != expected {
			t.Errorf("backend path: got %q want %q", r.URL.Path, expected)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing Bearer header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(canned))
	}))
	t.Cleanup(srv.Close)

	c := newMCP(t, srv.URL, pid)
	resp := c.call("tools/call", 1, map[string]any{
		"name":      "duckllo_list_specs",
		"arguments": map[string]any{},
	})
	result := resp["result"].(map[string]any)
	if iserr, _ := result["isError"].(bool); iserr {
		t.Fatalf("isError set: %+v", result)
	}
	content := result["content"].([]any)
	body := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(body, "spec-1") || !strings.Contains(body, "spec-2") {
		t.Errorf("text payload missing canned ids: %s", body)
	}
}

func TestMCP_ToolsCall_CreateSpec_BodyShape(t *testing.T) {
	pid := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		if got["title"] != "from MCP" {
			t.Errorf("title: got %v", got["title"])
		}
		if got["intent"] != "do something" {
			t.Errorf("intent: got %v", got["intent"])
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"spec-new"}`))
	}))
	t.Cleanup(srv.Close)

	c := newMCP(t, srv.URL, pid)
	resp := c.call("tools/call", 1, map[string]any{
		"name": "duckllo_create_spec",
		"arguments": map[string]any{
			"title": "from MCP", "intent": "do something",
		},
	})
	if _, ok := resp["result"]; !ok {
		t.Fatalf("no result: %+v", resp)
	}
}

func TestMCP_ToolsCall_BackendErrorSurfaces(t *testing.T) {
	pid := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	t.Cleanup(srv.Close)

	c := newMCP(t, srv.URL, pid)
	resp := c.call("tools/call", 1, map[string]any{
		"name":      "duckllo_list_specs",
		"arguments": map[string]any{},
	})
	result := resp["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("expected isError=true on backend 400; got %+v", result)
	}
	body := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(body, "400") {
		t.Errorf("error body should surface status: %s", body)
	}
}

func TestMCP_UnknownMethodReturns32601(t *testing.T) {
	c := newMCP(t, "http://nope", uuid.New())
	resp := c.call("totally/made_up", 7, map[string]any{})
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %+v", resp)
	}
	if int(errObj["code"].(float64)) != -32601 {
		t.Errorf("code: got %v want -32601", errObj["code"])
	}
}

func TestMCP_NotificationProducesNoResponse(t *testing.T) {
	c := newMCP(t, "http://nope", uuid.New())
	body := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	if _, err := c.stdin.Write(body); err != nil {
		t.Fatal(err)
	}
	// Follow-up request should still get a response — proves the server
	// processed (and silently dropped) the notification rather than
	// dying.
	resp := c.call("ping", 1, nil)
	if _, ok := resp["result"]; !ok {
		t.Errorf("ping after notification produced no result: %+v", resp)
	}
}

// sameStrings is a tiny order-insensitive set comparison; spec doesn't
// guarantee field order in the JSON-Schema "required" array.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, c := range seen {
		if c != 0 {
			return false
		}
	}
	return true
}

// silence unused imports if some helpers aren't needed in this version.
var _ = bytes.NewBuffer
var _ = fmt.Sprintf
