import { api, events, patch, post } from "/api.js";
import { go, el } from "/router.js";
import { toast } from "/toast.js";

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
  // Fetch spec + plans + every verification across every run for the
  // spec in parallel — the verifications fold under each criterion as
  // expandable artifact lists. Without the join, a user looking at a
  // validated spec has no way to see what the validator actually
  // produced without hopping into a run dashboard.
  const [data, allVerifs] = await Promise.all([
    api(`/api/projects/${pid}/specs/${sid}`),
    api(`/api/projects/${pid}/specs/${sid}/verifications`).catch(() => []),
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

      // Expanded body — verifications.
      if (history.length === 0) {
        card.appendChild(el("p", { class: "muted criterion-row__empty" },
          "No verification yet — fires when the run reaches the validate phase."));
      } else {
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
  let runs = [];
  try {
    runs = await api(`/api/projects/${pid}/specs/${sid}/runs`);
  } catch (_) { runs = []; }
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
