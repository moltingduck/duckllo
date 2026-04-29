// Command mcp-duckllo is an MCP (Model Context Protocol) bridge that
// exposes a small set of duckllo operations over stdio JSON-RPC. It lets
// Claude Code (or any MCP client) drive the harness loop directly: list
// specs, create them, add criteria, approve, start a run, read state.
//
// Wire format: newline-delimited JSON-RPC 2.0 over stdin/stdout.
//
// Configuration via env (auto-loaded from .duckllo.env):
//
//	DUCKLLO_URL      (default http://localhost:3000)
//	DUCKLLO_KEY      project API key
//	DUCKLLO_PROJECT  project UUID
//
// Configure in Claude Code's MCP settings as:
//
//	{ "mcpServers": { "duckllo": { "command": "/path/to/mcp-duckllo" } } }
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/dotenv"
	"github.com/moltingduck/duckllo/internal/runner/client"
)

const (
	mcpProtocolVersion = "2024-11-05"
	serverName         = "duckllo"
	serverVersion      = "0.1.0"
)

func main() {
	if path, err := dotenv.LoadDefault(); err == nil && path != "" {
		log.Printf("[mcp-duckllo] loaded env from %s", path)
	}
	baseURL := envOr("DUCKLLO_URL", "http://localhost:3000")
	key := os.Getenv("DUCKLLO_KEY")
	projectStr := os.Getenv("DUCKLLO_PROJECT")
	if key == "" || projectStr == "" {
		log.Fatal("DUCKLLO_KEY and DUCKLLO_PROJECT required")
	}
	pid, err := uuid.Parse(projectStr)
	if err != nil {
		log.Fatalf("DUCKLLO_PROJECT: %v", err)
	}

	srv := &mcpServer{client: client.New(baseURL, key, pid)}
	if err := srv.serve(os.Stdin, os.Stdout); err != nil && err != io.EOF {
		log.Fatalf("mcp-duckllo: %v", err)
	}
}

type mcpServer struct {
	client *client.Client
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *mcpServer) serve(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24) // 16 MiB cap for big tool replies
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		// Notifications (no id) get no response.
		if len(req.ID) == 0 {
			s.handleNotification(req)
			continue
		}
		result, rerr := s.handle(req)
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = result
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *mcpServer) handleNotification(req rpcRequest) {
	// MCP sends `notifications/initialized` after the handshake. Nothing
	// to do; we accept and stay quiet.
}

func (s *mcpServer) handle(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
		}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefs()}, nil
	case "tools/call":
		return s.handleToolCall(req.Params)
	case "ping":
		return map[string]any{}, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]any         `json:"arguments"`
}

func (s *mcpServer) handleToolCall(raw json.RawMessage) (any, *rpcError) {
	var p toolCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}
	ctx := context.Background()

	out, err := s.dispatchTool(ctx, p.Name, p.Arguments)
	if err != nil {
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "error: " + err.Error()}},
			"isError": true,
		}, nil
	}
	body, _ := json.MarshalIndent(out, "", "  ")
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(body)}},
	}, nil
}

