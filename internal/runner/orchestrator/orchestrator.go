// Package orchestrator drives a single PEVC iteration end-to-end. The
// runner main loop hands it a claimed work item and the orchestrator
// returns when the iteration has been posted and the run advanced.
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/runner/agent"
	"github.com/moltingduck/duckllo/internal/runner/client"
	"github.com/moltingduck/duckllo/internal/runner/tools"
	"github.com/moltingduck/duckllo/internal/sensors"
)

type Orchestrator struct {
	Client     *client.Client
	Provider   agent.Provider
	Sandbox    *tools.Sandbox
	Sensors    *sensors.Registry
	RunnerID   string
	MaxTurns   int
	DevURL     string // base URL the screenshot sensor should hit
	ChromePath string // optional override for chromedp
	Workspace  string
}

// Run performs one phase of work for a claimed run+work_item.
func (o *Orchestrator) Run(ctx context.Context, work *client.WorkItem, run *client.Run) error {
	bundle, err := o.Client.Bundle(ctx, work.RunID)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}

	switch work.Phase {
	case "plan":
		return o.runPlanner(ctx, work, bundle)
	case "execute":
		return o.runExecutor(ctx, work, bundle)
	case "validate":
		return o.runValidator(ctx, work, bundle)
	case "correct":
		return o.runCorrector(ctx, work, bundle)
	default:
		return fmt.Errorf("unknown phase %q", work.Phase)
	}
}

