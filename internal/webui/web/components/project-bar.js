// project-bar.js — persistent horizontal bar of projects shown above
// every authenticated page. Each tile shows the project name, three
// status-attention badges (drafts to edit / proposed to review / runs
// awaiting review), and a hover card with the full per-status
// breakdown. Drag tiles to reorder, click the star to pin, click the
// archive button to move a tile into the overflow "..." menu.
//
// Live updates: we open one EventSource per visible project and
// refetch its summary on any project event. Per-project subscriptions
// keep the channel-fan-in simple (the existing /events endpoint is
// per-project).

import { api, get, patch, post, events, auth } from "/api.js";
import { el } from "/router.js";
import { toast } from "/toast.js";
import { t as tr } from "/i18n.js";

const VISIBLE_LIMIT = 6;          // tiles rendered inline; rest go to "..."
const HOVER_OPEN_DELAY_MS = 120;  // tiny delay so chips don't flash on accidental hover

let mountEl = null;
let tiles = [];                   // current tiles, in display order
let sources = new Map();          // projectID -> EventSource (closed on render)

// One open hover card at a time, lives on document.body so it floats
// over scrollable containers. Module-level reference so any code path
// that ought to dismiss it (route change, repaint, click anywhere off
// a tile) can do so without going through the originating closure.
let openCard = null;
let openCardTimer = null;

export function closeHoverCard() {
  if (openCardTimer) { clearTimeout(openCardTimer); openCardTimer = null; }
  if (openCard) { openCard.remove(); openCard = null; }
}

// Wipe any orphaned card whenever the user navigates. Without this,
// hovering a tile then clicking through to a page leaves the card
// floating on document.body forever — mouseleave doesn't fire
// reliably when the click triggers a route change.
window.addEventListener("hashchange", closeHoverCard);
// Same risk on a global click off-tile (e.g. the user clicks a button
// somewhere on the page while a tile card was peeking).
document.addEventListener("click", (e) => {
  if (!openCard) return;
  if (e.target instanceof Element && e.target.closest(".project-bar__tile")) return;
  closeHoverCard();
}, true);

export function ensureMount() {
  if (mountEl) return mountEl;
  // The slot already lives inside #topbar so the bar renders in the
  // same row as the brand logo and the userbox — no DOM creation here,
  // just grab the pre-positioned element.
  mountEl = document.getElementById("project-bar");
  return mountEl;
}

export async function refresh() {
  if (!auth.token) {
    if (mountEl) mountEl.innerHTML = "";
    closeAll();
    return;
  }
  ensureMount();
  try {
    const data = await api("/api/projects/bar");
    tiles = data.projects || [];
    paint();
    resubscribe();
  } catch (err) {
    mountEl.innerHTML = `<div class="project-bar__error">project bar: ${err.message}</div>`;
  }
}

function paint() {
  // Repainting blows away tile DOM nodes — the still-open hover card
  // is on document.body, so close it explicitly or it survives orphaned.
  closeHoverCard();
  mountEl.innerHTML = "";

  // Visible tiles = pinned + non-archived non-pinned, capped to
  // VISIBLE_LIMIT. The rest spill into the overflow menu.
  const pinned = tiles.filter((t) => t.pref.pinned && !t.pref.archived);
  const normal = tiles.filter((t) => !t.pref.pinned && !t.pref.archived);
  const archived = tiles.filter((t) => t.pref.archived);

  const visible = [...pinned, ...normal].slice(0, VISIBLE_LIMIT);
  const overflow = [
    ...[...pinned, ...normal].slice(VISIBLE_LIMIT),
    ...archived,
  ];

  const row = el("div", { class: "project-bar__row" });
  visible.forEach((t) => row.appendChild(renderTile(t, false)));

  if (overflow.length > 0) {
    row.appendChild(renderOverflow(overflow));
  }

  // "+ New project" affordance lives at the right end so the user
  // doesn't have to navigate away to start one.
  const newBtn = el("button", { class: "project-bar__new", title: tr("nav.newProject") }, tr("nav.newProject"));
  newBtn.addEventListener("click", createProject);
  row.appendChild(newBtn);

  mountEl.appendChild(row);
}

