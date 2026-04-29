package http_test

// Integration test for the duckllo coordination plane. Walks the full
// harness flow at the API level (no UI, no real model): register a user,
// create a project, mint an API key, compose a spec with criteria,
// approve, enqueue a run, simulate a runner claiming the plan phase,
// post an iteration, create+approve a plan, advance to execute, post
// the executor iteration, advance to validate, post verifications for
// each criterion, advance to done. Assertions at every step.
//
// Requires a real Postgres reachable via TEST_DATABASE_URL. Skipped
// otherwise so `go test ./...` is clean on machines without Postgres.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/moltingduck/duckllo/internal/config"
	"github.com/moltingduck/duckllo/internal/db"
	httpapi "github.com/moltingduck/duckllo/internal/http"
)

func TestHarnessFlow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer pool.Close()

	wipeAll(t, ctx, pool)

	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := &config.Config{
		Addr:           ":0",
		DatabaseURL:    dsn,
		UploadsDir:     t.TempDir(),
		MaxUploadBytes: 32 * 1024 * 1024,
	}
	srv := httpapi.NewServer(cfg, pool)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c := &apiClient{t: t, base: ts.URL}

	// 1. Register first user — becomes admin.
	var sess struct {
		Token    string `json:"token"`
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	c.do("POST", "/api/auth/register", "", map[string]any{
		"username": "alice", "password": "alice-secret",
	}, &sess, http.StatusCreated)
	if sess.Token == "" {
		t.Fatalf("register: empty session token")
	}
	c.token = sess.Token

	// 2. /api/auth/me works with the session.
	var me map[string]any
	c.do("GET", "/api/auth/me", c.token, nil, &me, http.StatusOK)
	if me["username"] != "alice" {
		t.Fatalf("me: got %v", me)
	}

	// 3. Create project, member listing includes alice as product_manager.
	var project map[string]any
	c.do("POST", "/api/projects", c.token, map[string]any{
		"name": "test", "description": "integration test project",
	}, &project, http.StatusCreated)
	pid := project["id"].(string)

	var members []map[string]any
	c.do("GET", "/api/projects/"+pid+"/members", c.token, nil, &members, http.StatusOK)
	if len(members) == 0 || members[0]["role"] != "product_manager" {
		t.Fatalf("members: got %v", members)
	}

	// 4. Mint a project API key (used as the runner's bearer below).
	var keyResp struct {
		Plain  string         `json:"plain"`
		APIKey map[string]any `json:"api_key"`
	}
	c.do("POST", "/api/projects/"+pid+"/api-keys", c.token, map[string]any{
		"label": "test-runner",
	}, &keyResp, http.StatusCreated)
	if !strings.HasPrefix(keyResp.Plain, "duckllo_") {
		t.Fatalf("api key: bad format %q", keyResp.Plain)
	}
	runnerKey := keyResp.Plain

	// 5. Create spec, add criterion (judge so we can satisfy it without a real model).
	var spec map[string]any
	c.do("POST", "/api/projects/"+pid+"/specs", c.token, map[string]any{
		"title":  "Add a hello",
		"intent": "Print hello to stdout.",
	}, &spec, http.StatusCreated)
	sid := spec["id"].(string)

	c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/criteria", c.token, map[string]any{
		"text":        "prints hello",
		"sensor_kind": "judge",
	}, nil, http.StatusOK)

	c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/approve", c.token, nil, nil, http.StatusOK)

	// 6. Enqueue run; with no approved plan, it should start in 'plan' phase.
	var run map[string]any
	c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/runs", c.token,
		map[string]any{}, &run, http.StatusCreated)
	if run["status"] != "queued" {
		t.Fatalf("run status: %v", run["status"])
	}
	rid := run["id"].(string)

	// 7. Runner claims work — uses API key now.
	var claim struct {
		WorkItem map[string]any `json:"work_item"`
		Run      map[string]any `json:"run"`
	}
	c.do("POST", "/api/projects/"+pid+"/work/claim", runnerKey, map[string]any{
		"runner_id": "test-runner-1", "phases": []string{"plan", "execute", "validate", "correct"},
	}, &claim, http.StatusOK)
	if claim.WorkItem["phase"] != "plan" {
		t.Fatalf("first claim phase: got %v want plan", claim.WorkItem["phase"])
	}

	// 8. A second simultaneous claim must return no work — SKIP LOCKED proves
	//    it. Done before we post any iteration.
	res2 := c.raw("POST", "/api/projects/"+pid+"/work/claim", runnerKey, map[string]any{
		"runner_id": "test-runner-2", "phases": []string{"plan", "execute"},
	})
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("second claim: status=%d body=%s", res2.StatusCode, mustReadBody(res2))
	}

	// 9. Bundle endpoint returns the spec with no plan attached (planner phase).
	var bundle map[string]any
	c.do("GET", "/api/projects/"+pid+"/runs/"+rid+"/bundle", runnerKey, nil, &bundle, http.StatusOK)
	if _, ok := bundle["spec"]; !ok {
		t.Fatalf("bundle missing spec: %v", bundle)
	}

	// 10. Runner posts a planner iteration, creates a plan, approves it,
	//     advances with plan_id binding to execute.
	var planIter map[string]any
	c.do("POST", "/api/projects/"+pid+"/runs/"+rid+"/iterations", runnerKey, map[string]any{
		"phase": "plan", "agent_role": "planner",
		"summary": "Drafted a single-step plan",
	}, &planIter, http.StatusCreated)

	var plan map[string]any
	c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/plans", runnerKey, map[string]any{
		"created_by_role": "planner",
		"steps": []map[string]any{{
			"id": "s1", "order": 1, "summary": "echo hello",
		}},
	}, &plan, http.StatusCreated)
	planID := plan["id"].(string)

	c.do("POST", "/api/projects/"+pid+"/plans/"+planID+"/approve", c.token, nil, nil, http.StatusOK)

	c.do("POST", "/api/projects/"+pid+"/runs/"+rid+"/advance", runnerKey, map[string]any{
		"runner_id": "test-runner-1", "from_phase": "plan", "to_phase": "execute",
		"plan_id": planID,
	}, nil, http.StatusOK)

	// 11. Claim the next phase — should be 'execute'.
	var claim2 struct {
		WorkItem map[string]any `json:"work_item"`
	}
	c.do("POST", "/api/projects/"+pid+"/work/claim", runnerKey, map[string]any{
		"runner_id": "test-runner-1", "phases": []string{"execute"},
	}, &claim2, http.StatusOK)
	if claim2.WorkItem["phase"] != "execute" {
		t.Fatalf("execute claim phase: got %v", claim2.WorkItem["phase"])
	}

	// 12. Post executor iteration + advance to validate.
	c.do("POST", "/api/projects/"+pid+"/runs/"+rid+"/iterations", runnerKey, map[string]any{
		"phase": "execute", "agent_role": "executor", "summary": "Wrote main.go",
	}, nil, http.StatusCreated)
	c.do("POST", "/api/projects/"+pid+"/runs/"+rid+"/advance", runnerKey, map[string]any{
		"runner_id": "test-runner-1", "from_phase": "execute", "to_phase": "validate",
	}, nil, http.StatusOK)

	// 13. Validator phase: claim, post a verification per criterion (in this
	//     spec there is one judge criterion), advance with final_status=done.
	c.do("POST", "/api/projects/"+pid+"/work/claim", runnerKey, map[string]any{
		"runner_id": "test-runner-1", "phases": []string{"validate"},
	}, &claim2, http.StatusOK)

	// Re-read the spec so we get the criterion the server appended.
	var fresh map[string]any
	c.do("GET", "/api/projects/"+pid+"/specs/"+sid, c.token, nil, &fresh, http.StatusOK)
	freshSpec, _ := fresh["spec"].(map[string]any)
	crits, _ := freshSpec["acceptance_criteria"].([]any)
	if len(crits) == 0 {
		t.Fatalf("expected at least one criterion to verify, got %v", freshSpec["acceptance_criteria"])
	}
	firstCrit := crits[0].(map[string]any)

	c.do("POST", "/api/projects/"+pid+"/runs/"+rid+"/verifications", runnerKey, map[string]any{
		"criterion_id": firstCrit["id"], "kind": "judge", "class": "inferential",
		"status": "pass", "summary": "looks correct",
	}, nil, http.StatusCreated)

	c.do("POST", "/api/projects/"+pid+"/runs/"+rid+"/advance", runnerKey, map[string]any{
		"runner_id": "test-runner-1", "from_phase": "validate", "final_status": "done",
	}, nil, http.StatusOK)

	// 14. Run + spec end states.
	var finalRun map[string]any
	c.do("GET", "/api/projects/"+pid+"/runs/"+rid, runnerKey, nil, &finalRun, http.StatusOK)
	if rs, ok := finalRun["run"].(map[string]any); !ok || rs["status"] != "done" {
		t.Fatalf("final run status: %v", finalRun["run"])
	}

	var finalSpec map[string]any
	c.do("GET", "/api/projects/"+pid+"/specs/"+sid, c.token, nil, &finalSpec, http.StatusOK)
	if sp, ok := finalSpec["spec"].(map[string]any); !ok || sp["status"] != "validated" {
		t.Fatalf("final spec status: %v", finalSpec["spec"])
	}

	// 15. Annotation -> run flips to correcting (spec is past validating; we
	//     post on a fresh run to test the auto-status update).
	//     Skip in this happy-path test; covered by next test.
}

