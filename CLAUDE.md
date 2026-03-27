# Duckllo Development Rules

This file is the single source of truth for how any agent (Claude or otherwise) must work on this project. Every agent must read this before making changes. Violations are not acceptable.

## Owner Requirements (non-negotiable)

- The owner's kanban account is `gin`. Never remove or demote this account.
- All features and bugs must be tracked on the Duckllo kanban board before, during, and after development.
- Every card must record testing results and a demo GIF that a human can read and verify.
- The system must have an easy account system with project-based permission and API keys for agents.
- Never break existing features. Run tests before committing.

## Kanban Workflow

Every piece of work follows this flow:

1. **Before coding**: Create a card via the API. Agent cards always go to `Proposed` (pending approval). Once the product owner approves, the card auto-moves to `Todo`. If the owner requests revision (`revision_requested`), update the card based on feedback comments and call `/repropose` to re-submit. If the card is `rejected`, do NOT re-propose — the feature is not needed.
2. **Start work**: Use the pickup endpoint (`POST /cards/:cid/pickup`) to move the card from `Todo` to `In Progress` and assign yourself atomically. This ensures others can see who is working on what. Never leave cards in In Progress without an assignee.
3. **While coding**: Add comments to the card describing approach, decisions, and blockers.
4. **After coding**: Update the card with:
   - `testing_status`: `passing`, `failing`, or `partial`
   - `testing_result`: Paste actual test output (monospace-friendly)
   - `demo_gif_url`: Upload a GIF or screenshot showing the feature works
   - Related git commit hash in a comment
5. **Submit for review**: Move card to `Review`.
6. **Done**: Only move to `Done` after tests pass and demo media is attached.

Never skip steps. A card in `Done` without test results and a demo GIF is incomplete.

## Quality Gate Rules (server-enforced)

The server **rejects** moves to Review/Done (HTTP 422) if requirements are not met:

### All cards need at least ONE of:
- A **test result** (`testing_result` with actual output)
- A **demo GIF/media** (uploaded via the card)

### UI/UX cards MUST have demo media:
Cards with any of these labels **must** have a demo GIF/media — test results alone are not enough:
`ui`, `ux`, `ui/ux`, `frontend`, `user-operation`, `user-facing`, `demo-required`

### Bug fix / performance cards:
Only need a **test result** proving the fix works.

### Label your cards correctly:
- UI features, new pages, layout changes → add `ui` or `frontend` label
- User-facing workflows → add `user-operation` label
- Backend/API/performance/infrastructure → no special label needed (test result suffices)

Every new project auto-creates a pinned "Quality Gate Rules" card in Backlog for reference.

## Development Rules

### Git
- Commit messages: short summary line, blank line, details if needed.
- End every commit with `Co-Authored-By: <agent name> <noreply@anthropic.com>`.
- One logical change per commit. Don't bundle unrelated changes.
- Never force-push. Never amend published commits.
- Never commit secrets, `.env` files, or database files.

### Code Style
- Backend: Node.js + Express + PostgreSQL (pg). No ORMs.
- Frontend: Vanilla HTML/CSS/JS in `public/`. No frameworks unless owner approves.
- Keep it simple. No over-engineering. No premature abstraction.
- SQL: Use parameterized queries. Never interpolate user input into SQL.
- Auth: Bcrypt for passwords. UUID for tokens and IDs. API keys prefixed with `duckllo_`.

### Testing
- Run `node test/e2e.test.js` before committing any feature change.
- For new features: add test coverage in the E2E suite or verify manually and document.
- Test results must be human-readable. Format like:
  ```
  Test Suite: FeatureName
    [PASS] test description
    [FAIL] test description
  X/Y passed
  ```

### File Structure
```
server.js          # Backend API (Express + PostgreSQL)
public/            # Frontend (HTML/CSS/JS)
  index.html
  style.css
  app.js
test/              # Test suites
  e2e.test.js      # Puppeteer E2E tests
  demo-cdp.js      # CDP demo script
docs/              # Documentation and media
  FEATURES.md      # Feature documentation
  gifs/            # E2E test GIFs
  demo/            # CDP demo GIFs
uploads/           # User-uploaded media (gitignored)
Dockerfile         # App container (node:20-slim)
docker-compose.yml # App + PostgreSQL stack
.dockerignore      # Docker build exclusions
SKILL.md           # API reference for agents
CLAUDE.md          # This file - development rules
```

