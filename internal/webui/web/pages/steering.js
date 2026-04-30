// pages/steering.js — the harness steering loop. Humans converge on the
// guides (feedforward) and rule set the runner sees. Recurring-failure
// detection lands later; v1 just gives a clean topology + rule editor.

import { api, get, post, patch, del } from "/api.js";
import { go, el, escapeHTML } from "/router.js";
import { toast } from "/toast.js";

const RULE_KINDS = ["agents_md", "skill", "lint_config", "architectural_rule", "judge_prompt"];
const PEVC_PHASES = ["plan", "execute", "validate", "correct"];

// renderPhasePicker returns a row of toggleable phase chips for one
// rule. Empty selection = "applies to every phase" (the rule body
// just shows up in every prompt). Returns the live state object so
// the caller can read getPhases() at save time.
function renderPhasePicker(currentPhases) {
  const state = new Set(currentPhases || []);
  const wrap = el("div", { class: "row", style: "gap:4px;flex-wrap:wrap;margin:6px 0" });
  PEVC_PHASES.forEach((p) => {
    const chip = el("button", {
      class: "secondary chip" + (state.has(p) ? " active" : ""),
      type: "button",
      title: "Inject this rule into the " + p + " phase prompt",
    }, p);
    chip.addEventListener("click", () => {
      if (state.has(p)) state.delete(p); else state.add(p);
      chip.classList.toggle("active");
    });
    wrap.appendChild(chip);
  });
  const hint = el("span", {
    class: "muted",
    style: "font-size:11px;margin-left:6px",
    title: "Empty selection means the rule fires in every phase. Pick one or more chips to scope it.",
  }, "(none = all phases)");
  wrap.appendChild(hint);
  return { wrap, getPhases: () => Array.from(state) };
}

export async function render(mount, params) {
  const { pid } = params;
  const project = await get(`/api/projects/${pid}`);

  mount.innerHTML = "";
  mount.appendChild(el("h1", {}, "Steering loop — " + project.name));
  mount.appendChild(el("p", { class: "muted" },
    "Edit the guides and rules the runner bakes into every iteration's prompt. Issues that recur are a signal to encode a new rule here rather than retry."));

  const tabRow = el("div", { class: "row", style: "gap:8px;margin-bottom:16px" });
  const tRules = el("button", {}, "Harness rules");
  const tTopo  = el("button", { class: "secondary" }, "Topologies");
  const tFails = el("button", { class: "secondary" }, "Recurring failures");
  const tKeys  = el("button", { class: "secondary" }, "API keys");
  tabRow.appendChild(tRules);
  tabRow.appendChild(tTopo);
  tabRow.appendChild(tFails);
  tabRow.appendChild(tKeys);
  tabRow.appendChild(el("span", { class: "spacer" }));
  const back = el("button", { class: "secondary" }, "Back to specs");
  back.addEventListener("click", () => go(`/projects/${pid}/specs`));
  tabRow.appendChild(back);
  mount.appendChild(tabRow);

  const body = el("div");
  mount.appendChild(body);

  function setTab(active) {
    tRules.className = active === "rules"      ? "" : "secondary";
    tTopo.className  = active === "topologies" ? "" : "secondary";
    tFails.className = active === "fails"      ? "" : "secondary";
    tKeys.className  = active === "keys"       ? "" : "secondary";
  }
  tRules.addEventListener("click", () => { setTab("rules");      renderRules(body, pid); });
  tTopo.addEventListener("click",  () => { setTab("topologies"); renderTopologies(body, pid); });
  tFails.addEventListener("click", () => { setTab("fails");      renderRecurring(body, pid); });
  tKeys.addEventListener("click",  () => { setTab("keys");       renderKeys(body, pid, project.name); });

  setTab("rules");
  renderRules(body, pid);
}

