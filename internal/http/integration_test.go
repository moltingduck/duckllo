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
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
	"github.com/moltingduck/duckllo/internal/runner/agent"
)

// scriptedProvider is a deterministic agent.Provider for tests of the
// suggest endpoints. It checks the system-prompt prefix to tell which
// pass it's in (refine vs criteria) and returns a canned fenced-JSON
// block — exactly the shape the suggest package's parsers expect.
// Records the captured prompts so tests can assert (a) that the user
// content reached the model and (b) that QA pairs were folded in.
type scriptedProvider struct {
	refineJSON   string
	criteriaJSON string
	captured     []agent.Request
}

func (s *scriptedProvider) Name() string         { return "scripted" }
func (s *scriptedProvider) DefaultModel() string { return "scripted" }
func (s *scriptedProvider) Complete(_ context.Context, req agent.Request) (*agent.Response, error) {
	s.captured = append(s.captured, req)
	body := s.criteriaJSON
	if strings.Contains(req.System, "REFINE") {
		body = s.refineJSON
	}
	return &agent.Response{Text: "```json\n" + body + "\n```", StopReason: "end_turn"}, nil
}

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

// ─── Additional integration tests ─────────────────────────────────────────

// TestAnnotationEnqueuesCorrectWorkItem locks in the bug fix where
// fix_required annotations flipped run.status to 'correcting' but did
// not insert a 'correct' work_queue entry — meaning the corrector
// agent had nothing to claim and the run sat stuck in 'correcting'
// forever. After the fix, posting a fix_required annotation must
// produce a claimable correct work item, and posting a second one
// must NOT create a duplicate (the IDEMPOTENT clause).
func TestAnnotationEnqueuesCorrectWorkItem(t *testing.T) {
	e := setupTestEnv(t)

	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "annot enqueue", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "ok", "sensor_kind": "screenshot"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)
	rid, _ := e.runRunnerThroughValidate(t, sid)

	// Post a verification + fix_required annotation.
	var verif map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/runs/"+rid+"/verifications", e.apiKey,
		map[string]any{
			"kind": "screenshot", "class": "computational",
			"status": "fail", "summary": "missing toggle",
			"artifact_url": "/api/uploads/x.png",
		}, &verif, http.StatusCreated)
	vid := verif["id"].(string)

	e.c.do("POST", "/api/projects/"+e.pid+"/verifications/"+vid+"/annotations", e.c.token,
		map[string]any{
			"bbox":    map[string]any{"x": 0.1, "y": 0.1, "w": 0.2, "h": 0.05},
			"body":    "fix this",
			"verdict": "fix_required",
		}, nil, http.StatusCreated)

	// A claim with phase=correct must succeed (not return 204).
	var claim struct {
		WorkItem map[string]any `json:"work_item"`
	}
	e.c.do("POST", "/api/projects/"+e.pid+"/work/claim", e.apiKey,
		map[string]any{"runner_id": "test-corrector", "phases": []string{"correct"}},
		&claim, http.StatusOK)
	if claim.WorkItem["phase"] != "correct" {
		t.Fatalf("expected correct phase claimed, got %v", claim.WorkItem["phase"])
	}

	// A second annotation must NOT create a duplicate work item — the
	// existing claimed/pending one already covers it. The next claim
	// from a different runner should return 204 because the only
	// correct item is held by test-corrector.
	e.c.do("POST", "/api/projects/"+e.pid+"/verifications/"+vid+"/annotations", e.c.token,
		map[string]any{
			"bbox":    map[string]any{"x": 0.5, "y": 0.5, "w": 0.1, "h": 0.1},
			"body":    "also fix this",
			"verdict": "fix_required",
		}, nil, http.StatusCreated)
	res := e.c.raw("POST", "/api/projects/"+e.pid+"/work/claim", e.apiKey,
		map[string]any{"runner_id": "another-corrector", "phases": []string{"correct"}})
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("second corrector claim should be 204; got %d: %s", res.StatusCode, body)
	}
}

// TestAnnotationFlipsRunToCorrecting exercises the harness's correction
// signal: a fix_required annotation must move the parent run from
// 'validating' to 'correcting' so the corrector phase becomes claimable.
func TestAnnotationFlipsRunToCorrecting(t *testing.T) {
	e := setupTestEnv(t)

	// Spec with a screenshot criterion.
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "annot test", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "looks ok", "sensor_kind": "screenshot"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)

	rid, _ := e.runRunnerThroughValidate(t, sid)

	// Post a screenshot verification (JSON-only — no artifact needed for
	// this test; the next test covers multipart).
	var verif map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/runs/"+rid+"/verifications", e.apiKey,
		map[string]any{
			"kind": "screenshot", "class": "computational",
			"status": "pass", "summary": "fake",
			"artifact_url": "/api/uploads/fake.png",
		}, &verif, http.StatusCreated)
	vid := verif["id"].(string)

	// Annotate it.
	e.c.do("POST", "/api/projects/"+e.pid+"/verifications/"+vid+"/annotations", e.c.token,
		map[string]any{
			"bbox":    map[string]any{"x": 0.1, "y": 0.1, "w": 0.2, "h": 0.05},
			"body":    "fix this",
			"verdict": "fix_required",
		}, nil, http.StatusCreated)

	// Run must now be 'correcting'.
	var run map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/runs/"+rid, e.c.token, nil, &run, http.StatusOK)
	got := run["run"].(map[string]any)["status"]
	if got != "correcting" {
		t.Fatalf("run status: got %v want correcting", got)
	}

	// And the bundle must surface the open annotation.
	var bundle map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/runs/"+rid+"/bundle", e.apiKey, nil, &bundle, http.StatusOK)
	open := bundle["open_annotations"].([]any)
	if len(open) != 1 {
		t.Fatalf("bundle.open_annotations: got %d want 1", len(open))
	}
	if (open[0].(map[string]any))["body"] != "fix this" {
		t.Errorf("body: %v", open[0])
	}

	// 'acceptable' annotations must NOT trigger the flip — verify.
	e.c.do("POST", "/api/projects/"+e.pid+"/verifications/"+vid+"/annotations", e.c.token,
		map[string]any{"bbox": map[string]any{}, "body": "lgtm", "verdict": "acceptable"},
		nil, http.StatusCreated)
	e.c.do("GET", "/api/projects/"+e.pid+"/runs/"+rid, e.c.token, nil, &run, http.StatusOK)
	if got := run["run"].(map[string]any)["status"]; got != "correcting" {
		t.Errorf("status drifted off correcting: got %v", got)
	}
}

// TestMultipartArtifactRoundTrip uploads a PNG via the multipart
// verifications endpoint and reads it back through /api/uploads/.
func TestMultipartArtifactRoundTrip(t *testing.T) {
	e := setupTestEnv(t)

	// Minimal spec + run setup.
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "upload test", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "ok", "sensor_kind": "screenshot"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)
	rid, _ := e.runRunnerThroughValidate(t, sid)

	// Build a multipart body: 1x1 transparent PNG + meta JSON.
	pngBytes, _ := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+P+/HgAFhAJ/wlseKgAAAABJRU5ErkJggg==")
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	meta := `{"kind":"screenshot","class":"computational","status":"pass","summary":"smoke"}`
	_ = mw.WriteField("meta", meta)
	mh := make(map[string][]string)
	mh["Content-Disposition"] = []string{`form-data; name="file"; filename="tiny.png"`}
	mh["Content-Type"] = []string{"image/png"}
	fw, _ := mw.CreatePart(mh)
	_, _ = fw.Write(pngBytes)
	_ = mw.Close()

	req, err := http.NewRequest("POST",
		e.baseURL+"/api/projects/"+e.pid+"/runs/"+rid+"/verifications", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusCreated {
		rb, _ := io.ReadAll(res.Body)
		t.Fatalf("upload: status %d body=%s", res.StatusCode, string(rb))
	}

	// Verification list should now contain one row with an artifact_url.
	var verifs []map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/runs/"+rid+"/verifications", e.c.token, nil, &verifs, http.StatusOK)
	if len(verifs) != 1 {
		t.Fatalf("verifications: got %d want 1", len(verifs))
	}
	url := verifs[0]["artifact_url"].(string)
	if !strings.HasPrefix(url, "/api/uploads/") {
		t.Fatalf("artifact_url unexpected: %q", url)
	}

	// Fetch the artifact back; bytes must match.
	getRes, err := http.Get(e.baseURL + url)
	if err != nil {
		t.Fatal(err)
	}
	defer getRes.Body.Close()
	got, _ := io.ReadAll(getRes.Body)
	if !bytes.Equal(got, pngBytes) {
		t.Errorf("artifact bytes differ: got %d want %d", len(got), len(pngBytes))
	}
}

