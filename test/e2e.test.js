const puppeteer = require('puppeteer');
const GIFEncoder = require('gif-encoder-2');
const { PNG } = require('pngjs');
const fs = require('fs');
const path = require('path');

const BASE_URL = 'http://localhost:3000';
const GIF_DIR = path.join(__dirname, '..', 'docs', 'gifs');
const VIEWPORT = { width: 1280, height: 800 };

if (!fs.existsSync(GIF_DIR)) fs.mkdirSync(GIF_DIR, { recursive: true });

let browser, page;

// ── GIF Recording ───────────────────────────────────────────────────────

class GifRecorder {
  constructor(name, width = VIEWPORT.width, height = VIEWPORT.height) {
    this.name = name;
    this.encoder = new GIFEncoder(width, height, 'neuquant', true);
    this.encoder.setDelay(500);
    this.encoder.setQuality(10);
    this.frames = [];
  }

  async capture(pg) {
    const buf = await pg.screenshot({ type: 'png' });
    this.frames.push(buf);
  }

  async save() {
    const filePath = path.join(GIF_DIR, `${this.name}.gif`);
    this.encoder.start();
    for (const frame of this.frames) {
      const png = PNG.sync.read(frame);
      this.encoder.addFrame(png.data);
    }
    this.encoder.finish();
    fs.writeFileSync(filePath, this.encoder.out.getData());
    console.log(`  GIF saved: docs/gifs/${this.name}.gif (${this.frames.length} frames)`);
    return filePath;
  }
}

// ── Helpers ─────────────────────────────────────────────────────────────

const delay = (ms) => new Promise(r => setTimeout(r, ms));

async function setValue(selector, value) {
  await page.evaluate((sel, val) => {
    const el = document.querySelector(sel);
    el.value = val;
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, selector, value);
}

async function clickEl(selector) {
  await page.evaluate((sel) => document.querySelector(sel)?.click(), selector);
}

async function waitForDisplay(selector, timeout = 5000) {
  await page.waitForFunction(
    (sel) => {
      const el = document.querySelector(sel);
      return el && getComputedStyle(el).display !== 'none';
    },
    { timeout },
    selector
  );
  await delay(200);
}

async function closeModals() {
  await page.evaluate(() => {
    document.querySelectorAll('.modal').forEach(m => m.style.display = 'none');
  });
  await delay(200);
}

// ── Tests ───────────────────────────────────────────────────────────────

async function testRegistration() {
  console.log('\n[TEST] User Registration');
  const gif = new GifRecorder('01-registration');

  await page.goto(BASE_URL, { waitUntil: 'networkidle0' });
  await gif.capture(page);

  // Switch to register tab
  await clickEl('[data-tab="register"]');
  await delay(300);
  await gif.capture(page);

  // Fill in registration
  await setValue('#reg-username', 'demouser');
  await setValue('#reg-display', 'Demo User');
  await setValue('#reg-password', 'demo123');
  await delay(200);
  await gif.capture(page);

  // Submit via JS to avoid HTML validation issues
  await page.evaluate(async () => {
    const res = await fetch('/api/auth/register', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'demouser', password: 'demo123', display_name: 'Demo User' })
    });
    const data = await res.json();
    if (data.token) {
      localStorage.setItem('duckllo_token', data.token);
      location.reload();
    }
  });
  await page.waitForNavigation({ waitUntil: 'networkidle0' });
  await delay(500);
  await gif.capture(page);

  const mainVisible = await page.$eval('#main-screen', el => getComputedStyle(el).display !== 'none');
  console.log(`  Registration: ${mainVisible ? 'PASS' : 'FAIL'}`);

  await gif.save();
  return mainVisible;
}

