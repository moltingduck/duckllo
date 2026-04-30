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
	"strings"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/runner/agent"
	"github.com/moltingduck/duckllo/internal/runner/client"
	"github.com/moltingduck/duckllo/internal/runner/tools"
	"github.com/moltingduck/duckllo/internal/runner/workspace"
	"github.com/moltingduck/duckllo/internal/sensors"
)

type Orchestrator struct {
	Client         *client.Client
	Provider       agent.Provider
	Sensors        *sensors.Registry
	RunnerID       string
	MaxTurns       int
	DevURL         string // base URL the screenshot sensor should hit (Phase 1 default)
	ChromePath     string // optional override for chromedp
	Workspace      string // host fallback dir
	ContainerImage string // when set, runs each spec in its own container

	// Tailscale sidecar config. When TailscalePreauthKey is set the
	// per-run pod gets a tailscale container sharing the workspace's
	// netns, and screenshot sensors hit the MagicDNS hostname instead of
	// localhost.
	TailscalePreauthKey string
	TailscaleImage      string

	// Per-Run() state. Set in Run() before phase dispatch so the phase
	// methods can pick up the right Sandbox + dev URL.
	sandbox      *tools.Sandbox
	activeDevURL string
}

// Run performs one phase of work for a claimed run+work_item.
func (o *Orchestrator) Run(ctx context.Context, work *client.WorkItem, run *client.Run) error {
	bundle, err := o.Client.Bundle(ctx, work.RunID, work.Phase)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}

	// Hard cap on iterations so a runaway correction loop can't burn
	// budget forever. The schema stores turn_budget on the run; when
	// turns_used has reached it, fail the run loudly with a clear
	// summary so the operator sees why it stopped. Without this a spec
	// whose criteria the agent can't satisfy keeps cycling
	// execute → validate → correct → execute … indefinitely.
	if bundle.Run.TurnBudget > 0 && bundle.Run.TurnsUsed >= bundle.Run.TurnBudget {
		log.Printf("turn budget exceeded: turns_used=%d budget=%d — failing run",
			bundle.Run.TurnsUsed, bundle.Run.TurnBudget)
		summary := fmt.Sprintf("turn budget exceeded: %d / %d turns used",
			bundle.Run.TurnsUsed, bundle.Run.TurnBudget)
		_, _ = o.postIteration(ctx, work.RunID, work.Phase, "system", summary, nil)
		return o.Client.Advance(ctx, work.RunID, client.AdvanceRequest{
			RunnerID: o.RunnerID, FromPhase: work.Phase, FinalStatus: "failed",
		})
	}

	exec, devURL, teardown, err := o.openWorkspace(ctx, run)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	o.sandbox = tools.NewSandboxWith(exec)
	if devURL != "" {
		o.activeDevURL = devURL
	} else {
		o.activeDevURL = o.DevURL
	}

	switch work.Phase {
	case "plan":
		err = o.runPlanner(ctx, work, bundle)
	case "execute":
		err = o.runExecutor(ctx, work, bundle)
	case "validate":
		err = o.runValidator(ctx, work, bundle)
	case "correct":
		err = o.runCorrector(ctx, work, bundle)
	default:
		err = fmt.Errorf("unknown phase %q", work.Phase)
	}

	// Tear down the container only when the run is finishing — otherwise
	// the next phase claim re-uses it. A non-nil err here means the
	// runner loop in cmd/runner will mark the run failed *after* this
	// function returns (its Advance call happens later). To avoid
	// leaking containers on every failure we treat any returned error as
	// "this run is going terminal" and tear down too. Recoverable errors
	// (transient network, DB blips) cost a fresh container next claim,
	// which is the cheaper outcome compared to leaking N containers.
	if teardown != nil {
		needTeardown := err != nil || o.runShouldTearDown(ctx, work.RunID)
		if needTeardown {
			_ = teardown(ctx)
		}
	}
	return err
}