async function renderRecurring(mount, pid) {
  mount.innerHTML = '<p class="loading">Loading recurring failures…</p>';
  const fails = await get(`/api/projects/${pid}/steering/recurring-failures`);

  mount.innerHTML = "";
  mount.appendChild(el("h2", {}, "Recurring failures"));
  mount.appendChild(el("p", { class: "muted" },
    "Criteria that have failed or warned at least twice in the last 30 days. Each row is a signal that an inferential or computational sensor isn't catching the underlying pattern early — encode the lesson as a harness rule rather than retrying."));

  if (fails.length === 0) {
    mount.appendChild(el("p", { class: "empty" },
      "No recurring failures yet. Run a few specs and the steering loop will populate."));
    return;
  }

  const list = el("div", { class: "spec-list" });
  for (const f of fails) {
    const row = el("div", { class: "card" }, [
      el("div", { class: "row" }, [
        el("strong", {}, f.spec_title),
        el("span", { class: "spacer" }),
        el("span", { class: "pill mono" }, f.kind),
        el("span", { class: "pill fail" }, f.fail_count + "× failed"),
      ]),
      el("p", { style: "margin:6px 0" }, f.criterion_text || "(criterion deleted)"),
      f.last_summary
        ? el("p", { class: "muted mono", style: "font-size:11px;white-space:pre-wrap" },
            "last: " + f.last_summary)
        : null,
      el("div", { class: "row", style: "gap:8px;margin-top:6px" }, [
        el("span", { class: "muted mono", style: "font-size:11px" },
          "last seen " + new Date(f.last_seen).toLocaleString()),
        el("span", { class: "spacer" }),
        el("button", { class: "secondary" }, "Open spec"),
        el("button", {}, "Encode as rule"),
      ]),
    ]);
    const [openBtn, encodeBtn] = row.querySelectorAll("button");
    openBtn.addEventListener("click", () => go(`/projects/${pid}/specs/${f.spec_id}`));
    encodeBtn.addEventListener("click", () => {
      // Drop the user back into the rules tab with a pre-populated body
      // describing this failure class so they can save a new guide.
      const tab = mount.parentElement.previousElementSibling // tabRow above body
        ? mount.parentElement.previousElementSibling.querySelector('button:not(.secondary)')
        : null;
      const draftBody =
        `Avoid the failure pattern surfaced by criterion "${f.criterion_text}" of kind ${f.kind}.\n\n` +
        `Last seen failure: ${f.last_summary || "(no summary captured)"}\n\n` +
        `Add this guide to keep the agent from re-tripping on this pattern.`;
      sessionStorage.setItem("duckllo.draft-rule", JSON.stringify({
        kind: "agents_md",
        name: `Avoid: ${(f.criterion_text || "").slice(0, 60)}`,
        body: draftBody,
      }));
      // Click the rules tab to switch view; renderRules picks up the draft.
      if (tab) tab.click();
    });
    list.appendChild(row);
  }
  mount.appendChild(list);
}

