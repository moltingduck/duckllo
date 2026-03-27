#!/usr/bin/env node
// Capture demo screenshots/GIF for bug report feature
const puppeteer = require('puppeteer');
const path = require('path');
const fs = require('fs');

const BASE = 'http://localhost:3000';
const OUT = path.join(__dirname, '..', 'docs', 'gifs');
if (!fs.existsSync(OUT)) fs.mkdirSync(OUT, { recursive: true });

(async () => {
  const browser = await puppeteer.launch({
    executablePath: '/usr/bin/chromium',
    headless: 'new',
    args: ['--no-sandbox', '--disable-setuid-sandbox', '--user-data-dir=/tmp/puppeteer-bug-demo-' + Date.now()]
  });

  const page = await browser.newPage();
  await page.setViewport({ width: 1280, height: 800 });

  // Login as gin
  await page.goto(BASE);
  await page.waitForSelector('#login-form');
  await page.type('#login-username', 'gin');
  await page.type('#login-password', 'gin123');
  await page.click('#login-form button[type="submit"]');
  await page.waitForSelector('#main-screen', { visible: true });
  await new Promise(r => setTimeout(r, 1000));

  // Switch to Duckllo Development project
  await page.click('#project-dropdown-btn');
  await new Promise(r => setTimeout(r, 500));
  const projectOptions = await page.$$('.project-option');
  for (const opt of projectOptions) {
    const text = await opt.evaluate(el => el.textContent);
    if (text.includes('Duckllo Development')) {
      await opt.click();
      break;
    }
  }
  await new Promise(r => setTimeout(r, 1000));

  // Screenshot 1: Board view
  await page.screenshot({ path: path.join(OUT, 'bug-report-board.png'), fullPage: false });
  console.log('Captured: board view');

  // Open Settings to show bug reports section
  await page.click('#settings-btn');
  await new Promise(r => setTimeout(r, 1500));

  // Scroll down to bug section
  await page.evaluate(() => {
    const modal = document.querySelector('.settings-content');
    if (modal) modal.scrollTop = modal.scrollHeight;
  });
  await new Promise(r => setTimeout(r, 500));

  await page.screenshot({ path: path.join(OUT, 'bug-report-settings.png'), fullPage: false });
  console.log('Captured: settings with bug reports');

  // Close settings
  await page.keyboard.press('Escape');
  await new Promise(r => setTimeout(r, 500));

  // Screenshot 3: Public bug report form
  const pid = await page.evaluate(() => {
    return localStorage.getItem('duckllo_project');
  });
  await page.goto(`${BASE}/bugs.html?project=${pid}`);
  await new Promise(r => setTimeout(r, 1500));
  await page.screenshot({ path: path.join(OUT, 'bug-report-form.png'), fullPage: false });
  console.log('Captured: public bug report form');

  // Fill in the form for demo
  await page.type('#bug-title', 'Cards not draggable on mobile Safari');
  await page.type('#bug-description', 'Drag and drop does not work on iOS Safari. Cards cannot be moved between columns.');
  await page.type('#bug-steps', '1. Open Duckllo on iPhone Safari\n2. Try to drag a card\n3. Nothing happens - card does not move');
  await page.type('#bug-expected', 'Card should drag and drop between columns');
  await page.type('#bug-actual', 'Card is stuck, no drag interaction registered');
  await page.select('#bug-severity', 'high');
  await new Promise(r => setTimeout(r, 500));

  await page.screenshot({ path: path.join(OUT, 'bug-report-filled.png'), fullPage: false });
  console.log('Captured: filled bug report form');

  await browser.close();
  console.log(`\nDone! Screenshots saved to ${OUT}`);
})().catch(err => { console.error(err); process.exit(1); });
