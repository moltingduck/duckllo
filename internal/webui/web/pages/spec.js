import { api, events, patch, post } from "/api.js";
import { go, el } from "/router.js";
import { toast } from "/toast.js";
import { buildReportPanel } from "/components/report-panel.js";

const SENSOR_KINDS = ["lint", "typecheck", "unit_test", "e2e_test", "build", "screenshot", "judge", "manual"];

let currentSource = null;

export async function render(mount, params) {
  if (currentSource) { currentSource.close(); currentSource = null; }
  const { pid, sid } = params;
  await refresh(mount, pid, sid);
  currentSource = events(pid);
  ["spec.updated", "spec.criteria_changed", "run.queued", "run.advanced",
    "plan.created", "plan.updated", "plan.approved"].forEach((t) => {
    currentSource.addEventListener(t, (ev) => {
      try {
        const body = JSON.parse(ev.data);
        if (body && (body.id === sid || body.spec_id === sid)) refresh(mount, pid, sid);
      } catch (_) { /* ignore */ }
    });
  });
}

async function refresh(mount, pid, sid) {
  // Fetch spec + plans + every verification across every run + the
  // runs timeline in parallel. The verifications fold under each
  // criterion as expandable artifact lists; the runs list drives both
  // the "Runs" timeline at the bottom and the per-round grouping in
  // the validated-spec review block at the top. Without the join, a
  // user looking at a validated spec has no way to see what the
  // validator actually produced without hopping into a run dashboard.
  const [data, allVerifs, runs] = await Promise.all([
    api(`/api/projects/${pid}/specs/${sid}`),
    api(`/api/projects/${pid}/specs/${sid}/verifications`).catch(() => []),
    api(`/api/projects/${pid}/specs/${sid}/runs`).catch(() => []),
  ]);
  const spec = data.spec;
  const plans = data.plans || [];
  // Group verifications by criterion_id, latest first within each
  // bucket so the expanded card shows the most recent result up top.
  const verifByCrit = new Map();
  for (const v of allVerifs) {
    const k = v.criterion_id || "";
    if (!verifByCrit.has(k)) verifByCrit.set(k, []);
    verifByCrit.get(k).push(v);
  }

  mount.innerHTML = "";
  mount.appendChild(el("h1", {}, spec.title));
  const meta = el("p", { class: "muted mono" },
    `${spec.priority} · ${spec.status} · updated ${new Date(spec.updated_at).toLocaleString()}`);
  mount.appendChild(meta);

  // Review block — shown only on validated specs. The acceptance
  // criteria list further down is the editorial view (one row per
  // criterion, history nested); the review block is the audit view
  // (one row per round, criteria nested) so a reviewer can answer
  // "what did this run claim it did?" without scanning the whole
  // page. Merged specs skip the block — once shipped the contract is
  // closed and a review header would just be visual noise.
  if (spec.status === "validated") {
    mount.appendChild(renderReviewBlock({
      pid, sid, spec,
      runs,
      verifsByCrit: verifByCrit,
      onAfterDispute: () => refresh(mount, pid, sid),
    }));
  }

  // Intent — read mode by default; an Edit button swaps to a textarea.
  // Locked once the spec is past 'approved' so we don't drift the contract
  // out from under an in-flight run.
  const editable = spec.status === "draft" || spec.status === "proposed";
  const intentCard = el("div", { class: "card" });
  const pre = el("pre", { class: "mono", style: "white-space:pre-wrap;margin:0" },
    spec.intent || "(no intent yet)");
  intentCard.appendChild(pre);
  if (editable) {
    const editBtn = el("button", { class: "secondary", style: "margin-top:8px" }, "Edit intent");
    editBtn.addEventListener("click", () => {
      const ta = el("textarea", { rows: "6" });
      ta.value = spec.intent || "";
      const save = el("button", {}, "Save");
      const cancel = el("button", { class: "secondary" }, "Cancel");
      save.addEventListener("click", async () => {
        try {
          await patch(`/api/projects/${pid}/specs/${sid}`, { intent: ta.value });
          toast("Saved");
          refresh(mount, pid, sid);
        } catch (err) { toast(err.message, "error"); }
      });
      cancel.addEventListener("click", () => refresh(mount, pid, sid));
      intentCard.replaceChildren(ta,
        el("div", { class: "row", style: "gap:8px;margin-top:8px" }, [save, cancel]));
    });
    intentCard.appendChild(editBtn);
  }
  mount.appendChild(intentCard);

  // Acceptance criteria — one expandable card per criterion. Header
  // shows status (from the latest verification across all runs) +
  // kind + text. Click to expand: shows every verification for that
  // criterion newest first, with image artifacts (gif / screenshot /
  // visual_diff) rendered full-width inline so the captured proof
  // lives right under the criterion that demanded it.
  mount.appendChild(el("h2", {}, "Acceptance criteria"));
  const crits = readJSON(spec.acceptance_criteria);
  if (crits.length === 0) {
    mount.appendChild(el("p", { class: "empty" }, "No criteria yet."));
  } else {
    for (const c of crits) {
      const history = verifByCrit.get(c.id) || [];
      const latest = history[0]; // newest first
      // Card status reflects the freshest verification, not the
      // satisfied flag — a "satisfied" criterion that just regressed
      // should show its current state.
      const status = latest?.status || (c.satisfied ? "pass" : "pending");
      const card = el("details", { class: "criterion-row " + statusBorderClass(status) });

      const head = el("summary", { class: "criterion-row__head" }, [
        el("span", { class: "pill " + statusPillClass(status) }, status),
        el("span", { class: "pill mono criterion-row__kind" }, c.sensor_kind),
        el("span", { class: "criterion-row__text" }, c.text),
        history.length > 0
          ? el("span", { class: "muted mono", style: "font-size:11px" },
              `${history.length} result${history.length === 1 ? "" : "s"}`)
          : el("span", { class: "muted mono", style: "font-size:11px" }, "no run yet"),
      ]);
      if (editable) {
        const rm = el("button", { class: "danger criterion-row__remove" }, "remove");
        rm.addEventListener("click", async (e) => {
          e.preventDefault();
          if (!confirm(`Remove criterion "${c.text}"?`)) return;
          try {
            const remaining = crits.filter((x) => x.id !== c.id);
            await patch(`/api/projects/${pid}/specs/${sid}`, { acceptance_criteria: remaining });
            toast("Removed");
            refresh(mount, pid, sid);
          } catch (err) { toast(err.message, "error"); }
        });
        head.appendChild(rm);
      }
      card.appendChild(head);

      // Expanded body — verifications + (when the latest is pass) a
      // "report not met" affordance the user can fire to flip the run
      // back into correcting without writing a long annotation.
      if (history.length === 0) {
        card.appendChild(el("p", { class: "muted criterion-row__empty" },
          "No verification yet — fires when the run reaches the validate phase."));
      } else {
        // Latest verification only carries a "report not met" button
        // when the verification status is 'pass' — the whole point of
        // the affordance is to override a false positive. Anything
        // else (fail / warn / pending) is already in the corrector's
        // line of sight, so we hide the button to avoid noise.
        if (latest && latest.status === "pass") {
          card.appendChild(renderReportSlot(pid, sid, latest, c, () => refresh(mount, pid, sid)));
        }
        for (const v of history) {
          card.appendChild(renderVerification(v, pid));
        }
      }
      mount.appendChild(card);
    }
  }

  // Inline "add criterion" form. Visible only while the spec is still
  // editable; once approved we lock the criteria so an in-flight run
  // doesn't drift.
  if (editable) {
    const newText = el("input", { type: "text", placeholder: "User can toggle theme from the header" });
    const newKind = el("select", {}, SENSOR_KINDS.map((k) => el("option", { value: k }, k)));
    const addBtn = el("button", { class: "secondary" }, "Add criterion");
    addBtn.addEventListener("click", async () => {
      if (!newText.value.trim()) return toast("Text required", "error");
      try {
        await post(`/api/projects/${pid}/specs/${sid}/criteria`, {
          text: newText.value.trim(), sensor_kind: newKind.value,
        });
        newText.value = "";
        toast("Added");
        refresh(mount, pid, sid);
      } catch (err) { toast(err.message, "error"); }
    });
    mount.appendChild(el("div", { class: "row", style: "gap:8px;margin-top:10px" },
      [newKind, newText, addBtn]));
  }

  // Spec actions.
  const actionRow = el("div", { class: "row", style: "gap:8px;margin-top:14px" });
  if (editable) {
    const approveBtn = el("button", {}, "Approve spec");
    approveBtn.addEventListener("click", async () => {
      try { await api(`/api/projects/${pid}/specs/${sid}/approve`, { method: "POST" });
        toast("Approved"); refresh(mount, pid, sid);
      } catch (err) { toast(err.message, "error"); }
    });
    actionRow.appendChild(approveBtn);
  }
  if (spec.status === "approved") {
    const hasApprovedPlan = plans.find((p) => p.status === "approved");
    const runBtn = el("button", {}, hasApprovedPlan ? "Start run" : "Start run (planner will draft a plan)");
    runBtn.addEventListener("click", async () => {
      try {
        const run = await api(`/api/projects/${pid}/specs/${sid}/runs`, { method: "POST", body: {} });
        go(`/projects/${pid}/runs/${run.id}`);
      } catch (err) { toast(err.message, "error"); }
    });
    actionRow.appendChild(runBtn);
  }
  if (actionRow.children.length > 0) mount.appendChild(actionRow);

  // Plans.
  mount.appendChild(el("h2", {}, "Plans"));
  if (plans.length === 0) {
    mount.appendChild(el("p", { class: "empty" },
      "No plan yet. Plans are produced by the planner agent or pasted in by a human via the API."));
  } else {
    for (const p of plans) {
      const card = el("div", { class: "card" });
      card.appendChild(el("div", { class: "row" }, [
        el("strong", {}, `v${p.version}`),
        el("span", { class: "pill " + (p.status === "approved" ? "pass" : "pending") }, p.status),
        el("span", { class: "muted mono" }, p.created_by_role),
      ]));
      const steps = readJSON(p.steps);
      if (steps.length > 0) {
        const ol = el("ol", { style: "margin:8px 0;padding-left:22px" });
        for (const st of steps) {
          ol.appendChild(el("li", {}, st.summary || "(no summary)"));
        }
        card.appendChild(ol);
      } else {
        card.appendChild(el("p", { class: "muted" }, "(no steps)"));
      }
      if (p.status === "draft") {
        const approveBtn = el("button", { class: "secondary" }, "Approve plan");
        approveBtn.addEventListener("click", async () => {
          try { await api(`/api/projects/${pid}/plans/${p.id}/approve`, { method: "POST" });
            toast("Plan approved"); refresh(mount, pid, sid);
          } catch (err) { toast(err.message, "error"); }
        });
        card.appendChild(approveBtn);
      }
      mount.appendChild(card);
    }
  }

  // Runs timeline — newest first, click to open the run dashboard.
  // Without this section the user has no UI path to a finished run's
  // dashboard (where the GIFs and screenshots actually live), so any
  // captured artifact is effectively orphaned once the run completes.
  // Runs were fetched in parallel at the top of refresh().
  mount.appendChild(el("h2", {}, "Runs"));
  if (runs.length === 0) {
    mount.appendChild(el("p", { class: "empty" },
      "No runs yet. Approve the spec then click 'Start run' above."));
  } else {
    for (const r of runs) {
      const row = el("div", { class: "card" }, [
        el("div", { class: "row" }, [
          el("strong", { class: "mono" }, r.id.slice(0, 8)),
          el("span", { class: "pill " + statusPillClass(r.status) }, r.status),
          el("span", { class: "muted mono", style: "font-size:11px" },
            `${r.turns_used}/${r.turn_budget} turns · ${r.token_usage} tokens`),
          el("span", { class: "spacer" }),
          el("span", { class: "muted mono", style: "font-size:11px" },
            new Date(r.created_at).toLocaleString()),
        ]),
        el("div", { class: "row", style: "margin-top:8px;gap:8px" }, [
          el("button", { class: "secondary" }, "Open run dashboard"),
        ]),
      ]);
      row.querySelector("button").addEventListener("click",
        () => go(`/projects/${pid}/runs/${r.id}`));
      mount.appendChild(row);
    }
  }
}

