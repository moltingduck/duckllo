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

// userPromptFor renders the bundle as the role-specific user message body.
// Verbose-but-deterministic; the model gets every signal it needs in one
// turn so most tasks finish without re-prompting.
func userPromptFor(role string, b *client.Bundle) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Spec\n**%s**\n\n%s\n\n", b.Spec.Title, b.Spec.Intent)

	if len(b.Spec.AcceptanceCriteria) > 0 {
		fmt.Fprintln(&sb, "## Acceptance criteria")
		var crits []map[string]any
		_ = json.Unmarshal(b.Spec.AcceptanceCriteria, &crits)
		for _, c := range crits {
			fmt.Fprintf(&sb, "- [%s] (%s) %v\n", c["id"], c["sensor_kind"], c["text"])
		}
		fmt.Fprintln(&sb)
	}

	if role != "planner" && len(b.Plan.Steps) > 0 {
		fmt.Fprintln(&sb, "## Plan")
		var steps []map[string]any
		_ = json.Unmarshal(b.Plan.Steps, &steps)
		for _, s := range steps {
			fmt.Fprintf(&sb, "%d. %s\n", intOf(s["order"]), s["summary"])
		}
		fmt.Fprintln(&sb)
	}

	if len(b.HarnessRules) > 0 {
		fmt.Fprintln(&sb, "## Project guides")
		for _, r := range b.HarnessRules {
			fmt.Fprintf(&sb, "### %s — %s\n%s\n\n", r.Kind, r.Name, r.Body)
		}
	}

	// Validator + corrector both benefit from seeing the actual workspace
	// changes the executor produced. Without this section the judge has
	// no filesystem context on API providers and can only trust the
	// executor's self-reported summary — which the dogfood loop's first
	// run proved is unreliable.
	if role == "validator" || role == "corrector" {
		if diff := latestWorkspaceDiff(b); diff != "" {
			fmt.Fprintln(&sb, "## Workspace changes (from `git diff` after execute)")
			fmt.Fprintln(&sb, "```diff")
			fmt.Fprintln(&sb, diff)
			fmt.Fprintln(&sb, "```")
			fmt.Fprintln(&sb)
		}
	}

	if role == "corrector" && len(b.OpenAnnotations) > 0 {
		fmt.Fprintln(&sb, "## Annotations to address")
		for _, a := range b.OpenAnnotations {
			fmt.Fprintf(&sb, "- [%s] %s — %s\n", a.Verdict, string(a.BBox), a.Body)
		}
		fmt.Fprintln(&sb)
	}

	if role == "corrector" && len(b.Verifications) > 0 {
		fmt.Fprintln(&sb, "## Failed sensors")
		for _, v := range b.Verifications {
			if v.Kind != "" {
				fmt.Fprintf(&sb, "- (%s) %s\n", v.Kind, v.ID)
			}
		}
		fmt.Fprintln(&sb)
	}

	return sb.String()
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
