# Duckllo Agent Skill Guide

You are interacting with **Duckllo**, a Kanban board for tracking features, bugs, and tasks. This document tells you how to use the Duckllo API to manage cards on the board.

## Quick Start

**Base URL**: `http://localhost:3000`

All API requests require authentication via the `Authorization` header:
```
Authorization: Bearer <your_api_key>
```

Your API key starts with `duckllo_` and is scoped to a specific project. You do not need to specify the project in the header -- it is inferred from the key. However, you must include the `project_id` in the URL path.

## Authentication

### Using an API Key (recommended for agents)

You should have been given an API key that looks like `duckllo_abc123...`. Use it as a Bearer token:

```bash
curl -H "Authorization: Bearer duckllo_<key>" http://localhost:3000/api/projects
```

If you don't have a key, ask the project owner to generate one in **Settings > API Keys**.

### Using Username/Password (for bootstrapping)

```bash
# Register
curl -X POST http://localhost:3000/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username": "my_agent", "password": "secret123", "display_name": "My Agent"}'

# Login (returns a session token)
curl -X POST http://localhost:3000/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username": "my_agent", "password": "secret123"}'
# Response: {"token": "...", "user": {...}}
```

### Agent Self-Registration (first time setup)

If you are an agent starting work on a project and no Duckllo project ID or API key is configured, bootstrap yourself:

1. **Register** an account using your agent name (e.g. `claude-opus`)
2. **Create a project** using the **current folder name** (or git repository name) as the project name. This is the naming convention — every project maps to its directory/repo name so agents across sessions can find the same board.
3. **Generate an API key** and store it in the project's `CLAUDE.md` for future sessions.

```bash
# Example: agent working in ~/Projects/my-app
REPO_NAME=$(basename "$(git rev-parse --show-toplevel 2>/dev/null || pwd)")

# Register (skip if already registered)
TOKEN=$(curl -s -X POST http://localhost:3000/api/auth/register \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"claude-agent\",\"password\":\"agent123\",\"display_name\":\"Claude Agent\"}" \
  | jq -r '.token // empty')

# If already registered, login instead
[ -z "$TOKEN" ] && TOKEN=$(curl -s -X POST http://localhost:3000/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"claude-agent","password":"agent123"}' | jq -r '.token')

# Check if project already exists (search by repo name)
PID=$(curl -s http://localhost:3000/api/projects \
  -H "Authorization: Bearer $TOKEN" \
  | jq -r ".[] | select(.name == \"$REPO_NAME\") | .id // empty")

# Create project if it doesn't exist
if [ -z "$PID" ]; then
  PID=$(curl -s -X POST http://localhost:3000/api/projects \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "{\"name\":\"$REPO_NAME\"}" | jq -r '.id')
fi

# Generate API key
API_KEY=$(curl -s -X POST "http://localhost:3000/api/projects/$PID/api-keys" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"label":"Claude Agent"}' | jq -r '.key')

echo "Project: $REPO_NAME ($PID)"
echo "API Key: $API_KEY"
```

**Important**: Always use the folder/repo name as the project name. This ensures all agents working on the same codebase share the same kanban board.

## Core Concepts

- **Project**: A board named after its folder/repository. Default columns: `Backlog`, `Todo`, `In Progress`, `Review`, `Done`.
- **Card**: An item on the board. Has a type, priority, column, testing status, and optional demo GIF.
- **Card types**: `feature`, `bug`, `task`, `improvement`
- **Priorities**: `low`, `medium`, `high`, `critical`
- **Testing status**: `untested`, `passing`, `failing`, `partial`

## API Reference

All requests use JSON. Set `Content-Type: application/json` for POST/PATCH requests.
Replace `<pid>` with the project ID and `<cid>` with the card ID.

### List Projects

```
GET /api/projects
```

Returns all projects you have access to. Use the `id` field as `<pid>` in other calls.

### List Cards

```
GET /api/projects/<pid>/cards
```

Returns all cards in the project. Each card has:
```json
{
  "id": "uuid",
  "title": "Fix login bug",
  "description": "Login fails when...",
  "card_type": "bug",
  "column_name": "In Progress",
  "priority": "high",
  "testing_status": "failing",
  "testing_result": "test output here...",
  "demo_gif_url": "/uploads/abc.gif",
  "labels": ["auth", "urgent"],
  "position": 0
}
```

### Create a Card

```
POST /api/projects/<pid>/cards
```

