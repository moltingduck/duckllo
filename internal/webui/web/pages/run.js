// pages/run.js — run dashboard. Redesigned around three signals:
//
//  1. Hero status banner — answers "what's the state of this run?"
//  2. What's-left chip row — answers "where is it stuck?"
//  3. Tabbed body (Sensors | Iterations | Workspace) — scoped views
//     instead of one long vertical scroll.
//
// Sensors tab groups verifications by criterion (latest first); image
// artifacts render full-width inside their card and clicking opens a
// fullscreen modal so visual evidence is actually readable. Iterations
// tab is a condensed timeline you can scan; click a row to expand its
// transcript inline. Workspace tab only renders when there's actual
// docker / tailscale metadata to show.

import { api, events } from "/api.js";
import { go, el, escapeHTML } from "/router.js";
import { toast } from "/toast.js";
import { openAnnotator } from "/components/annotator.js";
import { t } from "/i18n.js";

let currentSource = null;
let currentTab = "sensors"; // persists across SSE-driven refreshes

export async function render(mount, params) {
  if (currentSource) { currentSource.close(); currentSource = null; }
  const { pid, rid } = params;
  await refresh(mount, pid, rid);
  currentSource = events(pid);
  ["iteration.appended", "iteration.updated", "verification.posted",
    "verification.updated", "annotation.added", "run.advanced",
    "run.workspace_set"].forEach((topic) => {
    currentSource.addEventListener(topic, () => refresh(mount, pid, rid));
  });
}

async function refresh(mount, pid, rid) {
  const [data, verifications] = await Promise.all([
    api(`/api/projects/${pid}/runs/${rid}`),
    api(`/api/projects/${pid}/runs/${rid}/verifications`),
  ]);
  const run = data.run;
  const iterations = data.iterations || [];

  // Spec lookup so we can name the criteria the verifications target
  // and offer "set as baseline" in the annotator.
  let spec = null;
  let critByID = {};
  try {
    spec = (await api(`/api/projects/${pid}/specs/${run.spec_id}`)).spec;
    for (const c of spec.acceptance_criteria || []) critByID[c.id] = c;
  } catch (_) { /* tolerable */ }

  const wm = run.workspace_meta || {};
  const hasWorkspace = Object.keys(wm).filter(k => wm[k]).length > 0;

  // Group verifications by criterion (latest first per criterion). The
  // sensor view shows the freshest result up top with prior attempts
  // tucked behind an expander — so a passing run after a failing one
  // *looks* passing instead of being buried under history.
  const byCrit = bucketByCriterion(verifications);
  const ambient = byCrit.get("") || []; // workspace_changes etc.
  byCrit.delete("");

  // Per-criterion verdict for the chip row + criteria summary.
  const verdicts = computeVerdicts(spec, byCrit);

  mount.innerHTML = "";
  mount.appendChild(renderHero(run, verdicts, pid, rid, mount));
  mount.appendChild(renderChips(verdicts));

  const tabRow = el("div", { class: "run-tabs" });
  const tabs = [
    { key: "sensors",     label: "Sensors" },
    { key: "iterations",  label: "Iterations · " + iterations.length },
  ];
  if (hasWorkspace) tabs.push({ key: "workspace", label: "Workspace" });

  const body = el("div", { class: "run-tab-body" });
  function setTab(k) {
    currentTab = k;
    tabRow.querySelectorAll(".run-tab").forEach((b) =>
      b.classList.toggle("active", b.dataset.tab === k));
    body.innerHTML = "";
    if (k === "sensors") body.appendChild(renderSensors(spec, critByID, byCrit, ambient, pid));
    else if (k === "iterations") body.appendChild(renderIterations(iterations));
    else if (k === "workspace") body.appendChild(renderWorkspace(wm));
  }
  for (const t of tabs) {
    const btn = el("button", { class: "run-tab", "data-tab": t.key }, t.label);
    btn.addEventListener("click", () => setTab(t.key));
    tabRow.appendChild(btn);
  }
  mount.appendChild(tabRow);
  mount.appendChild(body);

  // Restore the user's previous tab choice across SSE-triggered redraws.
  if (!tabs.find(x => x.key === currentTab)) currentTab = "sensors";
  setTab(currentTab);
}

