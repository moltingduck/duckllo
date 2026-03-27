const puppeteer = require('puppeteer');
const http = require('http');
const BASE = 'http://localhost:3000';
const PID = '38b88cea-3a6b-4b8d-bd17-52f4d5331170';

async function login(username, password) {
  const data = JSON.stringify({ username, password });
  return new Promise((resolve, reject) => {
    const req = http.request(`${BASE}/api/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Content-Length': data.length }
    }, res => {
      let body = '';
      res.on('data', chunk => body += chunk);
      res.on('end', () => resolve(JSON.parse(body)));
    });
    req.on('error', reject);
    req.write(data);
    req.end();
  });
}

(async () => {
  const { token } = await login('gin', 'gin123');
  const browser = await puppeteer.launch({
    headless: 'new',
    args: ['--no-sandbox', '--disable-setuid-sandbox']
  });
  const page = await browser.newPage();
  await page.setViewport({ width: 1400, height: 900 });

  await page.evaluateOnNewDocument((t, pid) => {
    localStorage.setItem('duckllo_token', t);
    localStorage.setItem('duckllo_project', pid);
  }, token, PID);

  await page.goto(BASE, { waitUntil: 'networkidle2' });
  await new Promise(r => setTimeout(r, 2000));

  // Open settings
  await page.evaluate(() => document.getElementById('settings-btn').click());
  await new Promise(r => setTimeout(r, 1500));

  // Scroll to Auto-Archive section
  await page.evaluate(() => {
    const el = document.getElementById('auto-archive-days');
    if (el) el.scrollIntoView({ behavior: 'instant', block: 'center' });
  });
  await new Promise(r => setTimeout(r, 500));

  await page.screenshot({ path: 'docs/gifs/auto-archive-settings.png', fullPage: false });
  console.log('Saved: docs/gifs/auto-archive-settings.png');

  await browser.close();
  console.log('Done!');
})();
