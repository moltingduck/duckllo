const puppeteer = require('puppeteer');
const path = require('path');

(async () => {
  const browser = await puppeteer.launch({
    headless: 'new',
    args: ['--no-sandbox', '--disable-setuid-sandbox', `--user-data-dir=/tmp/archive-demo-${Date.now()}`]
  });
  const page = await browser.newPage();
  await page.setViewport({ width: 1280, height: 800 });

  // Login as gin
  await page.goto('http://localhost:3000', { waitUntil: 'networkidle2' });
  await page.evaluate(async () => {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'gin', password: 'gin123' })
    });
    const data = await res.json();
    if (data.token) {
      localStorage.setItem('duckllo_token', data.token);
      location.reload();
    }
  });
  await page.waitForNavigation({ waitUntil: 'networkidle2' });
  await new Promise(r => setTimeout(r, 1000));

  // Switch to Duckllo Development project
  await page.evaluate(() => {
    const select = document.getElementById('project-select');
    if (select) {
      const opt = Array.from(select.options).find(o => o.textContent.includes('Duckllo Development'));
      if (opt) { select.value = opt.value; select.dispatchEvent(new Event('change')); }
    }
  });
  await new Promise(r => setTimeout(r, 1500));

  // Screenshot showing Done column with archive buttons (hover over a card)
  await page.screenshot({ path: 'docs/gifs/archive-board.png' });
  console.log('Saved archive-board.png');

  // Archive a card via API to populate the archived list
  const token = await page.evaluate(() => localStorage.getItem('duckllo_token'));
  const pid = await page.evaluate(() => {
    const select = document.getElementById('project-select');
    return select ? select.value : null;
  });
  
  // Get a Done card
  const cards = await page.evaluate(async (pid, token) => {
    const res = await fetch(`/api/projects/${pid}/cards?column=Done`, {
      headers: { 'Authorization': `Bearer ${token}` }
    });
    return res.json();
  }, pid, token);

  if (cards.length > 0) {
    // Archive first 2 cards
    for (let i = 0; i < Math.min(2, cards.length); i++) {
      await page.evaluate(async (pid, cid, token) => {
        await fetch(`/api/projects/${pid}/cards/${cid}/archive`, {
          method: 'POST', headers: { 'Authorization': `Bearer ${token}` }
        });
      }, pid, cards[i].id, token);
    }
    await new Promise(r => setTimeout(r, 500));
  }

  // Open archived panel
  await page.evaluate(() => document.getElementById('archived-toggle-btn')?.click());
  await new Promise(r => setTimeout(r, 1000));
  await page.screenshot({ path: 'docs/gifs/archive-panel.png' });
  console.log('Saved archive-panel.png');

  // Unarchive the cards back
  for (let i = 0; i < Math.min(2, cards.length); i++) {
    await page.evaluate(async (pid, cid, token) => {
      await fetch(`/api/projects/${pid}/cards/${cid}/unarchive`, {
        method: 'POST', headers: { 'Authorization': `Bearer ${token}` }
      });
    }, pid, cards[i].id, token);
  }

  await browser.close();
  console.log('Done');
})();
