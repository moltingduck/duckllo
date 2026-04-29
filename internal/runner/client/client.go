// Package client is the runner's HTTP wrapper around the duckllo API.
// It is intentionally small — the runner only needs a handful of
// endpoints (claim, bundle, post iteration, post verification, advance,
// heartbeat). Models are duplicated rather than imported so the runner
// can ship as a separate binary in the future without pulling the server
// module along.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

var _ = errors.New // keep imports stable when SDK churn drops one

type Client struct {
	BaseURL   string
	APIKey    string
	ProjectID uuid.UUID
	HTTP      *http.Client
}

func New(baseURL, apiKey string, projectID uuid.UUID) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		APIKey:    apiKey,
		ProjectID: projectID,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	contentType := ""
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusNoContent {
		return ErrNoWork
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, strings.TrimSpace(string(rb)))
	}
	if out != nil && len(rb) > 0 {
		return json.Unmarshal(rb, out)
	}
	return nil
}

// uploadMultipart wraps the duckllo verifications endpoint, which accepts
// either application/json (no artifact) or multipart with a "file" + "meta"
// JSON. We always use multipart for screenshot/gif so the runner has one
// codepath.
func (c *Client) uploadMultipart(ctx context.Context, path string, meta any, fileName, contentType string, fileBody io.Reader, out any) error {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)

	if meta != nil {
		mb, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		if err := mw.WriteField("meta", string(mb)); err != nil {
			return err
		}
	}
	if fileBody != nil {
		mh := make(map[string][]string)
		mh["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="file"; filename=%q`, fileName)}
		if contentType != "" {
			mh["Content-Type"] = []string{contentType}
		}
		fw, err := mw.CreatePart(mh)
		if err != nil {
			return err
		}
		if _, err := io.Copy(fw, fileBody); err != nil {
			return err
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return fmt.Errorf("POST %s: %d %s", path, res.StatusCode, strings.TrimSpace(string(rb)))
	}
	if out != nil && len(rb) > 0 {
		return json.Unmarshal(rb, out)
	}
	return nil
}

// ErrNoWork is returned by Claim when the queue is empty.
var ErrNoWork = errors.New("no work available")

// ─── Wire types (duplicated, intentionally minimal) ─────────────────────

type ClaimRequest struct {
	RunnerID string   `json:"runner_id"`
	Phases   []string `json:"phases,omitempty"`
}

type ClaimResponse struct {
	WorkItem WorkItem `json:"work_item"`
	Run      Run      `json:"run"`
}

type WorkItem struct {
	ID            uuid.UUID  `json:"id"`
	RunID         uuid.UUID  `json:"run_id"`
	Phase         string     `json:"phase"`
	Status        string     `json:"status"`
	ClaimedBy     string     `json:"claimed_by,omitempty"`
	ClaimedAt     *time.Time `json:"claimed_at,omitempty"`
	LockExpiresAt *time.Time `json:"lock_expires_at,omitempty"`
	Attempts      int        `json:"attempts"`
}

type Run struct {
	ID          uuid.UUID `json:"id"`
	SpecID      uuid.UUID `json:"spec_id"`
	PlanID      uuid.UUID `json:"plan_id"`
	Status      string    `json:"status"`
	TurnBudget  int       `json:"turn_budget"`
	TurnsUsed   int       `json:"turns_used"`
	TokenUsage  int       `json:"token_usage"`
}

type Spec struct {
	ID                 uuid.UUID       `json:"id"`
	ProjectID          uuid.UUID       `json:"project_id"`
	TopologyID         *uuid.UUID      `json:"topology_id,omitempty"`
	Title              string          `json:"title"`
	Intent             string          `json:"intent"`
	Status             string          `json:"status"`
	AcceptanceCriteria json.RawMessage `json:"acceptance_criteria"`
	ReferenceAssets    json.RawMessage `json:"reference_assets"`
}

type Plan struct {
	ID    uuid.UUID       `json:"id"`
	Steps json.RawMessage `json:"steps"`
}

type Iteration struct {
	ID  uuid.UUID `json:"id"`
	Idx int       `json:"idx"`
}

type Verification struct {
	ID   uuid.UUID `json:"id"`
	Kind string    `json:"kind"`
}

type Annotation struct {
	ID             uuid.UUID       `json:"id"`
	VerificationID uuid.UUID       `json:"verification_id"`
	BBox           json.RawMessage `json:"bbox"`
	Body           string          `json:"body"`
	Verdict        string          `json:"verdict"`
	Resolved       bool            `json:"resolved"`
}

type HarnessRule struct {
	ID         uuid.UUID  `json:"id"`
	TopologyID *uuid.UUID `json:"topology_id,omitempty"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name"`
	Body       string     `json:"body"`
	Enabled    bool       `json:"enabled"`
}

type Bundle struct {
	Run             Run            `json:"run"`
	Spec            Spec           `json:"spec"`
	Plan            Plan           `json:"plan"`
	HarnessRules    []HarnessRule  `json:"harness_rules"`
	Iterations      []Iteration    `json:"iterations"`
	Verifications   []Verification `json:"verifications"`
	OpenAnnotations []Annotation   `json:"open_annotations"`
}

// ─── Endpoints ──────────────────────────────────────────────────────────

func (c *Client) Claim(ctx context.Context, runnerID string, phases []string) (*ClaimResponse, error) {
	var out ClaimResponse
	err := c.do(ctx, "POST", c.projectPath("/work/claim"),
		ClaimRequest{RunnerID: runnerID, Phases: phases}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Heartbeat(ctx context.Context, runID uuid.UUID, runnerID string) error {
	return c.do(ctx, "POST", c.projectPath(fmt.Sprintf("/runs/%s/heartbeat", runID)),
		map[string]string{"runner_id": runnerID}, nil)
}

type AdvanceRequest struct {
	RunnerID    string `json:"runner_id"`
	FromPhase   string `json:"from_phase"`
	ToPhase     string `json:"to_phase,omitempty"`
	FinalStatus string `json:"final_status,omitempty"`
	PlanID      string `json:"plan_id,omitempty"`
}

func (c *Client) Advance(ctx context.Context, runID uuid.UUID, req AdvanceRequest) error {
	return c.do(ctx, "POST", c.projectPath(fmt.Sprintf("/runs/%s/advance", runID)), req, nil)
}

func (c *Client) Bundle(ctx context.Context, runID uuid.UUID) (*Bundle, error) {
	var b Bundle
	err := c.do(ctx, "GET", c.projectPath(fmt.Sprintf("/runs/%s/bundle", runID)), nil, &b)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

type PostIterationReq struct {
	Phase         string `json:"phase"`
	AgentRole     string `json:"agent_role"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Summary       string `json:"summary"`
	TranscriptURL string `json:"transcript_url"`
}

func (c *Client) PostIteration(ctx context.Context, runID uuid.UUID, req PostIterationReq) (*Iteration, error) {
	var it Iteration
	err := c.do(ctx, "POST", c.projectPath(fmt.Sprintf("/runs/%s/iterations", runID)), req, &it)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

type PatchIterationReq struct {
	Summary          *string `json:"summary,omitempty"`
	PromptTokens     *int    `json:"prompt_tokens,omitempty"`
	CompletionTokens *int    `json:"completion_tokens,omitempty"`
	Status           *string `json:"status,omitempty"`
}

func (c *Client) PatchIteration(ctx context.Context, iterID uuid.UUID, req PatchIterationReq) error {
	return c.do(ctx, "PATCH", c.projectPath(fmt.Sprintf("/iterations/%s", iterID)), req, nil)
}

type PostVerificationReq struct {
	IterationID string         `json:"iteration_id,omitempty"`
	CriterionID string         `json:"criterion_id,omitempty"`
	Kind        string         `json:"kind"`
	Class       string         `json:"class"`
	Direction   string         `json:"direction,omitempty"`
	Status      string         `json:"status"`
	Summary     string         `json:"summary"`
	ArtifactURL string         `json:"artifact_url,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

func (c *Client) PostVerification(ctx context.Context, runID uuid.UUID, req PostVerificationReq) (*Verification, error) {
	var v Verification
	err := c.do(ctx, "POST", c.projectPath(fmt.Sprintf("/runs/%s/verifications", runID)), req, &v)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (c *Client) PostVerificationWithArtifact(ctx context.Context, runID uuid.UUID, meta PostVerificationReq, fileName, contentType string, body io.Reader) (*Verification, error) {
	var v Verification
	err := c.uploadMultipart(ctx, c.projectPath(fmt.Sprintf("/runs/%s/verifications", runID)), meta, fileName, contentType, body, &v)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

type CreatePlanReq struct {
	CreatedByRole string           `json:"created_by_role"`
	Steps         []map[string]any `json:"steps"`
}

func (c *Client) CreatePlan(ctx context.Context, specID uuid.UUID, req CreatePlanReq) (*Plan, error) {
	var p Plan
	err := c.do(ctx, "POST", c.projectPath(fmt.Sprintf("/specs/%s/plans", specID)), req, &p)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *Client) ApprovePlan(ctx context.Context, planID uuid.UUID) error {
	return c.do(ctx, "POST", c.projectPath(fmt.Sprintf("/plans/%s/approve", planID)), nil, nil)
}

func (c *Client) projectPath(suffix string) string {
	return fmt.Sprintf("/api/projects/%s%s", c.ProjectID, suffix)
}

// SetWorkspaceMeta records the runner's workspace identifiers (container
// id, network id, dev URL, worktree path) on the run. Called after
// Docker provisioning succeeds.
func (c *Client) SetWorkspaceMeta(ctx context.Context, runID uuid.UUID, meta map[string]any) error {
	return c.do(ctx, "POST", c.projectPath(fmt.Sprintf("/runs/%s/workspace", runID)), meta, nil)
}

// FetchArtifact GETs an artifact (typically a screenshot baseline) by URL.
// Accepts either an absolute URL or a server-relative path like
// /api/uploads/<uuid>.png — the latter is what verification rows store.
func (c *Client) FetchArtifact(ctx context.Context, urlOrPath string) ([]byte, error) {
	full := urlOrPath
	if strings.HasPrefix(urlOrPath, "/") {
		full = c.BaseURL + urlOrPath
	}
	req, err := http.NewRequestWithContext(ctx, "GET", full, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch %s: %d", urlOrPath, res.StatusCode)
	}
	return io.ReadAll(res.Body)
}
