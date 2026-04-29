#!/usr/bin/env bash
# scripts/dogfood-smoke.sh — one-shot end-to-end smoke that verifies
# duckllo can drive a real spec through the harness against its own
# source tree using the claude-code provider.
#
# Spins up an ephemeral Postgres + duckllo server, runs `selfhost` to
# bootstrap the project, clones duckllo into a scratch workspace,
# creates a tiny spec ("append a one-line marker"), runs the runner
# with --exit-when-idle, asserts the resulting file appears with the
# expected content. Tears everything down regardless of pass/fail.
#
# Requires: docker, go, the `claude` CLI on PATH (logged in).
# Exits non-zero on any failure.

set -uo pipefail

REPO=$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)
cd "$REPO"

PORT=${DUCKLLO_SMOKE_PORT:-3402}
DB_PORT=${DUCKLLO_SMOKE_DB_PORT:-55433}
WORKSPACE=${DUCKLLO_SMOKE_WORKSPACE:-/tmp/duckllo-smoke-workspace}
SERVER_LOG=/tmp/duckllo-smoke-server.log
RUNNER_LOG=/tmp/duckllo-smoke-runner.log
DB_NAME=duckllo-smoke-db

cleanup() {
  set +e
  pkill -f "bin/duckllo serve" 2>/dev/null
  pkill -f 'bin/runner ' 2>/dev/null
  docker stop "$DB_NAME" 2>/dev/null
  rm -rf "$WORKSPACE" "$REPO/.duckllo.env.smoke" 2>/dev/null
  set -e
}
trap cleanup EXIT

step() { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; exit 1; }

command -v docker >/dev/null || fail "docker not on PATH"
command -v go >/dev/null     || fail "go not on PATH"
command -v claude >/dev/null || fail "claude CLI not on PATH (login first via 'claude login')"

step "Building binaries"
mkdir -p bin
go build -o bin/duckllo ./cmd/duckllo
go build -o bin/runner  ./cmd/runner

step "Starting Postgres on $DB_PORT"
docker rm -f "$DB_NAME" 2>/dev/null
docker run -d --rm --name "$DB_NAME" \
  -e POSTGRES_USER=duckllo -e POSTGRES_PASSWORD=duckllo -e POSTGRES_DB=duckllo \
  -p 127.0.0.1:"$DB_PORT":5432 postgres:16-alpine >/dev/null
for i in $(seq 1 30); do
  docker exec "$DB_NAME" pg_isready -U duckllo >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$DB_NAME" pg_isready -U duckllo >/dev/null 2>&1 || fail "Postgres never came up"

DSN="postgres://duckllo:duckllo@localhost:$DB_PORT/duckllo?sslmode=disable"

step "Starting server on :$PORT"
DATABASE_URL="$DSN" \
  DUCKLLO_ADDR=":$PORT" \
  DUCKLLO_UPLOADS=/tmp/duckllo-smoke-uploads \
  DUCKLLO_GIN_PASSWORD=changeme \
  ./bin/duckllo serve > "$SERVER_LOG" 2>&1 &
sleep 3
curl -sf "http://localhost:$PORT/api/status" >/dev/null || {
  cat "$SERVER_LOG"
  fail "server not reachable"
}

step "Bootstrapping selfhost"
SCRATCH=$(mktemp -d)
DATABASE_URL="$DSN" DUCKLLO_GIN_PASSWORD=changeme DUCKLLO_URL="http://localhost:$PORT" \
  bash -c "cd $SCRATCH && $REPO/bin/duckllo selfhost"
KEY=$(grep DUCKLLO_KEY "$SCRATCH/.duckllo.env" | cut -d= -f2)
PID=$(grep DUCKLLO_PROJECT "$SCRATCH/.duckllo.env" | cut -d= -f2)
[ -n "$KEY" ] && [ -n "$PID" ] || fail "selfhost didn't write env"