// TestPlanSupersession asserts that creating a new plan for a spec marks
// any prior non-approved plans 'superseded' inside the same transaction.
func TestPlanSupersession(t *testing.T) {
	e := setupTestEnv(t)
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "plan test", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)

	// Two drafts in a row.
	var p1, p2 map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/plans", e.c.token,
		map[string]any{"created_by_role": "human", "steps": []map[string]any{}},
		&p1, http.StatusCreated)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/plans", e.c.token,
		map[string]any{"created_by_role": "human", "steps": []map[string]any{}},
		&p2, http.StatusCreated)

	// Re-read via GET spec; first plan should be superseded.
	var read map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/specs/"+sid, e.c.token, nil, &read, http.StatusOK)
	plans := read["plans"].([]any)
	if len(plans) != 2 {
		t.Fatalf("plans: got %d want 2", len(plans))
	}
	statuses := map[int]string{}
	for _, p := range plans {
		m := p.(map[string]any)
		statuses[int(m["version"].(float64))] = m["status"].(string)
	}
	if statuses[1] != "superseded" {
		t.Errorf("v1 status: got %q want superseded", statuses[1])
	}
	if statuses[2] != "draft" {
		t.Errorf("v2 status: got %q want draft", statuses[2])
	}
}

// TestRecurringFailures inserts repeated failing verifications and asserts
// the steering aggregate surfaces them with the right count.
func TestRecurringFailures(t *testing.T) {
	e := setupTestEnv(t)
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "flaky", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "lint passes", "sensor_kind": "lint"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)
	rid, _ := e.runRunnerThroughValidate(t, sid)

	// Resolve the criterion id.
	var fresh map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/specs/"+sid, e.c.token, nil, &fresh, http.StatusOK)
	critID := fresh["spec"].(map[string]any)["acceptance_criteria"].([]any)[0].(map[string]any)["id"].(string)

	// Three failing verifications for the same (spec, criterion, kind).
	for i := 0; i < 3; i++ {
		e.c.do("POST", "/api/projects/"+e.pid+"/runs/"+rid+"/verifications", e.apiKey,
			map[string]any{
				"criterion_id": critID, "kind": "lint", "class": "computational",
				"status": "fail", "summary": fmt.Sprintf("attempt %d failed", i+1),
			}, nil, http.StatusCreated)
	}

	var fails []map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/steering/recurring-failures",
		e.c.token, nil, &fails, http.StatusOK)
	if len(fails) != 1 {
		t.Fatalf("recurring failures: got %d want 1: %+v", len(fails), fails)
	}
	row := fails[0]
	if int(row["fail_count"].(float64)) != 3 {
		t.Errorf("fail_count: got %v want 3", row["fail_count"])
	}
	if row["criterion_id"] != critID {
		t.Errorf("criterion_id mismatch: got %v want %s", row["criterion_id"], critID)
	}
	if row["criterion_text"] != "lint passes" {
		t.Errorf("criterion_text: got %v", row["criterion_text"])
	}
	if row["last_summary"] != "attempt 3 failed" {
		t.Errorf("last_summary: got %v", row["last_summary"])
	}
}

// TestSSEDeliversEvents subscribes to /events and asserts that creating a
// spec produces a spec.created event on the stream within a short window.
func TestSSEDeliversEvents(t *testing.T) {
	e := setupTestEnv(t)

	url := e.baseURL + "/api/projects/" + e.pid + "/events?token=" + e.c.token
	req, _ := http.NewRequest("GET", url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("subscribe: status %d", res.StatusCode)
	}

	type event struct {
		topic, data string
	}
	events := make(chan event, 8)
	go func() {
		sc := bufio.NewScanner(res.Body)
		var topic, data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				topic = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if topic != "" {
					events <- event{topic: topic, data: data}
					topic, data = "", ""
				}
			}
		}
	}()

	// Drain the connected event.
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive connected event")
	}

	// Create a spec; should fire spec.created.
	go func() {
		e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
			map[string]any{"title": "sse", "intent": "x"}, nil, http.StatusCreated)
	}()
	select {
	case ev := <-events:
		if ev.topic != "spec.created" {
			t.Errorf("topic: got %q want spec.created", ev.topic)
		}
		if !strings.Contains(ev.data, `"title":"sse"`) {
			t.Errorf("data missing title: %q", ev.data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive spec.created within 3s")
	}
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

// testEnv bundles a fresh server + auth + project + API key for the
// follow-on test functions to share. Each call rewrites the DB; tests
// run sequentially by default, so isolation is by destruction.
type testEnv struct {
	c        *apiClient
	pool     *pgxpool.Pool
	baseURL  string
	pid      string
	runnerID string
	apiKey   string // duckllo_<...> bearer for runner-side calls
	srv      *httpapi.Server
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(pool.Close)

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
	t.Cleanup(ts.Close)

	c := &apiClient{t: t, base: ts.URL}

	// Register first user (becomes admin).
	var sess struct{ Token string }
	c.do("POST", "/api/auth/register", "", map[string]any{
		"username": "alice", "password": "alice-secret",
	}, &sess, http.StatusCreated)
	c.token = sess.Token

	// Create a project + mint an API key the runner-side calls will use.
	var project map[string]any
	c.do("POST", "/api/projects", c.token, map[string]any{
		"name": "test", "description": "integration",
	}, &project, http.StatusCreated)
	pid := project["id"].(string)

	var keyResp struct {
		Plain string `json:"plain"`
	}
	c.do("POST", "/api/projects/"+pid+"/api-keys", c.token, map[string]any{
		"label": "test-runner",
	}, &keyResp, http.StatusCreated)

	return &testEnv{
		c: c, pool: pool, baseURL: ts.URL, srv: srv,
		pid: pid, runnerID: "test-runner-1", apiKey: keyResp.Plain,
	}
}

// runRunnerThroughValidate is a helper for tests that need the run to
// reach the validate phase but don't care about the planner+executor
// content. Returns a function that posts a screenshot verification and
// the verification id.
func (e *testEnv) runRunnerThroughValidate(t *testing.T, specID string) (runID, claimedExecPhase string) {
	t.Helper()
	// Start run.
	var run map[string]any
	e.c.do("POST", fmt.Sprintf("/api/projects/%s/specs/%s/runs", e.pid, specID),
		e.c.token, map[string]any{}, &run, http.StatusCreated)
	rid := run["id"].(string)

	// Claim plan, post planner iteration + plan, advance to execute.
	var claim struct {
		WorkItem map[string]any `json:"work_item"`
	}
	e.c.do("POST", fmt.Sprintf("/api/projects/%s/work/claim", e.pid), e.apiKey,
		map[string]any{"runner_id": e.runnerID, "phases": []string{"plan"}},
		&claim, http.StatusOK)
	if claim.WorkItem["phase"] != "plan" {
		t.Fatalf("first claim phase: %v", claim.WorkItem["phase"])
	}
	e.c.do("POST", fmt.Sprintf("/api/projects/%s/runs/%s/iterations", e.pid, rid),
		e.apiKey,
		map[string]any{"phase": "plan", "agent_role": "planner", "summary": "p"},
		nil, http.StatusCreated)
	var plan map[string]any
	e.c.do("POST", fmt.Sprintf("/api/projects/%s/specs/%s/plans", e.pid, specID),
		e.apiKey,
		map[string]any{
			"created_by_role": "planner",
			"steps":           []map[string]any{{"id": "s1", "order": 1, "summary": "x"}},
		}, &plan, http.StatusCreated)
	planID := plan["id"].(string)
	e.c.do("POST", fmt.Sprintf("/api/projects/%s/plans/%s/approve", e.pid, planID),
		e.c.token, nil, nil, http.StatusOK)
	e.c.do("POST", fmt.Sprintf("/api/projects/%s/runs/%s/advance", e.pid, rid),
		e.apiKey,
		map[string]any{"runner_id": e.runnerID, "from_phase": "plan", "to_phase": "execute", "plan_id": planID},
		nil, http.StatusOK)

	// Claim execute, advance to validate.
	e.c.do("POST", fmt.Sprintf("/api/projects/%s/work/claim", e.pid), e.apiKey,
		map[string]any{"runner_id": e.runnerID, "phases": []string{"execute"}},
		&claim, http.StatusOK)
	e.c.do("POST", fmt.Sprintf("/api/projects/%s/runs/%s/iterations", e.pid, rid),
		e.apiKey,
		map[string]any{"phase": "execute", "agent_role": "executor", "summary": "e"},
		nil, http.StatusCreated)
	e.c.do("POST", fmt.Sprintf("/api/projects/%s/runs/%s/advance", e.pid, rid),
		e.apiKey,
		map[string]any{"runner_id": e.runnerID, "from_phase": "execute", "to_phase": "validate"},
		nil, http.StatusOK)

	// Claim validate so the runner is locked on the run.
	e.c.do("POST", fmt.Sprintf("/api/projects/%s/work/claim", e.pid), e.apiKey,
		map[string]any{"runner_id": e.runnerID, "phases": []string{"validate"}},
		&claim, http.StatusOK)
	return rid, claim.WorkItem["phase"].(string)
}

// TestSpecApproval_RequiresAtLeastOneCriterion locks in the gate
// added on approve: a spec with zero acceptance criteria can't be
// approved, because the resulting run would do nothing meaningful
// (validator posts no verifications, judge doesn't fire, run
// auto-advances to validated with no signal).
func TestSpecApproval_RequiresAtLeastOneCriterion(t *testing.T) {
	e := setupTestEnv(t)

	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "empty spec", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)

	// Approve must reject: 400.
	res := e.c.raw("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, body)
	}

	// Add a criterion, retry approve — succeeds.
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "must hold", "sensor_kind": "judge"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)
}

