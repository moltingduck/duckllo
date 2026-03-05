#!/usr/bin/env node

/**
 * Duckllo Kanban Watcher
 *
 * Polls the activity feed and prints new events. Agents can pipe this
 * into their workflow or run it as a sidecar process.
 *
 * Usage:
 *   node watch.js                                  # Uses env vars
 *   DUCKLLO_KEY=duckllo_xxx DUCKLLO_PROJECT=pid node watch.js
 *   node watch.js --key duckllo_xxx --project pid
 *   node watch.js --key duckllo_xxx --project pid --interval 30
 *   node watch.js --key duckllo_xxx --project pid --json
 *   node watch.js --key duckllo_xxx --project pid --once
 *
 * Options:
 *   --key, -k        API key or session token (or DUCKLLO_KEY env var)
 *   --project, -p    Project ID (or DUCKLLO_PROJECT env var)
 *   --url, -u        Base URL (default: http://localhost:3000, or DUCKLLO_URL)
 *   --interval, -i   Poll interval in seconds (default: 15)
 *   --json, -j       Output raw JSON instead of formatted text
 *   --once, -1       Fetch once and exit (no polling)
 *   --since, -s      ISO timestamp to start from (default: now)
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
const INTERVAL = parseInt(getArg('interval', 'i', 'DUCKLLO_INTERVAL', '15'));
const JSON_OUTPUT = hasFlag('json', 'j');
const ONCE = hasFlag('once', '1');

if (!KEY || !PROJECT) {
  console.error('Error: --key and --project are required (or set DUCKLLO_KEY and DUCKLLO_PROJECT)');
  console.error('');
  console.error('Usage: node watch.js --key duckllo_xxx --project <project-id>');
  console.error('');
  console.error('To find your project ID:');
  console.error('  curl -H "Authorization: Bearer <key>" http://localhost:3000/api/projects');
  process.exit(1);
}

// Normalize to SQLite format "YYYY-MM-DD HH:MM:SS"
function toSqlite(isoStr) {
  return isoStr.replace('T', ' ').replace('Z', '').replace(/\.\d+$/, '');
}

let lastCheck = toSqlite(getArg('since', 's', '', new Date().toISOString()));

async function fetchActivity() {
  try {
    const url = `${BASE_URL}/api/projects/${PROJECT}/activity?since=${encodeURIComponent(lastCheck)}&limit=50`;
    const res = await fetch(url, {
      headers: { 'Authorization': `Bearer ${KEY}` }
    });

    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      console.error(`[watch] API error ${res.status}: ${err.error || 'unknown'}`);
      return;
    }

    const data = await res.json();

    if (data.events.length === 0) return;

    if (JSON_OUTPUT) {
      for (const ev of data.events.reverse()) {
        console.log(JSON.stringify(ev));
      }
    } else {
      for (const ev of data.events.reverse()) {
        const time = new Date(ev.timestamp).toLocaleTimeString();

        if (ev.event_type === 'card_updated') {
          const status = ev.testing_status !== 'untested' ? ` [${ev.testing_status}]` : '';
          console.log(`[${time}] CARD ${ev.card_type.toUpperCase()} in ${ev.column_name}: ${ev.title}${status}`);
        } else if (ev.event_type === 'comment_added') {
          const author = ev.display_name || ev.username || 'unknown';
          const preview = ev.content.length > 120 ? ev.content.substring(0, 120) + '...' : ev.content;
          console.log(`[${time}] COMMENT by ${author} on "${ev.card_title}": ${preview.replace(/\n/g, ' ')}`);
        }
      }
    }

    // Move cursor forward to latest event
    const latest = data.events[0]?.timestamp;
    if (latest && latest > lastCheck) {
      lastCheck = latest;
    }
  } catch (err) {
    console.error(`[watch] ${err.message}`);
  }
}

async function main() {
  if (!ONCE) {
    console.error(`[watch] Watching project ${PROJECT.substring(0, 8)}... every ${INTERVAL}s`);
    console.error(`[watch] Press Ctrl+C to stop\n`);
  }

  // Always fetch once immediately
  await fetchActivity();

  if (ONCE) {
    process.exit(0);
  }

  // Poll
  setInterval(fetchActivity, INTERVAL * 1000);
}

main();
