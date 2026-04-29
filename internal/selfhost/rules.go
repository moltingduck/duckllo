// Package selfhost encodes duckllo's own development conventions as
// harness rules so the runner sees them in every iteration's prompt
// when developing duckllo itself.
//
// Source of truth: CLAUDE.md (non-negotiables + lessons learned). The
// rules below are the executable form of that file — when CLAUDE.md
// changes, update this list and re-run `duckllo selfhost`.
package selfhost

// SeedRule is the codified form of one harness rule. The selfhost
// command POSTs each as `kind`+`name`+`body` to the duckllo
// `harness_rules` table (skipping rules whose name already exists, so
// re-runs don't duplicate).
type SeedRule struct {
	Kind string
	Name string
	Body string
}

// SelfHostRules is the canonical list. Order matches how a developer
// reads CLAUDE.md: stewardship, language, testing, commit etiquette,
// then specific lessons.
var SelfHostRules = []SeedRule{
	{
		Kind: "agents_md",
		Name: "Gin is the steward",
		Body: `The owner / system steward of every duckllo project is the user 'gin'.
Never remove or demote this account. The bootstrap routine creates
gin from DUCKLLO_GIN_PASSWORD; once present, it is auto-attached to
every project as a product_manager. CreateProject already does the
auto-attach — don't add code paths that bypass it.`,
	},
	{
		Kind: "agents_md",
		Name: "Go on the backend, no Node toolchain",
		Body: `Backend, runner, MCP adapter, and the migration runner are all in
Go. The frontend is plain HTML/CSS/ES2022 modules served from an
embed.FS — no Node, no npm, no bundler. Don't add a JS build step.
Don't pull in a frontend framework without explicit owner approval.`,
	},
	{
		Kind: "agents_md",
		Name: "JSONB columns use json.RawMessage in models",
		Body: `JSONB row fields (acceptance_criteria, plan steps, workspace_meta,
verification details, etc.) MUST be typed as json.RawMessage in the
models package — not []byte. Using []byte makes json.Marshal emit
base64 in API responses, which broke the integration test in commit
f131c84 and the workaround in the Web UI's readJSON helper carried
the cost for an entire phase. RawMessage passes JSON through
verbatim.`,
	},
	{
		Kind: "agents_md",
		Name: "FOR UPDATE doesn't compose with aggregates",
		Body: `Postgres rejects 'SELECT MAX(...) ... FOR UPDATE' with SQLSTATE
0A000. Use pg_advisory_xact_lock(hashtext(scope || ':' || id)) to
serialise per-row instead. See store/iterations.go and store/plans.go
for the canonical pattern.`,
	},
	{
		Kind: "agents_md",
		Name: "docker ps wants --no-trunc for stable IDs",
		Body: `'docker ps --format {{.ID}}' returns the 12-char short ID by
default while 'docker run -d' returns the full 64-char ID. The
adopt-by-name path in Workspace.DockerExecutor must use --no-trunc
or its containerID won't match a fresh provision's. See commit
6915eca for the bug-test pair.`,
	},
	{
		Kind: "agents_md",
		Name: "Tests before commit",
		Body: `Run 'go vet ./...' and 'go build ./...' before every commit. If
TEST_DATABASE_URL is set, run 'go test ./...' as well — the
integration tier asserts the harness coordination plane end-to-end
and surfaces real bugs (it caught both of the items above before
they shipped).`,
	},
	{
		Kind: "agents_md",
		Name: "Commit etiquette",
		Body: `One logical change per commit. Short summary line, blank line,
details. End every commit with:

    Co-Authored-By: <agent name> <noreply@anthropic.com>

Never force-push. Never amend published commits. Never commit
.duckllo.env, secrets, or DB files (.gitignore already excludes
the common ones).`,
	},
	{
		Kind: "agents_md",
		Name: "No emojis unless explicitly requested",
		Body: `Don't add emojis to code, commit messages, or documentation
unless the user explicitly asks. Plain ASCII reads better in
diffs and survives every terminal.`,
	},
	{
		Kind: "agents_md",
		Name: "Spec contract is locked once approved",
		Body: `Spec intent + acceptance_criteria are editable while status is
draft or proposed. Once a spec is approved (or any later state),
the contract is frozen — change it and the runner sees a
different target than the run was started against. The Web UI
hides edit affordances past 'approved'; don't bypass the gate via
the API.`,
	},
	{
		Kind: "agents_md",
		Name: "Sensors are typed, not free-text",
		Body: `Verifications carry a kind + class + status + structured details.
Don't post status='pass' with summary='works ok' — at minimum,
include the command argv (shell sensors) or the URL+selector
(visual sensors) in details so a human can reproduce the result.
The aggregate steering view depends on this signal.`,
	},
	{
		Kind: "skill",
		Name: "Adding a new sensor kind",
		Body: `1. Implement the sensor: a struct with Kind() and Run(ctx, c, env).
2. Register in internal/sensors/registry.go.
3. Add it to the SENSOR_KINDS array in
   internal/webui/web/pages/spec-new.js so the spec composer's
   selector includes it.
4. Update the criterion JSON shape doc in SKILL.md.
5. Add a unit test alongside (skip-if-no-Chrome for visual kinds).
6. If the sensor needs historical artifacts, use env.Fetch — don't
   add a second auth path.`,
	},
	{
		Kind: "judge_prompt",
		Name: "Inferential judge: keep it short and structured",
		Body: `When you're acting as the validator's LLM-as-judge, output a
single fenced JSON block with shape
  {"verdicts":[{"criterion_id":"...","status":"pass|fail|warn","summary":"..."}]}
and nothing else. Ambient prose breaks the parser; missing fields
silently demote the run to status=warn.`,
	},
}