// TestIterationTranscriptRoundTrip locks in the column added in
// migration 007. Posting an iteration with a `transcript` field and
// then GETting the run's iterations must surface the same text back.
// Without this guard a future schema change could silently drop
// transcripts and our dogfood debuggability story would regress.
func TestIterationTranscriptRoundTrip(t *testing.T) {
	e := setupTestEnv(t)

	// Spec + run setup boilerplate.
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "transcript test", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "ok", "sensor_kind": "judge"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)

	var run map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/runs", e.c.token,
		map[string]any{}, &run, http.StatusCreated)
	rid := run["id"].(string)

	// Claim plan phase so the runner has the lock.
	var claim struct{ WorkItem map[string]any `json:"work_item"` }
	e.c.do("POST", "/api/projects/"+e.pid+"/work/claim", e.apiKey,
		map[string]any{"runner_id": e.runnerID, "phases": []string{"plan"}},
		&claim, http.StatusOK)

	transcript := "# System\nyou are tested\n\n# user\nplan it\n\n# assistant\n```json\n{\"steps\":[]}\n```"
	var iter map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/runs/"+rid+"/iterations", e.apiKey,
		map[string]any{
			"phase": "plan", "agent_role": "planner",
			"summary":    "drafted a plan",
			"transcript": transcript,
		}, &iter, http.StatusCreated)

	// Re-read via GET /runs and confirm the transcript came back verbatim.
	var fresh map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/runs/"+rid, e.c.token, nil, &fresh, http.StatusOK)
	iters := fresh["iterations"].([]any)
	if len(iters) != 1 {
		t.Fatalf("iterations: got %d want 1", len(iters))
	}
	got := iters[0].(map[string]any)["transcript"].(string)
	if got != transcript {
		t.Errorf("transcript mismatch:\n got %q\nwant %q", got, transcript)
	}

	// PATCH the transcript and confirm the change persists.
	updated := transcript + "\n\n# tool result (xyz)\nfile written"
	res := e.c.raw("PATCH", "/api/projects/"+e.pid+"/iterations/"+iter["id"].(string),
		e.apiKey, map[string]any{"transcript": updated})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("PATCH iteration: %d %s", res.StatusCode, body)
	}
	e.c.do("GET", "/api/projects/"+e.pid+"/runs/"+rid, e.c.token, nil, &fresh, http.StatusOK)
	iters = fresh["iterations"].([]any)
	got2 := iters[0].(map[string]any)["transcript"].(string)
	if got2 != updated {
		t.Errorf("PATCH didn't update transcript:\n got %q\nwant %q", got2, updated)
	}
}

// TestPlanApproval_AgentCanApproveOwnPlan locks in the fix shipped in
// commit 28ae6eb: an authenticated principal (typically an API-key
// agent acting as the planner) is allowed to approve a plan they
// created. Without this the dogfood loop stalled — runs proceeded with
// unapproved plans because the orchestrator log-and-continued past the
// 403.
func TestPlanApproval_AgentCanApproveOwnPlan(t *testing.T) {
	e := setupTestEnv(t)

	// Spec, draft plan, all via the API key (agent role).
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.apiKey,
		map[string]any{"title": "agent plan test", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)

	var plan map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/plans", e.apiKey,
		map[string]any{"created_by_role": "planner",
			"steps": []map[string]any{{"id": "s1", "order": 1, "summary": "x"}}},
		&plan, http.StatusCreated)
	pid := plan["id"].(string)

	// Agent approves its own plan — must succeed.
	e.c.do("POST", "/api/projects/"+e.pid+"/plans/"+pid+"/approve", e.apiKey,
		nil, nil, http.StatusOK)
}

