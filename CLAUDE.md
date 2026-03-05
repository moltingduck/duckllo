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

1. **Before coding**: Create a card in `Todo` with clear title, description, type, and priority.
2. **Start work**: Move card to `In Progress`.
3. **While coding**: Add comments to the card describing approach, decisions, and blockers.
4. **After coding**: Update the card with:
   - `testing_status`: `passing`, `failing`, or `partial`
   - `testing_result`: Paste actual test output (monospace-friendly)
   - `demo_gif_url`: Upload a GIF or screenshot showing the feature works
   - Related git commit hash in a comment
5. **Submit for review**: Move card to `Review`.
6. **Done**: Only move to `Done` after tests pass and demo media is attached.

Never skip steps. A card in `Done` without test results and a demo GIF is incomplete.

## Development Rules

### Git
- Commit messages: short summary line, blank line, details if needed.
- End every commit with `Co-Authored-By: <agent name> <noreply@anthropic.com>`.
- One logical change per commit. Don't bundle unrelated changes.
- Never force-push. Never amend published commits.
- Never commit secrets, `.env` files, or database files.

### Code Style
- Backend: Node.js + Express + better-sqlite3. No ORMs.
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
server.js          # Backend API (Express + SQLite)
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

## Lessons Learned

These are patterns discovered during development. Update this section as new lessons emerge.

- **Puppeteer + modals**: `waitForSelector` with `{ visible: true }` doesn't work reliably for elements toggled via `style.display`. Use `waitForFunction` to check computed display instead.
- **Puppeteer + form submission**: `page.type()` can race with modal transitions. Prefer `page.evaluate()` to set values directly for reliability. Use `page.type()` only when you need visible typing for demos.
- **SQLite WAL mode**: Enable `PRAGMA journal_mode = WAL` for concurrent reads. The DB file must be on a local filesystem (not NFS).
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