// openWorkspace returns the executor for this run, lazily provisioning a
// Docker container the first time it sees the run. Returns (exec, devURL,
// teardown, err). teardown is nil for host mode (nothing to release).
func (o *Orchestrator) openWorkspace(ctx context.Context, run *client.Run) (workspace.Executor, string, func(context.Context) error, error) {
	if o.ContainerImage == "" {
		return workspace.NewHost(o.Workspace), "", nil, nil
	}

	name := "duckllo-" + shortID(run.ID)
	de := workspace.NewDocker(o.ContainerImage, name, nil, nil)
	if o.TailscalePreauthKey != "" {
		de.TailscalePreauthKey = o.TailscalePreauthKey
		de.TailscaleHostname = name
		if o.TailscaleImage != "" {
			de.TailscaleImage = o.TailscaleImage
		}
	}
	if err := de.Provision(ctx); err != nil {
		return nil, "", nil, err
	}

	devURL := ""
	if de.TailscaleHost() != "" {
		// Sensors append the port from sensor_spec.url (e.g. ":8080/path").
		// We hand them the bare http://hostname so a sensor_spec.url of
		// ":8080/" composes correctly.
		devURL = "http://" + de.TailscaleHost()
	}

	meta := map[string]any{
		"kind":         "docker",
		"container_id": de.ID(),
		"workspace":    de.WorkspacePath(),
	}
	if de.TailscaleID() != "" {
		meta["tailscale_node"] = de.TailscaleID()
		meta["tailscale_host"] = de.TailscaleHost()
		meta["dev_url"] = devURL
	}
	if err := o.Client.SetWorkspaceMeta(ctx, run.ID, meta); err != nil {
		log.Printf("workspace: SetWorkspaceMeta: %v", err)
	}
	return de, devURL, de.Close, nil
}

// runShouldTearDown checks whether the run has reached a terminal state
// (done|failed|aborted) so the orchestrator knows it's safe to remove the
// container. Done by re-fetching the run after the phase ran.
func (o *Orchestrator) runShouldTearDown(ctx context.Context, runID uuid.UUID) bool {
	// No phase needed here — we only inspect run.status. Pass "" to
	// keep the legacy bundle behaviour (returns all rules; cheap
	// since we ignore them).
	r, err := o.Client.Bundle(ctx, runID, "")
	if err != nil {
		return false
	}
	switch r.Run.Status {
	case "done", "failed", "aborted":
		return true
	}
	return false
}

func shortID(u uuid.UUID) string {
	s := u.String()
	if len(s) >= 8 {
		return s[:8]
	}
	return s
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
		// Approval failure must be loud, not silent. Earlier we only
		// log+continued, which let runs advance to execute against an
		// unapproved plan — the dogfood loop ran with apparently-correct
		// behaviour but the run record didn't reflect what actually
		// shipped. Now we post the iteration (so the operator sees the
		// planner's draft) and bubble up an error so the run is marked
		// failed; the operator can review the plan in the UI, approve
		// it, and start a fresh run that begins at execute.
		_, _ = o.postIteration(ctx, work.RunID, "plan", "planner",
			fmt.Sprintf("Drafted plan with %d steps; approval failed: %s",
				len(parsed.Steps), err.Error()), resp)
		return fmt.Errorf("auto-approve plan: %w", err)
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
	tools := o.sandbox.Defs()
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
			result := o.sandbox.Execute(ctx, tc)
			msgs = append(msgs, agent.Message{
				Role: "tool", ToolID: tc.ID, Content: result,
			})
		}
	}

	summary := "Executor completed"
	if lastResp != nil && lastResp.Text != "" {
		summary = trimSummary(lastResp.Text, 280)
	}
	log.Printf("execute: posting iteration with summary=%q", summary)

	// Multi-turn transcript: serialise the whole conversation so the run
	// dashboard's iteration timeline can show every tool call and its
	// result, not just the final summary. Capped at ~64 KiB so a runaway
	// executor doesn't blow the iterations.transcript column out.
	transcript := flattenExecutorTranscript(systemPromptFor("executor"), msgs)
	it, err := o.postIterationWithTranscript(ctx, work.RunID, "execute", "executor",
		summary, lastResp, transcript)
	if err != nil {
		log.Printf("execute: postIteration failed: %v", err)
		return err
	}
	log.Printf("execute: iteration posted id=%s; capturing workspace changes", it.ID)

	// Capture workspace changes so the validator's judge — which on most
	// providers (Anthropic / OpenAI / Ollama) has no filesystem access —
	// can see what actually changed in the workspace, not just what the
	// executor *said* it did.
	o.postWorkspaceChanges(ctx, work.RunID, it.ID)

	return o.Client.Advance(ctx, work.RunID, client.AdvanceRequest{
		RunnerID: o.RunnerID, FromPhase: "execute", ToPhase: "validate",
	})
}

