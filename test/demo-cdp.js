/**
 * Duckllo CDP Demo - Full feature walkthrough via Chrome DevTools Protocol
 * Connects to Chromium on port 9222 (headless with remote debugging)
 * Records GIFs of each feature demo
 */

const puppeteer = require('puppeteer');
const GIFEncoder = require('gif-encoder-2');
const { PNG } = require('pngjs');
const fs = require('fs');
const path = require('path');

const BASE_URL = 'http://localhost:3000';
const DEMO_DIR = path.join(__dirname, '..', 'docs', 'demo');
const VIEWPORT = { width: 1280, height: 800 };

if (!fs.existsSync(DEMO_DIR)) fs.mkdirSync(DEMO_DIR, { recursive: true });

let browser, page;

// ── GIF Recording ───────────────────────────────────────────────────────

class GifRecorder {
  constructor(name) {
    this.name = name;
    this.encoder = new GIFEncoder(VIEWPORT.width, VIEWPORT.height, 'neuquant', true);
    this.encoder.setDelay(600);
    this.encoder.setQuality(10);
    this.frames = [];
  }

  async snap() {
    const buf = await page.screenshot({ type: 'png' });
    this.frames.push(buf);
    return this;
  }

  async save() {
    const filePath = path.join(DEMO_DIR, `${this.name}.gif`);
    this.encoder.start();
    for (const f of this.frames) {
      const png = PNG.sync.read(f);
      this.encoder.addFrame(png.data);
    }
    this.encoder.finish();
    fs.writeFileSync(filePath, this.encoder.out.getData());
    const kb = (fs.statSync(filePath).size / 1024).toFixed(1);
    console.log(`    -> docs/demo/${this.name}.gif (${this.frames.length} frames, ${kb}KB)`);
  }
}

// ── Helpers ─────────────────────────────────────────────────────────────

const wait = (ms) => new Promise(r => setTimeout(r, ms));

async function typeInto(selector, text, delayMs = 30) {
  await page.focus(selector);
  await page.evaluate(sel => { document.querySelector(sel).value = ''; }, selector);
  await page.type(selector, text, { delay: delayMs });
}

async function clickBtn(selector) {
  await page.evaluate(sel => document.querySelector(sel)?.click(), selector);
}

async function waitVisible(selector, timeout = 5000) {
  await page.waitForFunction(
    sel => {
      const el = document.querySelector(sel);
      return el && getComputedStyle(el).display !== 'none';
    },
    { timeout },
    selector
  );
  await wait(300);
}

async function closeModals() {
  await page.evaluate(() => document.querySelectorAll('.modal').forEach(m => m.style.display = 'none'));
  await wait(200);
}

// ── Feature Demos ───────────────────────────────────────────────────────

async function demo01_Registration() {
  console.log('\n  [DEMO 1] User Registration');
  const gif = new GifRecorder('demo-01-registration');

  await page.goto(BASE_URL, { waitUntil: 'networkidle0' });
  await gif.snap();

  // Show register tab
  await clickBtn('[data-tab="register"]');
  await wait(400);
  await gif.snap();

  // Type credentials with visible typing
  await typeInto('#reg-username', 'alice');
  await typeInto('#reg-display', 'Alice Chen');
  await typeInto('#reg-password', 'alice123');
  await wait(300);
  await gif.snap();

  // Submit
  await page.evaluate(async () => {
    const res = await fetch('/api/auth/register', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'alice', password: 'alice123', display_name: 'Alice Chen' })
    });
    const data = await res.json();
    localStorage.setItem('duckllo_token', data.token);
    location.reload();
  });
  await page.waitForNavigation({ waitUntil: 'networkidle0' });
  await wait(600);
  await gif.snap();

  console.log('    PASS - User registered and logged in');
  await gif.save();
}

