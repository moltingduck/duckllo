const puppeteer = require('puppeteer');
const { createCanvas } = require('pngjs').PNG ? {} : {};
const GIFEncoder = require('gif-encoder-2');
const { PNG } = require('pngjs');
const fs = require('fs');
const path = require('path');

const BASE = 'http://localhost:3000';
const UPLOAD_DIR = path.join(__dirname, '..', 'uploads');

async function api(p, opts = {}) {
  const h = { 'Content-Type': 'application/json' };
  if (opts.token) h['Authorization'] = `Bearer ${opts.token}`;
  const res = await fetch(`${BASE}/api${p}`, { method: opts.method || 'GET', headers: h, body: opts.body ? JSON.stringify(opts.body) : undefined });
  return res.json();
}

async function screenshotToFile(page, name) {
  const filepath = path.join(UPLOAD_DIR, `${name}.png`);
  await page.screenshot({ path: filepath, fullPage: false });
  return `/uploads/${name}.png`;
}

async function captureGif(page, name, actions, opts = {}) {
  const width = opts.width || 1280;
  const height = opts.height || 800;
  const filepath = path.join(UPLOAD_DIR, `${name}.gif`);
  const frameInterval = opts.frameInterval || 80;
  const transitionFrames = opts.transitionFrames || 8;
  const holdFrames = opts.holdFrames || 15;

  const encoder = new GIFEncoder(width, height, 'neuquant');
  encoder.setDelay(frameInterval);
  encoder.setQuality(10);
  encoder.start();

  async function addFrame() {
    const buf = await page.screenshot({ clip: { x: 0, y: 0, width, height } });
    const png = PNG.sync.read(buf);
    encoder.addFrame(png.data);
  }

  // Hold on initial state
  for (let i = 0; i < holdFrames; i++) await addFrame();

  for (const action of actions) {
    await action();
    // Capture transition frames
    for (let i = 0; i < transitionFrames; i++) {
      await new Promise(r => setTimeout(r, frameInterval));
      await addFrame();
    }
    // Hold on result
    for (let i = 0; i < holdFrames; i++) await addFrame();
  }

  encoder.finish();
  fs.writeFileSync(filepath, encoder.out.getData());
  return `/uploads/${name}.gif`;
}

