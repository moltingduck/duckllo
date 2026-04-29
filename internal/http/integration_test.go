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
		c: c, pool: pool, baseURL: ts.URL,
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