// renderReviewBlock builds the validated-spec header: one card per
// completed round (run), each listing the run's per-criterion verdicts
// and any captured artifact. Default view shows the latest round only;
// "展開歷史" reveals older rounds inline. The 「回報未達成」 buttons
// inside the latest-round block let a reviewer dispute a verdict
// directly from this header, which is the whole point of opening up
// disputes after validation — the user shouldn't have to scroll past
// the contract to find the affordance.
function renderReviewBlock({ pid, sid, spec, runs, verifsByCrit, onAfterDispute }) {
  const crits = readJSON(spec.acceptance_criteria);
  // Run-id order, newest first, restricted to runs that have produced
  // at least one verification — a queued or planning run has nothing
  // to review yet.
  const runIDsWithVerifs = new Set();
  for (const list of verifsByCrit.values()) {
    for (const v of list) runIDsWithVerifs.add(v.run_id);
  }
  const rounds = runs.filter((r) => runIDsWithVerifs.has(r.id));

  const block = el("section", { class: "review-block" });
  block.appendChild(el("h2", { class: "review-block__title" }, "Review · 驗證結果"));
  if (rounds.length === 0) {
    block.appendChild(el("p", { class: "muted" },
      "No verifications captured for this spec yet."));
    return block;
  }

  const latest = rounds[0];
  const older = rounds.slice(1);
  block.appendChild(renderReviewRound({
    pid, run: latest, crits, verifsByCrit, isLatest: true, onAfterDispute,
  }));

  if (older.length > 0) {
    const history = el("div", { class: "review-block__history", style: "display:none" });
    for (const r of older) {
      history.appendChild(renderReviewRound({
        pid, run: r, crits, verifsByCrit, isLatest: false, onAfterDispute,
      }));
    }
    const toggle = el("button", { class: "secondary review-block__toggle" },
      `展開歷史 (${older.length})`);
    toggle.addEventListener("click", () => {
      const open = history.style.display !== "none";
      history.style.display = open ? "none" : "";
      toggle.textContent = open ? `展開歷史 (${older.length})` : "收合歷史";
    });
    block.appendChild(toggle);
    block.appendChild(history);
  }
  return block;
}

