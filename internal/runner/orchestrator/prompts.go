package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/moltingduck/duckllo/internal/runner/client"
)

// systemPromptFor returns the role-specific system prompt. Each role gets
// a tight contract describing what shape of output the runner expects, so
// JSON parsing on the back end stays deterministic.
func systemPromptFor(role string) string {
	common := `You are an autonomous agent operating inside the duckllo harness. You receive a Spec
(intent + acceptance criteria), a Plan (steps to take), and a workspace you can read and write to.
Stay scoped to the spec — do not refactor unrelated code. Be concise.`
	switch role {
	case "planner":
		return common + `

YOUR ROLE: Planner.
You produce a plan for the spec. The plan is a list of concrete steps that the executor will follow.
Each step has: a one-line summary, optional files_touched, and optional sensors_targeted (criterion ids).

OUTPUT CONTRACT: Reply with a single fenced code block tagged 'json' containing:
{"steps":[{"id":"s1","order":1,"summary":"...","files_touched":["..."],"sensors_targeted":["..."]}, ...]}
Do not include other prose outside the code block.`
	case "executor":
		return common + `

YOUR ROLE: Executor.
You implement the plan one step at a time, calling read_file / write_file / list_dir / exec tools.
When all steps are complete, reply with a one-line summary of what you changed.`
	case "validator":
		return common + `

YOUR ROLE: Validator.
You decide for each acceptance criterion whether it passes, fails, or warrants a warning.

IMPORTANT: a "## Workspace changes" section appears in your prompt with the literal output of
'git diff --no-color' inside the workspace right after the executor finished. That diff is
ground truth — base your verdict on it, not on the executor's self-reported summary. If the
diff is empty or doesn't show what the criterion demands, the verdict is fail (or warn, if
you can't tell from the diff alone).

For criteria of sensor_kind=judge, reason from the diff and any other context in the prompt.
For screenshot/lint/test-kind, the runner already ran the deterministic sensor and posted a
verification — DO NOT post a verdict for those kinds, only for judge criteria.

OUTPUT CONTRACT: Reply with a single fenced code block tagged 'json':
{"verdicts":[{"criterion_id":"...","status":"pass|fail|warn","summary":"..."}]}`
	case "corrector":
		return common + `

YOUR ROLE: Corrector.
You synthesise the next round of changes. Open annotations from humans and failed verifications
are listed below. Plan the smallest set of file edits that will satisfy them.

OUTPUT CONTRACT: Reply with a single fenced code block tagged 'json':
{"steps":[{"id":"c1","order":1,"summary":"..."}]}`
	}
	return common
}

// PromptSegment is one labeled chunk of the assembled prompt — a piece
// the user can trace back to its source (a harness rule, the spec
// intent, the plan, the workspace diff, etc.) and ideally edit. The
// preview UI renders a list of these so the user knows exactly which
// document is contributing each section of what the agent sees.
type PromptSegment struct {
	Source   string `json:"source"`             // spec | criteria | plan | harness_rule | workspace_diff | annotation | failed_sensor
	SourceID string `json:"source_id,omitempty"` // e.g. harness rule UUID, criterion id
	Heading  string `json:"heading"`            // human-readable e.g. "Harness rule: Trust the workspace diff"
	EditURL  string `json:"edit_url,omitempty"` // hash route the UI links to so the user can jump straight to the edit page
	Content  string `json:"content"`            // the actual text inserted into the prompt
}

// PromptPreview is what the preview endpoint hands the UI: the
// system prompt the runner sends as a string (no segments — it's a
// fixed role contract), plus the user message broken into traceable
// segments.
type PromptPreview struct {
	Role   string          `json:"role"`
	Phase  string          `json:"phase"`
	System string          `json:"system"`
	User   []PromptSegment `json:"user"`
}

