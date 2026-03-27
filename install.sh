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
      echo "  --force          Overwrite existing skill file"
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
elif [ "$MODE" = "project" ]; then
  if [ -z "$TARGET_DIR" ]; then
    read -rp "Project path [$(pwd)]: " TARGET_DIR
    TARGET_DIR="${TARGET_DIR:-$(pwd)}"
  fi
  TARGET_DIR="$(cd "$TARGET_DIR" && pwd)"
  COMMANDS_DIR="$TARGET_DIR/.claude/commands"
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

# ── Install slash command with injected config ─────────────────────────

mkdir -p "$COMMANDS_DIR"
DEST_FILE="$COMMANDS_DIR/duckllo.md"

if [ -f "$DEST_FILE" ] && [ "$FORCE" != true ]; then
  if grep -q "Project Owner" "$DEST_FILE" 2>/dev/null; then
    warn "Skill file already exists at $DEST_FILE — skipping."
    warn "Use --force to overwrite."
  else
    cp "$SKILL_FILE" "$DEST_FILE"
  fi
else
  cp "$SKILL_FILE" "$DEST_FILE"
fi

# Inject config block at the top of the skill file (after the title line)
INJECT_BLOCK="
## Project Configuration

- **Server**: ${DUCKLLO_URL}
- **Project Owner**: \`${DEV_USER}\` — always add this user as an \`owner\` member when creating a project
- **Approval Flow**: Agent cards go to \`Proposed\` column. The owner \`${DEV_USER}\` approves them, which auto-moves them to \`Todo\`.

### First-Time Setup (agent does this automatically)
On first use, the agent should:
1. Register an account on the Duckllo server (or login if one exists)
2. Create a project using the current folder/repo name: \\\`basename \\\$(git rev-parse --show-toplevel 2>/dev/null || pwd)\\\`
3. Generate an API key for the project
4. **Add the developer \\\`${DEV_USER}\\\` as a project member** (POST /api/projects/<pid>/members with {\"username\":\"${DEV_USER}\",\"role\":\"owner\"})
5. Store the Project ID and API key as a comment in this file or in CLAUDE.md
"

# Insert config block before "## Quick Start"
if grep -q "^## Quick Start" "$DEST_FILE" 2>/dev/null; then
  python3 - "$DEST_FILE" "$INJECT_BLOCK" <<'PYEOF'
import sys
dest, block = sys.argv[1], sys.argv[2]
with open(dest) as f:
    content = f.read()
marker = '## Quick Start'
idx = content.find(marker)
if idx >= 0:
    content = content[:idx] + block + '\n' + content[idx:]
with open(dest, 'w') as f:
    f.write(content)
PYEOF
fi

ok "Installed /duckllo slash command → $DEST_FILE (with config for ${DEV_USER})"

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
echo "  Owner account:  $DEV_USER"
echo ""
echo "The agent will auto-register and set up the project on first use."
echo "Just start Claude Code and begin working — it reads /duckllo for instructions."
echo ""
