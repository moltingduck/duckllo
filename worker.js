#!/usr/bin/env node

/**
 * Duckllo Agent Worker
 *
 * Polls the Todo column for unassigned cards and picks them up.
 * Designed to be run as a background process alongside Claude Code.
 *
 * Usage:
 *   node worker.js --key duckllo_xxx --project pid
 *   node worker.js --key duckllo_xxx --project pid --interval 60
 *   node worker.js --key duckllo_xxx --project pid --once
 *   node worker.js --key duckllo_xxx --project pid --dry-run
 *
 * Options:
 *   --key, -k        API key (or DUCKLLO_KEY env var)
 *   --project, -p    Project ID (or DUCKLLO_PROJECT env var)
 *   --url, -u        Base URL (default: http://localhost:3000, or DUCKLLO_URL)
 *   --interval, -i   Poll interval in seconds (default: 60)
 *   --once, -1       Check once and exit
 *   --dry-run, -d    Show what would be picked up without claiming
 *
 * The worker picks up ONE card at a time from Todo (unassigned).
 * After pickup, it prints the card details to stdout as JSON so the
 * calling agent can read and implement it.
 *
 * Integration with Claude Code:
 *   In your CLAUDE.md or skill, instruct the agent to:
 *   1. Run: node worker.js --key $KEY --project $PID --once
 *   2. If a card is returned, implement the feature described in the card
 *   3. Update the card with test results and demo GIF
 *   4. Move the card to Review
 *   5. Repeat
 */

const args = process.argv.slice(2);

function getArg(long, short, envVar, fallback) {
  for (let i = 0; i < args.length; i++) {
    if (args[i] === `--${long}` || args[i] === `-${short}`) return args[i + 1];
  }
  return process.env[envVar] || fallback;
}

function hasFlag(long, short) {
  return args.includes(`--${long}`) || args.includes(`-${short}`);
}

const KEY = getArg('key', 'k', 'DUCKLLO_KEY', '');
const PROJECT = getArg('project', 'p', 'DUCKLLO_PROJECT', '');
const BASE_URL = getArg('url', 'u', 'DUCKLLO_URL', 'http://localhost:3000');
const INTERVAL = parseInt(getArg('interval', 'i', 'DUCKLLO_INTERVAL', '60'));
const ONCE = hasFlag('once', '1');
const DRY_RUN = hasFlag('dry-run', 'd');

if (!KEY || !PROJECT) {
  console.error('Error: --key and --project are required (or set DUCKLLO_KEY and DUCKLLO_PROJECT)');
  console.error('');
  console.error('Usage: node worker.js --key duckllo_xxx --project <project-id>');
  process.exit(1);
}

async function apiCall(path, opts = {}) {
  const headers = { 'Authorization': `Bearer ${KEY}` };
  if (opts.body) headers['Content-Type'] = 'application/json';

  const res = await fetch(`${BASE_URL}/api${path}`, {
    method: opts.method || 'GET',
    headers,
    body: opts.body ? JSON.stringify(opts.body) : undefined
  });

  const data = await res.json();
  if (!res.ok) {
    const err = new Error(data.error || `HTTP ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return data;
}

async function getAvailableCards() {
  return apiCall(`/projects/${PROJECT}/cards?column=Todo&unassigned=true`);
}

async function pickupCard(cardId) {
  return apiCall(`/projects/${PROJECT}/cards/${cardId}/pickup`, { method: 'POST' });
}

async function checkAndPickup() {
  try {
    const cards = await getAvailableCards();

    // Filter out pinned/rules cards
    const available = cards.filter(c =>
      !(c.labels || []).some(l => l === 'pinned' || l === 'rules')
    );

    if (available.length === 0) {
      if (!ONCE) console.error(`[worker] No cards available in Todo. Waiting...`);
      return null;
    }

    // Pick the highest priority card (critical > high > medium > low)
    const priorityOrder = { critical: 0, high: 1, medium: 2, low: 3 };
    available.sort((a, b) => (priorityOrder[a.priority] || 2) - (priorityOrder[b.priority] || 2));

    const card = available[0];

    if (DRY_RUN) {
      console.error(`[worker] Would pick up: "${card.title}" (${card.priority}, ${card.card_type})`);
      console.log(JSON.stringify(card, null, 2));
      return card;
    }

    console.error(`[worker] Picking up: "${card.title}" (${card.priority}, ${card.card_type})`);

    const picked = await pickupCard(card.id);
    console.error(`[worker] Claimed card ${picked.id.substring(0, 8)}... -> In Progress`);

    // Output card as JSON for the calling agent to consume
    console.log(JSON.stringify(picked, null, 2));
    return picked;

  } catch (err) {
    if (err.status === 409) {
      console.error(`[worker] Card was claimed by another agent, retrying...`);
    } else {
      console.error(`[worker] Error: ${err.message}`);
    }
    return null;
  }
}

async function main() {
  if (ONCE) {
    await checkAndPickup();
    process.exit(0);
  }

  console.error(`[worker] Watching Todo in project ${PROJECT.substring(0, 8)}... every ${INTERVAL}s`);
  console.error(`[worker] Press Ctrl+C to stop\n`);

  // Check immediately
  const card = await checkAndPickup();
  if (card) {
    console.error(`[worker] Card picked up. Waiting for next available card...`);
  }

  // Poll
  setInterval(async () => {
    const card = await checkAndPickup();
    if (card) {
      console.error(`[worker] Card picked up. Waiting for next available card...`);
    }
  }, INTERVAL * 1000);
}

main();