(async () => {
  const browser = await puppeteer.launch({ headless: true, args: ['--no-sandbox'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 1280, height: 800 });

  // Login as claude
  const login = await api('/auth/login', { method: 'POST', body: { username: 'claude', password: 'claude123' } });
  const token = login.token;
  const projects = await api('/projects', { token });
  const pid = projects[0].id;

  // Set token in browser
  await page.goto(BASE, { waitUntil: 'networkidle0' });
  await page.evaluate((t) => localStorage.setItem('duckllo_token', t), token);
  await page.reload({ waitUntil: 'networkidle0' });
  await page.waitForSelector('.column', { timeout: 5000 });

  const demos = {};

  // 1. Kanban Board - show full board with columns and cards
  console.log('Capturing: Kanban Board...');
  demos['kanban-board'] = await screenshotToFile(page, 'demo-kanban-board');

  // 2. Auth System - show login page
  console.log('Capturing: Auth System...');
  await page.evaluate(() => {
    localStorage.removeItem('duckllo_token');
  });
  await page.reload({ waitUntil: 'networkidle0' });
  await page.waitForSelector('#login-form');
  demos['auth'] = await captureGif(page, 'demo-auth', [
    // Show login form
    async () => {},
    // Switch to register tab
    async () => await page.click('[data-tab="register"]'),
    // Switch back to login
    async () => await page.click('[data-tab="login"]'),
    // Show forgot password
    async () => await page.click('#forgot-password-link'),
    // Back to login
    async () => await page.click('#back-to-login-link'),
  ]);

  // Re-login
  await page.evaluate((t) => localStorage.setItem('duckllo_token', t), token);
  await page.reload({ waitUntil: 'networkidle0' });
  await page.waitForSelector('.column', { timeout: 5000 });

  // 3. Project Management - show settings modal with members
  console.log('Capturing: Project Settings...');
  await page.click('#settings-btn');
  await new Promise(r => setTimeout(r, 500));
  demos['project-settings'] = await screenshotToFile(page, 'demo-project-settings');
  await page.keyboard.press('Escape');
  await new Promise(r => setTimeout(r, 300));

  // 4. Card Detail - open a card and show details
  console.log('Capturing: Card Detail...');
  const cardEl = await page.$('.card');
  if (cardEl) {
    await cardEl.click();
    await new Promise(r => setTimeout(r, 500));
    demos['card-detail'] = await screenshotToFile(page, 'demo-card-detail');
    await page.keyboard.press('Escape');
    await new Promise(r => setTimeout(r, 300));
  }

  // 5. Forgot Password UI 
  console.log('Capturing: Forgot Password...');
  // Already captured in auth GIF above

  // 6. Quality Gate - try to move bare card, show toast
  console.log('Capturing: Quality Gate...');
  // Create a bare card for demo
  const bareCard = await api(`/projects/${pid}/cards`, { method: 'POST', token, body: { title: 'Demo: quality gate test', column_name: 'In Progress' } });
  await page.reload({ waitUntil: 'networkidle0' });
  await page.waitForSelector('.column', { timeout: 5000 });
  
  // Try move via API and show the error toast
  demos['quality-gate'] = await captureGif(page, 'demo-quality-gate', [
    async () => {},
    async () => {
      // Trigger a blocked move to show toast
      await page.evaluate(async (p, c) => {
        const t = localStorage.getItem('duckllo_token');
        const res = await fetch(`/api/projects/${p}/cards/${c}/move`, {
          method: 'POST',
          headers: { 'Authorization': `Bearer ${t}`, 'Content-Type': 'application/json' },
          body: JSON.stringify({ column_name: 'Review', position: 0 })
        });
        const data = await res.json();
        if (!res.ok) {
          // Trigger toast manually since showToast is in app scope
          const toast = document.createElement('div');
          toast.className = 'toast toast-error show';
          toast.textContent = data.error;
          document.body.appendChild(toast);
        }
      }, pid, bareCard.id);
    },
    async () => await new Promise(r => setTimeout(r, 600)),
  ]);
  // Clean up demo card
  await api(`/projects/${pid}/cards/${bareCard.id}`, { method: 'DELETE', token });

  // 7. New Card modal
  console.log('Capturing: New Card...');
  const addBtn = await page.$('.add-card-btn');
  if (addBtn) {
    await addBtn.click();
    await new Promise(r => setTimeout(r, 500));
    demos['new-card'] = await screenshotToFile(page, 'demo-new-card');
    await page.keyboard.press('Escape');
    await new Promise(r => setTimeout(r, 300));
  }

  // Now upload demos to cards
  console.log('\nUploading demos to cards...');
  const cards = await api(`/projects/${pid}/cards`, { token });
  
  const cardDemoMap = {
    'Core Kanban Board': 'kanban-board',
    'Authentication System': 'auth',
    'Project Management': 'project-settings',
    'Card Testing Status': 'card-detail',
    'Card Comments': 'card-detail',
    'Forgot password UI': 'auth',
    'Password reset via': 'auth',
    'Quality Gate': 'quality-gate',
    'E2E Test Suite': 'kanban-board',
    'SKILL.md': 'kanban-board',
    'CLAUDE.md': 'kanban-board',
    'Kanban Watcher': 'kanban-board',
    'Migrate database': 'kanban-board',
    'Dockerize': 'kanban-board',
  };

  for (const card of cards.filter(c => c.column_name === 'Done')) {
    let demoKey = null;
    for (const [prefix, key] of Object.entries(cardDemoMap)) {
      if (card.title.includes(prefix)) { demoKey = key; break; }
    }
    if (demoKey && demos[demoKey]) {
      await api(`/projects/${pid}/cards/${card.id}`, {
        method: 'PATCH', token,
        body: { demo_gif_url: demos[demoKey] }
      });
      console.log(`  Uploaded ${demos[demoKey]} → ${card.title.substring(0, 50)}`);
    }
  }

  console.log('\nDone!');
  await browser.close();
})();
