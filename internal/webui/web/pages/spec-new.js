import { api } from "/api.js";
import { go, el } from "/router.js";
import { toast } from "/toast.js";

const SENSOR_KINDS = ["lint", "unit_test", "e2e_test", "build", "screenshot", "judge", "manual"];

// crypto.randomUUID() is only defined in secure contexts (HTTPS or
// localhost). When duckllo is served over plain HTTP on a tailnet
// hostname it isn't, so we need a fallback. The id is purely a
// client-side handle for the criteria list — the server assigns the
// canonical id when the spec is persisted, so any unique string works.
function genID() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  // Timestamp + 8 hex chars of randomness. Good enough as a list key;
  // not used for anything that needs cryptographic strength.
  const r = Math.random().toString(16).slice(2, 10);
  return Date.now().toString(16) + "-" + r;
}

export async function render(mount, params) {
  mount.innerHTML = "";
  mount.appendChild(el("h1", {}, "New spec"));
  mount.appendChild(el("p", { class: "muted" },
    "Each acceptance criterion is a typed sensor target — the runner reads sensor_kind to decide which sensor fires."));

  const titleInput = el("input", { type: "text", placeholder: "Add dark-mode toggle" });
  const intentInput = el("textarea", { rows: "5",
    placeholder: "Why this matters, what success looks like, links to designs…" });
  const priorityInput = el("select", {}, [
    el("option", { value: "low" }, "low"),
    el("option", { value: "medium", selected: "" }, "medium"),
    el("option", { value: "high" }, "high"),
    el("option", { value: "critical" }, "critical"),
  ]);

  const criteriaList = el("ul", { class: "criteria-list" });
  let criteria = [];

  function renderCriteria() {
    criteriaList.innerHTML = "";
    criteria.forEach((c, i) => {
      const li = el("li", {}, [
        el("span", { class: "pill mono" }, c.sensor_kind),
        el("span", {}, c.text),
        el("button", { class: "danger" }, "remove"),
      ]);
      li.querySelector("button").addEventListener("click", () => {
        criteria.splice(i, 1); renderCriteria();
      });
      criteriaList.appendChild(li);
    });
  }

  const newCritText = el("input", { type: "text", placeholder: "User can toggle theme from header" });
  const newCritKind = el("select", {}, SENSOR_KINDS.map((k) => el("option", { value: k }, k)));
  const addCrit = el("button", { class: "secondary" }, "Add criterion");
  addCrit.addEventListener("click", () => {
    const text = newCritText.value.trim();
    if (!text) return toast("Text required", "error");
    criteria.push({ id: genID(), text, sensor_kind: newCritKind.value });
    newCritText.value = "";
    renderCriteria();
  });

  // Two-step suggest flow:
  //   1. /refine returns a tightened title + intent and 2-4 clarifying
  //      questions. The refine panel renders inline below the criteria
  //      list with editable refined fields, an "Apply refined draft"
  //      button (writes refined values back to the title/intent inputs
  //      so the user can still edit), and answer textareas.
  //   2. "Generate criteria" calls /suggest with the (possibly edited)
  //      title/intent + the user's answers. Returned criteria are
  //      appended to the criteria list as removable rows the user can
  //      tweak before pressing Create.
  const refinePanel = el("div", { class: "card",
    style: "margin-top:8px;background:rgba(255,255,255,0.03);display:none" });

  function showRefinePanelError(msg) {
    refinePanel.innerHTML = "";
    refinePanel.appendChild(el("p", { class: "error" }, msg));
    refinePanel.style.display = "block";
  }

  function renderRefinePanel(refined) {
    refinePanel.innerHTML = "";
    refinePanel.style.display = "block";

    refinePanel.appendChild(el("h3", { style: "margin-top:0" },
      "Refined draft + clarifying questions"));
    refinePanel.appendChild(el("p", { class: "muted" },
      "Edit the refined fields if you like, answer the questions, then generate criteria."));

    const refinedTitle = el("input", { type: "text", value: refined.refined_title || "" });
    const refinedIntent = el("textarea", { rows: "4" }, refined.refined_intent || "");
    const applyBtn = el("button", { class: "secondary" }, "Apply refined draft to my form");
    applyBtn.addEventListener("click", () => {
      titleInput.value = refinedTitle.value;
      intentInput.value = refinedIntent.value;
      toast("Title and intent updated — edit further or generate criteria");
    });

    refinePanel.appendChild(el("label", {}, "Refined title"));
    refinePanel.appendChild(refinedTitle);
    refinePanel.appendChild(el("label", {}, "Refined intent"));
    refinePanel.appendChild(refinedIntent);
    refinePanel.appendChild(el("div", { class: "row", style: "gap:8px;margin-top:6px" },
      [applyBtn]));

    const questions = (refined.questions || []).filter((q) => typeof q === "string" && q.trim());
    const answerEls = [];
    if (questions.length > 0) {
      refinePanel.appendChild(el("h4", { style: "margin-top:14px" }, "Questions"));
      questions.forEach((q) => {
        refinePanel.appendChild(el("p", { class: "question" }, q));
        const a = el("textarea", { rows: "2", placeholder: "Your answer…" });
        refinePanel.appendChild(a);
        answerEls.push({ q, a });
      });
    } else {
      refinePanel.appendChild(el("p", { class: "muted",
        style: "margin-top:14px" },
        "The model didn't have any clarifying questions — go ahead and generate criteria."));
    }

    const genBtn = el("button", {}, "Generate criteria");
    genBtn.addEventListener("click", async () => {
      const t = (refinedTitle.value || titleInput.value).trim();
      const i = (refinedIntent.value || intentInput.value).trim();
      if (!t) return toast("Refined title is empty", "error");
      const qa = answerEls
        .map(({ q, a }) => ({ q, a: a.value.trim() }))
        .filter(({ a }) => a !== "");
      genBtn.disabled = true;
      genBtn.textContent = "Asking the model…";
      try {
        const resp = await api(`/api/projects/${params.pid}/specs/suggest`, {
          method: "POST", body: { title: t, intent: i, qa } });
        const added = (resp.criteria || []).filter((s) => s.text && s.sensor_kind);
        if (added.length === 0) {
          toast("Model returned nothing usable", "error");
          return;
        }
        for (const s of added) {
          criteria.push({ id: genID(), text: s.text, sensor_kind: s.sensor_kind });
        }
        renderCriteria();
        toast(`Added ${added.length} suggestion${added.length === 1 ? "" : "s"} — review and edit`);
      } catch (err) {
        toast(err.message, "error");
      } finally {
        genBtn.disabled = false;
        genBtn.textContent = "Generate criteria";
      }
    });
    const dismissBtn = el("button", { class: "secondary" }, "Dismiss");
    dismissBtn.addEventListener("click", () => { refinePanel.style.display = "none"; });
    refinePanel.appendChild(el("div", { class: "row",
      style: "gap:8px;margin-top:14px" }, [genBtn, dismissBtn]));
  }

  const suggestBtn = el("button", { class: "secondary" }, "Suggest from title + intent");
  suggestBtn.addEventListener("click", async () => {
    const title = titleInput.value.trim();
    const intent = intentInput.value.trim();
    if (!title) return toast("Type a title first", "error");
    suggestBtn.disabled = true;
    suggestBtn.textContent = "Refining…";
    try {
      const refined = await api(`/api/projects/${params.pid}/specs/refine`, {
        method: "POST", body: { title, intent } });
      renderRefinePanel(refined);
    } catch (err) {
      if (err.status === 503) {
        showRefinePanelError("No LLM provider on the server. Set ANTHROPIC_API_KEY or install the claude CLI.");
      } else {
        toast(err.message, "error");
      }
    } finally {
      suggestBtn.disabled = false;
      suggestBtn.textContent = "Suggest from title + intent";
    }
  });

  const submit = el("button", {}, "Create");
  submit.addEventListener("click", async () => {
    if (!titleInput.value.trim()) return toast("Title required", "error");
    try {
      const sp = await api(`/api/projects/${params.pid}/specs`, { method: "POST",
        body: { title: titleInput.value.trim(), intent: intentInput.value, priority: priorityInput.value } });
      // Fold the criteria onto the spec via PATCH so we keep one network roundtrip.
      if (criteria.length > 0) {
        await api(`/api/projects/${params.pid}/specs/${sp.id}`, { method: "PATCH",
          body: { acceptance_criteria: criteria } });
      }
      toast("Created " + sp.title);
      go(`/projects/${params.pid}/specs/${sp.id}`);
    } catch (err) {
      toast(err.message, "error");
    }
  });

  const cancel = el("button", { class: "secondary" }, "Cancel");
  cancel.addEventListener("click", () => go(`/projects/${params.pid}/specs`));

  const card = el("div", { class: "card", style: "max-width:720px" }, [
    el("label", {}, "Title"), titleInput,
    el("label", {}, "Intent"), intentInput,
    el("label", {}, "Priority"), priorityInput,
    el("h2", { style: "margin-top:18px" }, "Acceptance criteria"),
    el("div", { class: "row", style: "gap:8px;margin-bottom:6px" }, [suggestBtn]),
    refinePanel,
    criteriaList,
    el("div", { class: "row", style: "gap:8px;margin-top:8px" }, [newCritKind, newCritText, addCrit]),
    el("div", { class: "row", style: "gap:8px;margin-top:18px" }, [submit, cancel]),
  ]);
  mount.appendChild(card);
}