// runPlanner asks the model for a JSON plan, posts a new plan revision,
// approves it, and advances to execute.
func (o *Orchestrator) runPlanner(ctx context.Context, work *client.WorkItem, b *client.Bundle) error {
	resp, err := o.Provider.Complete(ctx, agent.Request{
		System:   systemPromptFor("planner"),
		Messages: []agent.Message{{Role: "user", Content: userPromptFor("planner", b)}},
	})
	if err != nil {
		return fmt.Errorf("planner inference: %w", err)
	}
	raw, err := extractJSONBlock(resp.Text)
	if err != nil {
		return fmt.Errorf("planner output: %w (got %d chars)", err, len(resp.Text))
	}
	var parsed struct {
		Steps []map[string]any `json:"steps"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return fmt.Errorf("planner json: %w", err)
	}
	if len(parsed.Steps) == 0 {
		return fmt.Errorf("planner returned no steps")
	}

	plan, err := o.Client.CreatePlan(ctx, b.Spec.ID, client.CreatePlanReq{
		CreatedByRole: "planner", Steps: parsed.Steps,
	})
	if err != nil {
		return fmt.Errorf("create plan: %w", err)
	}
	if err := o.Client.ApprovePlan(ctx, plan.ID); err != nil {
		log.Printf("planner: approve failed (will need human approval): %v", err)
	}
	if _, err := o.postIteration(ctx, work.RunID, "plan", "planner",
		fmt.Sprintf("Drafted plan with %d steps", len(parsed.Steps)), resp); err != nil {
		return err
	}
	// Bind the new plan + transition to execute atomically.
	return o.Client.Advance(ctx, work.RunID, client.AdvanceRequest{
		RunnerID: o.RunnerID, FromPhase: "plan", ToPhase: "execute",
		PlanID: plan.ID.String(),
	})
}

// runExecutor runs an inference loop: the model can emit tool calls; we
// execute them in the sandbox and feed back tool_results until the model
// returns a final text reply or we hit MaxTurns.
func (o *Orchestrator) runExecutor(ctx context.Context, work *client.WorkItem, b *client.Bundle) error {
	tools := o.Sandbox.Defs()
	msgs := []agent.Message{{Role: "user", Content: userPromptFor("executor", b)}}

	maxTurns := o.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 12
	}

	var lastResp *agent.Response
	for turn := 0; turn < maxTurns; turn++ {
		resp, err := o.Provider.Complete(ctx, agent.Request{
			System: systemPromptFor("executor"), Messages: msgs, Tools: tools,
		})
		if err != nil {
			return fmt.Errorf("executor turn %d: %w", turn, err)
		}
		lastResp = resp

		// Append the assistant turn (text + tool_use) to the conversation.
		msgs = append(msgs, agent.Message{
			Role: "assistant", Content: resp.Text, Tools: resp.ToolCalls,
		})

		if len(resp.ToolCalls) == 0 {
			break // model finished
		}
		for _, tc := range resp.ToolCalls {
			result := o.Sandbox.Execute(ctx, tc)
			msgs = append(msgs, agent.Message{
				Role: "tool", ToolID: tc.ID, Content: result,
			})
		}
	}

	summary := "Executor completed"
	if lastResp != nil && lastResp.Text != "" {
		summary = trimSummary(lastResp.Text, 280)
	}
	if _, err := o.postIteration(ctx, work.RunID, "execute", "executor", summary, lastResp); err != nil {
		return err
	}
	return o.Client.Advance(ctx, work.RunID, client.AdvanceRequest{
		RunnerID: o.RunnerID, FromPhase: "execute", ToPhase: "validate",
	})
}

// runValidator fires every sensor matching the criterion kinds, posts a
// verification per criterion, and additionally calls the inferential
// judge once across all judge-kind criteria.
func (o *Orchestrator) runValidator(ctx context.Context, work *client.WorkItem, b *client.Bundle) error {
	var criteria []sensors.Criterion
	if err := json.Unmarshal(b.Spec.AcceptanceCriteria, &criteria); err != nil {
		return fmt.Errorf("decode criteria: %w", err)
	}

	env := sensors.Env{
		WorkspaceDir: o.Workspace,
		DevURL:       o.DevURL,
		ChromePath:   o.ChromePath,
		LogF:         func(f string, args ...any) { log.Printf(f, args...) },
	}

	deterministicFired := 0
	for _, c := range criteria {
		if c.SensorKind == "judge" || c.SensorKind == "manual" {
			continue // judge is handled below; manual stays human-driven.
		}
		s := o.Sensors.For(c.SensorKind)
		if s == nil {
			_, _ = o.Client.PostVerification(ctx, work.RunID, client.PostVerificationReq{
				CriterionID: c.ID, Kind: c.SensorKind, Class: "computational",
				Status: "skipped", Summary: "no sensor implementation for kind " + c.SensorKind,
			})
			continue
		}
		res, err := s.Run(ctx, c, env)
		if err != nil {
			_, _ = o.Client.PostVerification(ctx, work.RunID, client.PostVerificationReq{
				CriterionID: c.ID, Kind: c.SensorKind, Class: "computational",
				Status: "fail", Summary: "sensor error: " + err.Error(),
			})
			continue
		}
		if err := o.postSensorResult(ctx, work.RunID, c, res); err != nil {
			log.Printf("validator: post sensor: %v", err)
		}
		deterministicFired++
	}

	// Judge: aggregated single LLM pass over criteria of kind=judge.
	hasJudge := false
	for _, c := range criteria {
		if c.SensorKind == "judge" {
			hasJudge = true
			break
		}
	}
	var judgeResp *agent.Response
	if hasJudge {
		resp, err := o.Provider.Complete(ctx, agent.Request{
			System:   systemPromptFor("validator"),
			Messages: []agent.Message{{Role: "user", Content: userPromptFor("validator", b)}},
		})
		if err != nil {
			log.Printf("judge inference: %v", err)
		} else {
			judgeResp = resp
			raw, perr := extractJSONBlock(resp.Text)
			if perr != nil {
				_, _ = o.Client.PostVerification(ctx, work.RunID, client.PostVerificationReq{
					Kind: "judge", Class: "inferential", Status: "warn",
					Summary: "validator output missing JSON block",
				})
			} else {
				var parsed struct {
					Verdicts []struct {
						CriterionID string `json:"criterion_id"`
						Status      string `json:"status"`
						Summary     string `json:"summary"`
					} `json:"verdicts"`
				}
				if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
					for _, v := range parsed.Verdicts {
						_, _ = o.Client.PostVerification(ctx, work.RunID, client.PostVerificationReq{
							CriterionID: v.CriterionID, Kind: "judge", Class: "inferential",
							Status: v.Status, Summary: v.Summary,
						})
					}
				}
			}
		}
	}

	summary := fmt.Sprintf("Ran %d deterministic sensors", deterministicFired)
	if hasJudge {
		summary += " + judge pass"
	}
	if _, err := o.postIteration(ctx, work.RunID, "validate", "validator", summary, judgeResp); err != nil {
		return err
	}

	// Default behaviour: stop after one validation cycle. Humans review the
	// sensor grid; a fix_required annotation flips the run into
	// 'correcting' (see store.CreateAnnotation) and the corrector phase
	// claim becomes available on the next runner poll.
	return o.Client.Advance(ctx, work.RunID, client.AdvanceRequest{
		RunnerID: o.RunnerID, FromPhase: "validate", FinalStatus: "done",
	})
}

// postSensorResult uploads the artifact (if any) and posts the verification.
func (o *Orchestrator) postSensorResult(ctx context.Context, runID uuid.UUID, c sensors.Criterion, res *sensors.Result) error {
	meta := client.PostVerificationReq{
		CriterionID: c.ID, Kind: c.SensorKind, Class: res.Class,
		Status: res.Status, Summary: res.Summary, Details: res.Details,
	}
	if len(res.ArtifactBytes) > 0 {
		_, err := o.Client.PostVerificationWithArtifact(
			ctx, runID, meta, res.FileName, res.ContentType, bytes.NewReader(res.ArtifactBytes),
		)
		return err
	}
	_, err := o.Client.PostVerification(ctx, runID, meta)
	return err
}

func (o *Orchestrator) runCorrector(ctx context.Context, work *client.WorkItem, b *client.Bundle) error {
	resp, err := o.Provider.Complete(ctx, agent.Request{
		System:   systemPromptFor("corrector"),
		Messages: []agent.Message{{Role: "user", Content: userPromptFor("corrector", b)}},
	})
	if err != nil {
		return fmt.Errorf("corrector inference: %w", err)
	}

	if _, err := o.postIteration(ctx, work.RunID, "correct", "corrector",
		trimSummary(resp.Text, 280), resp); err != nil {
		return err
	}
	// After correcting, re-execute.
	return o.Client.Advance(ctx, work.RunID, client.AdvanceRequest{
		RunnerID: o.RunnerID, FromPhase: "correct", ToPhase: "execute",
	})
}

func (o *Orchestrator) postIteration(ctx context.Context, runID uuid.UUID, phase, role, summary string, resp *agent.Response) (*client.Iteration, error) {
	model := ""
	prompt, completion := 0, 0
	if resp != nil {
		model = resp.Model
		prompt = resp.PromptTokens
		completion = resp.CompletionTokens
	}
	it, err := o.Client.PostIteration(ctx, runID, client.PostIterationReq{
		Phase: phase, AgentRole: role,
		Provider: o.Provider.Name(), Model: model, Summary: summary,
	})
	if err != nil {
		return nil, fmt.Errorf("post iteration: %w", err)
	}
	if prompt > 0 || completion > 0 {
		_ = o.Client.PatchIteration(ctx, it.ID, client.PatchIterationReq{
			PromptTokens: &prompt, CompletionTokens: &completion,
			Status: ptr("done"),
		})
	} else {
		_ = o.Client.PatchIteration(ctx, it.ID, client.PatchIterationReq{
			Status: ptr("done"),
		})
	}
	return it, nil
}

func trimSummary(s string, max int) string {
	s = trimWhitespace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func trimWhitespace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\r' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func ptr[T any](v T) *T { return &v }
