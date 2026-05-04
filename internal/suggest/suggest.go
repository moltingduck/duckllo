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

// Question is one clarifying prompt with optional click-to-select
// answer choices. `options` is empty when the question is genuinely
// open-ended (e.g. "what's the keyboard shortcut?"); when present, the
// UI renders each as a button so the user doesn't have to type.
type Question struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

// RefinedDraft is what /refine returns: the model's tightened version of
// the user's title + intent, plus 2-4 clarifying questions whose answers
// would meaningfully change the acceptance criteria. The UI shows the
// refined fields as editable inputs (the user can accept, edit, or
// reject them) and renders the questions as answer-row prompts.
type RefinedDraft struct {
	RefinedTitle  string     `json:"refined_title"`
	RefinedIntent string     `json:"refined_intent"`
	Questions     []Question `json:"questions"`
}

// QA is one question + the user's answer, used as additional context on
// the criteria pass after the user has responded to the refine pass.
type QA struct {
	Q string `json:"q"`
	A string `json:"a"`
}

// validKinds matches what the spec composer's selector exposes. Keep in
// sync with internal/webui/web/pages/spec-new.js (SENSOR_KINDS) and the
// sensors registry — anything we let the model emit must be a kind a
// validator can actually fire.
var validKinds = map[string]bool{
	"lint": true, "unit_test": true, "e2e_test": true, "build": true,
	"screenshot": true, "judge": true, "manual": true,
}

const refineSystemPrompt = `You help a developer compose a software change spec.

You receive a draft title and intent. Two jobs:

1. REFINE the title and intent. Tighten the wording, make the title
   imperative-voice and short (<= 70 chars), and rewrite the intent as
   2-4 sentences that state the user-visible goal, the constraint, and
   what success looks like. Don't invent scope the user didn't mention.
   If the draft is already crisp, return it unchanged.

2. ASK 2 to 4 CLARIFYING QUESTIONS whose answers would *materially*
   change the acceptance criteria — e.g. "should the toggle persist
   across browsers, or only this device?", "is mobile in scope?", "must
   it work without JavaScript?". Don't ask trivia, don't ask things
   already answered by the intent. If you genuinely have nothing to
   ask, return an empty questions array.

   Each question SHOULD include 2-4 short answer "options" the user can
   click to select instead of typing — yes/no, or named alternatives.
   Phrase the options as full answers ("Per device, in localStorage",
   "Synced per account") not single words. Use an empty options array
   only when the question is genuinely open-ended (e.g. "what's the
   keyboard shortcut?").

Output ONLY a single fenced JSON block of this shape, no prose around it:

` + "```" + `json
{
  "refined_title": "…",
  "refined_intent": "…",
  "questions": [
    {"question": "…", "options": ["…", "…"]},
    {"question": "…", "options": []}
  ]
}
` + "```"

const criteriaSystemPrompt = `You help developers compose acceptance criteria for software change specs.

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

// Refine asks the provider to tighten the title + intent and return
// 2-4 clarifying questions whose answers would change the acceptance
// criteria. The UI uses this for the first step of the suggest flow:
// the user sees the refined draft (editable) and answers the questions
// before triggering the criteria pass.
// withLangDirective appends a "Reply in {language}" instruction to a
// system prompt. Empty / "en" passes through unchanged. JSON keys and
// enum values stay English so the parsers don't need a locale-aware
// translation layer; only the human-readable strings (refined_intent,
// criterion text, question wording) shift.
func withLangDirective(prompt, lang string) string {
	if lang == "" || lang == "en" {
		return prompt
	}
	switch lang {
	case "zh-TW":
		return prompt + "\n\nLANGUAGE: Reply in Traditional Chinese (zh-TW) for all human-readable strings — refined_title, refined_intent, question text, options, criterion text. Keep JSON keys and sensor_kind values in English exactly as the schema specifies; the parser is strict."
	}
	return prompt + "\n\nLANGUAGE: Reply in " + lang + " for all human-readable strings. Keep JSON keys and enum values in English."
}

func Refine(ctx context.Context, p agent.Provider, title, intent, lang string) (*RefinedDraft, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	user := fmt.Sprintf("Draft title: %s\n\nDraft intent:\n%s", title, intent)
	resp, err := p.Complete(ctx, agent.Request{
		System:    withLangDirective(refineSystemPrompt, lang),
		Messages:  []agent.Message{{Role: "user", Content: user}},
		MaxTokens: 1024,
	})
	if err != nil {
		return nil, fmt.Errorf("refine provider: %w", err)
	}
	raw, err := extractJSONBlock(resp.Text)
	if err != nil {
		return nil, fmt.Errorf("refine parse: %w (model said: %s)", err, truncate(resp.Text, 200))
	}
	var parsed RefinedDraft
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("refine decode: %w", err)
	}
	parsed.RefinedTitle = strings.TrimSpace(parsed.RefinedTitle)
	parsed.RefinedIntent = strings.TrimSpace(parsed.RefinedIntent)
	// Drop empty / whitespace-only questions so the UI doesn't render
	// blanks. Same for option strings.
	clean := make([]Question, 0, len(parsed.Questions))
	for _, q := range parsed.Questions {
		text := strings.TrimSpace(q.Question)
		if text == "" {
			continue
		}
		opts := make([]string, 0, len(q.Options))
		for _, o := range q.Options {
			if o = strings.TrimSpace(o); o != "" {
				opts = append(opts, o)
			}
		}
		clean = append(clean, Question{Question: text, Options: opts})
	}
	parsed.Questions = clean
	// If the model returned nothing useful, fall back to the user's draft.
	if parsed.RefinedTitle == "" {
		parsed.RefinedTitle = title
	}
	if parsed.RefinedIntent == "" {
		parsed.RefinedIntent = intent
	}
	return &parsed, nil
}

// Criteria asks the provider to propose acceptance criteria for the
// given title + intent, optionally enriched with the user's answers to
// clarifying questions from a prior Refine call. Returns the parsed
// list, or an error if the model output couldn't be decoded into the
// expected shape.
func Criteria(ctx context.Context, p agent.Provider, title, intent, lang string, qa []QA) ([]SuggestedCriterion, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\n\nIntent:\n%s", title, intent)
	if len(qa) > 0 {
		b.WriteString("\n\nClarifications:")
		for _, x := range qa {
			q := strings.TrimSpace(x.Q)
			a := strings.TrimSpace(x.A)
			if q == "" || a == "" {
				continue
			}
			fmt.Fprintf(&b, "\nQ: %s\nA: %s", q, a)
		}
	}

	resp, err := p.Complete(ctx, agent.Request{
		System:    withLangDirective(criteriaSystemPrompt, lang),
		Messages:  []agent.Message{{Role: "user", Content: b.String()}},
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
