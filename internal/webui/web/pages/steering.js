// pages/steering.js — the harness steering loop. Humans converge on the
// guides (feedforward) and rule set the runner sees. Recurring-failure
// detection lands later; v1 just gives a clean topology + rule editor.

import { api, get, post, patch, del } from "/api.js";
import { go, el, escapeHTML } from "/router.js";
import { toast } from "/toast.js";

const RULE_KINDS = ["agents_md", "skill", "lint_config", "architectural_rule", "judge_prompt"];

export async function render(mount, params) {
  const { pid } = params;
  const project = await get(`/api/projects/${pid}`);

  mount.innerHTML = "";
  mount.appendChild(el("h1", {}, "Steering loop — " + project.name));
  mount.appendChild(el("p", { class: "muted" },
    "Edit the guides and rules the runner bakes into every iteration's prompt. Issues that recur are a signal to encode a new rule here rather than retry."));

  const tabRow = el("div", { class: "row", style: "gap:8px;margin-bottom:16px" });
  const tRules = el("button", {}, "Harness rules");
  const tTopo = el("button", { class: "secondary" }, "Topologies");
  tabRow.appendChild(tRules);
  tabRow.appendChild(tTopo);
  tabRow.appendChild(el("span", { class: "spacer" }));
  const back = el("button", { class: "secondary" }, "Back to specs");
  back.addEventListener("click", () => go(`/projects/${pid}/specs`));
  tabRow.appendChild(back);
  mount.appendChild(tabRow);

  const body = el("div");
  mount.appendChild(body);

  function setTab(active) {
    tRules.className = active === "rules" ? "" : "secondary";
    tTopo.className = active === "topologies" ? "" : "secondary";
  }
  tRules.addEventListener("click", () => { setTab("rules"); renderRules(body, pid); });
  tTopo.addEventListener("click",  () => { setTab("topologies"); renderTopologies(body, pid); });

  setTab("rules");
  renderRules(body, pid);
}

async function renderRules(mount, pid) {
  mount.innerHTML = '<p class="loading">Loading rules…</p>';
  const rules = await get(`/api/projects/${pid}/harness-rules`);

  mount.innerHTML = "";
  mount.appendChild(el("h2", {}, "Harness rules"));

  if (rules.length === 0) {
    mount.appendChild(el("p", { class: "empty" }, "No rules yet. Create one below."));
  } else {
    const list = el("div", { class: "spec-list" });
    for (const r of rules) {
      const row = el("div", { class: "card" }, [
        el("div", { class: "row" }, [
          el("strong", {}, r.name),
          el("span", { class: "pill mono" }, r.kind),
          el("span", { class: "spacer" }),
          el("label", { class: "row", style: "gap:6px" }, [
            el("input", { type: "checkbox", ...(r.enabled ? { checked: "" } : {}) }),
            el("span", { class: "muted" }, r.enabled ? "enabled" : "disabled"),
          ]),
        ]),
        el("textarea", { rows: "6", value: r.body || "" }),
        el("div", { class: "row", style: "gap:8px;margin-top:8px" }, [
          el("button", { class: "secondary" }, "Save"),
          el("button", { class: "danger" }, "Delete"),
        ]),
      ]);

      const checkbox = row.querySelector('input[type="checkbox"]');
      const textarea = row.querySelector("textarea");
      const [saveBtn, delBtn] = row.querySelectorAll("button");

      // textarea is created with value attr but textarea content is set via .value
      textarea.value = r.body || "";

      saveBtn.addEventListener("click", async () => {
        try {
          await patch(`/api/projects/${pid}/harness-rules/${r.id}`,
            { body: textarea.value, enabled: checkbox.checked });
          toast("Saved " + r.name);
        } catch (err) { toast(err.message, "error"); }
      });
      delBtn.addEventListener("click", async () => {
        if (!confirm("Delete rule “" + r.name + "”?")) return;
        try {
          await del(`/api/projects/${pid}/harness-rules/${r.id}`);
          toast("Deleted");
          renderRules(mount, pid);
        } catch (err) { toast(err.message, "error"); }
      });
      list.appendChild(row);
    }
    mount.appendChild(list);
  }

  // Create form.
  mount.appendChild(el("h2", { style: "margin-top:24px" }, "New rule"));
  const nameInput = el("input", { type: "text", placeholder: "e.g. House style — short PR titles" });
  const kindInput = el("select", {}, RULE_KINDS.map((k) => el("option", { value: k }, k)));
  const bodyInput = el("textarea", { rows: "6", placeholder: "What the agent should do or avoid. This text is concatenated into the runner's per-iteration system prompt." });
  const create = el("button", {}, "Create rule");
  create.addEventListener("click", async () => {
    if (!nameInput.value.trim() || !bodyInput.value.trim()) {
      return toast("Name and body required", "error");
    }
    try {
      await post(`/api/projects/${pid}/harness-rules`, {
        kind: kindInput.value, name: nameInput.value.trim(), body: bodyInput.value,
      });
      toast("Created");
      nameInput.value = ""; bodyInput.value = "";
      renderRules(mount, pid);
    } catch (err) { toast(err.message, "error"); }
  });
  const card = el("div", { class: "card", style: "max-width:720px" }, [
    el("label", {}, "Name"), nameInput,
    el("label", {}, "Kind"), kindInput,
    el("label", {}, "Body"), bodyInput,
    el("div", { style: "margin-top:10px" }, create),
  ]);
  mount.appendChild(card);
}

async function renderTopologies(mount, pid) {
  mount.innerHTML = '<p class="loading">Loading topologies…</p>';
  const topos = await get(`/api/projects/${pid}/topologies`);

  mount.innerHTML = "";
  mount.appendChild(el("h2", {}, "Topologies"));
  mount.appendChild(el("p", { class: "muted" },
    "A topology is an Ashby's-Law variety reducer — a service archetype (e.g. \"Express+Postgres web app\") that ships its own default guides. Specs pick a topology to inherit guides automatically."));

  if (topos.length === 0) {
    mount.appendChild(el("p", { class: "empty" }, "No topologies yet."));
  } else {
    const list = el("div", { class: "spec-list" });
    for (const t of topos) {
      list.appendChild(el("div", { class: "card" }, [
        el("strong", {}, t.name),
        el("p", { class: "muted" }, t.description || "(no description)"),
        el("div", { class: "muted mono", style: "font-size:11px" }, t.id),
      ]));
    }
    mount.appendChild(list);
  }

  mount.appendChild(el("h2", { style: "margin-top:24px" }, "New topology"));
  const nameInput = el("input", { type: "text", placeholder: "Generic web app" });
  const descInput = el("input", { type: "text", placeholder: "Description (optional)" });
  const create = el("button", {}, "Create topology");
  create.addEventListener("click", async () => {
    if (!nameInput.value.trim()) return toast("Name required", "error");
    try {
      await post(`/api/projects/${pid}/topologies`, {
        name: nameInput.value.trim(), description: descInput.value,
      });
      toast("Created");
      renderTopologies(mount, pid);
    } catch (err) { toast(err.message, "error"); }
  });
  mount.appendChild(el("div", { class: "card", style: "max-width:520px" }, [
    el("label", {}, "Name"), nameInput,
    el("label", {}, "Description"), descInput,
    el("div", { style: "margin-top:10px" }, create),
  ]));
}

// silence unused import lint until escapeHTML is used here.
const _ = escapeHTML;