function renderTile(t, isOverflowItem) {
  const pid = t.pref.project_id;
  const tile = el("div", {
    class: "project-bar__tile" + (t.pref.pinned ? " pinned" : "") + (t.pref.archived ? " archived" : ""),
    draggable: !isOverflowItem ? "true" : null,
    "data-pid": pid,
  });

  // Header row: name + pin + archive controls.
  const head = el("div", { class: "project-bar__head" }, [
    el("a", { class: "project-bar__name", href: `#/projects/${pid}/specs` }, t.name),
  ]);
  const pinBtn = el("button", {
    class: "project-bar__icon",
    title: t.pref.pinned ? "Unpin" : "Pin to the front of the bar",
  }, t.pref.pinned ? "★" : "☆");
  pinBtn.addEventListener("click", async (e) => {
    e.preventDefault();
    await patch(`/api/projects/${pid}/prefs`, { pinned: !t.pref.pinned });
    t.pref.pinned = !t.pref.pinned;
    sortTiles();
    paint();
    resubscribe();
  });
  const archBtn = el("button", {
    class: "project-bar__icon",
    title: t.pref.archived ? "Unarchive — move back to the visible bar" : "Archive — hide in the overflow menu",
  }, t.pref.archived ? "↩" : "✕");
  archBtn.addEventListener("click", async (e) => {
    e.preventDefault();
    await patch(`/api/projects/${pid}/prefs`, { archived: !t.pref.archived });
    t.pref.archived = !t.pref.archived;
    paint();
    resubscribe();
  });
  head.appendChild(pinBtn);
  head.appendChild(archBtn);
  tile.appendChild(head);

  // Status badges row: small icons with counts that need user attention.
  tile.appendChild(renderBadges(t));

  // Hover card with the full breakdown.
  attachHoverCard(tile, t);

  // Drag-and-drop reorder. Only enabled in the visible row.
  if (!isOverflowItem) attachDragHandlers(tile, pid);

  return tile;
}

// renderBadges decides which counts surface as primary attention
// signals on the tile. The full breakdown is in the hover card; here
// we only show the "user needs to do something" subset.
function renderBadges(t) {
  const s = t.summary || { specs_by_status: {} };
  const counts = s.specs_by_status || {};
  const items = [
    { key: "draft",      label: counts.draft      || 0, title: "Drafts you can still edit", icon: "✎" },
    { key: "proposed",   label: counts.proposed   || 0, title: "Proposed specs awaiting your approval review", icon: "?" },
    { key: "validating", label: s.runs_validating || 0, title: "Runs parked in 'validating' awaiting your verdict", icon: "⚑" },
    { key: "annotation", label: s.open_annotations || 0, title: "Open fix_required annotations the corrector will address", icon: "✏" },
  ];
  const badges = el("div", { class: "project-bar__badges" });
  items.forEach((b) => {
    if (!b.label) return;
    const tag = el("span", { class: "project-bar__badge", title: b.title }, [
      el("span", { class: "project-bar__badge-icon" }, b.icon),
      el("span", {}, String(b.label)),
    ]);
    badges.appendChild(tag);
  });
  if (badges.children.length === 0) {
    badges.appendChild(el("span", { class: "project-bar__badge-empty muted" }, tr("bar.allClear")));
  }
  return badges;
}