// renderReviewRound renders one round (= one run) inside the review
// block: header with run status + timestamp, then one line per
// criterion showing kind, verdict pill, and a thumbnail/link for the
// captured artifact (when image-typed). For the latest round on a
// passed criterion we also surface the "report not met" affordance so
// disputes are reachable from the header without scrolling.
function renderReviewRound({ pid, run, crits, verifsByCrit, isLatest, onAfterDispute }) {
  const round = el("article", { class: "review-round" });
  round.appendChild(el("header", { class: "row review-round__head" }, [
    el("span", { class: "pill " + statusPillClass(run.status) }, run.status),
    el("span", { class: "muted mono", style: "font-size:11px" }, run.id.slice(0, 8)),
    el("span", { class: "spacer" }),
    el("span", { class: "muted mono", style: "font-size:11px" },
      new Date(run.created_at).toLocaleString()),
  ]));

  for (const c of crits) {
    // Pick the verification on this criterion that came from this
    // round's run (a criterion can appear once per round if the
    // sensor ran). Take the newest entry — repeated retries within
    // a single run keep their order in the array but the latest is
    // the one the validator went with.
    const matches = (verifsByCrit.get(c.id) || []).filter((v) => v.run_id === run.id);
    const v = matches[0];

    const row = el("div", { class: "review-criterion " + (v ? statusBorderClass(v.status) : "border-pending") });
    row.appendChild(el("div", { class: "row review-criterion__head" }, [
      el("span", { class: "pill " + (v ? statusPillClass(v.status) : "pending") },
        v ? v.status : "no result"),
      el("span", { class: "pill mono review-criterion__kind" }, c.sensor_kind),
      el("span", { class: "review-criterion__text" }, c.text),
    ]));
    if (v && v.summary) {
      row.appendChild(el("p", { class: "muted review-criterion__summary" }, v.summary));
    }
    if (v && v.artifact_url) {
      const isImage = v.kind === "gif" || v.kind === "screenshot" || v.kind === "visual_diff";
      if (isImage) {
        row.appendChild(el("img", {
          src: v.artifact_url, alt: v.kind,
          class: "review-criterion__art", loading: "lazy",
        }));
      } else {
        row.appendChild(el("a", { href: v.artifact_url, class: "muted",
                                   target: "_blank", rel: "noopener" }, "artifact"));
      }
    }
    // Dispute affordance — only on the latest round, and only when
    // the validator returned 'pass'. We want users on validated
    // specs to be able to flip a "pass" they disagree with back to
    // correcting; we don't surface the button on already-failed
    // rounds (they're already in the corrector's lane) or on
    // historical rounds (those are read-only audit trail now).
    if (isLatest && v && v.status === "pass") {
      row.appendChild(renderReportSlot(pid, run.spec_id, v, c, onAfterDispute));
    }
    round.appendChild(row);
  }
  return round;
}

