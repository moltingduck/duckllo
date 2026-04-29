# duckllo

A **harness-engineering platform** for AI coding agents. Define a **Spec** with typed acceptance criteria; the runner agent walks a **Plan → Execute → Validate → Correct** loop; humans **review screenshots and draw boxes** to feed structured corrections back to the agent.

Built around the harness-engineering ideas Anthropic, OpenAI and Thoughtworks have been writing about — guides (feedforward) + sensors (feedback) + a steering loop where humans iterate the harness rather than micro-review every diff.

## Why duckllo

The agent is the model; the harness is everything around it. duckllo is the harness:

- **Spec** is the unit of work — intent + criteria, each criterion typed as a sensor target.
- **Plan** is versioned, draftable, approvable. Planner agent drafts; humans approve.
- **Run** is one execution attempt; iterations are durable turn-by-turn.
- **Verifications** are typed sensor outputs (computational, inferential, human).
- **Annotations** are how humans correct visually — draw a bbox on a screenshot, write a comment, the corrector agent reads it on its next turn.
- **Harness rules** are the steering loop — when an issue recurs, encode a rule and the runner concatenates it into every iteration's prompt.

Phase 1 ships the schema, Web UI, runner, sensors, and visual annotator. Phase 2 layers per-spec Docker isolation and a Tailscale sidecar so the dev server is reachable on the tailnet for visual validation without poking holes in the host.

## Quick start

```bash
git clone https://github.com/moltingduck/duckllo.git
cd duckllo

# 1. One-time env setup
make setup                  # copies .duckllo.env.example -> .duckllo.env
$EDITOR .duckllo.env        # at minimum: DUCKLLO_GIN_PASSWORD

# 2. Bring up Postgres
make db                     # docker compose up db -d

# 3. Run the server (auto-applies migrations, auto-loads .duckllo.env)
make serve                  # → http://localhost:3000

# 4. Open the UI, register, create a project, mint an API key under
#    Project → Settings, paste the key + project UUID + your
#    ANTHROPIC_API_KEY into .duckllo.env, then:
make runner                 # in a second shell
```

Both binaries auto-load `.duckllo.env` (then `.env`) from the cwd. Existing process env vars override the file, so CI/Docker runs work unchanged.

Open <http://localhost:3000>. First load → register a user (becomes admin). Create a project. Compose a spec, add criteria, approve. Click **Start run** — the planner drafts a plan, the executor runs it, sensors fire. Click any screenshot tile to draw a correction box.

## Components

```
cmd/duckllo serve     coordination plane (Express analogue, Go)
cmd/duckllo migrate   apply pending DB migrations
cmd/runner            harness daemon — claims work, drives PEVC

internal/
  http/        chi-based REST + SSE
  store/       data access layer (pgx + JSONB)
  runner/
    agent/     model provider interface + Anthropic adapter
    client/    HTTP wrapper around the duckllo API
    orchestrator/  PEVC phase machine
    tools/     allow-listed exec / file IO
  sensors/     shell + chromedp screenshot, registry
  webui/web/   vanilla ES2022 SPA, embedded into the binary
```

No Node toolchain. No bundler. The Go binary embeds the entire UI; deployment is one binary plus Postgres.

## Spec composer

Title + intent + criteria. Each criterion is a typed sensor target — `lint`, `unit_test`, `e2e_test`, `build`, `screenshot`, `judge`, `manual`. The runner reads `sensor_kind` to decide which sensor fires.

Example:

```jsonc
{
  "title": "Add dark-mode toggle",
  "intent": "Add a theme switcher in the header that persists across reloads.",
  "acceptance_criteria": [
    {"id": "c1", "text": "lint passes",        "sensor_kind": "lint"},
    {"id": "c2", "text": "toggle visible",     "sensor_kind": "screenshot",
                                                "sensor_spec": {"selector": ".theme-toggle"}},
    {"id": "c3", "text": "theme persists",     "sensor_kind": "judge"}
  ]
}
```

## Run dashboard

Two-column live view (powered by SSE). Left: iteration timeline coloured by phase (`plan` purple, `execute` blue, `validate` amber, `correct` red). Right: sensor grid — one tile per criterion, screenshots rendered inline. Click an image tile to open the annotator.

## Visual annotator

`<canvas>` overlay on the screenshot. Click + drag to draw a bbox. Pick a verdict (`fix_required` | `nit` | `acceptable`) and type a comment. `fix_required` flips the run to `correcting`; the corrector agent's next bundle includes the annotation as a structured signal in its prompt.

Coordinates are stored image-relative (0..1) so they survive viewport changes.

## Steering loop

`/projects/{pid}/steering` lets product managers edit the harness rules and topologies the runner sees on every iteration. Enable / disable individual rules, edit their bodies live, scope them to a topology. This is where humans converge the agent rather than re-reviewing every diff.

## Configuration

| Env var | Purpose |
|---|---|
| `DATABASE_URL` | Postgres DSN (default `postgres://duckllo:duckllo@localhost:5432/duckllo?sslmode=disable`) |
| `DUCKLLO_ADDR` | HTTP listen, default `:3000` |
| `DUCKLLO_UPLOADS` | Path for uploaded artifacts, default `uploads` |
| `DUCKLLO_GIN_PASSWORD` | One-shot bootstrap password for the gin steward account |
| `ANTHROPIC_API_KEY` | Used by the runner |
| `TAILSCALE_PREAUTH_KEY` | Read but unused in Phase 1 |
| `CONTAINER_RUNTIME` | `docker` (Phase 1) or `podman` (Phase 2 stretch) |

## API reference

See [SKILL.md](SKILL.md) for the full endpoint list, runner protocol, and event stream details.

## Development rules

See [CLAUDE.md](CLAUDE.md) for the source-of-truth rules every contributor (human or agent) must follow.

## Roadmap

**Phase 1 (current)** — schema, UI, runner, sensors, annotator. Runs locally.

**Phase 2** — per-spec Docker workspace, Tailscale sidecar so visual sensors hit the dev server over the tailnet, baseline screenshots + pixel diff, GIF sensor.

**Phase 3** — multi-provider model routing, MCP server adapter for Claude Code-native integration, harness-coverage analytics ("recurring failure" detection).