// postWorkspaceChanges runs `git status --porcelain` + `git diff` inside
// the workspace and posts the result as a verification of kind
// `workspace_changes`. Best-effort — silently skips when the workspace
// isn't a git repo or git isn't available. The diff is also recorded in
// details_json so it surfaces in the bundle's verification list and the
// validator can include it in the judge's prompt.
func (o *Orchestrator) postWorkspaceChanges(ctx context.Context, runID, iterationID uuid.UUID) {
	if o.sandbox == nil {
		log.Printf("workspace_changes: sandbox nil, skipping")
		return
	}

	statusOut, err := o.sandbox.Workspace.Exec(ctx, []string{"git", "status", "--porcelain"})
	if err != nil {
		log.Printf("workspace_changes: git status failed: %v (output=%q)", err, string(statusOut))
		return
	}
	statusStr := strings.TrimSpace(string(statusOut))
	if statusStr == "" {
		// Clean tree — still post a verification so the validator knows
		// there was nothing to change. Useful signal vs "executor never
		// touched anything".
		_, _ = o.Client.PostVerification(ctx, runID, client.PostVerificationReq{
			IterationID: iterationID.String(),
			Kind:        "workspace_changes",
			Class:       "computational",
			Status:      "warn",
			Summary:     "executor produced no workspace changes (clean working tree)",
			Details:     map[string]any{"diff": "", "status": ""},
		})
		log.Printf("workspace_changes: clean tree (warn)")
		return
	}

	diffOut, err := o.sandbox.Workspace.Exec(ctx, []string{"git", "diff", "--no-color"})
	if err != nil {
		log.Printf("workspace_changes: git diff failed: %v", err)
		return
	}

	const diffCap = 16 * 1024
	diffStr := string(diffOut)
	truncated := false
	if len(diffStr) > diffCap {
		diffStr = diffStr[:diffCap] + "\n[truncated]"
		truncated = true
	}

	// Count changed paths from the porcelain output (one path per line).
	paths := []string{}
	for _, line := range strings.Split(statusStr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// porcelain format: "XY path" — keep just the path.
		if i := strings.Index(line, " "); i > 0 && i+1 < len(line) {
			paths = append(paths, strings.TrimSpace(line[i+1:]))
		}
	}
	summary := fmt.Sprintf("%d changed path(s): %s", len(paths), strings.Join(paths, ", "))
	if len(summary) > 240 {
		summary = summary[:240] + "…"
	}

	_, postErr := o.Client.PostVerification(ctx, runID, client.PostVerificationReq{
		IterationID: iterationID.String(),
		Kind:        "workspace_changes",
		Class:       "computational",
		Status:      "pass",
		Summary:     summary,
		Details: map[string]any{
			"status":    statusStr,
			"diff":      diffStr,
			"paths":     paths,
			"truncated": truncated,
		},
	})
	if postErr != nil {
		log.Printf("workspace_changes: post failed: %v", postErr)
		return
	}
	log.Printf("workspace_changes: posted (%d paths, %d-byte diff)", len(paths), len(diffStr))
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
		DevURL:       o.activeDevURL,
		ChromePath:   o.ChromePath,
		LogF:         func(f string, args ...any) { log.Printf(f, args...) },
		Fetch:        o.Client.FetchArtifact,
	}

	// Track per-criterion verdicts so we can decide whether to mark the
	// run done or leave it in 'validating' for human review at the end.
	verdicts := map[string]string{} // criterion_id → pass|fail|warn|skipped|manual

	deterministicFired := 0
	for _, c := range criteria {
		if c.SensorKind == "manual" {
			verdicts[c.ID] = "manual"
			continue
		}
		if c.SensorKind == "judge" {
			continue // judge is handled below.
		}
		s := o.Sensors.For(c.SensorKind)
		if s == nil {
			verdicts[c.ID] = "skipped"
			if _, err := o.Client.PostVerification(ctx, work.RunID, client.PostVerificationReq{
				CriterionID: c.ID, Kind: c.SensorKind, Class: "computational",
				Status: "skipped", Summary: "no sensor implementation for kind " + c.SensorKind,
			}); err != nil {
				log.Printf("validator: post skipped verification for %s: %v", c.SensorKind, err)
			}
			continue
		}
		res, err := s.Run(ctx, c, env)
		if err != nil {
			verdicts[c.ID] = "fail"
			if _, postErr := o.Client.PostVerification(ctx, work.RunID, client.PostVerificationReq{
				CriterionID: c.ID, Kind: c.SensorKind, Class: "computational",
				Status: "fail", Summary: "sensor error: " + err.Error(),
			}); postErr != nil {
				log.Printf("validator: post sensor-error verification for %s: %v", c.SensorKind, postErr)
			}
			continue
		}
		verdicts[c.ID] = res.Status
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
				log.Printf("judge: missing JSON block (got %d chars); posting warn verification", len(resp.Text))
				if _, err := o.Client.PostVerification(ctx, work.RunID, client.PostVerificationReq{
					Kind: "judge", Class: "inferential", Status: "warn",
					Summary: "validator output missing JSON block",
				}); err != nil {
					log.Printf("judge: post warn verification: %v", err)
				}
			} else {
				var parsed struct {
					Verdicts []struct {
						CriterionID string `json:"criterion_id"`
						Status      string `json:"status"`
						Summary     string `json:"summary"`
					} `json:"verdicts"`
				}
				if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
					log.Printf("judge: parse JSON: %v (raw=%q)", err, raw)
				} else {
					for _, v := range parsed.Verdicts {
						verdicts[v.CriterionID] = v.Status
						if _, err := o.Client.PostVerification(ctx, work.RunID, client.PostVerificationReq{
							CriterionID: v.CriterionID, Kind: "judge", Class: "inferential",
							Status: v.Status, Summary: v.Summary,
						}); err != nil {
							log.Printf("judge: post verdict %q: %v", v.Status, err)
						}
					}
				}
			}
		}
	}

	// Decide whether the run is actually finished or whether it should
	// stay parked in 'validating' for human review. A run is "done" only
	// when every criterion has a passing verdict. Anything else (fail,
	// warn, manual, skipped) blocks the auto-finish: humans look at the
	// sensor grid and either post fix_required annotations (which the
	// corrector phase picks up) or override individual verifications via
	// PATCH. Marking those runs done unconditionally — which is what the
	// orchestrator did before this commit — produced false-positive
	// validated specs whose criteria were never actually satisfied.
	allPass := true
	pending := []string{}
	for _, c := range criteria {
		v, ok := verdicts[c.ID]
		if !ok || v != "pass" {
			allPass = false
			pending = append(pending, fmt.Sprintf("%s=%s", c.SensorKind, v))
		}
	}

	summary := fmt.Sprintf("Ran %d deterministic sensors", deterministicFired)
	if hasJudge {
		summary += " + judge"
	}
	if allPass {
		summary += "; all criteria passed"
	} else {
		summary += "; awaiting human review (" + strings.Join(pending, ", ") + ")"
	}
	if _, err := o.postIteration(ctx, work.RunID, "validate", "validator", summary, judgeResp); err != nil {
		return err
	}

	advance := client.AdvanceRequest{RunnerID: o.RunnerID, FromPhase: "validate"}
	if allPass {
		advance.FinalStatus = "done"
	}
	// When not all-pass, we close the work_queue item but leave the run
	// in 'validating' status (no FinalStatus, no toPhase). The fix-loop
	// resumes when a human posts a fix_required annotation, which
	// enqueues a 'correct' work item via store.CreateAnnotation.
	return o.Client.Advance(ctx, work.RunID, advance)
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

