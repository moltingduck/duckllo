const puppeteer = require('puppeteer');
const http = require('http');
const https = require('https');
const fs = require('fs');
const path = require('path');

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
  // Login to get token
  const { token } = await login('gin', 'gin123');

  const browser = await puppeteer.launch({
    headless: 'new',
    args: ['--no-sandbox', '--disable-setuid-sandbox']
  });
  const page = await browser.newPage();
  await page.setViewport({ width: 1400, height: 900 });

  // Set auth and project before navigating
  await page.evaluateOnNewDocument((t, pid) => {
    localStorage.setItem('duckllo_token', t);
    localStorage.setItem('duckllo_project', pid);
  }, token, PID);

  await page.goto(BASE, { waitUntil: 'networkidle2' });
  await new Promise(r => setTimeout(r, 2000));

  // Screenshot 1: Board with blocker/link icons
  await page.screenshot({ path: 'docs/gifs/dependency-board.png', fullPage: false });
  console.log('Saved: docs/gifs/dependency-board.png');

  // Debug: list all cards found in DOM
  const allTitles = await page.evaluate(() => {
    return [...document.querySelectorAll('.card .card-title')].map(t => t.textContent);
  });
  console.log('Cards in DOM:', allTitles.length, allTitles.slice(0, 5));

  const clicked = await page.evaluate(() => {
    const cards = document.querySelectorAll('.card');
    for (const card of cards) {
      const title = card.querySelector('.card-title');
      if (title && title.textContent.includes('dependency links')) {
        card.click();
        return title.textContent;
      }
    }
    return false;
  });

  if (!clicked) {
    console.log('Could not find Card dependency links card');
    await browser.close();
    return;
  }

  await new Promise(r => setTimeout(r, 1500));

  // Scroll modal to show dependencies section
  await page.evaluate(() => {
    const modal = document.querySelector('.card-detail-main');
    if (modal) modal.scrollTop = modal.scrollHeight / 2;
  });
  await new Promise(r => setTimeout(r, 500));

  // Screenshot 2: Card detail with dependencies
  await page.screenshot({ path: 'docs/gifs/dependency-detail.png', fullPage: false });
  console.log('Saved: docs/gifs/dependency-detail.png');

  await browser.close();
  console.log('Done!');
})();