// TestPEVCFullCorrectionLoop is the end-to-end proof that the harness
// loop actually loops. TestHarnessFlow covers the happy path where
// the validator passes on the first try; this one drives the state
// machine through the *correction* arc:
//
//	Plan → Execute → Validate(fail) → human annotation →
//	Correct → Execute (loop) → Validate(pass) → Done
//
// Asserts at every joint that:
//   - the work queue produces the right phase to claim next
//   - the run.status string mirrors what the dashboard shows the
//     operator (queued / planning / executing / validating / correcting
//     / done)
//   - posting a fix_required annotation is the trigger that re-opens
//     the loop (run flips to 'correcting' and a 'correct' work item is
//     waiting to be claimed)
//   - reaching 'done' requires the second validate pass to actually
//     post 'pass' on every criterion — no shortcut path
//   - the per-iteration history persists in the right order so the
//     run timeline UI has something to render
//
// Without this test the correction loop's seven different state
// transitions could regress one at a time and only TestHarnessFlow's
// happy path would catch them — which never exercises 'correct'.
func TestPEVCFullCorrectionLoop(t *testing.T) {
	e := setupTestEnv(t)
	pid := e.pid
	runnerID := e.runnerID

	// ─── Compose the spec ────────────────────────────────────────────
	// One screenshot criterion: visual sensors are the canonical case
	// where a human reviewer overrides the validator with an
	// annotation, so that's the realistic target for this test.
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+pid+"/specs", e.c.token, map[string]any{
		"title": "Add a header banner", "intent": "Banner across the top of every page.",
	}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "banner is visible at the top of the home page",
			"sensor_kind": "screenshot"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)

	// ─── Enqueue run; expect plan phase pending ──────────────────────
	var run map[string]any
	e.c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/runs", e.c.token,
		map[string]any{}, &run, http.StatusCreated)
	rid := run["id"].(string)
	assertRunStatus(t, e, rid, "queued")

	// ─── Plan phase ──────────────────────────────────────────────────
	claimAndAssertPhase(t, e, runnerID, "plan")
	assertRunStatus(t, e, rid, "planning")

	postIteration(t, e, rid, "plan", "planner", "drafted plan")
	planID := createPlan(t, e, sid, []map[string]any{{"id": "s1", "order": 1, "summary": "add the banner div"}})
	approvePlan(t, e, planID)
	advance(t, e, rid, runnerID, "plan", "execute", "", planID)

	// ─── Execute phase (first time) ──────────────────────────────────
	claimAndAssertPhase(t, e, runnerID, "execute")
	assertRunStatus(t, e, rid, "executing")
	postIteration(t, e, rid, "execute", "executor", "wrote initial banner")
	advance(t, e, rid, runnerID, "execute", "validate", "", "")

	// ─── Validate phase (first time) — verdict is FAIL ───────────────
	claimAndAssertPhase(t, e, runnerID, "validate")
	assertRunStatus(t, e, rid, "validating")
	firstCrit := getFirstCriterion(t, e, sid)
	var verif map[string]any
	e.c.do("POST", "/api/projects/"+pid+"/runs/"+rid+"/verifications", e.apiKey,
		map[string]any{
			"criterion_id": firstCrit["id"],
			"kind":         "screenshot", "class": "computational",
			"status": "fail", "summary": "banner is missing entirely",
			"artifact_url": "/api/uploads/v1.png",
		}, &verif, http.StatusCreated)
	vid := verif["id"].(string)
	// Mirror the real validator orchestrator: post a summary iteration
	// before advancing so the run timeline shows the validate step.
	postIteration(t, e, rid, "validate", "validator", "ran sensors; criterion failed")
	// Validator advances *without* final_status — a fail leaves the run
	// parked in 'validating' for human review (this is the
	// runner-validator behaviour locked in by commit 0befe58).
	advance(t, e, rid, runnerID, "validate", "", "", "")
	assertRunStatus(t, e, rid, "validating")

	// At this point the work_queue should be empty until a human acts.
	if claimNoWork(t, e, runnerID, []string{"plan", "execute", "validate", "correct"}) == false {
		t.Fatalf("expected no work to claim while parked in validating")
	}

	// ─── Human posts fix_required annotation ─────────────────────────
	// This is the correction trigger: server flips run to 'correcting'
	// AND inserts a 'correct' work_queue row atomically.
	e.c.do("POST", "/api/projects/"+pid+"/verifications/"+vid+"/annotations", e.c.token,
		map[string]any{
			"bbox":    map[string]any{"x": 0.1, "y": 0.0, "w": 0.8, "h": 0.1},
			"body":    "banner should be styled with our brand color, not transparent",
			"verdict": "fix_required",
		}, nil, http.StatusCreated)
	assertRunStatus(t, e, rid, "correcting")

	// ─── Correct phase ───────────────────────────────────────────────
	// The corrector's job is to read the open annotations and route
	// the run back to execute with the fix instructions in hand.
	claimAndAssertPhase(t, e, runnerID, "correct")

	// Bundle must surface the open annotation so the corrector agent
	// has it as structured input — without that the corrector has no
	// signal to act on.
	var bundle map[string]any
	e.c.do("GET", "/api/projects/"+pid+"/runs/"+rid+"/bundle", e.apiKey, nil, &bundle, http.StatusOK)
	openAnnos, _ := bundle["open_annotations"].([]any)
	if len(openAnnos) != 1 {
		t.Fatalf("bundle.open_annotations during correct: got %d want 1", len(openAnnos))
	}
	if (openAnnos[0].(map[string]any))["body"] != "banner should be styled with our brand color, not transparent" {
		t.Errorf("annotation body: %v", openAnnos[0])
	}
	postIteration(t, e, rid, "correct", "corrector", "addressed annotation; routing back to execute")
	advance(t, e, rid, runnerID, "correct", "execute", "", "")

	// ─── Execute phase (loop iteration #2) ───────────────────────────
	claimAndAssertPhase(t, e, runnerID, "execute")
	assertRunStatus(t, e, rid, "executing")
	postIteration(t, e, rid, "execute", "executor", "applied the brand-color styling")
	advance(t, e, rid, runnerID, "execute", "validate", "", "")

	// ─── Validate phase (second time) — verdict is PASS ──────────────
	claimAndAssertPhase(t, e, runnerID, "validate")
	e.c.do("POST", "/api/projects/"+pid+"/runs/"+rid+"/verifications", e.apiKey,
		map[string]any{
			"criterion_id": firstCrit["id"],
			"kind":         "screenshot", "class": "computational",
			"status": "pass", "summary": "banner now uses brand color and is positioned correctly",
			"artifact_url": "/api/uploads/v2.png",
		}, nil, http.StatusCreated)
	postIteration(t, e, rid, "validate", "validator", "ran sensors; all criteria passed")
	advance(t, e, rid, runnerID, "validate", "", "done", "")

	// ─── Final assertions ────────────────────────────────────────────
	assertRunStatus(t, e, rid, "done")
	var finalSpec map[string]any
	e.c.do("GET", "/api/projects/"+pid+"/specs/"+sid, e.c.token, nil, &finalSpec, http.StatusOK)
	if got := finalSpec["spec"].(map[string]any)["status"]; got != "validated" {
		t.Errorf("final spec status: got %v want validated", got)
	}

	// The iteration timeline should show the full loop in order:
	// plan, execute, validate, correct, execute, validate. That's
	// proof the runner actually re-entered execute after the
	// annotation, not just that the state machine reached 'done'.
	var detail map[string]any
	e.c.do("GET", "/api/projects/"+pid+"/runs/"+rid, e.c.token, nil, &detail, http.StatusOK)
	iters, _ := detail["iterations"].([]any)
	if len(iters) != 6 {
		t.Fatalf("iteration count: got %d want 6 (plan, execute, validate, correct, execute, validate)", len(iters))
	}
	wantPhases := []string{"plan", "execute", "validate", "correct", "execute", "validate"}
	for i, w := range wantPhases {
		got := iters[i].(map[string]any)["phase"]
		if got != w {
			t.Errorf("iteration %d phase: got %v want %s", i, got, w)
		}
	}
}

// claimAndAssertPhase drives one runner claim and fails the test if the
// returned phase doesn't match. Returns nothing because the test never
// uses the work item's id directly — the runner identity in the
// store keeps things bound through the subsequent advance call.
func claimAndAssertPhase(t *testing.T, e *testEnv, runnerID, want string) {
	t.Helper()
	var claim struct {
		WorkItem map[string]any `json:"work_item"`
	}
	e.c.do("POST", "/api/projects/"+e.pid+"/work/claim", e.apiKey,
		map[string]any{"runner_id": runnerID, "phases": []string{want}},
		&claim, http.StatusOK)
	if got := claim.WorkItem["phase"]; got != want {
		t.Fatalf("claim: got phase=%v want %s", got, want)
	}
}

// claimNoWork returns true if the next claim across all PEVC phases
// returns 204 — i.e. the work queue is genuinely empty for this runner.
func claimNoWork(t *testing.T, e *testEnv, runnerID string, phases []string) bool {
	t.Helper()
	res := e.c.raw("POST", "/api/projects/"+e.pid+"/work/claim", e.apiKey,
		map[string]any{"runner_id": runnerID, "phases": phases})
	defer res.Body.Close()
	return res.StatusCode == http.StatusNoContent
}

func postIteration(t *testing.T, e *testEnv, rid, phase, role, summary string) {
	t.Helper()
	e.c.do("POST", "/api/projects/"+e.pid+"/runs/"+rid+"/iterations", e.apiKey,
		map[string]any{"phase": phase, "agent_role": role, "summary": summary},
		nil, http.StatusCreated)
}

func createPlan(t *testing.T, e *testEnv, sid string, steps []map[string]any) string {
	t.Helper()
	var plan map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/plans", e.apiKey,
		map[string]any{"created_by_role": "planner", "steps": steps},
		&plan, http.StatusCreated)
	return plan["id"].(string)
}

