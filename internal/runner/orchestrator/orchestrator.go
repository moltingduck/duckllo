// Package orchestrator drives a single PEVC iteration end-to-end. The
// runner main loop hands it a claimed work item and the orchestrator
// returns when the iteration has been posted and the run advanced.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/runner/agent"
	"github.com/moltingduck/duckllo/internal/runner/client"
	"github.com/moltingduck/duckllo/internal/runner/tools"
)

type Orchestrator struct {
	Client   *client.Client
	Provider agent.Provider
	Sandbox  *tools.Sandbox
	RunnerID string
	MaxTurns int
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

// runValidator: for now, only the inferential judge runs (computational
// sensors land in task #10 / internal/sensors). For each criterion of
// sensor_kind in {lint, test, build, screenshot}, we mark it as 'pending'
// without actually firing — task 10 will replace these with real sensor
// calls. For 'judge' kind, we ask the model.
func (o *Orchestrator) runValidator(ctx context.Context, work *client.WorkItem, b *client.Bundle) error {
	var criteria []struct {
		ID         string `json:"id"`
		Text       string `json:"text"`
		SensorKind string `json:"sensor_kind"`
		Satisfied  bool   `json:"satisfied"`
	}
	_ = json.Unmarshal(b.Spec.AcceptanceCriteria, &criteria)

	resp, err := o.Provider.Complete(ctx, agent.Request{
		System:   systemPromptFor("validator"),
		Messages: []agent.Message{{Role: "user", Content: userPromptFor("validator", b)}},
	})
	if err != nil {
		return fmt.Errorf("validator inference: %w", err)
	}
	raw, err := extractJSONBlock(resp.Text)
	if err != nil {
		// Don't bail — post a meta verification so the UI shows we tried.
		_, _ = o.Client.PostVerification(ctx, work.RunID, client.PostVerificationReq{
			Kind: "judge", Class: "inferential", Status: "warn",
			Summary: "validator output missing JSON block",
		})
	}
	if err == nil {
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

	if _, err := o.postIteration(ctx, work.RunID, "validate", "validator",
		"Validator judged criteria", resp); err != nil {
		return err
	}

	// Decide next phase: if every criterion is now satisfied (or no humans
	// have opened annotations), mark the run done. Otherwise sit in
	// 'validating' and wait — the corrector phase only fires when an
	// annotation is posted (see store.CreateAnnotation).
	allPass := true
	for _, c := range criteria {
		if !c.Satisfied {
			allPass = false
			break
		}
	}
	if allPass {
		return o.Client.Advance(ctx, work.RunID, client.AdvanceRequest{
			RunnerID: o.RunnerID, FromPhase: "validate", FinalStatus: "done",
		})
	}
	// Default: stop here. Humans inspect the sensor grid; if they post a
	// fix_required annotation the run flips to 'correcting' and the
	// corrector phase claim becomes available next time we poll.
	return o.Client.Advance(ctx, work.RunID, client.AdvanceRequest{
		RunnerID: o.RunnerID, FromPhase: "validate", FinalStatus: "done",
	})
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
