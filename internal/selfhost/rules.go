// Package selfhost encodes duckllo's own development conventions as
// harness rules so the runner sees them in every iteration's prompt
// when developing duckllo itself.
//
// Source of truth: CLAUDE.md (non-negotiables + lessons learned). The
// rules below are the executable form of that file — when CLAUDE.md
// changes, update this list and re-run `duckllo selfhost`.
package selfhost

// SeedRule is the codified form of one harness rule. The selfhost
// command POSTs each as `kind`+`name`+`body`+`phases` to the duckllo
// `harness_rules` table (skipping rules whose name already exists, so
// re-runs don't duplicate). Phases is optional — leaving it nil/empty
// is the default "applies to every PEVC phase" behaviour.
type SeedRule struct {
	Kind   string
	Name   string
	Body   string
	Phases []string // nil/empty = apply to every phase
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
		Phases: []string{"validate"},
	},
	{
		Kind: "judge_prompt",
		Name: "Trust the workspace diff, not the executor's claims",
		Body: `The "## Workspace changes" section in your prompt is the
output of  ` + "`git diff --no-color`" + `  inside the workspace right after
the executor finished. That diff is the ground truth of what happened
this iteration — base your verdict on it, not on the executor's
self-reported summary. The executor has been observed claiming files
were created when they weren't (see commit 28ae6eb).

If the diff is empty or doesn't contain the changes the criterion
demands, the verdict is fail (or warn, if you genuinely can't tell
from the diff alone).`,
		Phases: []string{"validate"},
	},
	{
		Kind: "agents_md",
		Name: "claude-code provider needs --permission-mode acceptEdits",
		Body: `When the duckllo client drives Claude Code (provider=claude-code),
the executor's prompt is sent to the ` + "`claude -p`" + ` subprocess. In
non-interactive print mode, file-editing tools prompt by default and
Claude Code will *describe* the change instead of making it. The
provider's default Args ship with --permission-mode acceptEdits; do
not strip it. See commit 28ae6eb for the bug-fix pair.`,
	},
	{
		Kind: "agents_md",
		Name: "Validator parks runs that didn't fully pass — don't bypass it",
		Body: `runValidator only advances with FinalStatus=done when every
criterion has a verdict of "pass". Anything else (fail / warn /
skipped / manual) leaves the run in 'validating', closes the
work_queue item, and waits for human input. Don't add code paths that
mark runs done unconditionally — that re-introduces the false-positive
validated specs the harness shipped with for two days. If you
legitimately want to force-finish a run, use POST /runs/{rid}/complete
(the human escape hatch) which updates the spec consistently.`,
	},
	{
		Kind: "agents_md",
		Name: "Annotations must enqueue, not just flip status",
		Body: `Posting a fix_required annotation does TWO things in
store.CreateAnnotation: (1) flips runs.status to 'correcting' so the
dashboard reflects what's happening, AND (2) inserts a 'correct'
work_queue row so the corrector agent has something to claim. Status
without a queue entry leaves the run permanently stuck. The insert is
idempotent (NOT EXISTS guard), so multiple annotations during one
correction cycle don't pile up duplicate work items.`,
	},
	{
		Kind: "agents_md",
		Name: "Iteration transcripts are durable; use them",
		Body: `Every iteration row carries a transcript column with the full
prompt + response (single-turn) or the entire multi-turn message
history (executor). Capped at 64 KiB. When debugging a misbehaving
run, the transcript is the source of truth for what the model
actually saw and said — don't try to reconstruct from logs or
re-run the spec. The Web UI's iteration timeline exposes a
"View transcript" expander on each card.`,
	},
	{
		Kind: "agents_md",
		Name: "SQL: parenthesise OR clauses before adding ANDs",
		Body: `SQL binds AND tighter than OR. The work_queue claim shipped
with

    WHERE status = 'pending'
       OR (status = 'claimed' AND lock_expires_at < NOW())
      AND ($2 IS NULL OR phase = ANY($2))

which silently parses as 'pending' OR (claimed AND expired AND phase
filter), so a runner asking phases=['execute'] would steal a pending
plan row. Fix is one set of outer parens around the status disjunction.
Whenever you write OR across status branches and follow it with another
filter, wrap the OR explicitly. See commit e1feafa for the bug-test pair.`,
	},
	{
		Kind: "agents_md",
		Name: "Lifecycle endpoints are atomic and full-cleanup",
		Body: `Whenever a run reaches a terminal state (done/failed/aborted),
three pieces of state must move together in one transaction: (1) the run
row, (2) any pending/claimed work_queue rows for that run, (3) the spec
status (validated on done, approved on failed/aborted/non-pass). And
(4) the HTTP handler must publish the resulting run.advanced over SSE
or the dashboard goes stale. AbortRun, AdvanceRun(failed),
CompleteRunByHuman all follow this pattern; if you add a new lifecycle
verb, port the pattern. See commit 1d5e742 for what happens when you
forget step (2).`,
	},
	{
		Kind: "agents_md",
		Name: "Spec → run is an atomic check-and-set",
		Body: `EnqueueRun gates the transition with
'UPDATE specs SET status=running WHERE id=$1 AND status=approved
RETURNING id'. If 0 rows come back, return ErrSpecNotEnqueueable —
which the HTTP handler maps to 400. This is the only thing keeping two
concurrent POST /runs against the same spec from spawning two runners
on the same workspace. Don't bypass it: if you need to "re-run" a
validated spec, the user should explicitly move the spec back to
approved (e.g. by editing criteria), not have the run-creation path
do an implicit transition.`,
	},
	{
		Kind: "agents_md",
		Name: "Tear down workspaces on phase error too",
		Body: `Orchestrator.Run() previously called teardown() only when the
re-fetched run.status was already terminal. But cmd/runner.Run's
Advance(FinalStatus=failed) call happens *after* Run() returns — so on
phase error the teardown saw 'executing' and skipped removal, leaking
one container per failure. Treat any non-nil err from a phase function
as "this run is going terminal" and tear down. The cost on a transient
error is one fresh container next claim; the cost of the old behaviour
was N containers per failing run. See commit 3a9027d.`,
	},
}
