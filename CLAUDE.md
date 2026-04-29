# Duckllo Development Rules

This file is the source of truth for how any agent (Claude or otherwise) works on this project. Read it before making changes.

## Owner Requirements (non-negotiable)

- The owner / system steward is `gin`. Never remove or demote this account.
- Every unit of work is a **Spec** — a structured document with intent + typed acceptance criteria. Don't bypass the spec model with ad-hoc commits.
- Every spec must reach `validated` (or `merged`) only after its acceptance criteria carry sensor verifications a human can read. Free-text "I tested it" is not a substitute for a verification.
- The platform must keep an account system with project-based permission and `duckllo_*` API keys for agents.
- Don't break existing features. Run `go vet ./...` and `go test ./...` before committing.

## Domain model

```
Spec ──► Plan(versioned) ──► Run ──► Iteration (per turn) ──► Verification (per sensor)
                                          │                          │
                                          └─► Annotation (human bbox+comment, on screenshots)
```

- **Spec** — title + intent + `acceptance_criteria[]`. Each criterion is a *typed sensor target* (`sensor_kind` ∈ lint | typecheck | unit_test | e2e_test | build | screenshot | gif | judge | manual).
- **Plan** — versioned per spec. Plans go `draft → approved → superseded`. Only one approved plan at a time. The runner's planner agent can produce one; humans can edit a draft before approval.
- **Run** — one execution attempt of a (spec, plan) pair. States: `queued → planning → executing → validating → correcting → done | failed | aborted`.
- **Iteration** — one model turn during a phase. Carries provider/model/token usage and a transcript pointer.
- **Verification** — typed sensor output. `class` is `computational | inferential | human`. Posting one with a `criterion_id` mirrors pass/fail back into the spec's criteria.
- **Annotation** — a human's bbox+comment on a screenshot/visual_diff/gif verification. Posting one with `verdict=fix_required` flips the parent run to `correcting`; the corrector agent then includes every open annotation as a structured signal in its next prompt.

## PEVC workflow (the harness loop)

For every spec:

1. **Compose** — UI: `/projects/{pid}/specs/new`. Title + intent + criteria (each typed). Approve.
2. **Plan** — Click "Start run". If no approved plan exists, the run starts in the `plan` phase and the planner agent drafts + auto-approves a plan.
3. **Execute** — Executor agent runs an inference + tool-call loop, editing files in the runner's workspace.
4. **Validate** — Sensors fire per criterion: shell sensors for lint/test/build, chromedp screenshot for visual, LLM judge for inferential. Each posts a `Verification` row.
5. **Correct** — Human reviews the sensor grid. Drawing a bbox + verdict on a screenshot creates an `Annotation`. `fix_required` annotations route back through the corrector agent → executor → validator.
6. **Merge / Done** — Human marks the spec `merged` once every criterion is green and they're satisfied.

Steering the loop: when an issue recurs, encode the rule in the project's harness rules (`/projects/{pid}/steering`). The runner concatenates enabled rules into every iteration's prompt — guides are how you stop chasing the same mistake.

## Quality gates

- A run cannot move past `validating` until at least one verification per non-`manual` criterion has been posted.
- A run cannot transition to `done` while any criterion's `satisfied` flag is false (the sensor mirror logic in `store.CreateVerification` keeps these in sync).
- Visual criteria (`screenshot`, `gif`, `visual_diff`) gate on artifact presence — no PNG = no pass.
- The runner's `advance` is server-validated. Phase transitions must follow `plan → execute → validate → correct → execute` (loop) or terminate via `final_status ∈ done | failed | aborted`.

## Code style

- Backend / runner: Go (1.26+). Stdlib first; pgx/v5 + chi/v5 + chromedp + bcrypt the only third-party imports. No ORMs.
- Frontend: vanilla HTML/CSS + ES2022 JS modules served from `embed.FS`. No bundler, no Node. Don't add a framework without explicit owner approval.
- SQL: parameterised queries always (`$1`, `$2`). Never interpolate user input.
- Auth: bcrypt for passwords; `crypto/rand` UUIDs for tokens; API keys prefixed with `duckllo_<8hex>_<48hex>` and the prefix is indexed.
- JSONB: store as raw bytes in models; marshal with `encoding/json`. Don't pre-decode at the row scanner.

## Git

- Commit messages: short summary, blank line, details if needed.
- End every commit with `Co-Authored-By: <agent name> <noreply@anthropic.com>`.
- One logical change per commit. Don't bundle unrelated changes.
- Never force-push. Never amend published commits.
- Never commit secrets, `.env` files, or database files. `.gitignore` already excludes the common ones.

## Testing

- `go vet ./...` and `go build ./...` are the floor — neither should break before commit.
- For new features, add a Go test alongside the package. Integration tests live under `test/`.
- Test results must be human-readable. Standard `testing.T` output is fine.

