#!/usr/bin/env bash
set -euo pipefail

# ── Duckllo Skill Installer for Claude Code ──────────────────────────────
# Installs the /duckllo slash command so Claude agents know how to use
# the Duckllo kanban. The agent handles registration, project creation,
# and API key generation automatically on first use.
#
# Usage:
#   ./install.sh                  # Interactive
#   ./install.sh --global         # Install to ~/.claude (all projects)
#   ./install.sh --project        # Install to ./.claude (current project)
#   ./install.sh --project /path  # Install to a specific project
# ─────────────────────────────────────────────────────────────────────────

DUCKLLO_DIR="$(cd "$(dirname "$0")" && pwd)"
SKILL_FILE="$DUCKLLO_DIR/SKILL.md"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
RED='\033[0;31m'
NC='\033[0m'

info()  { echo -e "${CYAN}[info]${NC} $1"; }
ok()    { echo -e "${GREEN}[ok]${NC} $1"; }
warn()  { echo -e "${YELLOW}[warn]${NC} $1"; }
err()   { echo -e "${RED}[error]${NC} $1"; }

if [ ! -f "$SKILL_FILE" ]; then
  err "SKILL.md not found. Run this script from the duckllo directory."
  exit 1
fi

# ── Parse args ───────────────────────────────────────────────────────────

MODE=""
TARGET_DIR=""
DUCKLLO_URL="http://localhost:3000"
FORCE=false

while [[ $# -gt 0 ]]; do
  case $1 in
    --global)  MODE="global"; shift ;;
    --project)
      MODE="project"
      if [[ ${2:-} != "" && ${2:-} != --* ]]; then
        TARGET_DIR="$2"; shift
      fi
      shift ;;
    --url)     DUCKLLO_URL="$2"; shift 2 ;;
    --force)   FORCE=true; shift ;;
    -h|--help)
      echo "Usage: ./install.sh [--global | --project [path]] [--url http://host:port] [--force]"
      echo ""
      echo "  --global         Install to ~/.claude/commands (all projects)"
      echo "  --project [path] Install to .claude/commands in current or given dir"
      echo "  --url URL        Duckllo server URL (default: http://localhost:3000)"
      echo "  --force          Overwrite existing Duckllo config in CLAUDE.md"
      echo "  (no args)        Interactive mode"
      exit 0 ;;
    *) err "Unknown option: $1"; exit 1 ;;
  esac
done

# ── Interactive mode ─────────────────────────────────────────────────────

if [ -z "$MODE" ]; then
  echo ""
  echo -e "${CYAN}Duckllo Skill Installer${NC}"
  echo "━━━━━━━━━━━━━━━━━━━━━━━"
  echo ""
  echo "  1) Global   — All Claude agents on this machine (~/.claude/commands/)"
  echo "  2) Project  — Only agents in a specific project (.claude/commands/)"
  echo ""
  read -rp "Choose [1/2]: " choice
  case $choice in
    1) MODE="global" ;;
    2) MODE="project" ;;
    *) err "Invalid choice"; exit 1 ;;
  esac
fi

# ── Determine paths ─────────────────────────────────────────────────────

if [ "$MODE" = "global" ]; then
  COMMANDS_DIR="$HOME/.claude/commands"
  CLAUDE_MD_TARGET="$HOME/.claude/CLAUDE.md"
elif [ "$MODE" = "project" ]; then
  if [ -z "$TARGET_DIR" ]; then
    read -rp "Project path [$(pwd)]: " TARGET_DIR
    TARGET_DIR="${TARGET_DIR:-$(pwd)}"
  fi
  TARGET_DIR="$(cd "$TARGET_DIR" && pwd)"
  COMMANDS_DIR="$TARGET_DIR/.claude/commands"
  CLAUDE_MD_TARGET="$TARGET_DIR/CLAUDE.md"
fi

# ── Install slash command ────────────────────────────────────────────────

# ── Ask for developer account name ───────────────────────────────────────

echo ""
read -rp "Your Duckllo username (the developer/owner account): " DEV_USER
if [ -z "$DEV_USER" ]; then
  err "Developer username is required — the agent needs to add you to the project."
  exit 1
