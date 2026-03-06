#!/usr/bin/env bash
set -euo pipefail

# ── Duckllo Skill Installer for Claude Code ──────────────────────────────
# Installs the Duckllo kanban skill so Claude agents can track work.
#
# Usage:
#   ./install.sh                  # Interactive — prompts for everything
#   ./install.sh --global         # Install to ~/.claude (all projects)
#   ./install.sh --project        # Install to ./.claude (current project)
#   ./install.sh --project /path  # Install to a specific project
# ─────────────────────────────────────────────────────────────────────────

DUCKLLO_DIR="$(cd "$(dirname "$0")" && pwd)"
SKILL_FILE="$DUCKLLO_DIR/SKILL.md"
CLAUDE_MD_FILE="$DUCKLLO_DIR/CLAUDE.md"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${CYAN}[info]${NC} $1"; }
ok()    { echo -e "${GREEN}[ok]${NC} $1"; }
warn()  { echo -e "${YELLOW}[warn]${NC} $1"; }
err()   { echo -e "${RED}[error]${NC} $1"; }

# ── Check prerequisites ─────────────────────────────────────────────────

if [ ! -f "$SKILL_FILE" ]; then
  err "SKILL.md not found at $SKILL_FILE"
  err "Run this script from the duckllo directory."
  exit 1
fi

if ! command -v claude &>/dev/null; then
  warn "Claude Code CLI not found in PATH. The skill files will still be installed,"
  warn "but you need Claude Code to use them."
fi

# ── Parse args ───────────────────────────────────────────────────────────

MODE=""
TARGET_DIR=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --global)  MODE="global"; shift ;;
    --project)
      MODE="project"
      if [[ ${2:-} != "" && ${2:-} != --* ]]; then
        TARGET_DIR="$2"; shift
      fi
      shift ;;
    -h|--help)
      echo "Usage: ./install.sh [--global | --project [path]]"
      echo ""
      echo "  --global         Install to ~/.claude/commands (all projects)"
      echo "  --project [path] Install to .claude/commands in current or given dir"
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
  echo "Where should the Duckllo skill be installed?"
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

# ── Determine install path ──────────────────────────────────────────────

if [ "$MODE" = "global" ]; then
  COMMANDS_DIR="$HOME/.claude/commands"
  CLAUDE_MD_TARGET="$HOME/.claude/CLAUDE.md"
  info "Installing globally to $COMMANDS_DIR"
elif [ "$MODE" = "project" ]; then
  if [ -z "$TARGET_DIR" ]; then
    read -rp "Project path [$(pwd)]: " TARGET_DIR
    TARGET_DIR="${TARGET_DIR:-$(pwd)}"
  fi
  TARGET_DIR="$(cd "$TARGET_DIR" && pwd)"
  COMMANDS_DIR="$TARGET_DIR/.claude/commands"
  CLAUDE_MD_TARGET="$TARGET_DIR/CLAUDE.md"
  info "Installing to project at $TARGET_DIR"
fi

# ── Install slash command ────────────────────────────────────────────────

mkdir -p "$COMMANDS_DIR"
cp "$SKILL_FILE" "$COMMANDS_DIR/duckllo.md"
ok "Installed /duckllo slash command → $COMMANDS_DIR/duckllo.md"

# ── Configure server connection ──────────────────────────────────────────

echo ""
info "Configure Duckllo server connection (press Enter to skip any)."
echo ""

read -rp "Duckllo server URL [http://localhost:3000]: " DUCKLLO_URL
DUCKLLO_URL="${DUCKLLO_URL:-http://localhost:3000}"

read -rp "Project ID (from /api/projects): " DUCKLLO_PROJECT
read -rp "API key (duckllo_...): " DUCKLLO_KEY

# ── Write CLAUDE.md config block ─────────────────────────────────────────

CONFIG_BLOCK="
## Duckllo Kanban Integration

This project uses [Duckllo](${DUCKLLO_URL}) for task tracking.
Run \`/duckllo\` for the full API reference.

- **Server**: ${DUCKLLO_URL}
- **Project ID**: ${DUCKLLO_PROJECT:-<ask admin for project ID>}
- **API Key**: ${DUCKLLO_KEY:-<ask admin for API key>}

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
    warn "Duckllo config already exists in $CLAUDE_MD_TARGET — skipping."
  else
    echo "$CONFIG_BLOCK" >> "$CLAUDE_MD_TARGET"
    ok "Appended Duckllo config to $CLAUDE_MD_TARGET"
  fi
else
  echo "$CONFIG_BLOCK" > "$CLAUDE_MD_TARGET"
  ok "Created $CLAUDE_MD_TARGET with Duckllo config"
fi

# ── Verify connection ───────────────────────────────────────────────────

echo ""
if [ -n "$DUCKLLO_URL" ]; then
  info "Verifying server connection..."
  if curl -sf "$DUCKLLO_URL" >/dev/null 2>&1; then
    ok "Server is reachable at $DUCKLLO_URL"
  else
    warn "Cannot reach $DUCKLLO_URL — make sure the server is running:"
    warn "  cd $DUCKLLO_DIR && docker compose up -d"
  fi
fi

if [ -n "$DUCKLLO_KEY" ] && [ -n "$DUCKLLO_PROJECT" ]; then
  info "Verifying API key..."
  RESP=$(curl -sf "$DUCKLLO_URL/api/projects/$DUCKLLO_PROJECT/cards" \
    -H "Authorization: Bearer $DUCKLLO_KEY" 2>/dev/null) && \
    ok "API key is valid — $(echo "$RESP" | python3 -c 'import sys,json; print(len(json.load(sys.stdin)),"cards found")' 2>/dev/null || echo 'connected')" || \
    warn "API key verification failed — check the key and project ID"
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
echo "Start a new Claude Code session and try:"
echo "  /duckllo              — Show API reference"
echo "  \"list kanban cards\"   — Agent reads the board"
echo ""