// renderReportSlot owns the open/close lifecycle of the "report not
// met" affordance for a passed criterion. We keep a button visible by
// default and swap it for the panel on click — single render avoids
// the panel ghosting back if the user cancels and re-opens.
function renderReportSlot(pid, sid, latest, criterion, onSubmitted) {
  const slot = el("div", { class: "criterion-row__report-slot" });
  const open = el("button", { class: "secondary criterion-row__report-btn" },
    "Report not met");
  open.addEventListener("click", () => {
    slot.innerHTML = "";
    slot.appendChild(buildReportPanel({
      pid,
      verificationID: latest.id,
      sensorKind: criterion.sensor_kind,
      onSubmitted,
      onCancel: () => { slot.innerHTML = ""; slot.appendChild(open); },
    }));
  });
  slot.appendChild(open);
  return slot;
}

function statusPillClass(s) {
  return ({ done: "pass", pass: "pass", validated: "pass",
            failed: "fail", aborted: "fail", fail: "fail",
            warn: "warn" }[s]) || "pending";
}

function statusBorderClass(s) {
  return "border-" + statusPillClass(s);
}

// renderVerification produces the body card for one verification in
// the criterion's expanded list. Image kinds (gif / screenshot /
// visual_diff) get the artifact rendered full-width and clicking opens
// the run dashboard for that run so the user can annotate it from
// the same toolbox they already have. Non-image kinds (judge, lint,
// shell) just show summary + run link.
function renderVerification(v, pid) {
  const isImage = v.artifact_url &&
    (v.kind === "gif" || v.kind === "screenshot" || v.kind === "visual_diff");
  const card = el("div", { class: "criterion-row__verif" });
  card.appendChild(el("div", { class: "row criterion-row__verif-head" }, [
    el("span", { class: "pill " + statusPillClass(v.status) }, v.status),
    el("span", { class: "muted mono", style: "font-size:11px" },
      new Date(v.created_at).toLocaleString()),
    el("span", { class: "spacer" }),
    el("a", { href: `#/projects/${pid}/runs/${v.run_id}`, class: "muted",
              style: "font-size:11px" }, "open run dashboard →"),
  ]));
  if (v.summary) {
    card.appendChild(el("p", { class: "criterion-row__verif-summary" }, v.summary));
  }
  if (isImage) {
    card.appendChild(el("img", {
      src: v.artifact_url, alt: v.kind,
      class: "criterion-row__verif-img",
      loading: "lazy",
    }));
  }
  return card;
}

// JSONB columns now arrive as raw JSON (json.RawMessage on the server) so
// they passthrough to the client as arrays/objects directly. This shim
// stays only as a safety net during migrations.
function readJSON(b) {
  if (b == null) return [];
  if (Array.isArray(b)) return b;
  if (typeof b === "object") return b;
  if (typeof b === "string") {
    try { return JSON.parse(b); } catch (_) {}
  }
  return [];
}
