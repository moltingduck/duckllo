const puppeteer = require('puppeteer');
const GIFEncoder = require('gif-encoder-2');
const { PNG } = require('pngjs');
const fs = require('fs');
const path = require('path');

const BASE = 'http://localhost:3000';
const UPLOAD_DIR = path.join(__dirname, '..', 'uploads');

async function apiCall(p, opts = {}) {
  const h = { 'Content-Type': 'application/json' };
  if (opts.token) h['Authorization'] = `Bearer ${opts.token}`;
  if (opts.key) h['Authorization'] = `Bearer ${opts.key}`;
  const res = await fetch(`${BASE}/api${p}`, { method: opts.method || 'GET', headers: h, body: opts.body ? JSON.stringify(opts.body) : undefined });
  return res.json();
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
  // Setup: register user, create project, generate API key, create agent card
  let loginResp = await apiCall('/auth/login', { method: 'POST', body: { username: 'gin', password: 'gin123' } });
  const token = loginResp.token;

  const projects = await apiCall('/projects', { token });
  const pid = projects[0].id;

  // Generate an API key (agent)
  const keyResp = await apiCall(`/projects/${pid}/api-keys`, { method: 'POST', token, body: { label: 'Demo Agent' } });
  const apiKey = keyResp.key;

  // Agent creates a card (should be pending)
  const agentCard = await apiCall(`/projects/${pid}/cards`, {
    method: 'POST', key: apiKey,
    body: { title: 'Demo: Agent Feature Request', card_type: 'feature', column_name: 'Todo', priority: 'high', description: 'This card was created by an agent and requires approval.' }
  });
  console.log(`Agent card created: ${agentCard.id}, approval=${agentCard.approval_status}`);

  // Launch browser
  const browser = await puppeteer.launch({ headless: 'new', args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 1280, height: 800 });

  // Login as gin
  await page.goto(BASE);
  await page.evaluate(() => {
    document.getElementById('login-username').value = 'gin';
    document.getElementById('login-password').value = 'gin123';
  });
  await page.click('#login-form button[type="submit"]');
  await new Promise(r => setTimeout(r, 2000));

  // Capture GIF: show board with pending card, open it, approve it
  const gifUrl = await captureGif(page, 'approval-flow-demo', [
    // Frame 1: Board showing pending badge
    async () => { /* already on board */ },
    // Frame 2: Click the agent card to open detail
    async () => {
      await page.evaluate(() => {
        const cards = document.querySelectorAll('.card');
        for (const card of cards) {
          if (card.querySelector('.card-title')?.textContent.includes('Demo: Agent Feature Request')) {
            card.click();
            break;
          }
        }
      });
      await new Promise(r => setTimeout(r, 1000));
    },
    // Frame 3: Show the approval section in detail modal
    async () => { /* detail modal visible with pending badge */ },
    // Frame 4: Click approve
    async () => {
      await page.evaluate(() => document.getElementById('approve-card-btn').click());
      await new Promise(r => setTimeout(r, 1500));
    },
    // Frame 5: Board showing approved badge
    async () => { /* board refreshed */ },
  ], { delay: 1200 });

  console.log(`GIF saved: ${gifUrl}`);

  // Upload GIF to the approval feature card
  const featureCards = await apiCall(`/projects/${pid}/cards`, { token });
  const approvalCard = featureCards.find(c => c.title === 'Product Owner Approval Flow');
  if (approvalCard) {
    // Copy GIF into uploads and set demo_gif_url
    await apiCall(`/projects/${pid}/cards/${approvalCard.id}`, {
      method: 'PATCH', token,
      body: { demo_gif_url: gifUrl }
    });
    console.log(`Demo GIF attached to card: ${approvalCard.id}`);
  }

  // Cleanup: delete the demo card
  await apiCall(`/projects/${pid}/cards/${agentCard.id}`, { method: 'DELETE', token });
  console.log('Demo card cleaned up');

  await browser.close();
  console.log('Done!');
})().catch(err => { console.error(err); process.exit(1); });