fi
ok "Developer account: $DEV_USER"

# ── Install slash command ────────────────────────────────────────────────

mkdir -p "$COMMANDS_DIR"
cp "$SKILL_FILE" "$COMMANDS_DIR/duckllo.md"
ok "Installed /duckllo slash command → $COMMANDS_DIR/duckllo.md"

# ── Write CLAUDE.md config ──────────────────────────────────────────────

CONFIG_BLOCK="
## Duckllo Kanban Integration

This project uses Duckllo for task tracking. Run \`/duckllo\` for the full API reference.

- **Server**: ${DUCKLLO_URL}
- **Owner**: ${DEV_USER}

### First-Time Setup (agent does this automatically)
On first use, the agent should:
1. Register an account on the Duckllo server (or login if one exists)
2. Create a project using the current folder/repo name: \`basename \$(git rev-parse --show-toplevel 2>/dev/null || pwd)\`
3. Generate an API key for the project
4. **Add the developer \`${DEV_USER}\` as a project member** (POST /api/projects/<pid>/members with {\"username\":\"${DEV_USER}\",\"role\":\"owner\"})
5. Store the Project ID and API key as a comment in this file

### Mandatory Workflow (do not skip)
1. **Before coding**: Create a card in Todo/In Progress with title, description, type, priority, and labels
2. **While coding**: Add comments to the card with approach and decisions
3. **After coding**: Update card with testing_status, testing_result, and demo GIF (if UI)
4. **Commit**: Add a comment with the git commit hash
5. **Move to Review/Done**: Server enforces quality gates — cards need test results or demo media

### Quality Gate Labels
Cards with these labels MUST have a demo GIF/media to move to Review/Done:
\`ui\`, \`ux\`, \`frontend\`, \`user-operation\`, \`user-facing\`, \`demo-required\`

All other cards need at least a test result.
"

if [ -f "$CLAUDE_MD_TARGET" ]; then
  if grep -q "Duckllo Kanban Integration" "$CLAUDE_MD_TARGET" 2>/dev/null; then
    if [ "$FORCE" = true ]; then
      # Strip old Duckllo block and rewrite
      python3 -c "
import re
with open('$CLAUDE_MD_TARGET') as f:
    content = f.read()
content = re.sub(r'\n## Duckllo Kanban Integration\n.*?(?=\n## |\Z)', '', content, flags=re.DOTALL)
with open('$CLAUDE_MD_TARGET', 'w') as f:
    f.write(content.rstrip())
" 2>/dev/null
      echo "$CONFIG_BLOCK" >> "$CLAUDE_MD_TARGET"
      ok "Overwrote Duckllo config in $CLAUDE_MD_TARGET"
    else
      warn "Duckllo config already exists in $CLAUDE_MD_TARGET — skipping."
      warn "Use --force to overwrite."
    fi
  else
    echo "$CONFIG_BLOCK" >> "$CLAUDE_MD_TARGET"
    ok "Appended Duckllo config to $CLAUDE_MD_TARGET"
  fi
else
  echo "$CONFIG_BLOCK" > "$CLAUDE_MD_TARGET"
  ok "Created $CLAUDE_MD_TARGET with Duckllo config"
fi

# ── Check server ────────────────────────────────────────────────────────

echo ""
if curl -sf "$DUCKLLO_URL" >/dev/null 2>&1; then
  ok "Server is reachable at $DUCKLLO_URL"
else
  warn "Server not reachable at $DUCKLLO_URL"
  warn "Start it with:  cd $DUCKLLO_DIR && docker compose up -d"
fi

# ── Done ─────────────────────────────────────────────────────────────────

echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  Duckllo skill installed!${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "  Slash command:  /duckllo"
echo "  Skill file:     $COMMANDS_DIR/duckllo.md"
echo "  Config:         $CLAUDE_MD_TARGET"
echo ""
echo "The agent will auto-register and set up the project on first use."
echo "Just start Claude Code and begin working — it reads /duckllo for instructions."
echo ""