async function demo02_CreateProject() {
  console.log('\n  [DEMO 2] Create Project');
  const gif = new GifRecorder('demo-02-create-project');
  await gif.snap();

  // Click "+ Project" button
  await clickBtn('#new-project-btn');
  await wait(500);
  await gif.snap();

  // Type project details
  await typeInto('#new-project-name', 'Duckllo v1.0');
  await typeInto('#new-project-desc', 'Feature tracking for the Duckllo kanban app');
  await wait(300);
  await gif.snap();

  // Submit via API + reload (reliable)
  await page.evaluate(async () => {
    const token = localStorage.getItem('duckllo_token');
    const res = await fetch('/api/projects', {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'Duckllo v1.0', description: 'Feature tracking for the Duckllo kanban app' })
    });
    const p = await res.json();
    localStorage.setItem('duckllo_project', p.id);
    location.reload();
  });
  await page.waitForNavigation({ waitUntil: 'networkidle0' });
  await wait(800);
  await gif.snap();

  const cols = await page.$$('.column');
  console.log(`    PASS - Project created with ${cols.length} columns`);
  await gif.save();
}

async function demo03_CreateCards() {
  console.log('\n  [DEMO 3] Create Cards (all types)');
  const gif = new GifRecorder('demo-03-create-cards');
  await gif.snap();

  const cards = [
    { title: 'User authentication system', desc: 'Login, register, sessions, API keys', type: 'feature', priority: 'high', col: 'Done' },
    { title: 'Kanban drag & drop', desc: 'Drag cards between columns', type: 'feature', priority: 'high', col: 'Done' },
    { title: 'Card detail modal', desc: 'View and edit card fields in a modal', type: 'feature', priority: 'medium', col: 'Review' },
    { title: 'Testing results display', desc: 'Show test output with pass/fail status', type: 'feature', priority: 'medium', col: 'Review' },
    { title: 'Demo GIF upload', desc: 'Upload and display GIF demos per card', type: 'feature', priority: 'medium', col: 'In Progress' },
    { title: 'Mobile responsive layout', desc: 'Board should work on tablets and phones', type: 'task', priority: 'medium', col: 'Todo' },
    { title: 'Cards disappear after drag on Safari', desc: 'Drag events not firing correctly on WebKit', type: 'bug', priority: 'high', col: 'Todo' },
    { title: 'Optimize card rendering', desc: 'Large boards get sluggish with 100+ cards', type: 'improvement', priority: 'low', col: 'Backlog' },
  ];

  const token = await page.evaluate(() => localStorage.getItem('duckllo_token'));
  const pid = await page.evaluate(() => localStorage.getItem('duckllo_project'));

  for (const c of cards) {
    await page.evaluate(async (card, projectId, tok) => {
      await fetch(`/api/projects/${projectId}/cards`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${tok}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({
          title: card.title, description: card.desc, card_type: card.type,
          priority: card.priority, column_name: card.col
        })
      });
    }, c, pid, token);
  }

  await page.reload({ waitUntil: 'networkidle0' });
  await wait(800);
  await gif.snap();

  // Also show the "Add card" UI modal
  await clickBtn('.add-card-btn[data-column="Todo"]');
  await wait(500);
  await gif.snap();
  await closeModals();
  await wait(300);
  await gif.snap();

  const cardEls = await page.$$('.card');
  console.log(`    PASS - ${cardEls.length} cards created across all columns`);
  await gif.save();
}

async function demo04_CardDetail() {
  console.log('\n  [DEMO 4] Card Detail & Testing Results');
  const gif = new GifRecorder('demo-04-card-detail-testing');
  await gif.snap();

  // Click the first "Done" card
  await page.evaluate(() => {
    const cards = document.querySelectorAll('.card');
    cards[0]?.click();
  });
  await waitVisible('#card-modal');
  await wait(400);
  await gif.snap();

  // Fill in testing status and results
  await page.evaluate(() => {
    document.querySelector('#detail-testing-status').value = 'passing';
    document.querySelector('#detail-testing-result').value = [
      'Test Suite: AuthSystem',
      '  [PASS] POST /api/auth/register creates user',
      '  [PASS] POST /api/auth/login returns token',
      '  [PASS] GET /api/auth/me returns current user',
      '  [PASS] POST /api/auth/logout invalidates session',
      '  [PASS] API key auth works for card CRUD',
      '  [PASS] Invalid credentials return 401',
      '',
      '6/6 tests passed - 0.34s'
    ].join('\n');
    document.querySelector('#detail-labels').value = 'auth, core, v1.0';
  });
  await wait(400);
  await gif.snap();

  // Save
  await clickBtn('#save-card-btn');
  await wait(800);
  await gif.snap();

  // Open second Done card and set it to passing too
  await page.evaluate(() => {
    const cards = document.querySelectorAll('.card');
    for (const c of cards) {
      if (c.querySelector('.card-title')?.textContent.includes('drag')) {
        c.click(); break;
      }
    }
  });
  await waitVisible('#card-modal');
  await wait(400);

  await page.evaluate(() => {
    document.querySelector('#detail-testing-status').value = 'passing';
    document.querySelector('#detail-testing-result').value = [
      'Test Suite: DragDrop',
      '  [PASS] renders 5 kanban columns',
      '  [PASS] cards are draggable elements',
      '  [PASS] drop event moves card to new column',
      '  [PASS] card position preserved after refresh',
      '',
      '4/4 tests passed - 0.18s'
    ].join('\n');
    document.querySelector('#detail-labels').value = 'ui, dnd, v1.0';
  });
  await clickBtn('#save-card-btn');
  await wait(800);
  await gif.snap();

  console.log('    PASS - Card details edited with testing results');
  await gif.save();
}