func approvePlan(t *testing.T, e *testEnv, planID string) {
	t.Helper()
	e.c.do("POST", "/api/projects/"+e.pid+"/plans/"+planID+"/approve", e.c.token, nil, nil, http.StatusOK)
}

func advance(t *testing.T, e *testEnv, rid, runnerID, from, to, finalStatus, planID string) {
	t.Helper()
	body := map[string]any{"runner_id": runnerID, "from_phase": from}
	if to != "" {
		body["to_phase"] = to
	}
	if finalStatus != "" {
		body["final_status"] = finalStatus
	}
	if planID != "" {
		body["plan_id"] = planID
	}
	e.c.do("POST", "/api/projects/"+e.pid+"/runs/"+rid+"/advance", e.apiKey,
		body, nil, http.StatusOK)
}

func assertRunStatus(t *testing.T, e *testEnv, rid, want string) {
	t.Helper()
	var detail map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/runs/"+rid, e.c.token, nil, &detail, http.StatusOK)
	got := detail["run"].(map[string]any)["status"]
	if got != want {
		t.Fatalf("run.status: got %v want %s", got, want)
	}
}

func getFirstCriterion(t *testing.T, e *testEnv, sid string) map[string]any {
	t.Helper()
	var fresh map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/specs/"+sid, e.c.token, nil, &fresh, http.StatusOK)
	crits, _ := fresh["spec"].(map[string]any)["acceptance_criteria"].([]any)
	if len(crits) == 0 {
		t.Fatalf("spec %s has no criteria", sid)
	}
	return crits[0].(map[string]any)
}

// TestHarnessRule_PhaseFilter locks in the contract that
// (a) the harness_rules.phases column scopes a rule to specific PEVC
//     phases (empty array = "all phases", preserving the old behaviour),
// (b) the bundle endpoint with ?phase=X only returns rules that match,
// (c) PATCH can update phases.
//
// Without this test the runner could regress to seeing every rule in
// every phase, which means a validate-only judge_prompt would bleed
// into the planner's prompt and the planner would try to emit verdicts.
func TestHarnessRule_PhaseFilter(t *testing.T) {
	e := setupTestEnv(t)
	pid := e.pid

	// Three rules: one all-phases, one validate-only, one plan-only.
	var allRule, validateRule, planRule map[string]any
	e.c.do("POST", "/api/projects/"+pid+"/harness-rules", e.c.token,
		map[string]any{"kind": "agents_md", "name": "all", "body": "every phase"},
		&allRule, http.StatusCreated)
	e.c.do("POST", "/api/projects/"+pid+"/harness-rules", e.c.token,
		map[string]any{"kind": "judge_prompt", "name": "v", "body": "validate text",
			"phases": []string{"validate"}},
		&validateRule, http.StatusCreated)
	e.c.do("POST", "/api/projects/"+pid+"/harness-rules", e.c.token,
		map[string]any{"kind": "agents_md", "name": "p", "body": "plan text",
			"phases": []string{"plan"}},
		&planRule, http.StatusCreated)

	// Build a spec + run so the bundle endpoint has something to point at.
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+pid+"/specs", e.c.token,
		map[string]any{"title": "phase scope test", "intent": "x"},
		&spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "ok", "sensor_kind": "judge"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)
	var run map[string]any
	e.c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/runs", e.c.token,
		map[string]any{}, &run, http.StatusCreated)
	rid := run["id"].(string)

	// Helper: count the rule names returned for a given phase filter.
	bundleNames := func(phase string) []string {
		var b map[string]any
		path := "/api/projects/" + pid + "/runs/" + rid + "/bundle"
		if phase != "" {
			path += "?phase=" + phase
		}
		e.c.do("GET", path, e.apiKey, nil, &b, http.StatusOK)
		raws, _ := b["harness_rules"].([]any)
		out := make([]string, 0, len(raws))
		for _, r := range raws {
			out = append(out, r.(map[string]any)["name"].(string))
		}
		return out
	}

	// No filter — every enabled rule comes back.
	all := bundleNames("")
	if len(all) != 3 {
		t.Fatalf("no-filter bundle: got %d rules, want 3 (%v)", len(all), all)
	}

	// phase=plan — all-rule + plan-only. Validate-only must NOT show up.
	planNames := bundleNames("plan")
	hasV := false
	for _, n := range planNames {
		if n == "v" {
			hasV = true
		}
	}
	if hasV {
		t.Errorf("validate-only rule leaked into the plan bundle: %v", planNames)
	}
	if len(planNames) != 2 {
		t.Errorf("plan-bundle rule count: got %d want 2 (%v)", len(planNames), planNames)
	}

	// phase=validate — all-rule + validate-only.
	valNames := bundleNames("validate")
	hasP := false
	for _, n := range valNames {
		if n == "p" {
			hasP = true
		}
	}
	if hasP {
		t.Errorf("plan-only rule leaked into the validate bundle: %v", valNames)
	}

	// PATCH the planRule to validate-only — phase filter response must update.
	prID := planRule["id"].(string)
	e.c.do("PATCH", "/api/projects/"+pid+"/harness-rules/"+prID, e.c.token,
		map[string]any{"phases": []string{"validate"}}, nil, http.StatusOK)
	planNames2 := bundleNames("plan")
	for _, n := range planNames2 {
		if n == "p" {
			t.Errorf("rule should have moved off the plan phase after PATCH; still in: %v", planNames2)
		}
	}

	// Invalid phase string is rejected at create time.
	res := e.c.raw("POST", "/api/projects/"+pid+"/harness-rules", e.c.token,
		map[string]any{"kind": "agents_md", "name": "bad", "body": "x",
			"phases": []string{"not-a-phase"}})
	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Errorf("invalid phase should be 400; got %d: %s", res.StatusCode, body)
	}
	res.Body.Close()
}

// TestRunPreview_LabeledSegments exercises the new prompt-preview
// endpoint. Asserts that:
//   - GET /preview returns a structured payload with role + system +
//     user-segment list (matches what the UI consumes)
//   - the spec intent appears in a "spec" segment with an edit URL
//     pointing at the spec page
//   - a harness rule shows up as its own segment with source_id and
//     the steering edit URL
//   - phase=validate vs phase=plan returns different rules (proves the
//     bundle phase filter is wired into the preview)
func TestRunPreview_LabeledSegments(t *testing.T) {
	e := setupTestEnv(t)
	pid := e.pid

	// Plan-only rule; should NOT appear when previewing the validate phase.
	var planRule map[string]any
	e.c.do("POST", "/api/projects/"+pid+"/harness-rules", e.c.token,
		map[string]any{"kind": "agents_md", "name": "plan-rule",
			"body": "PLAN-ONLY", "phases": []string{"plan"}},
		&planRule, http.StatusCreated)

	var spec map[string]any
	e.c.do("POST", "/api/projects/"+pid+"/specs", e.c.token,
		map[string]any{"title": "preview test", "intent": "make the thing work"},
		&spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "thing works", "sensor_kind": "judge"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)
	var run map[string]any
	e.c.do("POST", "/api/projects/"+pid+"/specs/"+sid+"/runs", e.c.token,
		map[string]any{}, &run, http.StatusCreated)
	rid := run["id"].(string)

	// preview?phase=plan must include the plan-only rule + the spec.
	var pv map[string]any
	e.c.do("GET", "/api/projects/"+pid+"/runs/"+rid+"/preview?phase=plan", e.c.token, nil, &pv, http.StatusOK)
	if pv["role"] != "planner" {
		t.Errorf("plan phase role: got %v want planner", pv["role"])
	}
	user, _ := pv["user"].([]any)
	hasSpec, hasRule, hasIntent := false, false, false
	for _, raw := range user {
		seg := raw.(map[string]any)
		if seg["source"] == "spec" {
			hasSpec = true
			if !strings.Contains(seg["content"].(string), "make the thing work") {
				t.Errorf("spec segment didn't include the intent text: %v", seg["content"])
			}
			if !strings.Contains(seg["edit_url"].(string), "/specs/"+sid) {
				t.Errorf("spec segment edit_url didn't link to the spec page: %v", seg["edit_url"])
			}
			hasIntent = true
		}
		if seg["source"] == "harness_rule" && seg["source_id"] == planRule["id"] {
			hasRule = true
			if !strings.Contains(seg["edit_url"].(string), "/steering") {
				t.Errorf("harness_rule edit_url should point to /steering: %v", seg["edit_url"])
			}
		}
	}
	if !hasSpec || !hasIntent {
		t.Errorf("missing 'spec' segment in plan-phase preview: %v", user)
	}
	if !hasRule {
		t.Errorf("plan-only rule should appear in plan-phase preview: %v", user)
	}

	// preview?phase=validate must EXCLUDE the plan-only rule.
	var pv2 map[string]any
	e.c.do("GET", "/api/projects/"+pid+"/runs/"+rid+"/preview?phase=validate", e.c.token, nil, &pv2, http.StatusOK)
	if pv2["role"] != "validator" {
		t.Errorf("validate phase role: got %v want validator", pv2["role"])
	}
	user2, _ := pv2["user"].([]any)
	for _, raw := range user2 {
		seg := raw.(map[string]any)
		if seg["source"] == "harness_rule" && seg["source_id"] == planRule["id"] {
			t.Errorf("plan-only rule leaked into validate-phase preview")
		}
	}

	// Invalid phase rejected.
	res := e.c.raw("GET", "/api/projects/"+pid+"/runs/"+rid+"/preview?phase=banana", e.c.token, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid phase should be 400; got %d", res.StatusCode)
	}
	res.Body.Close()
}