// ─── Hero status banner ─────────────────────────────────────────────

function renderHero(run, verdicts, pid, rid, mount) {
  const head = el("div", { class: "run-hero" });

  const left = el("div", { class: "run-hero__left" }, [
    el("h1", {}, "Run " + run.id.slice(0, 8)),
    el("div", { class: "run-hero__meta muted mono" },
      `${run.turns_used}/${run.turn_budget} turns · ${run.token_usage} tokens`),
  ]);

  const status = el("span", { class: "pill " + statusPillClass(run.status) }, run.status);
  const summary = el("div", { class: "run-hero__summary" }, [
    el("span", { class: "run-hero__summary-num " + (verdicts.allPass ? "ok" : "") },
      `${verdicts.passed} / ${verdicts.total}`),
    " ",
    el("span", { class: "muted" }, "criteria pass"),
  ]);

  const actions = el("div", { class: "run-hero__actions" });
  if (!["done", "failed", "aborted"].includes(run.status)) {
    const abort = el("button", { class: "danger" }, t("run.btn.abort"));
    abort.addEventListener("click", async () => {
      try { await api(`/api/projects/${pid}/runs/${rid}/abort`, { method: "POST" });
        toast("Run aborted"); refresh(mount, pid, rid);
      } catch (err) { toast(err.message, "error"); }
    });
    actions.appendChild(abort);
  }
  if (run.status === "validating" || run.status === "correcting") {
    const complete = el("button", {}, t("run.btn.complete"));
    complete.addEventListener("click", async () => {
      if (!confirm("Force this run to 'done' and mark the spec validated?")) return;
      try {
        await api(`/api/projects/${pid}/runs/${rid}/complete`, { method: "POST" });
        toast("Run marked complete");
        refresh(mount, pid, rid);
      } catch (err) { toast(err.message, "error"); }
    });
    actions.appendChild(complete);
  }
  const previewBtn = el("button", { class: "secondary", title: t("run.btn.previewHelp") },
    t("run.btn.preview"));
  previewBtn.addEventListener("click", () => go(`/projects/${pid}/runs/${rid}/preview`));
  actions.appendChild(previewBtn);
  const back = el("button", { class: "secondary" }, t("run.btn.backToSpec"));
  back.addEventListener("click", () => go(`/projects/${pid}/specs/${run.spec_id}`));
  actions.appendChild(back);

  head.appendChild(left);
  head.appendChild(el("div", { class: "run-hero__center" }, [status, summary]));
  head.appendChild(actions);
  return head;
}

// ─── What's-left chip row ───────────────────────────────────────────

function renderChips(verdicts) {
  const row = el("div", { class: "run-chips" });
  for (const c of verdicts.list) {
    const chip = el("span", {
      class: "run-chip " + chipStatusClass(c.status),
      title: c.text + (c.summary ? " — " + c.summary : ""),
    }, [
      el("span", { class: "run-chip__icon" }, chipIcon(c.status)),
      " ",
      el("span", { class: "run-chip__kind mono" }, c.sensor_kind),
      " ",
      el("span", { class: "run-chip__text" }, truncate(c.text, 42)),
    ]);
    row.appendChild(chip);
  }
  if (verdicts.list.length === 0) {
    row.appendChild(el("span", { class: "muted" }, "no acceptance criteria — add some on the spec page"));
  }
  return row;
}

function chipStatusClass(s) {
  return ({ pass: "pass", fail: "fail", warn: "warn", pending: "pending" }[s]) || "pending";
}

function chipIcon(s) {
  return ({ pass: "✓", fail: "✕", warn: "⚠", pending: "…" }[s]) || "…";
}

// ─── Sensors tab ────────────────────────────────────────────────────