### Security
- Never disable auth middleware for convenience.
- API keys are project-scoped. Never allow cross-project access.
- Validate all user input at API boundaries.
- File uploads: whitelist extensions, enforce size limits.

## How to Use the Kanban API

See `SKILL.md` for the full API reference. Quick version:

```bash
# Your API key (get from project Settings)
KEY="duckllo_<your-key>"
PID="<project-id>"

# Create card before starting work
CID=$(curl -s -X POST "http://localhost:3000/api/projects/$PID/cards" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"title":"...","card_type":"feature","column_name":"Todo"}' | jq -r '.id')

# Move to In Progress
curl -s -X POST "http://localhost:3000/api/projects/$PID/cards/$CID/move" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"column_name":"In Progress","position":0}'

# After implementing: update test results + upload demo + add commit ref
curl -s -X PATCH "http://localhost:3000/api/projects/$PID/cards/$CID" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"testing_status":"passing","testing_result":"..."}'

curl -s -X POST "http://localhost:3000/api/projects/$PID/cards/$CID/upload" \
  -H "Authorization: Bearer $KEY" -F "file=@demo.gif"

curl -s -X POST "http://localhost:3000/api/projects/$PID/cards/$CID/comments" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"content":"Git commit: abc1234 ...","comment_type":"agent_update"}'

# Move to Review
curl -s -X POST "http://localhost:3000/api/projects/$PID/cards/$CID/move" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"column_name":"Review","position":0}'
```

## Watching the Kanban

Agents should watch the kanban for new tasks assigned to them. Use `watch.js`:

```bash
# Continuous watch (prints new events every 15s)
node watch.js --key $KEY --project $PID

# One-shot check
node watch.js --key $KEY --project $PID --once

# JSON output for programmatic use
node watch.js --key $KEY --project $PID --json --once
```

Or poll the activity API directly:
```bash
curl "http://localhost:3000/api/projects/$PID/activity?since=<last-check-timestamp>" \
  -H "Authorization: Bearer $KEY"
```

See `SKILL.md` for detailed watcher documentation and integration patterns.

## Lessons Learned

These are patterns discovered during development. Update this section as new lessons emerge.

- **Puppeteer + modals**: `waitForSelector` with `{ visible: true }` doesn't work reliably for elements toggled via `style.display`. Use `waitForFunction` to check computed display instead.
- **Puppeteer + form submission**: `page.type()` can race with modal transitions. Prefer `page.evaluate()` to set values directly for reliability. Use `page.type()` only when you need visible typing for demos.
- **PostgreSQL JSONB**: `columns_config`, `labels`, and `permissions` are JSONB columns — the `pg` driver returns native JS objects/arrays, no `JSON.parse()` needed on reads. Always `JSON.stringify()` when inserting.
- **PostgreSQL transactions**: Use `const client = await pool.connect()` then `client.query('BEGIN')` / `COMMIT` / `ROLLBACK` for multi-step operations like card moves.
- **API key auth**: Bcrypt comparison for every request is slow with many keys. If this becomes a bottleneck, add a key prefix index to narrow the search.
- **GIF recording**: Use `gif-encoder-2` with `neuquant` algorithm and quality 10 for good size/quality balance. Capture PNGs, decode with `pngjs`, then encode.
- **CDP testing**: Always use a unique `--user-data-dir` per test run to avoid conflicts with other Chrome sessions. Connect via `puppeteer.connect({ browserURL })` to reuse an existing Chromium instance.
- **Background server**: Use `nohup node server.js > /tmp/duckllo.log 2>&1 &` to keep it alive. The `run_in_background` tool parameter causes the process to exit when output stream closes.

## Agent Checklist

Before submitting any work, verify:

- [ ] Card exists on kanban for this work
- [ ] Card has testing_status set (passing/failing/partial)
- [ ] Card has testing_result with actual test output
- [ ] Card has a demo GIF uploaded
- [ ] Card has a comment with the git commit hash
- [ ] Card is in the correct column (Review or Done)
- [ ] All existing E2E tests still pass
- [ ] Code committed with descriptive message and co-author line
- [ ] No secrets or database files in the commit
