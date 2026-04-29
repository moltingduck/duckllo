# Duckllo API Reference

This is the contract a duckllo client (the per-host driver, today
`cmd/runner`) uses to talk to a duckllo server. All endpoints under
`/api/` are JSON unless noted (artifact upload is multipart). Bearer
auth is either a session UUID (web UI) or a `duckllo_<prefix>_<secret>`
API key (client / agent).

## Architecture: server / client / agent

```
[ user ] ──► [ SERVER ]  ◄── many ──► [ CLIENT ] ──► [ agent on same host ]
```

- **Server** is the single coordination plane (this file describes its
  REST surface). It owns Postgres, the SSE event bus, the Web UI, and
  every spec / plan / run / iteration / verification record.
- **Client** is the per-host daemon. It authenticates with a project
  API key, claims work via `POST /work/claim`, drives a local agent
  through one PEVC phase, and posts iterations + verifications back.
  Multiple clients per project is supported and recommended —
  `FOR UPDATE SKIP LOCKED` makes concurrent claims race-free.
- **Agent** is what the client drives this iteration. Four providers
  ship today: `anthropic`, `openai`, `ollama`, and `claude-code`
  (which shells out to the `claude` CLI on the same host so a
  developer's Claude Code session is driven by duckllo specs).

Users only ever talk to the server. The client is invisible to the
user beyond the `runner_id` badge on each run.

## Endpoint map

```
GET    /api/health                     liveness
GET    /api/status                     bootstrap state (unauthenticated)

POST   /api/auth/register              first user becomes admin
POST   /api/auth/login                 → session token
GET    /api/auth/me                    identity probe (auth required)
POST   /api/auth/logout

GET    /api/projects                   list user's projects
POST   /api/projects                   create

──── /api/projects/{pid}/ ────
GET    /                               read project
PATCH  /                               edit
DELETE /                               (owner only)

GET    /members
POST   /members                        add by username or user_id
DELETE /members/{uid}                  refuses to remove gin

GET    /api-keys                       list project-scoped keys
POST   /api-keys                       mint (plaintext returned once)
DELETE /api-keys/{kid}

GET    /specs?status=                  list
POST   /specs                          create

GET    /specs/{sid}                    spec + plans tree
PATCH  /specs/{sid}                    edit (intent, criteria, assets)
POST   /specs/{sid}/criteria           append a criterion
POST   /specs/{sid}/approve            spec → approved
POST   /specs/{sid}/reject             spec → rejected
POST   /specs/{sid}/plans              create draft plan
POST   /specs/{sid}/runs               enqueue a run

PATCH  /plans/{pid}                    edit draft only
POST   /plans/{pid}/approve            draft → approved

GET    /runs/{rid}                     run + iterations
GET    /runs/{rid}/bundle              client's context-pull (one call/turn)
POST   /runs/{rid}/abort
POST   /runs/{rid}/heartbeat           lease keepalive
POST   /runs/{rid}/advance             phase transition
POST   /runs/{rid}/iterations          append iteration
POST   /runs/{rid}/workspace           record container_id, dev_url, etc.
GET    /runs/{rid}/verifications       list
POST   /runs/{rid}/verifications       JSON or multipart (artifact + meta)

PATCH  /iterations/{iid}               streaming updates
PATCH  /verifications/{vid}            human override
GET    /verifications/{vid}/annotations
POST   /verifications/{vid}/annotations  draw a bbox

GET    /comments?target_kind=&target_id=
POST   /comments                       generic threaded discussion

GET    /topologies
POST   /topologies
GET    /harness-rules                  enabled rules, optional topology filter
POST   /harness-rules
PATCH  /harness-rules/{rid}
DELETE /harness-rules/{rid}

GET    /steering/recurring-failures    aggregate failure signal

GET    /events                         SSE: spec.* run.* iteration.*
                                       verification.* annotation.*
                                       comment.* run.workspace_set

POST   /work/claim                     client pulls next pending work item
                                       (project API key required)
```

## Auth

Two token formats:
- **Session UUID** — issued by `/api/auth/login`. 30-day TTL; sliding
  (extends on every authenticated request when remaining lease < TTL − 24h).
- **API key** — issued by `POST /api/projects/{pid}/api-keys`. Format
  `duckllo_<8hex>_<48hex>`. Project-scoped; cannot access other
  projects. The plaintext is returned once at creation and never again.

Both go in `Authorization: Bearer <token>`.

For SSE, EventSource cannot set headers, so `?token=<...>` is honoured
on `/events`.

## Spec lifecycle

```
draft → proposed → approved → running → validated → merged
                          └→ rejected
```

`draft` is the default at creation. Move to `approved` via
`POST /specs/{sid}/approve`. The first `POST /specs/{sid}/runs` flips
the spec to `running`. When the run finishes successfully it lands in
`validated`. Humans set `merged` (final).

## Acceptance criteria

Each criterion is a typed sensor target. Shape stored in
`specs.acceptance_criteria` JSONB:

```json
{
  "id": "<uuid>",
  "text": "User can toggle theme from the header",
  "sensor_kind": "screenshot",
  "sensor_spec": { "url": "/", "selector": ".theme-toggle" },
  "satisfied": false,
  "last_verification_id": null
}
```

`sensor_kind` ∈ `lint | typecheck | unit_test | e2e_test | build |
screenshot | visual_diff | gif | judge | manual`. The client's sensor
registry maps kinds to implementations. `visual_diff` is the screenshot
sensor's behaviour when `sensor_spec.baseline_url` is set — it captures
+ pixel-diffs against the baseline.

## Run states

`queued → planning → executing → validating → correcting → done | failed | aborted`

Held-by-client states (`planning`, `executing`, `validating`,
`correcting`) are accompanied by a 90-second lock with `runner_id`. The
client heartbeats every 30s. If the client dies, the lock expires and
another client can reclaim.

## Client protocol

A client that holds a project API key drives the loop:

1. **Claim.** `POST /work/claim {"runner_id":"<id>","phases":["plan","execute","validate","correct"]}` returns `{work_item, run}` or `204` (no work).
2. **Bundle.** `GET /runs/{rid}/bundle` returns the spec, plan, harness
   rules, prior iterations, prior verifications, and any open
   annotations. This is the only context endpoint the client needs per
   turn.
3. **Inference.** Client calls a provider:
   - `anthropic` — `/v1/messages` with API key
   - `openai` — `/v1/chat/completions` with API key
   - `ollama` — local `/api/chat`
   - `claude-code` — `claude -p` subprocess on the same host
4. **Tools** (Phase 1): `read_file`, `write_file`, `list_dir`, `exec`
   (allow-listed). Run inside `--workspace` on the host.
   (Phase 2): same tools but `docker exec` against the spec's
   per-run container.
5. **Append iteration.** `POST /runs/{rid}/iterations` with phase,
   agent_role, provider, model, summary, transcript pointer.
6. **Run sensors** (validate phase): `POST /runs/{rid}/verifications`
   per criterion. JSON for plain results, multipart (`file` + JSON
   `meta` field) for screenshot/gif artifacts.
7. **Heartbeat.** `POST /runs/{rid}/heartbeat {"runner_id":"<id>"}` every 30s while working.
8. **Advance.** `POST /runs/{rid}/advance {"runner_id":"...","from_phase":"...","to_phase":"...","plan_id":"<optional>","final_status":"<optional>"}`. The planner uses `plan_id` to atomically bind a freshly-created plan to the run.

## Verification shape

```json
{
  "criterion_id": "<uuid>",
  "kind":   "screenshot",
  "class":  "computational",
  "direction": "feedback",
  "status": "pass",
  "summary": "captured 1280x800 of /",
  "artifact_url": "/api/uploads/<uuid>.png",
  "details": { "url": "...", "selector": "..." }
}
```

For multipart uploads, post to `POST /runs/{rid}/verifications` with
`file` (the binary) and `meta` (JSON string of the same shape minus
`artifact_url`).

## Annotations (the correction signal)

```json
{
  "bbox":    { "x": 0.12, "y": 0.04, "w": 0.28, "h": 0.06 },
  "body":    "this button should be on the left",
  "verdict": "fix_required"
}
```

`bbox` is image-relative (0..1) so annotations survive viewport
changes. `verdict` ∈ `fix_required | nit | acceptable`. Posting
`fix_required` flips the parent run to `correcting` server-side; the
corrector agent's next bundle will include the annotation.

## SSE events

`GET /api/projects/{pid}/events` (or with `?token=...`) streams typed
events. Drive live UI updates from these. Topics:

| Topic | Body |
|---|---|
| `spec.created` | spec |
| `spec.updated` | spec |
| `spec.criteria_changed` | spec |
| `run.queued` | run |
| `run.advanced` | run |
| `run.workspace_set` | `{run_id, workspace_meta}` |
| `iteration.appended` | iteration |
| `iteration.updated` | iteration |
| `verification.posted` | verification |
| `annotation.added` | annotation |
| `comment.posted` | comment |

## Harness rules

Each rule is a piece of guidance the client concatenates into every
iteration's prompt. Edit via `/projects/{pid}/steering` in the UI, or
seed via `duckllo selfhost`. Kinds:

- `agents_md` — generic conventions
- `skill` — a named capability with usage notes
- `lint_config` — describes the project's lint rules
- `architectural_rule` — module / dependency boundaries
- `judge_prompt` — extra criteria the validator's LLM-judge should weigh

Disabled rules are excluded from the bundle.

## Recurring failures

`GET /steering/recurring-failures` aggregates verifications grouped by
(spec, criterion, kind) where status ∈ {fail,warn} over the last 30
days, count ≥ 2. The Web UI's "Recurring failures" tab reads this and
offers a one-click "encode as rule" button that drops a draft into the
new-rule form.

## Workspace metadata

Phase 2 only. The client posts `POST /runs/{rid}/workspace` with a
JSONB blob after provisioning a Docker container. Shape:

```json
{
  "kind": "docker",
  "container_id": "<sha>",
  "workspace": "/workspace",
  "tailscale_node": "<sha>",
  "tailscale_host": "duckllo-<short-run-id>",
  "dev_url": "http://duckllo-<short-run-id>"
}
```

`dev_url` is what the screenshot sensor hits — Phase 1 it's empty and
sensors fall back to `localhost`; Phase 2 with a Tailscale sidecar it
points at the tailnet hostname.

## Error semantics

- `400` invalid input
- `401` missing or bad bearer token
- `403` authenticated but not a project member (or not a product
  manager for restricted endpoints)
- `404` resource doesn't exist or doesn't belong to the loaded project
- `409` conflict (claim race, plan already approved)
- `410` lock expired or runner mismatch on heartbeat/advance

Errors are JSON: `{"error":"<message>"}`.

## MCP adapter

`bin/mcp-duckllo` exposes a small subset of the API over stdio
JSON-RPC so Claude Code can drive duckllo natively (without spawning
its own client). Tools:

- `duckllo_list_specs(status?)`
- `duckllo_create_spec(title, intent, priority?)`
- `duckllo_add_criterion(spec_id, text, sensor_kind, sensor_spec?)`
- `duckllo_approve_spec(spec_id)`
- `duckllo_start_run(spec_id)`
- `duckllo_get_run(run_id)`
- `duckllo_list_verifications(run_id)`
- `duckllo_post_annotation(verification_id, bbox, body, verdict)`

Configure in Claude Code's MCP settings as
`{ "mcpServers": { "duckllo": { "command": "/path/to/bin/mcp-duckllo" } } }`
with `DUCKLLO_URL`, `DUCKLLO_KEY`, `DUCKLLO_PROJECT` exported (or in
`.duckllo.env`).