function renderSensors(spec, critByID, byCrit, ambient, pid) {
  const wrap = el("div", { class: "run-sensors" });

  // Ambient verifications (no criterion_id) — workspace_changes, etc.
  // Pinned at the top because the diff is the ground truth the
  // validator's verdicts hang off of.
  for (const v of ambient) {
    wrap.appendChild(renderAmbientCard(v, pid));
  }

  // One card per criterion, ordered by spec definition so the user
  // sees them in the same order they composed.
  const orderedCriteria = (spec?.acceptance_criteria || [])
    .map((c) => ({ crit: c, history: byCrit.get(c.id) || [] }));
  // Append any orphan criteria buckets (criterion deleted from spec
  // after run started) so we don't lose history.
  for (const [cid, history] of byCrit.entries()) {
    if (!critByID[cid]) {
      orderedCriteria.push({ crit: { id: cid, text: "(deleted criterion)", sensor_kind: history[0]?.kind || "?" }, history });
    }
  }

  for (const { crit, history } of orderedCriteria) {
    wrap.appendChild(renderCriterionCard(crit, history, spec, pid));
  }

  if (orderedCriteria.length === 0 && ambient.length === 0) {
    wrap.appendChild(el("p", { class: "empty" },
      "No verifications posted yet. Sensors fire during the validate phase."));
  }
  return wrap;
}

function renderAmbientCard(v, pid) {
  const card = el("div", { class: "criterion-card ambient" }, [
    el("div", { class: "criterion-card__head" }, [
      el("span", { class: "pill " + statusPillClass(v.status) }, v.status),
      el("span", { class: "criterion-card__title" }, kindLabel(v.kind)),
      el("span", { class: "spacer" }),
      el("span", { class: "muted mono", style: "font-size:11px" }, timeAgo(v.created_at)),
    ]),
    el("p", { class: "criterion-card__summary" },
      v.summary || el("span", { class: "muted" }, "(no summary)")),
  ]);
  if (v.kind === "workspace_changes" && v.details_json && v.details_json.diff) {
    const det = el("details", { class: "criterion-card__diff" });
    det.appendChild(el("summary", {}, "View diff"));
    const pre = el("pre", { class: "mono" });
    pre.textContent = v.details_json.diff;
    det.appendChild(pre);
    card.appendChild(det);
  }
  return card;
}

function renderCriterionCard(crit, history, spec, pid) {
  const latest = history[0]; // already sorted desc
  const status = latest?.status || "pending";
  const card = el("div", { class: "criterion-card " + statusBorderClass(status) });

  card.appendChild(el("div", { class: "criterion-card__head" }, [
    el("span", { class: "pill " + statusPillClass(status) }, status),
    el("span", { class: "criterion-card__title" }, crit.text),
    el("span", { class: "spacer" }),
    el("span", { class: "pill mono criterion-card__kind" }, crit.sensor_kind),
  ]));

  if (!latest) {
    card.appendChild(el("p", { class: "muted criterion-card__pending" },
      "Waiting for sensor — fires during the validate phase."));
    return card;
  }

  if (latest.summary) {
    card.appendChild(el("p", { class: "criterion-card__summary" }, latest.summary));
  }

  // Image artifacts go FULL WIDTH inside the card so the visual evidence
  // isn't a thumbnail you have to squint at.
  const isImage = latest.artifact_url &&
    (latest.kind === "screenshot" || latest.kind === "visual_diff" || latest.kind === "gif");
  if (isImage) {
    const img = el("img", {
      src: latest.artifact_url, alt: latest.kind,
      class: "criterion-card__artifact",
      loading: "lazy",
    });
    img.addEventListener("click", () => openArtifactModal(latest, pid, spec, crit));
    card.appendChild(img);
    card.appendChild(el("div", { class: "criterion-card__artifact-foot muted" },
      "Click to view fullscreen / annotate"));
  }

  // Prior attempts — collapsed so the latest pass isn't crowded by
  // older fails the user already addressed.
  if (history.length > 1) {
    const det = el("details", { class: "criterion-card__history" });
    det.appendChild(el("summary", {}, `View ${history.length - 1} prior attempt${history.length === 2 ? "" : "s"}`));
    for (const v of history.slice(1)) {
      det.appendChild(renderHistoryRow(v, pid, spec, crit));
    }
    card.appendChild(det);
  }
  return card;
}