// attachHoverCard renders the per-status breakdown as a floating
// card. The card mounts on document.body (not on the tile) and
// positions itself with `position: fixed` from the tile's bounding
// rect — that way it floats over scrollable containers without
// getting clipped, and looks consistent regardless of how the bar is
// scrolled. Delayed open + immediate close so a quick mouseover or
// drag doesn't flash a card.
function attachHoverCard(tile, tile_obj) {
  function build(s) {
    // Compact one-row-per-bucket layout: muted label, then a chain
    // of `count name · count name` pairs. Reads at a glance instead
    // of forcing the eye down a tall list of rows.
    function chain(pairs) {
      const out = [];
      pairs.forEach(([n, name, hint], i) => {
        if (i > 0) out.push(el("span", { class: "project-bar__sep" }, "·"));
        out.push(el("span", {
          class: "project-bar__chain-item" + (n > 0 ? "" : " zero"),
          title: hint,
        }, [
          el("span", { class: "project-bar__chain-num" }, String(n)),
          " ",
          el("span", { class: "project-bar__chain-label" }, name),
        ]));
      });
      return out;
    }

    const specRow = chain([
      [s.specs_by_status?.draft     || 0, tr("bar.draft"),     "Title and intent still mutable"],
      [s.specs_by_status?.proposed  || 0, tr("bar.proposed"),  "Submitted for review; PM hasn't approved"],
      [s.specs_by_status?.approved  || 0, tr("bar.approved"),  "Criteria frozen; ready to run"],
      [s.specs_by_status?.running   || 0, tr("bar.running"),   "A run is in flight"],
      [s.specs_by_status?.validated || 0, tr("bar.validated"), "Run finished; awaiting merge"],
    ]);
    const runRow = chain([
      [s.runs_active     || 0, tr("bar.active"),     "Any non-terminal run"],
      [s.runs_validating || 0, tr("bar.reviewing"),  "Parked in 'validating' awaiting human verdict"],
      [s.runs_correcting || 0, tr("bar.correcting"), "Fix-loop in flight"],
    ]);

    return el("div", { class: "project-bar__hover-card" }, [
      el("div", { class: "project-bar__hover-title" }, tile_obj.name),
      tile_obj.description
        ? el("div", { class: "project-bar__hover-desc muted" }, tile_obj.description)
        : null,
      el("div", { class: "project-bar__hover-row" }, [
        el("span", { class: "project-bar__hover-key" }, tr("bar.specs")),
        el("span", { class: "project-bar__hover-chain" }, specRow),
      ]),
      el("div", { class: "project-bar__hover-row" }, [
        el("span", { class: "project-bar__hover-key" }, tr("bar.runs")),
        el("span", { class: "project-bar__hover-chain" }, runRow),
      ]),
      (s.open_annotations || 0) > 0
        ? el("div", { class: "project-bar__hover-row alert" }, [
            el("span", { class: "project-bar__hover-key" }, tr("bar.alerts")),
            el("span", {}, [
              el("span", { class: "project-bar__chain-num warn" }, String(s.open_annotations)),
              " ", el("span", { class: "project-bar__chain-label" }, tr("bar.openAnnotations")),
            ]),
          ])
        : null,
    ]);
  }

  function position() {
    if (!openCard) return;
    const r = tile.getBoundingClientRect();
    openCard.style.left = Math.max(8, Math.min(r.left, window.innerWidth - 320 - 8)) + "px";
    const cardH = openCard.offsetHeight || 120;
    if (r.bottom + cardH + 12 > window.innerHeight) {
      openCard.style.top = Math.max(8, r.top - cardH - 8) + "px";
    } else {
      openCard.style.top = (r.bottom + 6) + "px";
    }
  }

  function open() {
    closeHoverCard();
    openCard = build(tile_obj.summary || { specs_by_status: {} });
    document.body.appendChild(openCard);
    requestAnimationFrame(() => {
      position();
      if (openCard) openCard.classList.add("show");
    });
    // Auto-close on scroll because a fixed-position card would
    // otherwise drift relative to the (now-moved) tile.
    window.addEventListener("scroll", closeHoverCard, { once: true, capture: true });
  }
  tile.addEventListener("mouseenter", () => {
    if (openCardTimer) clearTimeout(openCardTimer);
    openCardTimer = setTimeout(open, HOVER_OPEN_DELAY_MS);
  });
  tile.addEventListener("mouseleave", closeHoverCard);
  // Click on the tile (any link / button inside) — close immediately
  // so the card doesn't survive a navigation. hashchange covers the
  // route-change path; this covers in-place clicks (pin, archive,
  // drag-end) where the tile stays but the card should still go.
  tile.addEventListener("click", closeHoverCard);
}