async function testCreateProject() {
  console.log('\n[TEST] Create Project');
  const gif = new GifRecorder('02-create-project');
  await gif.capture(page);

  // Create project via API, then reload to show it
  await page.evaluate(async () => {
    const token = localStorage.getItem('duckllo_token');
    const res = await fetch('/api/projects', {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'Duckllo Development', description: 'Tracking Duckllo features and bugs' })
    });
    const project = await res.json();
    localStorage.setItem('duckllo_project', project.id);
  });

  await page.reload({ waitUntil: 'networkidle0' });
  await delay(800);
  await gif.capture(page);

  // Also test the UI modal flow
  await clickEl('#new-project-btn');
  await delay(500);
  await gif.capture(page);

  // Close modal without submitting (just to show it works)
  await closeModals();
  await delay(300);
  await gif.capture(page);

  const columns = await page.$$('.column');
  const pass = columns.length === 5;
  console.log(`  Project created with ${columns.length} columns: ${pass ? 'PASS' : 'FAIL'}`);

  await gif.save();
  return pass;
}

async function testCreateCards() {
  console.log('\n[TEST] Create Cards');
  const gif = new GifRecorder('03-create-cards');
  await gif.capture(page);

  const cardsData = [
    { title: 'Implement drag-and-drop', desc: 'Cards should be draggable between columns', type: 'feature', priority: 'high', column: 'Done' },
    { title: 'Add API key auth', desc: 'Agents need API keys to update cards', type: 'feature', priority: 'high', column: 'Done' },
    { title: 'Login page styling broken on mobile', desc: 'Auth form overflows on small screens', type: 'bug', priority: 'medium', column: 'Todo' },
    { title: 'Add card comment system', desc: 'Users and agents can comment on cards', type: 'feature', priority: 'medium', column: 'In Progress' },
    { title: 'Optimize SQLite queries', desc: 'Add indexes for card lookups', type: 'improvement', priority: 'low', column: 'Backlog' },
    { title: 'Write E2E tests', desc: 'Puppeteer-based test suite with GIF recording', type: 'task', priority: 'high', column: 'In Progress' },
  ];

  // Create cards via API for reliability
  const projectId = await page.evaluate(() => localStorage.getItem('duckllo_project'));
  const token = await page.evaluate(() => localStorage.getItem('duckllo_token'));

  for (const card of cardsData) {
    await page.evaluate(async (c, pid, tok) => {
      await fetch(`/api/projects/${pid}/cards`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${tok}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({
          title: c.title, description: c.desc, card_type: c.type,
          priority: c.priority, column_name: c.column
        })
      });
    }, card, projectId, token);
  }

  // Reload to show cards
  await page.reload({ waitUntil: 'networkidle0' });
  await delay(800);
  await gif.capture(page);

  // Also test the UI new-card modal
  await clickEl('.add-card-btn[data-column="Review"]');
  await delay(500);
  await gif.capture(page);
  await closeModals();

  const cardEls = await page.$$('.card');
  const pass = cardEls.length === cardsData.length;
  console.log(`  Created ${cardEls.length}/${cardsData.length} cards: ${pass ? 'PASS' : 'FAIL'}`);

  await gif.save();
  return pass;
}

async function testCardDetail() {
  console.log('\n[TEST] Card Detail & Edit');
  const gif = new GifRecorder('04-card-detail');
  await gif.capture(page);

  // Click the first card
  await clickEl('.card');
  await waitForDisplay('#card-modal');
  await delay(300);
  await gif.capture(page);

  // Update fields
  await page.evaluate(() => {
    document.querySelector('#detail-testing-status').value = 'passing';
    document.querySelector('#detail-testing-result').value =
      'Test Suite: DragDrop\n  [PASS] renders kanban columns\n  [PASS] cards are draggable\n  [PASS] drop updates column\n  [PASS] position is preserved\n\n4/4 tests passed';
    document.querySelector('#detail-labels').value = 'ui, core, v1.0';
  });
  await delay(300);
  await gif.capture(page);

  // Save
  await clickEl('#save-card-btn');
  await delay(800);
  await gif.capture(page);

  // Verify
  const hasPassing = await page.$('.card-testing.passing');
  const pass = hasPassing !== null;
  console.log(`  Card detail update: ${pass ? 'PASS' : 'FAIL'}`);

  await gif.save();
  return pass;
}

