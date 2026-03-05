const puppeteer = require('puppeteer');

const BASE = 'http://localhost:3000';
const results = [];
function pass(t) { results.push(`  [PASS] ${t}`); }
function fail(t, e) { results.push(`  [FAIL] ${t}: ${e}`); }

async function api(path, opts = {}) {
  const headers = { ...opts.headers, 'Content-Type': 'application/json' };
  if (opts.token) headers['Authorization'] = `Bearer ${opts.token}`;
  const res = await fetch(`${BASE}/api${path}`, {
    method: opts.method || 'GET',
    headers,
    body: opts.body ? JSON.stringify(opts.body) : undefined
  });
  return { status: res.status, data: await res.json() };
}

(async () => {
  // Setup: register user, create project
  const reg = await api('/auth/register', { method: 'POST', body: { username: 'qgtest_' + Date.now(), password: 'test123456' } });
  const token = reg.data.token;

  const proj = await api('/projects', { method: 'POST', token, body: { name: 'QG Test ' + Date.now() } });
  const pid = proj.data.id;

  // ── Test 1: Pinned rules card auto-created ──
  const cards = await api(`/projects/${pid}/cards`, { token });
  const rulesCard = cards.data.find(c => c.title.includes('Quality Gate'));
  if (rulesCard && rulesCard.labels.includes('pinned') && rulesCard.column_name === 'Backlog') {
    pass('Pinned rules card auto-created in Backlog with pinned label');
  } else {
    fail('Pinned rules card', JSON.stringify(rulesCard?.title || 'not found'));
  }

  // ── Test 2: Bare card → Review blocked ──
  const bare = await api(`/projects/${pid}/cards`, { method: 'POST', token, body: { title: 'Bare card', column_name: 'In Progress' } });
  const moveReview = await api(`/projects/${pid}/cards/${bare.data.id}/move`, { method: 'POST', token, body: { column_name: 'Review', position: 0 } });
  if (moveReview.status === 422 && moveReview.data.error.includes('test result or demo media')) {
    pass('Bare card blocked from Review (422)');
  } else {
    fail('Bare card blocked from Review', `${moveReview.status}: ${moveReview.data.error}`);
  }

  // ── Test 3: Bare card → Done blocked ──
  const moveDone = await api(`/projects/${pid}/cards/${bare.data.id}/move`, { method: 'POST', token, body: { column_name: 'Done', position: 0 } });
  if (moveDone.status === 422) {
    pass('Bare card blocked from Done (422)');
  } else {
    fail('Bare card blocked from Done', `${moveDone.status}`);
  }

  // ── Test 4: Card with test result → Review OK ──
  await api(`/projects/${pid}/cards/${bare.data.id}`, { method: 'PATCH', token, body: { testing_result: 'All 5 tests pass' } });
  const moveOk = await api(`/projects/${pid}/cards/${bare.data.id}/move`, { method: 'POST', token, body: { column_name: 'Review', position: 0 } });
  if (moveOk.status === 200 && moveOk.data.column_name === 'Review') {
    pass('Card with test result moves to Review');
  } else {
    fail('Card with test result → Review', `${moveOk.status}`);
  }

  // ── Test 5: Card with test result → Done OK ──
  const moveDone2 = await api(`/projects/${pid}/cards/${bare.data.id}/move`, { method: 'POST', token, body: { column_name: 'Done', position: 0 } });
  if (moveDone2.status === 200) {
    pass('Card with test result moves to Done');
  } else {
    fail('Card with test result → Done', `${moveDone2.status}`);
  }

  // ── Test 6: Card with only demo → Review OK ──
  const demoOnly = await api(`/projects/${pid}/cards`, { method: 'POST', token, body: { title: 'Demo only card', column_name: 'In Progress' } });
  await api(`/projects/${pid}/cards/${demoOnly.data.id}`, { method: 'PATCH', token, body: { demo_gif_url: '/uploads/test.gif' } });
  const moveDemoReview = await api(`/projects/${pid}/cards/${demoOnly.data.id}/move`, { method: 'POST', token, body: { column_name: 'Review', position: 0 } });
  if (moveDemoReview.status === 200) {
    pass('Card with only demo media moves to Review');
  } else {
    fail('Card with only demo → Review', `${moveDemoReview.status}: ${moveDemoReview.data.error}`);
  }

  // ── Test 7: UI-tagged card with test only → Review blocked ──
  const uiCard = await api(`/projects/${pid}/cards`, { method: 'POST', token, body: { title: 'New login page', column_name: 'In Progress', labels: ['ui', 'frontend'] } });
  await api(`/projects/${pid}/cards/${uiCard.data.id}`, { method: 'PATCH', token, body: { testing_result: 'Tests pass' } });
  const uiMoveBlocked = await api(`/projects/${pid}/cards/${uiCard.data.id}/move`, { method: 'POST', token, body: { column_name: 'Review', position: 0 } });
  if (uiMoveBlocked.status === 422 && uiMoveBlocked.data.error.includes('demo GIF/media')) {
    pass('UI-tagged card without demo blocked from Review (422)');
  } else {
    fail('UI-tagged card blocked', `${uiMoveBlocked.status}: ${uiMoveBlocked.data.error}`);
  }

  // ── Test 8: UI-tagged card with demo → Review OK ──
  await api(`/projects/${pid}/cards/${uiCard.data.id}`, { method: 'PATCH', token, body: { demo_gif_url: '/uploads/login-demo.gif' } });
  const uiMoveOk = await api(`/projects/${pid}/cards/${uiCard.data.id}/move`, { method: 'POST', token, body: { column_name: 'Review', position: 0 } });
  if (uiMoveOk.status === 200) {
    pass('UI-tagged card with demo moves to Review');
  } else {
    fail('UI-tagged card with demo → Review', `${uiMoveOk.status}`);
  }

  // ── Test 9: Each demo-required tag works ──
  const tags = ['ux', 'ui/ux', 'user-operation', 'user-facing', 'demo-required'];
  let allTagsWork = true;
  for (const tag of tags) {
    const c = await api(`/projects/${pid}/cards`, { method: 'POST', token, body: { title: `Tag test: ${tag}`, column_name: 'In Progress', labels: [tag] } });
    await api(`/projects/${pid}/cards/${c.data.id}`, { method: 'PATCH', token, body: { testing_result: 'Tests pass' } });
    const m = await api(`/projects/${pid}/cards/${c.data.id}/move`, { method: 'POST', token, body: { column_name: 'Review', position: 0 } });
    if (m.status !== 422) { allTagsWork = false; fail(`Tag "${tag}" requires demo`, `got ${m.status} instead of 422`); }
  }
  if (allTagsWork) pass('All demo-required tags enforced: ux, ui/ux, user-operation, user-facing, demo-required');

  // ── Test 10: PATCH column_name to Review enforces gates ──
  const patchCard = await api(`/projects/${pid}/cards`, { method: 'POST', token, body: { title: 'Patch test', column_name: 'In Progress' } });
  const patchBlocked = await api(`/projects/${pid}/cards/${patchCard.data.id}`, { method: 'PATCH', token, body: { column_name: 'Review' } });
  if (patchBlocked.status === 422) {
    pass('PATCH column_name to Review enforces quality gate');
  } else {
    fail('PATCH column to Review', `${patchBlocked.status}`);
  }

  // ── Test 11: PATCH column_name to Done enforces gates ──
  const patchDone = await api(`/projects/${pid}/cards/${patchCard.data.id}`, { method: 'PATCH', token, body: { column_name: 'Done' } });
  if (patchDone.status === 422) {
    pass('PATCH column_name to Done enforces quality gate');
  } else {
    fail('PATCH column to Done', `${patchDone.status}`);
  }

  // ── Test 12: PATCH with test result + column_name together passes ──
  const patchBoth = await api(`/projects/${pid}/cards/${patchCard.data.id}`, { method: 'PATCH', token, body: { column_name: 'Review', testing_result: 'Fixed and verified' } });
  if (patchBoth.status === 200 && patchBoth.data.column_name === 'Review') {
    pass('PATCH with test result + column_name together passes gate');
  } else {
    fail('PATCH both together', `${patchBoth.status}: ${patchBoth.data.error}`);
  }

  // ── Test 13: Non-gated columns always allowed ──
  const freeCard = await api(`/projects/${pid}/cards`, { method: 'POST', token, body: { title: 'Free card', column_name: 'Backlog' } });
  const cols = ['Todo', 'In Progress', 'Backlog'];
  let freeOk = true;
  for (const col of cols) {
    const m = await api(`/projects/${pid}/cards/${freeCard.data.id}/move`, { method: 'POST', token, body: { column_name: col, position: 0 } });
    if (m.status !== 200) { freeOk = false; fail(`Move to ${col}`, `${m.status}`); }
  }
  if (freeOk) pass('Non-gated columns (Backlog, Todo, In Progress) always allowed');

  // ── Test 14: Bug card with test result only → Review OK ──
  const bugCard = await api(`/projects/${pid}/cards`, { method: 'POST', token, body: { title: 'Fix crash on login', card_type: 'bug', column_name: 'In Progress', labels: ['backend', 'bugfix'] } });
  await api(`/projects/${pid}/cards/${bugCard.data.id}`, { method: 'PATCH', token, body: { testing_result: 'Crash no longer reproduces. Regression test added.' } });
  const bugMove = await api(`/projects/${pid}/cards/${bugCard.data.id}/move`, { method: 'POST', token, body: { column_name: 'Review', position: 0 } });
  if (bugMove.status === 200) {
    pass('Bug card with test result (no demo) moves to Review');
  } else {
    fail('Bug card with test → Review', `${bugMove.status}: ${bugMove.data.error}`);
  }

  // ── Test 15: UI drag-and-drop shows toast on block (Puppeteer) ──
  let browser;
  try {
    browser = await puppeteer.launch({ headless: true, args: ['--no-sandbox'] });
    const page = await browser.newPage();
    await page.setViewport({ width: 1280, height: 800 });

    // Login
    await page.goto(BASE, { waitUntil: 'networkidle0' });
    const username = 'uitoast_' + Date.now();
    // Register via API
    const r = await api('/auth/register', { method: 'POST', body: { username, password: 'test123456' } });
    // Create project with a bare card in "In Progress"
    const p2 = await api('/projects', { method: 'POST', token: r.data.token, body: { name: 'Toast Test' } });
    const c2 = await api(`/projects/${p2.data.id}/cards`, { method: 'POST', token: r.data.token, body: { title: 'Drag me', column_name: 'In Progress' } });

    // Login in browser
    await page.evaluate(async (u) => {
      const res = await fetch('/api/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: u, password: 'test123456' }) });
      const data = await res.json();
      localStorage.setItem('duckllo_token', data.token);
    }, username);
    await page.reload({ waitUntil: 'networkidle0' });

    // Wait for board to load
    await page.waitForSelector('.card', { timeout: 5000 });

    // Try to move card to Review via API call in page context (simulating drag)
    const toastMsg = await page.evaluate(async (pid2, cid2) => {
      const token = localStorage.getItem('duckllo_token');
      const res = await fetch(`/api/projects/${pid2}/cards/${cid2}/move`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ column_name: 'Review', position: 0 })
      });
      const data = await res.json();
      return { status: res.status, error: data.error };
    }, p2.data.id, c2.data.id);

    if (toastMsg.status === 422 && toastMsg.error.includes('test result or demo')) {
      pass('Browser API call returns 422 with quality gate message');
    } else {
      fail('Browser API quality gate', `${toastMsg.status}`);
    }
  } catch (e) {
    fail('Puppeteer toast test', e.message);
  } finally {
    if (browser) await browser.close();
  }

  // ── Summary ──
  const passed = results.filter(r => r.includes('[PASS]')).length;
  const total = results.length;
  console.log('Test Suite: Quality Gates');
  results.forEach(r => console.log(r));
  console.log(`\n${passed}/${total} passed`);
  process.exit(passed === total ? 0 : 1);
})();