async function renderRules(mount, pid) {
  mount.innerHTML = '<p class="loading">Loading rules…</p>';
  const rules = await get(`/api/projects/${pid}/harness-rules`);

  mount.innerHTML = "";
  mount.appendChild(el("h2", {}, "Harness rules"));
  mount.appendChild(el("p", { class: "muted" },
    "Rules are concatenated into the runner's prompt. Each rule can be scoped to specific PEVC phases — leave the chips empty to apply it to every phase, or click chips to restrict (e.g. a judge_prompt only matters in 'validate')."));

  // Phase filter: shows only rules that apply to the chosen phase.
  // 'all' means no filter — show every rule regardless of scope.
  let filterPhase = "all";
  const filterRow = el("div", { class: "row", style: "gap:6px;margin-bottom:14px" });
  filterRow.appendChild(el("span", {
    class: "muted help-tip",
    title: "Filter rules to those that fire in the selected phase. 'All' shows everything.",
  }, "Filter:"));
  const filterBtns = [];
  ["all", ...PEVC_PHASES].forEach((p) => {
    const b = el("button", {
      class: p === "all" ? "" : "secondary",
      type: "button",
    }, p);
    b.addEventListener("click", () => {
      filterPhase = p;
      filterBtns.forEach((x) => { x.className = (x.textContent === filterPhase) ? "" : "secondary"; });
      renderList();
    });
    filterRow.appendChild(b);
    filterBtns.push(b);
  });
  mount.appendChild(filterRow);

  const listEl = el("div", { class: "spec-list" });
  mount.appendChild(listEl);

  function ruleMatchesFilter(r) {
    if (filterPhase === "all") return true;
    if (!r.phases || r.phases.length === 0) return true; // empty = all phases
    return r.phases.includes(filterPhase);
  }

  function renderList() {
    listEl.innerHTML = "";
    const visible = rules.filter(ruleMatchesFilter);
    if (visible.length === 0) {
      listEl.appendChild(el("p", { class: "empty" },
        filterPhase === "all"
          ? "No rules yet. Create one below."
          : `No rules apply to phase '${filterPhase}'.`));
      return;
    }
    for (const r of visible) {
      const phaseChips = (r.phases && r.phases.length > 0)
        ? r.phases.map((p) => el("span", { class: "pill mono" }, p))
        : [el("span", { class: "muted", style: "font-size:11px" }, "all phases")];
      const head = el("div", { class: "row" }, [
        el("strong", {}, r.name),
        el("span", { class: "pill mono" }, r.kind),
        ...phaseChips,
        el("span", { class: "spacer" }),
        el("label", { class: "row", style: "gap:6px" }, [
          el("input", { type: "checkbox", ...(r.enabled ? { checked: "" } : {}) }),
          el("span", { class: "muted" }, r.enabled ? "enabled" : "disabled"),
        ]),
      ]);
      const picker = renderPhasePicker(r.phases);
      const textarea = el("textarea", { rows: "6" });
      textarea.value = r.body || "";
      const actions = el("div", { class: "row", style: "gap:8px;margin-top:8px" }, [
        el("button", { class: "secondary" }, "Save"),
        el("button", { class: "danger" }, "Delete"),
      ]);
      const row = el("div", { class: "card" }, [head, picker.wrap, textarea, actions]);
      const checkbox = head.querySelector('input[type="checkbox"]');
      const [saveBtn, delBtn] = actions.querySelectorAll("button");

      saveBtn.addEventListener("click", async () => {
        try {
          await patch(`/api/projects/${pid}/harness-rules/${r.id}`, {
            body: textarea.value,
            enabled: checkbox.checked,
            phases: picker.getPhases(),
          });
          toast("Saved " + r.name);
          renderRules(mount, pid);
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
      listEl.appendChild(row);
    }
  }
  renderList();

  // Create form. If we landed here via the "Encode as rule" button on
  // the recurring-failures tab, sessionStorage carries a draft that we
  // pre-populate.
  mount.appendChild(el("h2", { style: "margin-top:24px" }, "New rule"));
  const draftRaw = sessionStorage.getItem("duckllo.draft-rule");
  let draft = null;
  if (draftRaw) {
    try { draft = JSON.parse(draftRaw); } catch (_) { /* ignore */ }
    sessionStorage.removeItem("duckllo.draft-rule");
  }
  const nameInput = el("input", { type: "text", placeholder: "e.g. House style — short PR titles" });
  const kindInput = el("select", {}, RULE_KINDS.map((k) => el("option", { value: k, ...(draft && draft.kind === k ? { selected: "" } : {}) }, k)));
  const bodyInput = el("textarea", { rows: "6", placeholder: "What the agent should do or avoid. This text is concatenated into the runner's per-iteration system prompt." });
  const newPicker = renderPhasePicker([]);
  if (draft) {
    nameInput.value = draft.name || "";
    bodyInput.value = draft.body || "";
  }
  const create = el("button", {}, "Create rule");
  create.addEventListener("click", async () => {
    if (!nameInput.value.trim() || !bodyInput.value.trim()) {
      return toast("Name and body required", "error");
    }
    try {
      await post(`/api/projects/${pid}/harness-rules`, {
        kind: kindInput.value, name: nameInput.value.trim(), body: bodyInput.value,
        phases: newPicker.getPhases(),
      });
      toast("Created");
      nameInput.value = ""; bodyInput.value = "";
      renderRules(mount, pid);
    } catch (err) { toast(err.message, "error"); }
  });
  const card = el("div", { class: "card", style: "max-width:720px" }, [
    el("label", {}, "Name"), nameInput,
    el("label", { class: "help-tip", title: "How the rule reaches the agent. agents_md is general guidance; skill is a how-to recipe; lint_config / architectural_rule are stricter; judge_prompt is validate-phase only." }, "Kind"), kindInput,
    el("label", { class: "help-tip", title: "Empty = applies to every phase. Click chips to scope." }, "Phases"), newPicker.wrap,
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

async function renderKeys(mount, pid, projectName) {
  mount.innerHTML = '<p class="loading">Loading keys…</p>';
  const keys = await get(`/api/projects/${pid}/api-keys`);

  mount.innerHTML = "";
  mount.appendChild(el("h2", {}, "API keys"));
  mount.appendChild(el("p", { class: "muted" },
    "Project-scoped tokens for the runner and any other automation. Plaintext is shown once at mint and never again — copy it straight into your .duckllo.env."));

  if (keys.length === 0) {
    mount.appendChild(el("p", { class: "empty" }, "No keys yet. Mint one below."));
  } else {
    const list = el("div", { class: "spec-list" });
    for (const k of keys) {
      const row = el("div", { class: "card" }, [
        el("div", { class: "row" }, [
          el("strong", {}, k.label || el("span", { class: "muted" }, "(unlabeled)")),
          el("span", { class: "spacer" }),
          el("span", { class: "pill mono" }, k.key_prefix + "…"),
          el("button", { class: "danger" }, "Revoke"),
        ]),
        el("div", { class: "muted mono", style: "font-size:11px;margin-top:4px" },
          `created ${new Date(k.created_at).toLocaleString()}` +
          (k.last_used_at ? ` · last used ${new Date(k.last_used_at).toLocaleString()}` : " · never used")),
      ]);
      const [revoke] = row.querySelectorAll("button.danger");
      revoke.addEventListener("click", async () => {
        if (!confirm(`Revoke key ${k.key_prefix}…? Any runner using it will start failing on next request.`)) return;
        try {
          await del(`/api/projects/${pid}/api-keys/${k.id}`);
          toast("Revoked");
          renderKeys(mount, pid, projectName);
        } catch (err) { toast(err.message, "error"); }
      });
      list.appendChild(row);
    }
    mount.appendChild(list);
  }

  // Mint form.
  mount.appendChild(el("h2", { style: "margin-top:24px" }, "Mint a new key"));
  const labelInput = el("input", { type: "text", placeholder: "e.g. mac-mini runner" });
  const create = el("button", {}, "Mint key");
  const mintCard = el("div", { class: "card", style: "max-width:520px" }, [
    el("label", {}, "Label"), labelInput,
    el("div", { style: "margin-top:10px" }, create),
  ]);
  mount.appendChild(mintCard);

  create.addEventListener("click", async () => {
    if (!labelInput.value.trim()) return toast("Label required", "error");
    let resp;
    try {
      resp = await post(`/api/projects/${pid}/api-keys`, { label: labelInput.value.trim() });
    } catch (err) { return toast(err.message, "error"); }

    // Show the plaintext key + a copy-paste snippet for .duckllo.env. Once
    // the user dismisses this card the plaintext is gone for good.
    const snippet =
      `# duckllo runner credentials for ${projectName}\n` +
      `DUCKLLO_URL=${location.origin}\n` +
      `DUCKLLO_PROJECT=${pid}\n` +
      `DUCKLLO_KEY=${resp.plain}\n` +
      `# Add your Anthropic key:\n` +
      `# ANTHROPIC_API_KEY=sk-...\n`;

    const reveal = el("div", { class: "card", style: "border-color:var(--accent);margin-top:12px" }, [
      el("h3", { style: "margin-top:0" }, "Key minted — copy it now"),
      el("p", { class: "muted" }, "This is the only time the plaintext key is visible. Paste the snippet below into your .duckllo.env."),
      el("textarea", { rows: "8", readonly: "", style: "font-family:var(--mono);font-size:12px" }),
      el("div", { class: "row", style: "gap:8px;margin-top:8px" }, [
        el("button", {}, "Copy to clipboard"),
        el("button", { class: "secondary" }, "Done — refresh list"),
      ]),
    ]);
    const ta = reveal.querySelector("textarea");
    ta.value = snippet;
    const [copyBtn, doneBtn] = reveal.querySelectorAll("button");
    copyBtn.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(snippet);
        toast("Copied to clipboard");
      } catch (_) {
        ta.select();
        toast("Select-all + copy manually");
      }
    });
    doneBtn.addEventListener("click", () => renderKeys(mount, pid, projectName));
    mintCard.after(reveal);
    labelInput.value = "";
  });
}

// silence unused import lint until escapeHTML is used here.
const _ = escapeHTML;
