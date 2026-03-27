#!/usr/bin/env node
const puppeteer = require('puppeteer');
const path = require('path');
const fs = require('fs');
const BASE = 'http://localhost:3000';
const OUT = path.join(__dirname, '..', 'docs', 'gifs');
if (!fs.existsSync(OUT)) fs.mkdirSync(OUT, { recursive: true });

(async () => {
  const browser = await puppeteer.launch({
    executablePath: '/usr/bin/chromium', headless: 'new',
    args: ['--no-sandbox', '--disable-setuid-sandbox', '--user-data-dir=/tmp/puppeteer-timetrack-' + Date.now()]
  });
  const page = await browser.newPage();
  await page.setViewport({ width: 1280, height: 800 });

  await page.goto(BASE);
  await page.waitForSelector('#login-form');
  await page.type('#login-username', 'gin');
  await page.type('#login-password', 'gin123');
  await page.click('#login-form button[type="submit"]');
  await page.waitForSelector('#main-screen', { visible: true });
  await new Promise(r => setTimeout(r, 1500));

  // Switch to Duckllo Development
  await page.click('#project-dropdown-btn');
  await new Promise(r => setTimeout(r, 500));
  const opts = await page.$$('.project-option');
  for (const opt of opts) {
    const text = await opt.evaluate(el => el.textContent);
    if (text.includes('Duckllo Development')) { await opt.click(); break; }
  }
  await new Promise(r => setTimeout(r, 2000));

  // Use page.evaluate to find and click the card
  const clicked = await page.evaluate(() => {
    const cards = document.querySelectorAll('.card');
    for (const card of cards) {
      const title = card.querySelector('.card-title');
      if (title && title.textContent.toLowerCase().includes('time tracking')) {
        card.click();
        return title.textContent;
      }
    }
    // Fallback: click first card
    if (cards.length > 0) {
      cards[0].click();
      const t = cards[0].querySelector('.card-title');
      return t ? t.textContent : 'first card';
    }
    return null;
  });
  console.log('Clicked:', clicked);

  await new Promise(r => setTimeout(r, 2000));
  await page.screenshot({ path: path.join(OUT, 'time-tracking-detail.png') });
  console.log('Captured: time tracking card detail');

  await browser.close();
  console.log('Done!');
})().catch(err => { console.error(err); process.exit(1); });