async function demo05_Comments() {
  console.log('\n  [DEMO 5] Card Comments');
  const gif = new GifRecorder('demo-05-comments');

  // Open the bug card
  await page.evaluate(() => {
    const cards = document.querySelectorAll('.card');
    for (const c of cards) {
      if (c.querySelector('.card-title')?.textContent.includes('Safari')) {
        c.click(); break;
      }
    }
  });
  await waitVisible('#card-modal');
  await wait(400);
  await gif.snap();

  // Add first comment
  await typeInto('#new-comment', 'Reproduced on Safari 17.2. The dragend event fires before drop, causing the card element to be removed prematurely.');
  await wait(200);
  await gif.snap();
  await clickBtn('#add-comment-btn');
  await wait(600);

  // Add second comment
  await typeInto('#new-comment', 'Potential fix: use a setTimeout(0) in dragend handler to defer cleanup until after drop completes.');
  await clickBtn('#add-comment-btn');
  await wait(600);
  await gif.snap();

  await closeModals();
  console.log('    PASS - Comments added to card');
  await gif.save();
}

async function demo06_Settings_APIKeys() {
  console.log('\n  [DEMO 6] Project Settings & API Key Generation');
  const gif = new GifRecorder('demo-06-settings-apikeys');
  await gif.snap();

  await clickBtn('#settings-btn');
  await waitVisible('#settings-modal');
  await wait(400);
  await gif.snap();

  // Generate API key
  await typeInto('#new-key-label', 'Claude Code Agent');
  await wait(200);
  await gif.snap();

  await clickBtn('#create-key-btn');
  await wait(1000);
  await gif.snap();

  const key = await page.$eval('#new-key-value', el => el.textContent);
  console.log(`    PASS - API key generated: ${key.substring(0, 15)}...`);

  // Store key for agent demo
  await page.evaluate((k) => { window.__agentKey = k; }, key);

  await closeModals();
  await gif.save();
  return key;
}