Body:
```json
{
  "title": "Implement search feature",
  "description": "Add full-text search across card titles and descriptions",
  "card_type": "feature",
  "column_name": "Todo",
  "priority": "medium",
  "labels": ["search", "v2"]
}
```

Required: `title`. Everything else has sensible defaults (type=feature, column=Backlog, priority=medium).

### Update a Card

```
PATCH /api/projects/<pid>/cards/<cid>
```

Send only the fields you want to change:
```json
{
  "testing_status": "passing",
  "testing_result": "All 5 tests passed.\n  [PASS] test_login\n  [PASS] test_logout",
  "column_name": "Review"
}
```

Updatable fields: `title`, `description`, `card_type`, `column_name`, `position`, `priority`, `assignee_id`, `testing_status`, `testing_result`, `demo_gif_url`, `labels`.

### Move a Card

```
POST /api/projects/<pid>/cards/<cid>/move
```

Body:
```json
{
  "column_name": "Done",
  "position": 0
}
```

Valid columns: `Backlog`, `Todo`, `In Progress`, `Review`, `Done` (or whatever the project is configured with).

### Delete a Card

```
DELETE /api/projects/<pid>/cards/<cid>
```

### Add a Comment

```
POST /api/projects/<pid>/cards/<cid>/comments
```

Body:
```json
{
  "content": "Fixed the race condition. Root cause was...",
  "comment_type": "agent_update"
}
```

Comment types: `comment` (default), `agent_update`, `test_result`.

### List Comments

```
GET /api/projects/<pid>/cards/<cid>/comments
```

### Upload Demo GIF

```
POST /api/projects/<pid>/cards/<cid>/upload
Content-Type: multipart/form-data
```

```bash
curl -X POST \
  -H "Authorization: Bearer duckllo_<key>" \
  -F "file=@demo.gif" \
  http://localhost:3000/api/projects/<pid>/cards/<cid>/upload
```

Accepted formats: `.gif`, `.png`, `.jpg`, `.jpeg`, `.webp`, `.mp4`. Max 50MB.

## Typical Agent Workflows

### 1. Report a bug you found

```bash
# Create the bug card
curl -X POST http://localhost:3000/api/projects/<pid>/cards \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{
    "title": "Memory leak in connection pool",
    "description": "RSS grows 2MB/hour under sustained load",
    "card_type": "bug",
    "column_name": "Todo",
    "priority": "critical",
    "labels": ["memory", "backend"]
  }'
```

### 2. Update a card with test results

```bash
curl -X PATCH http://localhost:3000/api/projects/<pid>/cards/<cid> \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{
    "testing_status": "passing",
    "testing_result": "Suite: AuthTests\n  [PASS] login\n  [PASS] register\n  [PASS] logout\n\n3/3 passed (0.4s)"
  }'
```

### 3. Move a card to Done after fixing it

```bash
curl -X POST http://localhost:3000/api/projects/<pid>/cards/<cid>/move \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"column_name": "Done", "position": 0}'
```

### 4. Add a diagnostic comment

```bash
curl -X POST http://localhost:3000/api/projects/<pid>/cards/<cid>/comments \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{
    "content": "Root cause: unclosed DB connections in pool.js:142. Fix: add cleanup in reconnect handler.",
    "comment_type": "agent_update"
  }'
```

### 5. Upload a demo GIF after implementing a feature

```bash
curl -X POST \
  -H "Authorization: Bearer $KEY" \
  -F "file=@feature-demo.gif" \
  http://localhost:3000/api/projects/<pid>/cards/<cid>/upload
```

### 6. Full workflow: create card, implement, test, move to done

```bash
PID="<project-id>"
KEY="duckllo_<your-key>"
API="http://localhost:3000/api/projects/$PID"

# 1. Create card
CID=$(curl -s -X POST "$API/cards" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"title":"Add search","card_type":"feature","column_name":"In Progress"}' \
  | jq -r '.id')

# 2. ... do the work ...

# 3. Update with test results
curl -s -X PATCH "$API/cards/$CID" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"testing_status":"passing","testing_result":"All tests pass"}'

# 4. Upload demo
curl -s -X POST "$API/cards/$CID/upload" \
  -H "Authorization: Bearer $KEY" -F "file=@demo.gif"

# 5. Add completion comment
curl -s -X POST "$API/cards/$CID/comments" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"content":"Feature implemented and tested.","comment_type":"agent_update"}'

# 6. Move to Done
curl -s -X POST "$API/cards/$CID/move" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"column_name":"Done","position":0}'
```

## Watching for Updates

### Activity Feed API

```
GET /api/projects/<pid>/activity?since=<ISO-timestamp>&limit=50
```