func (s *mcpServer) dispatchTool(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "duckllo_list_specs":
		path := "/api/projects/" + s.client.ProjectID.String() + "/specs"
		if status := getStr(args, "status"); status != "" {
			path += "?status=" + status
		}
		var out any
		return out, s.rawGet(ctx, path, &out)
	case "duckllo_create_spec":
		body := map[string]any{
			"title":    getStr(args, "title"),
			"intent":   getStr(args, "intent"),
			"priority": getStr(args, "priority"),
		}
		var out any
		return out, s.rawPost(ctx, "/api/projects/"+s.client.ProjectID.String()+"/specs", body, &out)
	case "duckllo_add_criterion":
		sid := getStr(args, "spec_id")
		body := map[string]any{
			"text":        getStr(args, "text"),
			"sensor_kind": getStr(args, "sensor_kind"),
		}
		if ss, ok := args["sensor_spec"].(map[string]any); ok {
			body["sensor_spec"] = ss
		}
		var out any
		return out, s.rawPost(ctx, "/api/projects/"+s.client.ProjectID.String()+"/specs/"+sid+"/criteria", body, &out)
	case "duckllo_approve_spec":
		sid := getStr(args, "spec_id")
		var out any
		return out, s.rawPost(ctx, "/api/projects/"+s.client.ProjectID.String()+"/specs/"+sid+"/approve", map[string]any{}, &out)
	case "duckllo_start_run":
		sid := getStr(args, "spec_id")
		var out any
		return out, s.rawPost(ctx, "/api/projects/"+s.client.ProjectID.String()+"/specs/"+sid+"/runs", map[string]any{}, &out)
	case "duckllo_get_run":
		rid := getStr(args, "run_id")
		var out any
		return out, s.rawGet(ctx, "/api/projects/"+s.client.ProjectID.String()+"/runs/"+rid, &out)
	case "duckllo_list_verifications":
		rid := getStr(args, "run_id")
		var out any
		return out, s.rawGet(ctx, "/api/projects/"+s.client.ProjectID.String()+"/runs/"+rid+"/verifications", &out)
	case "duckllo_post_annotation":
		vid := getStr(args, "verification_id")
		body := map[string]any{
			"bbox":    args["bbox"],
			"body":    getStr(args, "body"),
			"verdict": getStr(args, "verdict"),
		}
		var out any
		return out, s.rawPost(ctx, "/api/projects/"+s.client.ProjectID.String()+"/verifications/"+vid+"/annotations", body, &out)
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

func toolDefs() []map[string]any {
	return []map[string]any{
		{
			"name":        "duckllo_list_specs",
			"description": "List specs in the project. Optional status filter (draft|proposed|approved|running|validated|merged|rejected).",
			"inputSchema": objSchema(map[string]any{
				"status": str("optional spec status filter"),
			}, nil),
		},
		{
			"name":        "duckllo_create_spec",
			"description": "Create a new spec. Title is required; intent and priority are optional.",
			"inputSchema": objSchema(map[string]any{
				"title":    str("short, action-oriented spec title"),
				"intent":   str("markdown description of why and what success looks like"),
				"priority": str("low|medium|high|critical"),
			}, []string{"title"}),
		},
		{
			"name":        "duckllo_add_criterion",
			"description": "Add an acceptance criterion to a spec. sensor_kind ∈ lint|typecheck|unit_test|e2e_test|build|screenshot|gif|judge|manual.",
			"inputSchema": objSchema(map[string]any{
				"spec_id":     str("uuid of the spec"),
				"text":        str("human-readable criterion text"),
				"sensor_kind": str("which sensor will validate this criterion"),
				"sensor_spec": map[string]any{"type": "object", "description": "kind-specific spec, e.g. {url, selector} for screenshot"},
			}, []string{"spec_id", "text", "sensor_kind"}),
		},
		{
			"name":        "duckllo_approve_spec",
			"description": "Approve a spec so a run can be enqueued against it.",
			"inputSchema": objSchema(map[string]any{
				"spec_id": str("uuid of the spec"),
			}, []string{"spec_id"}),
		},
		{
			"name":        "duckllo_start_run",
			"description": "Enqueue a run for the given approved spec. If no plan exists, the planner agent will draft one.",
			"inputSchema": objSchema(map[string]any{
				"spec_id": str("uuid of the spec"),
			}, []string{"spec_id"}),
		},
		{
			"name":        "duckllo_get_run",
			"description": "Read a run with its iterations.",
			"inputSchema": objSchema(map[string]any{
				"run_id": str("uuid of the run"),
			}, []string{"run_id"}),
		},
		{
			"name":        "duckllo_list_verifications",
			"description": "List sensor verifications for a run.",
			"inputSchema": objSchema(map[string]any{
				"run_id": str("uuid of the run"),
			}, []string{"run_id"}),
		},
		{
			"name":        "duckllo_post_annotation",
			"description": "Annotate a screenshot/visual_diff/gif verification. fix_required flips the run to correcting and the corrector agent reads it on next claim.",
			"inputSchema": objSchema(map[string]any{
				"verification_id": str("uuid of the verification"),
				"bbox":            map[string]any{"type": "object", "description": "{x,y,w,h} in image-relative coords (0..1)"},
				"body":            str("comment text"),
				"verdict":         str("fix_required|nit|acceptable"),
			}, []string{"verification_id", "bbox", "body", "verdict"}),
		},
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────

func (s *mcpServer) rawGet(ctx context.Context, path string, out any) error {
	return clientDo(ctx, s.client, "GET", path, nil, out)
}
func (s *mcpServer) rawPost(ctx context.Context, path string, body, out any) error {
	return clientDo(ctx, s.client, "POST", path, body, out)
}

// clientDo reaches into the runner's HTTP client. Right now Client.do is
// unexported; we use a thin shim using its public knobs (BaseURL, APIKey,
// HTTP) so we don't fork the client.
func clientDo(ctx context.Context, c *client.Client, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytesReader(buf)
	}
	req, err := newReq(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	if res.StatusCode == 204 {
		return nil
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, string(rb))
	}
	if out != nil && len(rb) > 0 {
		return json.Unmarshal(rb, out)
	}
	return nil
}

// objSchema is the small JSON-Schema builder MCP tool definitions use.
func objSchema(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