async function demo07_AgentAPI(apiKey) {
  console.log('\n  [DEMO 7] Agent Creates & Updates Card via API Key');
  const gif = new GifRecorder('demo-07-agent-api');
  await gif.snap();

  // Register agent user and use the API key
  const result = await page.evaluate(async (key) => {
    const origToken = localStorage.getItem('duckllo_token');
    const projRes = await fetch('/api/projects', { headers: { 'Authorization': `Bearer ${origToken}` } });
    const projects = await projRes.json();
    const pid = projects[0].id;

    // Register agent_bot
    const regRes = await fetch('/api/auth/register', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'claude_agent', password: 'agent123', display_name: 'Claude Agent' })
    });
    const regData = await regRes.json();

    // Add as member
    await fetch(`/api/projects/${pid}/members`, {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${origToken}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'claude_agent', role: 'member' })
    });

    // Generate key for agent
    const keyRes = await fetch(`/api/projects/${pid}/api-keys`, {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${regData.token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ label: 'Claude CI Bot' })
    });
    const keyData = await keyRes.json();
    const agentKey = keyData.key;

    // Agent creates a bug report
    const cardRes = await fetch(`/api/projects/${pid}/cards`, {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${agentKey}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title: '[Agent] Memory leak in WebSocket handler',
        description: 'Automated monitoring detected: RSS grows 2MB/hour under sustained connections. Heap snapshot shows detached EventListener objects accumulating.',
        card_type: 'bug', column_name: 'Todo', priority: 'critical',
        labels: ['agent-detected', 'memory', 'websocket']
      })
    });
    const card = await cardRes.json();

    // Agent updates with test results
    await fetch(`/api/projects/${pid}/cards/${card.id}`, {
      method: 'PATCH',
      headers: { 'Authorization': `Bearer ${agentKey}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        testing_status: 'failing',
        testing_result: 'Memory Leak Test (10 min soak):\n  t=0m    RSS: 84MB   Heap: 62MB\n  t=2m    RSS: 88MB   Heap: 65MB\n  t=5m    RSS: 96MB   Heap: 74MB\n  t=10m   RSS: 112MB  Heap: 91MB\n\n  [FAIL] RSS delta: +28MB (threshold: 10MB)\n  [FAIL] Heap delta: +29MB (threshold: 8MB)\n\n  Detached listeners found: 847\n  Suspected source: ws.on(\"message\") in pool.js:142'
      })
    });

    // Agent adds diagnostic comment
    await fetch(`/api/projects/${pid}/cards/${card.id}/comments`, {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${agentKey}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        content: 'Root cause identified: WebSocket message handlers are registered on each reconnect but never cleaned up. The pool.js reconnection logic at line 142 calls ws.on("message", handler) without first calling ws.removeAllListeners("message").\n\nProposed fix: Add ws.removeAllListeners() before re-registering in the reconnect path.',
        comment_type: 'agent_update'
      })
    });

    return { success: true, cardId: card.id };
  }, apiKey);

  // Reload to show agent-created card
  await page.reload({ waitUntil: 'networkidle0' });
  await wait(800);
  await gif.snap();

  // Open the agent-created card to show its details
  await page.evaluate(() => {
    const cards = document.querySelectorAll('.card');
    for (const c of cards) {
      if (c.querySelector('.card-title')?.textContent.includes('[Agent]')) {
        c.click(); break;
      }
    }
  });
  await waitVisible('#card-modal');
  await wait(500);
  await gif.snap();

  // Scroll down to show test results
  await page.evaluate(() => {
    document.querySelector('#detail-testing-result')?.scrollIntoView({ behavior: 'smooth' });
  });
  await wait(400);
  await gif.snap();

  await closeModals();
  console.log(`    PASS - Agent created bug card with test results and diagnostic comment`);
  await gif.save();
}

async function demo08_CardMove() {
  console.log('\n  [DEMO 8] Move Cards Between Columns');
  const gif = new GifRecorder('demo-08-card-move');
  await gif.snap();

  // Move the "In Progress" card to "Review" via API
  await page.evaluate(async () => {
    const token = localStorage.getItem('duckllo_token');
    const pid = localStorage.getItem('duckllo_project');
    const cardsRes = await fetch(`/api/projects/${pid}/cards`, { headers: { 'Authorization': `Bearer ${token}` } });
    const cards = await cardsRes.json();
    const inProg = cards.find(c => c.column_name === 'In Progress');
    if (inProg) {
      await fetch(`/api/projects/${pid}/cards/${inProg.id}/move`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ column_name: 'Review', position: 0 })
      });
    }
    // Also move bug from Todo to In Progress
    const bug = cards.find(c => c.column_name === 'Todo' && c.card_type === 'bug' && c.title.includes('[Agent]'));
    if (bug) {
      await fetch(`/api/projects/${pid}/cards/${bug.id}/move`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ column_name: 'In Progress', position: 0 })
      });
    }
  });

  await page.reload({ waitUntil: 'networkidle0' });
  await wait(800);
  await gif.snap();

  console.log('    PASS - Cards moved between columns');
  await gif.save();
}

async function demo09_LoginLogout() {
  console.log('\n  [DEMO 9] Logout & Login Flow');
  const gif = new GifRecorder('demo-09-login-logout');
  await gif.snap();

  // Logout
  await page.evaluate(async () => {
    const token = localStorage.getItem('duckllo_token');
    await fetch('/api/auth/logout', { method: 'POST', headers: { 'Authorization': `Bearer ${token}` } });
    localStorage.removeItem('duckllo_token');
  });
  await page.reload({ waitUntil: 'networkidle0' });
  await wait(500);
  await gif.snap();

  // Show login form with typing
  await typeInto('#login-username', 'alice');
  await typeInto('#login-password', 'alice123');
  await wait(300);
  await gif.snap();

  // Login via API and reload
  await page.evaluate(async () => {
    const res = await fetch('/api/auth/login', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'alice', password: 'alice123' })
    });
    const data = await res.json();
    localStorage.setItem('duckllo_token', data.token);
    location.reload();
  });
  await page.waitForNavigation({ waitUntil: 'networkidle0' });
  await wait(600);
  await gif.snap();

  console.log('    PASS - Logged out and back in');
  await gif.save();
}

async function demo10_FinalOverview() {
  console.log('\n  [DEMO 10] Final Board Overview');
  const gif = new GifRecorder('demo-10-board-overview');

  await page.reload({ waitUntil: 'networkidle0' });
  await wait(1000);
  await gif.snap();
  await wait(500);
  await gif.snap();

  // Count cards per column
  const counts = await page.evaluate(() => {
    const cols = document.querySelectorAll('.column');
    return Array.from(cols).map(c => ({
      name: c.querySelector('.column-title')?.textContent,
      count: c.querySelector('.column-count')?.textContent
    }));
  });

  for (const c of counts) {
    console.log(`    ${c.name}: ${c.count} cards`);
  }

  await gif.save();
}

// ── Main ────────────────────────────────────────────────────────────────

(async () => {
  console.log('============================================');
  console.log('  Duckllo CDP Demo - Full Feature Walkthrough');
  console.log('============================================');
  console.log(`  Connecting to Chromium on port 9222...`);

  browser = await puppeteer.connect({
    browserURL: 'http://localhost:9222',
    defaultViewport: VIEWPORT
  });

  // Create a new page (don't reuse existing tabs)
  page = await browser.newPage();
  await page.setViewport(VIEWPORT);

  console.log('  Connected. Starting demos...\n');

  let apiKey;
  const demos = [
    ['Registration', demo01_Registration],
    ['Create Project', demo02_CreateProject],
    ['Create Cards', demo03_CreateCards],
    ['Card Detail & Testing', demo04_CardDetail],
    ['Comments', demo05_Comments],
    ['Settings & API Keys', demo06_Settings_APIKeys],
    ['Agent API', demo07_AgentAPI],
    ['Card Move', demo08_CardMove],
    ['Login/Logout', demo09_LoginLogout],
    ['Final Overview', demo10_FinalOverview],
  ];

  const results = {};
  for (const [name, fn] of demos) {
    try {
      const ret = await fn(apiKey);
      if (name === 'Settings & API Keys') apiKey = ret;
      results[name] = true;
    } catch (err) {
      console.error(`    FAIL: ${err.message}`);
      try { await page.screenshot({ path: path.join(DEMO_DIR, `debug-${name.toLowerCase().replace(/\W+/g, '-')}.png`) }); } catch {}
      results[name] = false;
    }
  }

  // Don't close browser (it's shared), just close our page
  await page.close();

  // Summary
  console.log('\n============================================');
  console.log('  Demo Results');
  console.log('============================================');
  for (const [name, pass] of Object.entries(results)) {
    console.log(`  ${pass ? 'PASS' : 'FAIL'}  ${name}`);
  }

  const total = Object.keys(results).length;
  const passed = Object.values(results).filter(Boolean).length;
  console.log(`\n  ${passed}/${total} demos completed`);

  console.log('\n  Generated demo GIFs:');
  const files = fs.readdirSync(DEMO_DIR).filter(f => f.endsWith('.gif')).sort();
  files.forEach(f => {
    const kb = (fs.statSync(path.join(DEMO_DIR, f)).size / 1024).toFixed(1);
    console.log(`    docs/demo/${f} (${kb}KB)`);
  });

  process.exit(passed === total ? 0 : 1);
})();