// TestProjectBar_SummaryAndPrefs locks in the API contract the new
// project bar consumes. The bar fetches /api/projects/bar on initial
// render to get all projects + each one's prefs + summary in a single
// round trip; on SSE events it re-fetches /api/projects/{pid}/summary
// for just the affected tile. PATCH /prefs flips pin/archive,
// /reorder writes positions in one transaction.
//
// Asserts the counts actually move with state (a draft spec shows up
// in the draft bucket, an approved one moves to approved, a parked
// run shows up under runs_validating) so regressions in the count
// SQL break the test.
func TestProjectBar_SummaryAndPrefs(t *testing.T) {
	e := setupTestEnv(t)

	// Create a second project so we can exercise reorder/pin/archive
	// against more than one tile. setupTestEnv already created the
	// first ("test").
	var projB map[string]any
	e.c.do("POST", "/api/projects", e.c.token,
		map[string]any{"name": "second", "description": "for ordering"}, &projB, http.StatusCreated)
	pidB := projB["id"].(string)

	// /bar fetch: should return both projects with default prefs and
	// empty summaries.
	var bar map[string]any
	e.c.do("GET", "/api/projects/bar", e.c.token, nil, &bar, http.StatusOK)
	tiles, _ := bar["projects"].([]any)
	if len(tiles) != 2 {
		t.Fatalf("bar tiles: got %d want 2", len(tiles))
	}

	// Drive the first project's summary through state changes.
	pidA := e.pid

	// Empty summary first.
	var sum0 map[string]any
	e.c.do("GET", "/api/projects/"+pidA+"/summary", e.c.token, nil, &sum0, http.StatusOK)
	if got := sum0["specs_by_status"].(map[string]any); len(got) != 0 {
		t.Errorf("empty project should have empty specs_by_status; got %v", got)
	}

	// Add a draft spec. summary.specs_by_status.draft should be 1.
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+pidA+"/specs", e.c.token,
		map[string]any{"title": "first", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)

	var sum1 map[string]any
	e.c.do("GET", "/api/projects/"+pidA+"/summary", e.c.token, nil, &sum1, http.StatusOK)
	if got := sum1["specs_by_status"].(map[string]any)["draft"]; got != float64(1) {
		t.Errorf("draft count after creating one spec: got %v", got)
	}

	// Approve it (via criterion add + approve). draft -> approved.
	e.c.do("POST", "/api/projects/"+pidA+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "ok", "sensor_kind": "judge"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+pidA+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)

	var sum2 map[string]any
	e.c.do("GET", "/api/projects/"+pidA+"/summary", e.c.token, nil, &sum2, http.StatusOK)
	specs := sum2["specs_by_status"].(map[string]any)
	if specs["draft"] != nil && specs["draft"].(float64) != 0 {
		t.Errorf("draft should drop to 0 after approve; got %v", specs["draft"])
	}
	if specs["approved"] != float64(1) {
		t.Errorf("approved count: got %v", specs["approved"])
	}

	// Enqueue a run + claim plan phase. Spec moves to running, runs_active=1.
	var run map[string]any
	e.c.do("POST", "/api/projects/"+pidA+"/specs/"+sid+"/runs", e.c.token,
		map[string]any{}, &run, http.StatusCreated)
	e.c.do("POST", "/api/projects/"+pidA+"/work/claim", e.apiKey,
		map[string]any{"runner_id": e.runnerID, "phases": []string{"plan"}},
		nil, http.StatusOK)

	var sum3 map[string]any
	e.c.do("GET", "/api/projects/"+pidA+"/summary", e.c.token, nil, &sum3, http.StatusOK)
	if got := sum3["runs_active"]; got != float64(1) {
		t.Errorf("runs_active during planning: got %v want 1", got)
	}

	// PATCH prefs: pin project A.
	e.c.do("PATCH", "/api/projects/"+pidA+"/prefs", e.c.token,
		map[string]any{"pinned": true}, nil, http.StatusNoContent)

	// /bar should now have project A first (pinned sorts first).
	var bar2 map[string]any
	e.c.do("GET", "/api/projects/bar", e.c.token, nil, &bar2, http.StatusOK)
	tiles2, _ := bar2["projects"].([]any)
	first := tiles2[0].(map[string]any)
	if first["pref"].(map[string]any)["project_id"] != pidA {
		t.Errorf("after pinning A, A should be first; got %v", first["pref"])
	}
	if first["pref"].(map[string]any)["pinned"] != true {
		t.Errorf("A.pinned should be true; got %v", first["pref"])
	}

	// Unpin A, then reorder: B, A.
	e.c.do("PATCH", "/api/projects/"+pidA+"/prefs", e.c.token,
		map[string]any{"pinned": false}, nil, http.StatusNoContent)
	e.c.do("POST", "/api/projects/reorder", e.c.token,
		map[string]any{"project_ids": []string{pidB, pidA}}, nil, http.StatusNoContent)

	var bar3 map[string]any
	e.c.do("GET", "/api/projects/bar", e.c.token, nil, &bar3, http.StatusOK)
	tiles3, _ := bar3["projects"].([]any)
	if tiles3[0].(map[string]any)["pref"].(map[string]any)["project_id"] != pidB {
		t.Errorf("after reorder B-then-A, B should be first; got %v",
			tiles3[0].(map[string]any)["pref"])
	}

	// Archive B. /bar still returns it, but the UI puts archived
	// projects in the overflow menu — server returns the flag, the
	// UI does the partitioning.
	e.c.do("PATCH", "/api/projects/"+pidB+"/prefs", e.c.token,
		map[string]any{"archived": true}, nil, http.StatusNoContent)
	var bar4 map[string]any
	e.c.do("GET", "/api/projects/bar", e.c.token, nil, &bar4, http.StatusOK)
	tiles4, _ := bar4["projects"].([]any)
	var bArch bool
	for _, raw := range tiles4 {
		t := raw.(map[string]any)
		if t["pref"].(map[string]any)["project_id"] == pidB {
			bArch = t["pref"].(map[string]any)["archived"].(bool)
		}
	}
	if !bArch {
		t.Errorf("B should be marked archived in /bar response")
	}
}