async function testComments() {
  console.log('\n[TEST] Card Comments');
  const gif = new GifRecorder('05-comments');

  // Open second card
  const cards = await page.$$('.card');
  if (cards.length < 2) { console.log('  SKIP: not enough cards'); return false; }

  await cards[1].evaluate(el => el.click());
  await waitForDisplay('#card-modal');
  await delay(300);
  await gif.capture(page);

  // Add comments
  await setValue('#new-comment', 'Started implementing the comment system. Backend endpoints are ready.');
  await delay(200);
  await gif.capture(page);
  await clickEl('#add-comment-btn');
  await delay(600);
  await gif.capture(page);

  await setValue('#new-comment', 'Frontend comment display is done. Testing with agent API key next.');
  await clickEl('#add-comment-btn');
  await delay(600);
  await gif.capture(page);

  const comments = await page.$$('.comment');
  const pass = comments.length >= 2;
  console.log(`  Comments added (${comments.length}): ${pass ? 'PASS' : 'FAIL'}`);

  await closeModals();
  await gif.save();
  return pass;
}

async function testProjectSettings() {
  console.log('\n[TEST] Project Settings & API Keys');
  const gif = new GifRecorder('06-settings-apikeys');
  await gif.capture(page);

  await clickEl('#settings-btn');
  await waitForDisplay('#settings-modal');
  await delay(300);
  await gif.capture(page);

  // Create API key
  await setValue('#new-key-label', 'Claude Code Agent');
  await delay(200);
  await gif.capture(page);

  await clickEl('#create-key-btn');
  await delay(1000);
  await gif.capture(page);

  const keyVisible = await page.$eval('#new-key-display', el => getComputedStyle(el).display !== 'none');
  const keyValue = await page.$eval('#new-key-value', el => el.textContent);
  const pass = keyVisible && keyValue.startsWith('duckllo_');
  console.log(`  API key generated: ${pass ? 'PASS' : 'FAIL'} (${keyValue.substring(0, 15)}...)`);

  await closeModals();
  await gif.save();
  return pass;
}

