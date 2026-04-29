import { api, events } from "/api.js";
import { go, el } from "/router.js";
import { toast } from "/toast.js";

let currentSource = null;

export async function render(mount, params) {
  if (currentSource) { currentSource.close(); currentSource = null; }
  const { pid, sid } = params;
  await refresh(mount, pid, sid);
  currentSource = events(pid);
  ["spec.updated", "spec.criteria_changed", "run.queued", "run.advanced"].forEach((t) => {
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

  if (spec.intent) {
    const intentCard = el("div", { class: "card" });
    const pre = el("pre", { class: "mono", style: "white-space:pre-wrap;margin:0" }, spec.intent);
    intentCard.appendChild(pre);
    mount.appendChild(intentCard);
  }

  // Acceptance criteria.
  mount.appendChild(el("h2", {}, "Acceptance criteria"));
  const crits = readJSON(spec.acceptance_criteria);
  if (crits.length === 0) {
    mount.appendChild(el("p", { class: "empty" }, "No criteria yet."));
  } else {
    const ul = el("ul", { class: "criteria-list" });
    for (const c of crits) {
      const status = c.satisfied ? "pass" : "pending";
      ul.appendChild(el("li", {}, [
        el("span", { class: "pill mono " + status }, c.sensor_kind),
        el("span", {}, c.text),
        el("span", { class: "pill " + status }, c.satisfied ? "pass" : "pending"),
      ]));
    }
    mount.appendChild(ul);
  }

  // Spec actions.
  const actionRow = el("div", { class: "row", style: "gap:8px;margin-top:14px" });
  if (spec.status === "draft" || spec.status === "proposed") {
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