Returns card updates and new comments since the given timestamp. Events are sorted newest-first.

```bash
curl -H "Authorization: Bearer $KEY" \
  "http://localhost:3000/api/projects/<pid>/activity?since=2026-03-05T00:00:00Z"
```

### Watcher Script

Run `watch.js` to poll the kanban for changes:

```bash
# Watch continuously (prints new events every 15s)
node watch.js --key duckllo_xxx --project <pid>

# Check once and exit
node watch.js --key duckllo_xxx --project <pid> --once

# JSON output (pipe to jq or other tools)
node watch.js --key duckllo_xxx --project <pid> --json

# Custom poll interval (30 seconds)
node watch.js --key duckllo_xxx --project <pid> --interval 30

# Using environment variables
DUCKLLO_KEY=duckllo_xxx DUCKLLO_PROJECT=<pid> node watch.js
```

Output looks like:
```
[7:28:19 AM] CARD FEATURE in Done: Express + SQLite backend [passing]
[7:49:21 AM] COMMENT by Claude Opus 4.6 on "API key system": Git commit: bbc891d ...
[8:02:33 AM] CARD TASK in Backlog: CLAUDE.md - Development Rules [passing]
```

### Agent Integration Pattern

Run the watcher as a background process and react to events:

```bash
# Start watcher in background, pipe to a log
node watch.js --key $KEY --project $PID --json >> /tmp/kanban-events.log &

# In your agent loop, tail the log for new assignments
tail -f /tmp/kanban-events.log | while read line; do
  echo "$line" | jq -r 'select(.event_type == "card_updated" and .column_name == "Todo")'
done
```

## Error Handling

All errors return JSON with an `error` field:
```json
{"error": "Authentication required"}
{"error": "Card not found"}
{"error": "Invalid column. Valid columns: Backlog, Todo, In Progress, Review, Done"}
```

HTTP status codes: `400` (bad request), `401` (auth required), `403` (forbidden), `404` (not found), `409` (conflict).

## Deployment

Run with Docker Compose (recommended):
```bash
docker compose up --build -d
```

This starts PostgreSQL + the app on port 3000. Data persists in Docker volumes (`pgdata`, `uploads`).

To run locally without Docker, set `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD` env vars pointing to a PostgreSQL instance.

## Installing the Skill for Claude Agents

### Quick Install (recommended)

```bash
cd /path/to/duckllo
./install.sh
```

Interactive installer — prompts for global vs project scope, server URL, project ID, API key. See below for manual steps.

### Method 1: Project-Level (agents in one repo)

```bash
# From your target project directory:
mkdir -p .claude/commands
cp /path/to/duckllo/SKILL.md .claude/commands/duckllo.md
```

Then add to your project's `CLAUDE.md`:

```markdown
## Duckllo Kanban
- Server: http://localhost:3000
- Project ID: <your-project-id>
- API Key: duckllo_<your-key>
Run /duckllo for the full API. Always create a kanban card before coding.
```

### Method 2: User-Level (all projects on this machine)

```bash
mkdir -p ~/.claude/commands
cp /path/to/duckllo/SKILL.md ~/.claude/commands/duckllo.md
```

Add Duckllo config to `~/.claude/CLAUDE.md` (same content as above).

### Method 3: Per-Agent (CI / automation)

Set environment variables and pass instructions at launch:

```bash
export DUCKLLO_URL="http://localhost:3000"
export DUCKLLO_PROJECT="<project-id>"
export DUCKLLO_KEY="duckllo_<key>"
```

### After Installation

Start a new Claude Code session and verify:

```
/duckllo                    — Shows the full API reference
"list cards on kanban"      — Agent calls the API and shows your board
"create a card for X"       — Agent creates a card via the API
```

The `/duckllo` slash command is available anywhere Claude Code runs, as long as the `duckllo.md` file is in the appropriate `.claude/commands/` directory.

### File Paths Reference

| Scope | Slash command file | Config file |
|-------|-------------------|-------------|
| Project | `<project>/.claude/commands/duckllo.md` | `<project>/CLAUDE.md` |
| User (global) | `~/.claude/commands/duckllo.md` | `~/.claude/CLAUDE.md` |

## Notes

- API keys are project-scoped. One key works for one project only.
- The `testing_result` field is displayed in monospace font -- format it like terminal output for readability.
- Use `\n` for newlines in JSON string fields.
- Card `position` is zero-indexed within a column. Use `0` to place at top.
- Labels are stored as a JSON array of strings.
