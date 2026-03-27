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
    args: ['--no-sandbox', '--disable-setuid-sandbox', '--user-data-dir=/tmp/puppeteer-assignee-demo-' + Date.now()]
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

  // Click on a card to open detail
  const cardEls = await page.$$('.card');
  if (cardEls.length > 0) {
    await cardEls[0].click();
    await new Promise(r => setTimeout(r, 1000));

    // Screenshot: card detail with assignee dropdown
    await page.screenshot({ path: path.join(OUT, 'assignee-detail.png') });
    console.log('Captured: card detail with assignee dropdown');

    // Click "Me" button to self-assign
    await page.click('#self-assign-btn');
    await new Promise(r => setTimeout(r, 300));
    await page.screenshot({ path: path.join(OUT, 'assignee-self-assign.png') });
    console.log('Captured: self-assign');

    // Save the card
    await page.click('#save-card-btn');
    await new Promise(r => setTimeout(r, 1000));
  }

  // Screenshot: board with assignee badges
  await page.screenshot({ path: path.join(OUT, 'assignee-board.png') });
  console.log('Captured: board with assignee badges');

  await browser.close();
  console.log(`Done! Screenshots in ${OUT}`);
})().catch(err => { console.error(err); process.exit(1); });