function renderHistoryRow(v, pid, spec, crit) {
  const row = el("div", { class: "criterion-card__history-row" }, [
    el("span", { class: "pill " + statusPillClass(v.status) }, v.status),
    el("span", { class: "muted mono", style: "font-size:11px" }, timeAgo(v.created_at)),
    el("span", { style: "flex:1" }, v.summary || el("span", { class: "muted" }, "(no summary)")),
  ]);
  if (v.artifact_url && (v.kind === "screenshot" || v.kind === "visual_diff" || v.kind === "gif")) {
    const link = el("a", { href: "#" }, "view");
    link.addEventListener("click", (e) => { e.preventDefault(); openArtifactModal(v, pid, spec, crit); });
    row.appendChild(link);
  }
  return row;
}

// ─── Iterations tab ─────────────────────────────────────────────────

function renderIterations(iterations) {
  const wrap = el("div", { class: "run-iterations" });
  if (iterations.length === 0) {
    wrap.appendChild(el("p", { class: "empty" },
      "No iterations yet. Once a runner claims this run, you'll see the planner output here first."));
    return wrap;
  }
  for (const it of iterations) {
    wrap.appendChild(renderIterationLine(it));
  }
  return wrap;
}

function renderIterationLine(it) {
  const line = el("div", { class: "iter-line " + it.phase });
  const head = el("div", { class: "iter-line__head" }, [
    el("span", { class: "iter-line__idx" }, "#" + it.idx),
    el("span", { class: "iter-line__phase pill mono" }, it.phase),
    el("span", { class: "iter-line__role muted" }, it.agent_role),
    el("span", { class: "iter-line__time muted mono" },
      it.started_at ? new Date(it.started_at).toLocaleTimeString() : ""),
    el("span", { class: "iter-line__summary" },
      truncate(it.summary || "(no summary)", 160)),
  ]);
  line.appendChild(head);
  // Click row → expand transcript inline (only if there is one).
  if (it.transcript) {
    const det = el("details", { class: "iter-line__det" });
    const sm = el("summary", {}, "View transcript · " +
      `in=${it.prompt_tokens} out=${it.completion_tokens} · ${it.provider}/${it.model || "?"}`);
    det.appendChild(sm);
    const pre = el("pre", { class: "mono" });
    pre.textContent = it.transcript;
    det.appendChild(pre);
    line.appendChild(det);
  }
  return line;
}

// ─── Workspace tab ──────────────────────────────────────────────────

function renderWorkspace(wm) {
  const wrap = el("div", { class: "run-workspace card" });
  const tbl = el("table", { class: "mono" });
  for (const k of Object.keys(wm)) {
    if (!wm[k]) continue;
    const tr = el("tr");
    tr.appendChild(el("td", { class: "muted" }, k));
    const v = String(wm[k]);
    if (k === "dev_url" && /^https?:\/\//.test(v)) {
      tr.appendChild(el("td", {}, [el("a", { href: v, target: "_blank", rel: "noopener" }, v)]));
    } else {
      tr.appendChild(el("td", {}, v));
    }
    tbl.appendChild(tr);
  }
  wrap.appendChild(tbl);
  return wrap;
}

// ─── Artifact modal ─────────────────────────────────────────────────