// flattenExecutorTranscript renders the executor's full message history
// into a human-readable transcript for the iterations.transcript column.
// We cap at 64 KiB so a runaway loop can't bloat the row indefinitely;
// the (truncated) marker tells the reader where the cut happened.
func flattenExecutorTranscript(system string, msgs []agent.Message) string {
	const cap = 64 * 1024
	var b strings.Builder
	if system != "" {
		b.WriteString("# System\n")
		b.WriteString(system)
		b.WriteString("\n\n")
	}
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			fmt.Fprintf(&b, "# tool result (%s)\n%s\n\n", m.ToolID, m.Content)
		case "assistant":
			b.WriteString("# assistant\n")
			if m.Content != "" {
				b.WriteString(m.Content)
				b.WriteString("\n")
			}
			for _, tc := range m.Tools {
				args, _ := json.Marshal(tc.Input)
				fmt.Fprintf(&b, "[tool_call %s name=%s input=%s]\n", tc.ID, tc.Name, args)
			}
			b.WriteString("\n")
		default:
			fmt.Fprintf(&b, "# %s\n%s\n\n", m.Role, m.Content)
		}
		if b.Len() >= cap {
			break
		}
	}
	out := b.String()
	if len(out) > cap {
		out = out[:cap] + "\n[transcript truncated]"
	}
	return out
}

// postIterationWithTranscript is a fork of postIteration that lets
// runExecutor pass the multi-turn transcript built above. The base
// postIteration auto-derives a single-turn transcript from resp.Text.
func (o *Orchestrator) postIterationWithTranscript(ctx context.Context, runID uuid.UUID, phase, role, summary string, resp *agent.Response, transcript string) (*client.Iteration, error) {
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
		Transcript: transcript,
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

func (o *Orchestrator) postIteration(ctx context.Context, runID uuid.UUID, phase, role, summary string, resp *agent.Response) (*client.Iteration, error) {
	model := ""
	prompt, completion := 0, 0
	transcript := ""
	if resp != nil {
		model = resp.Model
		prompt = resp.PromptTokens
		completion = resp.CompletionTokens
		// Single-turn transcript: just the model's text. The executor
		// (which is multi-turn) overrides this with a richer transcript
		// — see runExecutor's full-conversation capture below.
		transcript = resp.Text
	}
	it, err := o.Client.PostIteration(ctx, runID, client.PostIterationReq{
		Phase: phase, AgentRole: role,
		Provider: o.Provider.Name(), Model: model, Summary: summary,
		Transcript: transcript,
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