## Security

- Never disable `authenticate` or `requireProjectAccess` for convenience.
- API keys are project-scoped. Cross-project access is impossible by construction (the key carries `project_id`).
- Validate user input at API boundaries; reject unknown JSON fields (`DisallowUnknownFields`).
- File uploads: enforce size cap (`DUCKLLO_MAX_UPLOAD`) and content-type sanity.
- The runner's `exec` tool is allow-listed. Don't add new commands to the allow-list without thinking about supply-chain risk.

## File structure

```
cmd/
  duckllo/main.go        # server entrypoint (subcommands: serve, migrate)
  runner/main.go         # runner daemon entrypoint
internal/
  auth/                  # bcrypt + API-key minting
  bootstrap/             # gin steward seeding
  config/                # env-driven config
  db/                    # pgxpool + embedded migrations
  http/                  # routes, middleware, handlers, SSE
  models/                # row structs
  runner/
    agent/               # provider interface + Anthropic adapter
    client/              # HTTP wrapper around the duckllo API
    orchestrator/        # PEVC phase machine
    tools/               # whitelisted exec / file IO
  sensors/               # shell + screenshot sensors, registry
  store/                 # data access (one file per entity)
  uploads/               # multipart artifact storage
  webui/web/             # static HTML/CSS/JS UI, embedded
```

## How to use the API

See `SKILL.md` for the full reference. Quick path:

```bash
KEY="duckllo_<your-key>"
PID="<project-uuid>"

# 1. Create a spec
SID=$(curl -s -X POST http://localhost:3000/api/projects/$PID/specs \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"title":"Add dark-mode toggle","intent":"Add a theme switcher in the header"}' | jq -r '.id')

# 2. Add a criterion
curl -s -X POST http://localhost:3000/api/projects/$PID/specs/$SID/criteria \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"text":"theme persists after reload","sensor_kind":"judge"}'

# 3. Approve + start run (planner will draft a plan if none exists)
curl -s -X POST http://localhost:3000/api/projects/$PID/specs/$SID/approve -H "Authorization: Bearer $KEY"
RID=$(curl -s -X POST http://localhost:3000/api/projects/$PID/specs/$SID/runs \
  -H "Authorization: Bearer $KEY" | jq -r '.id')

# 4. Watch it via SSE
curl -N "http://localhost:3000/api/projects/$PID/events?token=$KEY"
```

## Self-hosting

`duckllo selfhost` (or `make selfhost`) bootstraps the dogfood loop:
ensures the `gin` steward, ensures a project named `duckllo`, mints an
API key labeled `selfhost-runner`, seeds the project's harness rules
from the codified list in `internal/selfhost/rules.go`, and writes
`.duckllo.env` with `DUCKLLO_URL`/`DUCKLLO_PROJECT`/`DUCKLLO_KEY`
pre-filled. Idempotent on re-run — only the freshly-minted key
triggers an env-file write because the plaintext is unrecoverable
afterwards.

When CLAUDE.md's rules change, mirror the change in
`internal/selfhost/rules.go` and re-run `selfhost`. New rules are
appended; existing rules with the same name are left in place so
operators can edit them in the UI without selfhost overwriting their
work.

## Lessons Learned

Patterns discovered during development. Update this section as new ones emerge.

- **chromedp + Chrome path**: on macOS, set `--chrome-path` to `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome` if the default exec allocator can't find Chromium.
- **pgx JSONB**: keep raw `[]byte` in model fields and let handlers `json.Marshal/Unmarshal` on the boundary; this avoids double-encoding bugs and keeps the row scan dumb.
- **Sliding session**: TouchSession is throttled — bumps `expires_at` only when remaining lease < TTL − 24h, so it's effectively one UPDATE per session per day even on chatty UIs.
- **work_queue claim**: `FOR UPDATE SKIP LOCKED` is the right primitive; without it two runners polling at the same instant will conflict on the same row.
- **Heartbeat lease**: 90s. The runner heartbeats every 30s. If the runner dies, another runner can reclaim after 90s lapses.
- **API key prefix index**: bcrypt-comparing every key on every request is slow; the indexed `key_prefix` narrows lookups to O(1).

## Agent checklist

Before submitting any work, verify:

- [ ] A spec exists for the change (or the change is a doc/infra-only commit explicitly noted in the message)
- [ ] All criteria carry a verification (or are explicitly `manual` and noted)
- [ ] No criterion in `fail` status without a follow-up plan
- [ ] `go vet ./...` clean
- [ ] `go build ./...` clean
- [ ] Existing tests still pass
- [ ] Commit ends with `Co-Authored-By` line
- [ ] No secrets or DB files in the diff
- [ ] If the spec touched UI, a screenshot verification exists