// userPromptSegments is the labeled-segments form of the user message.
// userPromptFor below joins them into a single string with the same
// shape the runner has always used; both share this single source of
// truth so the preview can never drift from what the runner actually
// sends.
func userPromptSegments(role string, b *client.Bundle) []PromptSegment {
	var out []PromptSegment
	specEdit := fmt.Sprintf("#/projects/%s/specs/%s", b.Run.SpecID, b.Spec.ID)

	out = append(out, PromptSegment{
		Source: "spec", SourceID: b.Spec.ID.String(),
		Heading: "Spec — " + b.Spec.Title,
		EditURL: specEdit,
		Content: fmt.Sprintf("## Spec\n**%s**\n\n%s\n", b.Spec.Title, b.Spec.Intent),
	})

	if len(b.Spec.AcceptanceCriteria) > 0 {
		var crits []map[string]any
		_ = json.Unmarshal(b.Spec.AcceptanceCriteria, &crits)
		var sb strings.Builder
		fmt.Fprintln(&sb, "## Acceptance criteria")
		for _, c := range crits {
			fmt.Fprintf(&sb, "- [%s] (%s) %v\n", c["id"], c["sensor_kind"], c["text"])
		}
		out = append(out, PromptSegment{
			Source: "criteria", Heading: "Acceptance criteria",
			EditURL: specEdit, Content: sb.String(),
		})
	}

	if role != "planner" && len(b.Plan.Steps) > 0 {
		var steps []map[string]any
		_ = json.Unmarshal(b.Plan.Steps, &steps)
		var sb strings.Builder
		fmt.Fprintln(&sb, "## Plan")
		for _, s := range steps {
			fmt.Fprintf(&sb, "%d. %s\n", intOf(s["order"]), s["summary"])
		}
		out = append(out, PromptSegment{
			Source: "plan", SourceID: b.Plan.ID.String(),
			Heading: "Plan",
			EditURL: specEdit, // plan lives on the spec page; same edit target
			Content: sb.String(),
		})
	}

	// Each harness rule is its own segment so the user can click
	// straight to its edit page if a rule looks wrong in context.
	if len(b.HarnessRules) > 0 {
		// Project ID isn't on the bundle directly — get it via the
		// spec's project link, threaded through Run on bundle JSON.
		// Fall back to "" if absent so the link gracefully degrades.
		projectID := b.Spec.ProjectID.String()
		for _, r := range b.HarnessRules {
			out = append(out, PromptSegment{
				Source: "harness_rule", SourceID: r.ID.String(),
				Heading: "Harness rule: " + r.Name + " (" + r.Kind + ")",
				EditURL: fmt.Sprintf("#/projects/%s/steering", projectID),
				Content: fmt.Sprintf("### %s — %s\n%s\n", r.Kind, r.Name, r.Body),
			})
		}
	}

	// Validator + corrector see the workspace diff (the executor's
	// real output, ground truth over the executor's self-reported
	// summary).
	if role == "validator" || role == "corrector" {
		if diff := latestWorkspaceDiff(b); diff != "" {
			out = append(out, PromptSegment{
				Source: "workspace_diff", Heading: "Workspace changes (git diff after execute)",
				Content: "## Workspace changes (from `git diff` after execute)\n```diff\n" + diff + "\n```\n",
			})
		}
	}

	if role == "corrector" && len(b.OpenAnnotations) > 0 {
		var sb strings.Builder
		fmt.Fprintln(&sb, "## Annotations to address")
		for _, a := range b.OpenAnnotations {
			fmt.Fprintf(&sb, "- [%s] %s — %s\n", a.Verdict, string(a.BBox), a.Body)
		}
		out = append(out, PromptSegment{
			Source: "annotation", Heading: "Open annotations to address",
			EditURL: fmt.Sprintf("#/projects/%s/runs/%s", b.Spec.ProjectID, b.Run.ID),
			Content: sb.String(),
		})
	}

	if role == "corrector" && len(b.Verifications) > 0 {
		var failures []client.Verification
		for _, v := range b.Verifications {
			if v.Kind == "" || v.Kind == "workspace_changes" {
				continue
			}
			if v.Status == "fail" || v.Status == "warn" {
				failures = append(failures, v)
			}
		}
		if len(failures) > 0 {
			var sb strings.Builder
			fmt.Fprintln(&sb, "## Failed / warning sensors")
			for _, v := range failures {
				summary := v.Summary
				if summary == "" {
					summary = "(no summary)"
				}
				fmt.Fprintf(&sb, "- [%s] (%s) %s\n", v.Status, v.Kind, summary)
			}
			out = append(out, PromptSegment{
				Source: "failed_sensor", Heading: "Failed / warning sensors",
				EditURL: fmt.Sprintf("#/projects/%s/runs/%s", b.Spec.ProjectID, b.Run.ID),
				Content: sb.String(),
			})
		}
	}

	return out
}

// userPromptFor renders the bundle as the role-specific user message
// body. Verbose-but-deterministic; the model gets every signal it
// needs in one turn so most tasks finish without re-prompting. Now a
// thin string-joiner over userPromptSegments — keeps the preview
// endpoint's output exactly what the runner actually sends.
func userPromptFor(role string, b *client.Bundle) string {
	var sb strings.Builder
	for _, seg := range userPromptSegments(role, b) {
		sb.WriteString(seg.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// PreviewFor builds the labeled preview the UI renders. Same role/phase
// mapping the orchestrator uses (plan→planner, execute→executor,
// validate→validator, correct→corrector). Exposed so internal/http can
// call into it without re-implementing prompt assembly.
func PreviewFor(phase string, b *client.Bundle) PromptPreview {
	role := roleForPhase(phase)
	return PromptPreview{
		Role:   role,
		Phase:  phase,
		System: systemPromptFor(role),
		User:   userPromptSegments(role, b),
	}
}

// roleForPhase maps the work_queue phase to the agent role the
// orchestrator dispatches it to. Mirrors the switch in Run().
func roleForPhase(phase string) string {
	switch phase {
	case "plan":
		return "planner"
	case "execute":
		return "executor"
	case "validate":
		return "validator"
	case "correct":
		return "corrector"
	}
	return ""
}

// extractJSONBlock pulls a single ```json ... ``` block out of the model
// reply. The stricter contract avoids JSON-recovery heuristics later.
func extractJSONBlock(text string) (string, error) {
	const fence = "```json"
	i := strings.Index(text, fence)
	if i < 0 {
		// Fallback: try a bare ``` block.
		i = strings.Index(text, "```")
		if i < 0 {
			return "", fmt.Errorf("no fenced json block in reply")
		}
	}
	rest := text[i:]
	rest = strings.TrimPrefix(rest, "```json")
	rest = strings.TrimPrefix(rest, "```")
	end := strings.Index(rest, "```")
	if end < 0 {
		return "", fmt.Errorf("unterminated fenced block")
	}
	return strings.TrimSpace(rest[:end]), nil
}

func intOf(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}

// latestWorkspaceDiff scans the bundle's verifications for the most
// recent `workspace_changes` posting and returns its `details_json.diff`
// content. Returns "" when no such verification exists, or when the diff
// is empty (clean tree).
func latestWorkspaceDiff(b *client.Bundle) string {
	for i := len(b.Verifications) - 1; i >= 0; i-- {
		v := b.Verifications[i]
		if v.Kind != "workspace_changes" {
			continue
		}
		if len(v.Details) == 0 {
			return ""
		}
		var details struct {
			Diff string `json:"diff"`
		}
		if err := json.Unmarshal(v.Details, &details); err == nil && details.Diff != "" {
			return details.Diff
		}
	}
	return ""
}
