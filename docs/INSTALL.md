# Installing Duckllo Skill for Claude Agents

This guide explains how to set up Duckllo so that **every Claude Code agent** on your machine automatically knows how to use the Duckllo kanban API.

## Prerequisites

- Duckllo server running (see [Deployment](#1-deploy-duckllo-server))
- Claude Code CLI installed
- An API key for your project

---

## 1. Deploy Duckllo Server

```bash
cd /path/to/duckllo
docker compose up --build -d
```

Server runs at `http://localhost:3000`. Verify:

```bash
curl -s http://localhost:3000/ | head -1
```

## 2. Create a User and Project

```bash
# Register
curl -s -X POST http://localhost:3000/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"myadmin","password":"mypassword","display_name":"Admin"}'

# Save the token from the response
TOKEN="<token-from-response>"

# Create a project
curl -s -X POST http://localhost:3000/api/projects \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"My Project"}'

# Save the project ID
PROJECT_ID="<id-from-response>"
```

## 3. Generate an API Key for Agents

```bash
curl -s -X POST "http://localhost:3000/api/projects/$PROJECT_ID/api-keys" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"label":"Claude Agent Key"}'
```

Save the `key` value (starts with `duckllo_`). This is shown only once.

## 4. Install Skill for All Claude Agents

There are three levels. Choose what fits your setup.

### Option A: Project-Level (recommended for teams)

Every agent working in a specific repo gets the skill automatically.

```bash
# In your project root
mkdir -p .claude/commands

# Copy the skill file as a slash command
cp /path/to/duckllo/SKILL.md .claude/commands/duckllo.md
```

Then add Duckllo config to your project's `CLAUDE.md`:

```markdown
## Duckllo Kanban

This project uses Duckllo for task tracking. Every feature and bug must be tracked.

- **Server**: http://localhost:3000
- **Project ID**: <your-project-id>
- **API Key**: <your-duckllo-api-key>

Before starting any work, create a card. See `/duckllo` for the full API reference.

### Workflow
1. Create card in Todo → move to In Progress
2. Code the feature
3. Test and update card with results
4. Upload demo GIF for UI features
5. Add git commit reference as comment
6. Move to Review → Done
```

Now any agent in this repo can type `/duckllo` to see the full API, and the `CLAUDE.md` tells them the workflow.

### Option B: User-Level (all your projects)

Every Claude agent session on your machine gets the skill.

```bash
# Create user-level commands directory
mkdir -p ~/.claude/commands

# Copy skill as a global slash command
cp /path/to/duckllo/SKILL.md ~/.claude/commands/duckllo.md
```

Then create `~/.claude/CLAUDE.md` with the Duckllo config:

```bash
cat >> ~/.claude/CLAUDE.md << 'EOF'

## Duckllo Kanban

All projects use Duckllo for task tracking at http://localhost:3000.
Run `/duckllo` for the full API reference.

### Agent Setup
When starting work on any project:
1. Check if the project has a Duckllo project ID and API key in its CLAUDE.md
2. If yes, follow the kanban workflow (create card first, then code)
3. If no, ask the user for the project ID and API key
EOF
```

### Option C: Per-Agent Bootstrap (for CI/automation)

Pass the skill directly when launching Claude Code:

```bash
claude --print "$(cat /path/to/duckllo/SKILL.md)" \
  --system-prompt "You are working on project $PROJECT_ID. Use API key $API_KEY to track work on Duckllo at http://localhost:3000. Always create a kanban card before starting any coding."
```

Or set it as an environment variable your automation reads:

```bash
export DUCKLLO_URL="http://localhost:3000"
export DUCKLLO_PROJECT="<project-id>"
export DUCKLLO_KEY="duckllo_<your-key>"
```

---

## 5. Verify the Installation

Start a new Claude Code session and test:

```
You: /duckllo
```

Claude should display the full Duckllo API reference. Then ask it to list cards:

```
You: list all cards on the duckllo kanban
```

Claude should call the API and show your board.

## 6. Add Agents as Project Members

Each agent that registers its own account should be added as a project member:

```bash
# Agent registers itself
curl -s -X POST http://localhost:3000/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"agent-claude","password":"agent123","display_name":"Claude Agent"}'

# Admin adds agent to project
curl -s -X POST "http://localhost:3000/api/projects/$PROJECT_ID/members" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"agent-claude","role":"member"}'
```

Or generate a scoped API key per agent (preferred — no password needed):

```bash
curl -s -X POST "http://localhost:3000/api/projects/$PROJECT_ID/api-keys" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"label":"Agent: Claude Opus"}'
```

## 7. Watch the Kanban for New Tasks

Agents can poll for new work:

```bash
# Continuous watch
node /path/to/duckllo/watch.js --key $DUCKLLO_KEY --project $PROJECT_ID

# One-shot check (good for cron or agent loops)
node /path/to/duckllo/watch.js --key $DUCKLLO_KEY --project $PROJECT_ID --once --json
```

---

## Quick Reference: File Locations

| What | Where |
|------|-------|
| Skill file (API reference) | `SKILL.md` |
| Development rules | `CLAUDE.md` |
| Project-level slash command | `.claude/commands/duckllo.md` |
| User-level slash command | `~/.claude/commands/duckllo.md` |
| Watcher script | `watch.js` |
| Server entry point | `server.js` |
| Docker stack | `docker-compose.yml` |

## Quality Gate Rules

The server enforces these rules — cards cannot be moved to Review or Done without meeting them:

| Card Type | Requirement |
|-----------|------------|
| All cards | At least a test result OR demo media |
| UI/UX/frontend cards (tagged `ui`, `ux`, `frontend`, `user-operation`, `user-facing`, `demo-required`) | MUST have demo GIF/media |
| Bug fix / performance | Test result suffices |

The server returns HTTP 422 if requirements are not met. Every new project auto-creates a pinned rules card explaining this.

## Password Reset

If an agent or user forgets their password:

```bash
# Admin generates a recovery code (run on server)
docker compose exec app node server.js --recover <username>

# User resets password
curl -X POST http://localhost:3000/api/auth/reset-password \
  -H "Content-Type: application/json" \
  -d '{"username":"<user>","recovery_code":"<code>","new_password":"<new>"}'
```