// ── Helpers ──────────────────────────────────────────────────────────────

type apiClient struct {
	t     *testing.T
	base  string
	token string
}

func (c *apiClient) do(method, path, token string, body any, out any, wantStatus int) {
	c.t.Helper()
	res := c.raw(method, path, token, body)
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	if res.StatusCode != wantStatus {
		c.t.Fatalf("%s %s: status %d (want %d) body=%s", method, path, res.StatusCode, wantStatus, string(rb))
	}
	if out != nil && len(rb) > 0 {
		if err := json.Unmarshal(rb, out); err != nil {
			c.t.Fatalf("decode %s %s: %v body=%s", method, path, err, string(rb))
		}
	}
}

func (c *apiClient) raw(method, path, token string, body any) *http.Response {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token == "" {
		token = c.token
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return res
}

func mustReadBody(res *http.Response) string {
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return string(b)
}

// wipeAll drops every duckllo table to give the test a clean slate.
// The migration runner re-creates them.
func wipeAll(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	tables := []string{
		"comments", "annotations", "verifications",
		"work_queue", "agent_sessions", "iterations", "runs",
		"plans", "specs",
		"harness_rules", "topologies",
		"recovery_codes", "sessions", "api_keys", "project_members",
		"projects", "users",
		"schema_migrations",
	}
	for _, tbl := range tables {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
}