step "Cloning duckllo into $WORKSPACE"
rm -rf "$WORKSPACE"
git clone "$REPO" "$WORKSPACE" 2>&1 | tail -1
cat > "$WORKSPACE/.duckllo.env" <<EOF
DUCKLLO_URL=http://localhost:$PORT
DUCKLLO_PROJECT=$PID
DUCKLLO_KEY=$KEY
DUCKLLO_PROVIDER=claude-code
DUCKLLO_WORKSPACE=$WORKSPACE
DUCKLLO_CLAUDE_CWD=$WORKSPACE
EOF

step "Creating smoke spec via API"
TOKEN=$(curl -sS -X POST "http://localhost:$PORT/api/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"gin","password":"changeme"}' \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")

# Plain ASCII marker — no markdown-special characters that an agent
# might "fix up" via implicit formatting. Includes a hex tag so two
# concurrent smoke runs against the same workspace are distinguishable.
EXPECTED="DOGFOOD-SMOKE-$(date +%s)"
SPEC=$(curl -sS -X POST "http://localhost:$PORT/api/projects/$PID/specs" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{
    \"title\":\"Append marker for smoke test\",
    \"intent\":\"Append exactly this single line to the end of $WORKSPACE/README.md (no extra blank lines):\\n\\n$EXPECTED\\n\\nUse your file-editing tools to actually append the line.\"
  }" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
curl -sS -X POST "http://localhost:$PORT/api/projects/$PID/specs/$SPEC/criteria" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"text":"README.md ends with the smoke marker line","sensor_kind":"judge"}' >/dev/null
curl -sS -X POST "http://localhost:$PORT/api/projects/$PID/specs/$SPEC/approve" \
  -H "Authorization: Bearer $TOKEN" >/dev/null
RUN=$(curl -sS -X POST "http://localhost:$PORT/api/projects/$PID/specs/$SPEC/runs" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
echo "spec=$SPEC run=$RUN"

step "Driving runner --exit-when-idle"
cd "$WORKSPACE"
"$REPO/bin/runner" --exit-when-idle --poll-interval 2 > "$RUNNER_LOG" 2>&1 &
RUNNER_PID=$!
cd "$REPO"

# Wait for the run to reach a terminal status, with a generous cap.
for i in $(seq 1 60); do
  STATUS=$(curl -sf -H "Authorization: Bearer $KEY" \
    "http://localhost:$PORT/api/projects/$PID/runs/$RUN" 2>/dev/null \
    | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['run']['status'])" 2>/dev/null)
  case "$STATUS" in
    done|failed|aborted) break ;;
  esac
  sleep 5
done

# Wait for the runner to actually exit (drain).
wait "$RUNNER_PID" 2>/dev/null

step "Verifying side effect"
[ "$STATUS" = "done" ] || {
  echo "--- runner log ---"
  cat "$RUNNER_LOG"
  fail "run did not reach 'done'; final status=$STATUS"
}
[ -f "$WORKSPACE/README.md" ] || fail "README.md missing in workspace"
# 'contains' rather than 'last line is' — agents sometimes add a
# trailing newline or apply implicit markdown fixups. The point of the
# smoke is "the agent applied a recognisable change", not byte-for-byte
# fidelity of the marker line.
grep -q -F "$EXPECTED" "$WORKSPACE/README.md" || {
  echo "--- last 5 lines of README.md ---"
  tail -5 "$WORKSPACE/README.md"
  fail "marker '$EXPECTED' not found in README.md"
}

step "Verifying server-side state"
VERIFS=$(curl -sf -H "Authorization: Bearer $KEY" \
  "http://localhost:$PORT/api/projects/$PID/runs/$RUN/verifications")
HAS_WC=$(echo "$VERIFS" | python3 -c "import sys,json;print(any(v['kind']=='workspace_changes' for v in json.load(sys.stdin)))")
HAS_JUDGE=$(echo "$VERIFS" | python3 -c "import sys,json;print(any(v['kind']=='judge' and v['status']=='pass' for v in json.load(sys.stdin)))")
[ "$HAS_WC" = "True" ]    || fail "workspace_changes verification not posted"
[ "$HAS_JUDGE" = "True" ] || fail "judge verification not pass"

printf '\n\033[1;32mPASS\033[0m  spec=%s run=%s\n' "$SPEC" "$RUN"