// attachDragHandlers wires HTML5 drag-and-drop to reorder tiles within
// the visible row. After a drop we POST the new order back to the
// server so it persists across reloads.
function attachDragHandlers(tile, pid) {
  tile.addEventListener("dragstart", (e) => {
    e.dataTransfer.setData("text/plain", pid);
    e.dataTransfer.effectAllowed = "move";
    tile.classList.add("dragging");
  });
  tile.addEventListener("dragend", () => tile.classList.remove("dragging"));
  tile.addEventListener("dragover", (e) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    tile.classList.add("drop-target");
  });
  tile.addEventListener("dragleave", () => tile.classList.remove("drop-target"));
  tile.addEventListener("drop", async (e) => {
    e.preventDefault();
    tile.classList.remove("drop-target");
    const draggedID = e.dataTransfer.getData("text/plain");
    if (!draggedID || draggedID === pid) return;
    reorderTiles(draggedID, pid);
    paint();
    // Persist: send the post-drag visible order to the server.
    const visible = tiles.filter((t) => !t.pref.archived).map((t) => t.pref.project_id);
    try {
      await post("/api/projects/reorder", { project_ids: visible });
    } catch (err) {
      toast("reorder failed: " + err.message, "error");
      refresh();
    }
  });
}

function reorderTiles(draggedID, targetID) {
  const from = tiles.findIndex((t) => t.pref.project_id === draggedID);
  const to = tiles.findIndex((t) => t.pref.project_id === targetID);
  if (from < 0 || to < 0) return;
  const [moved] = tiles.splice(from, 1);
  tiles.splice(to, 0, moved);
  // Renumber positions so the next paint sorts correctly.
  tiles.forEach((t, i) => { t.pref.position = i; });
  sortTiles();
}

function sortTiles() {
  tiles.sort((a, b) => {
    if (a.pref.pinned !== b.pref.pinned) return b.pref.pinned - a.pref.pinned;
    return a.pref.position - b.pref.position;
  });
}

function renderOverflow(items) {
  const wrap = el("div", { class: "project-bar__overflow" });
  const btn = el("button", { class: "project-bar__overflow-btn", title: "More projects (archived + extras)" }, "...");
  const menu = el("div", { class: "project-bar__overflow-menu" });
  items.forEach((t) => {
    const row = el("div", { class: "project-bar__overflow-row" + (t.pref.archived ? " archived" : "") }, [
      el("a", { href: `#/projects/${t.pref.project_id}/specs` }, t.name),
      el("span", { class: "muted" }, t.pref.archived ? "archived" : ""),
    ]);
    menu.appendChild(row);
  });
  btn.addEventListener("click", () => {
    menu.classList.toggle("open");
  });
  document.addEventListener("click", (e) => {
    if (!wrap.contains(e.target)) menu.classList.remove("open");
  });
  wrap.appendChild(btn);
  wrap.appendChild(menu);
  return wrap;
}

async function createProject() {
  const name = prompt("Project name");
  if (!name) return;
  try {
    await api("/api/projects", { method: "POST", body: { name, description: "" } });
    refresh();
  } catch (err) {
    toast(err.message, "error");
  }
}

// resubscribe opens one EventSource per visible project so each tile
// can refresh its summary independently when its project's events
// fire. Closes any leftover sources from the previous render.
function resubscribe() {
  closeAll();
  tiles.forEach((t) => {
    const pid = t.pref.project_id;
    const src = events(pid);
    sources.set(pid, src);
    const refresh = async () => {
      try {
        const sum = await get(`/api/projects/${pid}/summary`);
        t.summary = sum;
        // Repaint just this tile to avoid flicker on the rest of the bar.
        const old = mountEl.querySelector(`[data-pid="${pid}"]`);
        if (old) {
          const fresh = renderTile(t, false);
          old.replaceWith(fresh);
        }
      } catch (_) { /* ignore — next event will retry */ }
    };
    ["spec.created", "spec.updated", "spec.criteria_changed",
      "run.queued", "run.advanced", "verification.posted",
      "annotation.added"].forEach((topic) => {
      src.addEventListener(topic, refresh);
    });
  });
}

function closeAll() {
  for (const s of sources.values()) {
    try { s.close(); } catch (_) {}
  }
  sources.clear();
}
