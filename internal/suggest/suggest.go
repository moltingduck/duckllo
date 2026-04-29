// Package suggest helps the user compose a spec by asking the LLM for
// likely acceptance criteria given a title + intent. It's a UI-side
// affordance — *not* part of the harness loop. The runner has its own
// planner agent for the contract-bound work; this is just to seed the
// composer so a developer doesn't stare at a blank list.
package suggest

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/moltingduck/duckllo/internal/runner/agent"
)

// SuggestedCriterion is the JSON shape the UI receives. Mirrors a subset
// of models.AcceptanceCriterion — we deliberately don't ask the model to
// invent ids or sensor_spec, because those are decided when the criterion
// is actually saved.
type SuggestedCriterion struct {
	Text       string `json:"text"`
	SensorKind string `json:"sensor_kind"`
}

// validKinds matches what the spec composer's selector exposes. Keep in
// sync with internal/webui/web/pages/spec-new.js (SENSOR_KINDS) and the
// sensors registry — anything we let the model emit must be a kind a
// validator can actually fire.
var validKinds = map[string]bool{
	"lint": true, "unit_test": true, "e2e_test": true, "build": true,
	"screenshot": true, "judge": true, "manual": true,
}

const systemPrompt = `You help developers compose acceptance criteria for software change specs.

Given the spec's title and intent, propose 3 to 6 acceptance criteria
that, taken together, would convince a reviewer the change is correct.
Each criterion is "a typed sensor target" — pick the sensor_kind that
will best verify it:

- lint        : style/format checks (golangci-lint, eslint, …)
- unit_test   : ` + "`go test`" + ` / ` + "`pytest`" + ` / framework-native unit suites
- e2e_test    : end-to-end flow tests (HTTP API integration, UI scripted)
- build       : the project still compiles cleanly
- screenshot  : a visual check on a specific viewport+selector
- judge       : an LLM judge reads the diff and verifies a property that
                no deterministic sensor can (e.g. "the new help text is
                clear and uses imperative voice")
- manual      : human-only verification when nothing else fits

Output ONLY a single fenced JSON block of this shape:

` + "```" + `json
{"criteria":[{"text":"…","sensor_kind":"…"}]}
` + "```" + `

No prose before or after. The "text" field is a concise sentence (one
clause is fine). Don't repeat the spec intent verbatim — the criterion
should be a *check*, not a restatement of the goal.`

// Criteria asks the provider to propose acceptance criteria for the
// given title + intent. Returns the parsed list, or an error if the
// model output couldn't be decoded into the expected shape.
func Criteria(ctx context.Context, p agent.Provider, title, intent string) ([]SuggestedCriterion, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	user := fmt.Sprintf("Title: %s\n\nIntent:\n%s", title, intent)

	resp, err := p.Complete(ctx, agent.Request{
		System:    systemPrompt,
		Messages:  []agent.Message{{Role: "user", Content: user}},
		MaxTokens: 1024,
	})
	if err != nil {
		return nil, fmt.Errorf("suggest provider: %w", err)
	}
	raw, err := extractJSONBlock(resp.Text)
	if err != nil {
		return nil, fmt.Errorf("suggest parse: %w (model said: %s)", err, truncate(resp.Text, 200))
	}
	var parsed struct {
		Criteria []SuggestedCriterion `json:"criteria"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("suggest decode: %w", err)
	}
	out := make([]SuggestedCriterion, 0, len(parsed.Criteria))
	for _, c := range parsed.Criteria {
		text := strings.TrimSpace(c.Text)
		kind := strings.TrimSpace(c.SensorKind)
		if text == "" || !validKinds[kind] {
			continue
		}
		out = append(out, SuggestedCriterion{Text: text, SensorKind: kind})
	}
	return out, nil
}

var fencedJSON = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// extractJSONBlock pulls the inner JSON object out of a fenced block, or
// returns the input unchanged if it parses as JSON directly. Models
// occasionally drop the fence even when asked to keep it.
func extractJSONBlock(s string) (string, error) {
	s = strings.TrimSpace(s)
	if m := fencedJSON.FindStringSubmatch(s); len(m) == 2 {
		return m[1], nil
	}
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		return s, nil
	}
	return "", fmt.Errorf("no JSON block found")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
