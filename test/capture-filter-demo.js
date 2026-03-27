#!/usr/bin/env node
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
    args: ['--no-sandbox', '--disable-setuid-sandbox', '--user-data-dir=/tmp/puppeteer-filter-demo-' + Date.now()]
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
  const opts = await page.$$('.project-option');
  for (const opt of opts) {
    const text = await opt.evaluate(el => el.textContent);
    if (text.includes('Duckllo Development')) { await opt.click(); break; }
  }
  await new Promise(r => setTimeout(r, 1000));

  // Open filter bar
  await page.click('#filter-toggle-btn');
  await new Promise(r => setTimeout(r, 300));

  // Screenshot 1: filter bar open (no filters active)
  await page.screenshot({ path: path.join(OUT, 'filter-bar-open.png') });
  console.log('Captured: filter bar open');

  // Type search
  await page.type('#filter-search', 'bug');
  await new Promise(r => setTimeout(r, 300));
  await page.screenshot({ path: path.join(OUT, 'filter-bar-search.png') });
  console.log('Captured: filter bar with search');

  // Clear and filter by type
  await page.evaluate(() => { document.getElementById('filter-search').value = ''; });
  await page.select('#filter-type', 'feature');
  await page.evaluate(() => document.getElementById('filter-type').dispatchEvent(new Event('change')));
  await new Promise(r => setTimeout(r, 300));
  await page.screenshot({ path: path.join(OUT, 'filter-bar-type.png') });
  console.log('Captured: filter bar with type filter');

  await browser.close();
  console.log(`Done! Screenshots in ${OUT}`);
})().catch(err => { console.error(err); process.exit(1); });
