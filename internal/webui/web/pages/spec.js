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
  const data = await api(`/api/projects/${pid}/specs/${sid}`);
  const spec = data.spec;
  const plans = data.plans || [];

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

  // Acceptance criteria — list with remove buttons (only while editable).
  mount.appendChild(el("h2", {}, "Acceptance criteria"));
  const crits = readJSON(spec.acceptance_criteria);
  if (crits.length === 0) {
    mount.appendChild(el("p", { class: "empty" }, "No criteria yet."));
  } else {
    const ul = el("ul", { class: "criteria-list" });
    for (const c of crits) {
      const statusClass = c.satisfied ? "pass" : "pending";
      const li = el("li", {}, [
        el("span", { class: "pill mono " + statusClass }, c.sensor_kind),
        el("span", {}, c.text),
        el("span", { class: "row", style: "gap:8px;justify-content:flex-end" }, [
          el("span", { class: "pill " + statusClass }, c.satisfied ? "pass" : "pending"),
          editable
            ? el("button", { class: "danger" }, "remove")
            : null,
        ]),
      ]);
      if (editable) {
        const removeBtn = li.querySelector("button.danger");
        removeBtn.addEventListener("click", async () => {
          if (!confirm(`Remove criterion "${c.text}"?`)) return;
          try {
            const remaining = crits.filter((x) => x.id !== c.id);
            await patch(`/api/projects/${pid}/specs/${sid}`, { acceptance_criteria: remaining });
            toast("Removed");
            refresh(mount, pid, sid);
          } catch (err) { toast(err.message, "error"); }
        });
      }
      ul.appendChild(li);
    }
    mount.appendChild(ul);
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