function openArtifactModal(verification, pid, spec, criterion) {
  // Click the artifact, get a real lightbox: blurred backdrop, image
  // sized to fit, prominent close button. For screenshot / visual_diff,
  // the existing annotator renders below the image with its annotation
  // tools — same UX as before, just bigger.
  const overlay = el("div", { class: "artifact-modal" });
  const close = () => overlay.remove();
  overlay.addEventListener("click", (e) => { if (e.target === overlay) close(); });

  const closeBtn = el("button", { class: "artifact-modal__close", title: "Close (Esc)" }, "×");
  closeBtn.addEventListener("click", close);

  const inner = el("div", { class: "artifact-modal__inner" });
  inner.appendChild(closeBtn);
  inner.appendChild(el("div", { class: "artifact-modal__title" }, [
    el("span", { class: "pill " + statusPillClass(verification.status) }, verification.status),
    el("span", { class: "mono" }, " " + verification.kind),
    el("span", { class: "muted" }, "  —  " + (verification.summary || "")),
  ]));
  inner.appendChild(el("img", {
    src: verification.artifact_url, class: "artifact-modal__img", alt: verification.kind,
  }));

  // For screenshots / visual_diff specifically, hand off to the
  // existing annotator inside the modal so a human can mark-up the
  // image without leaving the dashboard.
  if (verification.kind === "screenshot" || verification.kind === "visual_diff") {
    const annotateBtn = el("button", {}, "Open annotator");
    annotateBtn.addEventListener("click", () => {
      close();
      const ctx = spec ? { specID: spec.id, criterion } : {};
      openAnnotator(pid, verification, ctx);
    });
    inner.appendChild(el("div", { style: "margin-top:10px" }, [annotateBtn]));
  }

  overlay.appendChild(inner);
  document.body.appendChild(overlay);
  const onKey = (e) => { if (e.key === "Escape") { close(); document.removeEventListener("keydown", onKey); } };
  document.addEventListener("keydown", onKey);
}

// ─── Helpers ────────────────────────────────────────────────────────

function bucketByCriterion(verifs) {
  // verifs already arrive newest-first from the API. Group by
  // criterion_id; preserve descending-by-created_at order within each
  // bucket. Return Map<criterion_id, [v, v, ...]>.
  const m = new Map();
  for (const v of verifs) {
    const k = v.criterion_id || "";
    if (!m.has(k)) m.set(k, []);
    m.get(k).push(v);
  }
  // Defensive sort to handle out-of-order arrivals.
  for (const arr of m.values()) {
    arr.sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
  }
  return m;
}

function computeVerdicts(spec, byCrit) {
  // Map every spec criterion to its current verdict status. Pending
  // when no verification exists yet.
  const list = [];
  let passed = 0, total = 0;
  for (const c of spec?.acceptance_criteria || []) {
    total++;
    const latest = (byCrit.get(c.id) || [])[0];
    const status = latest?.status || "pending";
    if (status === "pass") passed++;
    list.push({
      id: c.id, text: c.text, sensor_kind: c.sensor_kind,
      status, summary: latest?.summary || "",
    });
  }
  return { list, passed, total, allPass: total > 0 && passed === total };
}

function statusPillClass(s) {
  return ({ pass: "pass", fail: "fail", warn: "warn", done: "pass",
            failed: "fail", aborted: "fail", pending: "pending" }[s]) || "pending";
}

function statusBorderClass(s) {
  return "border-" + (statusPillClass(s));
}

function kindLabel(k) {
  return ({
    workspace_changes: "Workspace changes",
    judge: "LLM judge",
    screenshot: "Screenshot", gif: "Animated GIF",
    visual_diff: "Visual diff",
    lint: "Lint", build: "Build",
    unit_test: "Unit tests", e2e_test: "E2E tests",
  }[k]) || k;
}

function timeAgo(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  const sec = (Date.now() - d.getTime()) / 1000;
  if (sec < 60) return Math.round(sec) + "s ago";
  if (sec < 3600) return Math.round(sec / 60) + "m ago";
  if (sec < 86400) return Math.round(sec / 3600) + "h ago";
  return d.toLocaleString();
}

function truncate(s, n) {
  if (!s) return "";
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
}

// silence unused import lint until escapeHTML is actively used.
const _ = escapeHTML;