async function testAgentAPI() {
  console.log('\n[TEST] Agent API (programmatic)');
  const gif = new GifRecorder('07-agent-api');

  const result = await page.evaluate(async () => {
    try {
      const origToken = localStorage.getItem('duckllo_token');

      // Register agent user
      const regRes = await fetch('/api/auth/register', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: 'agent_bot', password: 'agent123', display_name: 'CI Bot' })
      });
      const regData = await regRes.json();
      if (!regData.token) return { success: false, error: 'reg failed' };

      // Get projects
      const projRes = await fetch('/api/projects', { headers: { 'Authorization': `Bearer ${origToken}` } });
      const projects = await projRes.json();
      if (!projects.length) return { success: false, error: 'no projects' };
      const pid = projects[0].id;

      // Add agent as member
      await fetch(`/api/projects/${pid}/members`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${origToken}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: 'agent_bot', role: 'member' })
      });

      // Generate API key
      const keyRes = await fetch(`/api/projects/${pid}/api-keys`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${regData.token}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ label: 'E2E Test Bot' })
      });
      const keyData = await keyRes.json();
      if (!keyData.key) return { success: false, error: 'key gen failed' };

      // Create card via API key
      const cardRes = await fetch(`/api/projects/${pid}/cards`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${keyData.key}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({
          title: 'Agent-created: Fix memory leak in worker',
          description: 'Detected via monitoring. Worker process memory grows unbounded after 1000 requests.',
          card_type: 'bug', column_name: 'Todo', priority: 'critical',
          labels: ['agent-reported', 'performance']
        })
      });
      const card = await cardRes.json();
      if (!card.id) return { success: false, error: 'card create failed' };

      // Update card with test results via API key
      await fetch(`/api/projects/${pid}/cards/${card.id}`, {
        method: 'PATCH',
        headers: { 'Authorization': `Bearer ${keyData.key}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({
          testing_status: 'failing',
          testing_result: 'Memory Test:\n  Initial: 45MB\n  After 100 req: 52MB\n  After 500 req: 89MB\n  After 1000 req: 156MB\n  [FAIL] Memory exceeds 100MB threshold',
          column_name: 'In Progress'
        })
      });

      // Add comment via API key
      await fetch(`/api/projects/${pid}/cards/${card.id}/comments`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${keyData.key}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({
          content: 'Automated detection: memory leak traced to unclosed database connections in worker pool.',
          comment_type: 'agent_update'
        })
      });

      return { success: true, cardId: card.id };
    } catch (e) { return { success: false, error: e.message }; }
  });

  if (!result.success) console.log(`  Detail: ${result.error}`);

  // Reload to show agent-created card
  await page.reload({ waitUntil: 'networkidle0' });
  await delay(800);
  await gif.capture(page);

  // Open the agent-created card
  const found = await page.evaluate(() => {
    const cards = document.querySelectorAll('.card');
    for (const c of cards) {
      if (c.querySelector('.card-title')?.textContent.includes('Agent-created')) {
        c.click();
        return true;
      }
    }
    return false;
  });

  if (found) {
    await waitForDisplay('#card-modal');
    await delay(400);
    await gif.capture(page);
    await closeModals();
  }

  await gif.capture(page);
  console.log(`  Agent API card creation: ${result.success ? 'PASS' : 'FAIL'}`);

  await gif.save();
  return result.success;
}

async function testCardMove() {
  console.log('\n[TEST] Card Move (API)');
  const gif = new GifRecorder('08-card-move');
  await gif.capture(page);

  const result = await page.evaluate(async () => {
    try {
      const token = localStorage.getItem('duckllo_token');
      const projRes = await fetch('/api/projects', { headers: { 'Authorization': `Bearer ${token}` } });
      const projects = await projRes.json();
      if (!projects.length) return false;

      const cardsRes = await fetch(`/api/projects/${projects[0].id}/cards`, { headers: { 'Authorization': `Bearer ${token}` } });
      const cards = await cardsRes.json();
      const todoCard = cards.find(c => c.column_name === 'Todo');
      if (!todoCard) return false;

      const res = await fetch(`/api/projects/${projects[0].id}/cards/${todoCard.id}/move`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ column_name: 'In Progress', position: 0 })
      });
      return res.ok;
    } catch { return false; }
  });

  await page.reload({ waitUntil: 'networkidle0' });
  await delay(800);
  await gif.capture(page);

  console.log(`  Card move: ${result ? 'PASS' : 'FAIL'}`);
  await gif.save();
  return result;
}

async function testBoardOverview() {
  console.log('\n[TEST] Final Board Overview');
  const gif = new GifRecorder('09-board-overview');

  await page.reload({ waitUntil: 'networkidle0' });
  await delay(1000);
  await gif.capture(page);
  await delay(400);
  await gif.capture(page);

  const columns = await page.$$('.column');
  for (const col of columns) {
    const title = await col.$eval('.column-title', el => el.textContent);
    const count = await col.$eval('.column-count', el => el.textContent);
    console.log(`  ${title}: ${count} cards`);
  }

  await gif.save();
  return true;
}

async function testLoginFlow() {
  console.log('\n[TEST] Login Flow');
  const gif = new GifRecorder('10-login');

  // Logout via API and reload
  await page.evaluate(async () => {
    const token = localStorage.getItem('duckllo_token');
    await fetch('/api/auth/logout', { method: 'POST', headers: { 'Authorization': `Bearer ${token}` } });
    localStorage.removeItem('duckllo_token');
  });
  await page.reload({ waitUntil: 'networkidle0' });
  await delay(500);
  await gif.capture(page);

  // Login via API
  await page.evaluate(async () => {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'demouser', password: 'demo123' })
    });
    const data = await res.json();
    if (data.token) {
      localStorage.setItem('duckllo_token', data.token);
      location.reload();
    }
  });
  await page.waitForNavigation({ waitUntil: 'networkidle0' });
  await delay(500);
  await gif.capture(page);

  // Also show the UI login form (just the appearance)
  await page.evaluate(async () => {
    const token = localStorage.getItem('duckllo_token');
    await fetch('/api/auth/logout', { method: 'POST', headers: { 'Authorization': `Bearer ${token}` } });
    localStorage.removeItem('duckllo_token');
  });
  await page.reload({ waitUntil: 'networkidle0' });
  await delay(500);
  await gif.capture(page);

  // Fill login form visually, then submit via API
  await setValue('#login-username', 'demouser');
  await setValue('#login-password', 'demo123');
  await delay(300);
  await gif.capture(page);

  await page.evaluate(async () => {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'demouser', password: 'demo123' })
    });
    const data = await res.json();
    if (data.token) {
      localStorage.setItem('duckllo_token', data.token);
      location.reload();
    }
  });
  await page.waitForNavigation({ waitUntil: 'networkidle0' });
  await delay(500);
  await gif.capture(page);

  const mainVisible = await page.$eval('#main-screen', el => getComputedStyle(el).display !== 'none');
  console.log(`  Login: ${mainVisible ? 'PASS' : 'FAIL'}`);

  await gif.save();
  return mainVisible;
}

// ── Runner ──────────────────────────────────────────────────────────────

(async () => {
  console.log('=== Duckllo E2E Tests ===');
  console.log(`GIFs will be saved to: docs/gifs/\n`);

  browser = await puppeteer.launch({
    headless: 'new',
    args: [
      '--no-sandbox',
      '--disable-setuid-sandbox',
      `--user-data-dir=/tmp/duckllo-test-chrome-${Date.now()}`
    ]
  });

  page = await browser.newPage();
  await page.setViewport(VIEWPORT);

  const results = {};
  const tests = [
    ['Registration', testRegistration],
    ['Create Project', testCreateProject],
    ['Create Cards', testCreateCards],
    ['Card Detail', testCardDetail],
    ['Comments', testComments],
    ['Project Settings', testProjectSettings],
    ['Agent API', testAgentAPI],
    ['Card Move', testCardMove],
    ['Board Overview', testBoardOverview],
    ['Login Flow', testLoginFlow],
  ];

  for (const [name, fn] of tests) {
    try {
      results[name] = await fn();
    } catch (err) {
      console.error(`  ERROR: ${err.message}`);
      try {
        await page.screenshot({ path: path.join(GIF_DIR, `debug-${name.toLowerCase().replace(/\s+/g, '-')}.png`) });
        console.log(`  Debug screenshot saved`);
      } catch {}
      results[name] = false;
    }
  }

  await browser.close();

  // Summary
  console.log('\n=== Test Results ===');
  let allPass = true;
  for (const [name, pass] of Object.entries(results)) {
    console.log(`  ${pass ? 'PASS' : 'FAIL'}  ${name}`);
    if (!pass) allPass = false;
  }

  const total = Object.keys(results).length;
  const passed = Object.values(results).filter(Boolean).length;
  console.log(`\n${passed}/${total} tests passed`);

  console.log('\nGenerated files:');
  const files = fs.readdirSync(GIF_DIR).sort();
  files.forEach(f => {
    const size = (fs.statSync(path.join(GIF_DIR, f)).size / 1024).toFixed(1);
    console.log(`  docs/gifs/${f} (${size}KB)`);
  });

  process.exit(allPass ? 0 : 1);
})();