// TestSuggest_NoProvider503 covers the cold path: when the server has
// no LLM provider wired (no ANTHROPIC_API_KEY, no claude CLI), both
// /specs/refine and /specs/suggest must return 503 with a clear message
// instead of erroring opaquely. setupTestEnv leaves provider unset by
// default so this is the natural state.
func TestSuggest_NoProvider503(t *testing.T) {
	e := setupTestEnv(t)
	// setupTestEnv may auto-detect a real provider (e.g. the claude CLI
	// on PATH on a developer's machine) — force the no-provider state
	// for this test so the assertion isn't environment-dependent.
	e.srv.SetSuggestProvider(nil)

	for _, path := range []string{"/specs/refine", "/specs/suggest"} {
		res := e.c.raw("POST", "/api/projects/"+e.pid+path, e.c.token,
			map[string]any{"title": "x", "intent": "y"})
		if res.StatusCode != http.StatusServiceUnavailable {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			t.Errorf("%s with no provider: got %d, want 503: %s", path, res.StatusCode, body)
			continue
		}
		res.Body.Close()
	}
}

// TestSuggest_TwoStepFlow walks the full refine → suggest sequence
// against a deterministic scripted provider. Asserts:
//   - /specs/refine returns the refined draft + structured questions
//     (with options) the spec composer's UI consumes
//   - /specs/suggest takes the refined draft + qa pairs and produces
//     well-typed criteria
//   - QA answers actually reach the second-pass prompt (so the user's
//     clarifying answers influence what the model proposes)
//   - invalid sensor_kinds in the model output get filtered out
//     (defence against the model hallucinating a kind we can't run)
func TestSuggest_TwoStepFlow(t *testing.T) {
	e := setupTestEnv(t)

	prov := &scriptedProvider{
		refineJSON: `{
		  "refined_title": "Add a dark-mode theme toggle",
		  "refined_intent": "Let users switch between light and dark themes from the header. The choice persists across reloads. Success means every page renders correctly in both palettes.",
		  "questions": [
		    {"question": "How should the choice persist?", "options": ["Per device, localStorage", "Synced per account"]},
		    {"question": "Is mobile in scope?", "options": ["Yes", "No"]},
		    {"question": "What's the default?", "options": []}
		  ]
		}`,
		criteriaJSON: `{
		  "criteria": [
		    {"text": "header shows a theme toggle button", "sensor_kind": "screenshot"},
		    {"text": "toggle swaps the active theme", "sensor_kind": "e2e_test"},
		    {"text": "choice persists after reload", "sensor_kind": "e2e_test"},
		    {"text": "rationale: contrast and palette", "sensor_kind": "judge"},
		    {"text": "this should be skipped", "sensor_kind": "not_a_real_kind"}
		  ]
		}`,
	}
	e.srv.SetSuggestProvider(prov)

	// Step 1 — refine.
	var refined map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/refine", e.c.token,
		map[string]any{"title": "add dark mode", "intent": "users want a dark theme"},
		&refined, http.StatusOK)
	if got := refined["refined_title"]; got != "Add a dark-mode theme toggle" {
		t.Errorf("refined_title: got %v", got)
	}
	qs, ok := refined["questions"].([]any)
	if !ok || len(qs) != 3 {
		t.Fatalf("questions: got %v want 3 entries", refined["questions"])
	}
	q0 := qs[0].(map[string]any)
	if q0["question"] != "How should the choice persist?" {
		t.Errorf("first question text: got %v", q0["question"])
	}
	opts, _ := q0["options"].([]any)
	if len(opts) != 2 || opts[0] != "Per device, localStorage" {
		t.Errorf("first question options: got %v", opts)
	}

	// The third question deliberately has no options — assert UI gets
	// an empty array, not null, so its renderer doesn't NPE.
	q2 := qs[2].(map[string]any)
	emptyOpts, _ := q2["options"].([]any)
	if len(emptyOpts) != 0 {
		t.Errorf("open-ended question should have empty options; got %v", emptyOpts)
	}

	// Step 2 — suggest, with the user's answers folded in as qa.
	var sugg map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/suggest", e.c.token,
		map[string]any{
			"title":  refined["refined_title"],
			"intent": refined["refined_intent"],
			"qa": []map[string]any{
				{"q": "How should the choice persist?", "a": "Per device, localStorage"},
				{"q": "Is mobile in scope?", "a": "Yes"},
				{"q": "What's the default?", "a": "follow OS preference"},
			},
		}, &sugg, http.StatusOK)

	crits, _ := sugg["criteria"].([]any)
	// Five returned, one (not_a_real_kind) is filtered out → 4 left.
	if len(crits) != 4 {
		t.Fatalf("criteria count: got %d want 4 (one invalid kind should be dropped)", len(crits))
	}
	first := crits[0].(map[string]any)
	if first["sensor_kind"] != "screenshot" || first["text"] != "header shows a theme toggle button" {
		t.Errorf("first criterion: got %v", first)
	}

	// Two requests captured: one with the refine system prompt, one
	// with the criteria system prompt. The criteria call's user text
	// must include the user's clarifying answers — otherwise the
	// two-step flow has no point.
	if len(prov.captured) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(prov.captured))
	}
	if !strings.Contains(prov.captured[0].System, "REFINE") {
		t.Errorf("first call should be the refine pass; got system=%q",
			prov.captured[0].System[:min(80, len(prov.captured[0].System))])
	}
	criteriaUser := prov.captured[1].Messages[0].Content
	if !strings.Contains(criteriaUser, "Per device, localStorage") {
		t.Errorf("criteria user prompt did not include the user's QA answer; got:\n%s", criteriaUser)
	}
	if !strings.Contains(criteriaUser, "follow OS preference") {
		t.Errorf("criteria user prompt missing free-text answer for open-ended question:\n%s", criteriaUser)
	}
}

// TestSpecContract_FrozenAfterApproval enforces the selfhost rule
// "Spec contract is locked once approved" at the API boundary. Once a
// spec is past 'proposed' (i.e. approved/running/validated/etc.) the
// contract — intent, acceptance criteria, reference assets, affected
// components — must not be mutated. Otherwise an in-flight run is
// evaluated against a different target than the one it was started
// against, which silently invalidates whatever the model produced.
// Title / priority / assignee remain editable as organisational metadata.
func TestSpecContract_FrozenAfterApproval(t *testing.T) {
	e := setupTestEnv(t)

	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "frozen test", "intent": "v1"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "v1 criterion", "sensor_kind": "judge"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)

	// PATCH intent post-approval → 409.
	res := e.c.raw("PATCH", "/api/projects/"+e.pid+"/specs/"+sid, e.c.token,
		map[string]any{"intent": "v2"})
	if res.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("intent edit post-approval: got %d: %s", res.StatusCode, body)
	}
	res.Body.Close()

	// Append criterion post-approval → 409.
	res2 := e.c.raw("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "second criterion", "sensor_kind": "judge"})
	if res2.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(res2.Body)
		res2.Body.Close()
		t.Fatalf("criterion add post-approval: got %d: %s", res2.StatusCode, body)
	}
	res2.Body.Close()

	// Title PATCH post-approval is still allowed (organisational metadata).
	e.c.do("PATCH", "/api/projects/"+e.pid+"/specs/"+sid, e.c.token,
		map[string]any{"title": "renamed"}, nil, http.StatusOK)
}

// TestEnqueueRun_GatedOnApprovedStatus locks in the atomic gate added
// to EnqueueRun. Prior to the fix, POST /specs/{sid}/runs would happily
// create a second run on an already-running spec — two runners would
// then race against the same workspace and post conflicting
// verifications. It would also accept a run on a draft spec, which is
// nonsensical because the criteria contract isn't frozen yet. The fix
// gates the spec → run transition with
//
//	UPDATE specs SET status='running' WHERE id=$1 AND status='approved'
//
// inside the EnqueueRun transaction. Concurrent or repeat callers see
// 0 rows and get ErrSpecNotEnqueueable → HTTP 400.
func TestEnqueueRun_GatedOnApprovedStatus(t *testing.T) {
	e := setupTestEnv(t)

	// Draft spec must not be enqueueable.
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "gate test", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	res := e.c.raw("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/runs", e.c.token, map[string]any{})
	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("draft-spec run: got %d: %s", res.StatusCode, body)
	}
	res.Body.Close()

	// Approve and the first run goes through.
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "ok", "sensor_kind": "judge"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/runs", e.c.token,
		map[string]any{}, nil, http.StatusCreated)

	// A second run on the now-running spec must be rejected.
	res2 := e.c.raw("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/runs", e.c.token, map[string]any{})
	if res2.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res2.Body)
		res2.Body.Close()
		t.Fatalf("second run on running spec: got %d: %s", res2.StatusCode, body)
	}
	res2.Body.Close()
}

// TestClaimWork_RespectsPhaseFilterForPendingItems locks in the SQL
// precedence fix on ClaimWork. The original WHERE clause was
//
//	status = 'pending'
//	   OR (status = 'claimed' AND lock_expires_at < NOW())
//	  AND ($2::text[] IS NULL OR phase = ANY($2::text[]))
//
// SQL binds AND tighter than OR, so the phase filter only constrained
// the stale-claimed branch — a runner asking for phases=['execute']
// could (and would, deterministically) snatch a pending phase='plan'
// item, which then made the whole protocol go sideways (planner code
// runs against an executor's claim). This test creates a fresh run
// (which seeds a 'plan' pending row), then asks for phases=['execute']
// and asserts 204 No Content. Without the fix the claim returns 200
// with a phase='plan' item.
func TestClaimWork_RespectsPhaseFilterForPendingItems(t *testing.T) {
	e := setupTestEnv(t)

	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "phase filter", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "ok", "sensor_kind": "judge"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/runs", e.c.token,
		map[string]any{}, nil, http.StatusCreated)

	// A runner that only wants 'execute' phases must NOT claim the
	// pending 'plan' row this run created.
	res := e.c.raw("POST", "/api/projects/"+e.pid+"/work/claim", e.apiKey,
		map[string]any{"runner_id": "executor-only", "phases": []string{"execute"}})
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("execute-only claim should be 204; got %d: %s", res.StatusCode, body)
	}

	// And conversely: a planner-scoped claim DOES pick it up.
	var claim struct {
		WorkItem map[string]any `json:"work_item"`
	}
	e.c.do("POST", "/api/projects/"+e.pid+"/work/claim", e.apiKey,
		map[string]any{"runner_id": "planner-only", "phases": []string{"plan"}},
		&claim, http.StatusOK)
	if claim.WorkItem["phase"] != "plan" {
		t.Errorf("planner claim picked %v, want plan", claim.WorkItem["phase"])
	}
}

// TestAbortRun_CleansUpWorkQueueAndSpec locks in the fix where AbortRun
// only flipped run.status='aborted' and left two states inconsistent:
// (1) any pending/claimed work_queue rows for the run sat there forever,
// so a fresh runner could keep claiming phases for an aborted run, and
// (2) the parent spec stayed 'running' so the UI showed it as live and
// you couldn't enqueue a new run without manual surgery. After the fix
// abort must close out work_queue, drop spec.status back to 'approved',
// and reject a second abort with 400.
func TestAbortRun_CleansUpWorkQueueAndSpec(t *testing.T) {
	e := setupTestEnv(t)

	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "abort cleanup", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/criteria", e.c.token,
		map[string]any{"text": "ok", "sensor_kind": "judge"}, nil, http.StatusOK)
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/approve", e.c.token, nil, nil, http.StatusOK)

	var run map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/runs", e.c.token,
		map[string]any{}, &run, http.StatusCreated)
	rid := run["id"].(string)

	// Claim plan so a work_queue row is in 'claimed' state, then leak it
	// (don't advance) — exactly the situation a runner crash creates.
	var claim struct {
		WorkItem map[string]any `json:"work_item"`
	}
	e.c.do("POST", "/api/projects/"+e.pid+"/work/claim", e.apiKey,
		map[string]any{"runner_id": e.runnerID, "phases": []string{"plan"}},
		&claim, http.StatusOK)

	// Spec must currently be 'running' (set by EnqueueRun).
	var specBefore map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/specs/"+sid, e.c.token, nil, &specBefore, http.StatusOK)
	if got := specBefore["spec"].(map[string]any)["status"]; got != "running" {
		t.Fatalf("pre-abort spec status: got %v want running", got)
	}

	// Abort. Must publish the new state via the run resource.
	res := e.c.raw("POST", "/api/projects/"+e.pid+"/runs/"+rid+"/abort", e.c.token, nil)
	if res.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("abort: got %d: %s", res.StatusCode, body)
	}
	res.Body.Close()

	// Run.status = aborted.
	var runAfter map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/runs/"+rid, e.c.token, nil, &runAfter, http.StatusOK)
	if got := runAfter["run"].(map[string]any)["status"]; got != "aborted" {
		t.Fatalf("post-abort run status: got %v want aborted", got)
	}

	// Spec dropped back to 'approved' so a new run can be enqueued.
	var specAfter map[string]any
	e.c.do("GET", "/api/projects/"+e.pid+"/specs/"+sid, e.c.token, nil, &specAfter, http.StatusOK)
	if got := specAfter["spec"].(map[string]any)["status"]; got != "approved" {
		t.Errorf("post-abort spec status: got %v want approved", got)
	}

	// Work queue is drained: a fresh claim across all phases returns 204.
	res2 := e.c.raw("POST", "/api/projects/"+e.pid+"/work/claim", e.apiKey,
		map[string]any{"runner_id": "fresh-runner",
			"phases": []string{"plan", "execute", "validate", "correct"}})
	if res2.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(res2.Body)
		res2.Body.Close()
		t.Errorf("post-abort claim should be 204; got %d: %s", res2.StatusCode, body)
	}
	res2.Body.Close()

	// Second abort on already-terminal run returns 400, not a silent 204.
	res3 := e.c.raw("POST", "/api/projects/"+e.pid+"/runs/"+rid+"/abort", e.c.token, nil)
	if res3.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res3.Body)
		res3.Body.Close()
		t.Errorf("second abort should be 400; got %d: %s", res3.StatusCode, body)
	}
	res3.Body.Close()
}

// TestPlanApproval_AgentCannotApproveOthersPlan asserts the negative
// case: an API-key agent shouldn't be able to approve a plan someone
// else created. The product-manager gate stays on for plans you didn't
// author.
func TestPlanApproval_AgentCannotApproveOthersPlan(t *testing.T) {
	e := setupTestEnv(t)

	// Plan created by gin (session token).
	var spec map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs", e.c.token,
		map[string]any{"title": "gin's spec", "intent": "x"}, &spec, http.StatusCreated)
	sid := spec["id"].(string)
	var plan map[string]any
	e.c.do("POST", "/api/projects/"+e.pid+"/specs/"+sid+"/plans", e.c.token,
		map[string]any{"created_by_role": "human",
			"steps": []map[string]any{{"id": "s1", "order": 1, "summary": "x"}}},
		&plan, http.StatusCreated)
	planID := plan["id"].(string)

	// Mint a *separate* user + key whose role on the project is just
	// "agent" (added via the project members endpoint as a regular
	// developer-equivalent? Actually our API keys come with role=agent
	// inherently, so we just use the existing e.apiKey).
	//
	// e.apiKey is owned by alice (the same gin-equivalent admin since
	// alice is the first registered user). To get a real "agent
	// approving someone else's plan" scenario we need a separate user.
	// Register a second user "bob" who is a member of the project but
	// not the plan's creator, and have him try to approve via API key.
	var bobSess struct{ Token string }
	e.c.do("POST", "/api/auth/register", "", map[string]any{
		"username": "bob", "password": "bob-secret",
	}, &bobSess, http.StatusCreated)

	// alice (admin) adds bob to the project as a developer (not PM).
	e.c.do("POST", "/api/projects/"+e.pid+"/members", e.c.token,
		map[string]any{"username": "bob", "role": "developer"}, nil, http.StatusNoContent)

	// bob tries to approve alice's plan via session — should be 403.
	res := e.c.raw("POST", "/api/projects/"+e.pid+"/plans/"+planID+"/approve", bobSess.Token, nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 403, got %d: %s", res.StatusCode, body)
	}
}
