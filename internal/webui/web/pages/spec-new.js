import { api } from "/api.js";
import { go, el } from "/router.js";
import { toast } from "/toast.js";
import { t, getLang } from "/i18n.js";

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
  mount.appendChild(el("h1", {}, t("specNew.title")));
  mount.appendChild(el("p", { class: "muted" }, t("specNew.subtitle")));

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
      // Header row: kind pill + text + remove + (for sensor kinds with
      // a useful sensor_spec) a small "configure" toggle that reveals
      // a JSON editor below.
      const removeBtn = el("button", { class: "danger" }, "remove");
      const head = el("div", { class: "row", style: "gap:8px;align-items:flex-start" }, [
        el("span", { class: "pill mono" }, c.sensor_kind),
        el("span", { style: "flex:1" }, c.text),
      ]);
      const li = el("li", {}, [head]);

      // sensor_spec editor — only meaningful for sensors that take one.
      if (sensorKindNeedsSpec(c.sensor_kind)) {
        const toggle = el("button", { class: "secondary", type: "button", title: "Edit sensor_spec — selectors, viewport, scenario, hover_selector, etc." },
          c._specOpen ? "− spec" : "+ spec");
        head.appendChild(toggle);
        head.appendChild(removeBtn);

        const editor = el("textarea", {
          rows: "6",
          spellcheck: "false",
          style: "font-family:var(--mono);font-size:11px;margin-top:6px;display:" + (c._specOpen ? "block" : "none"),
          placeholder: defaultSpecHint(c.sensor_kind),
        });
        editor.value = c.sensor_spec ? JSON.stringify(c.sensor_spec, null, 2) : "";
        editor.addEventListener("blur", () => {
          const raw = editor.value.trim();
          if (raw === "") { delete c.sensor_spec; return; }
          try {
            c.sensor_spec = JSON.parse(raw);
          } catch (err) {
            toast("Invalid JSON in sensor_spec: " + err.message, "error");
          }
        });
        toggle.addEventListener("click", () => {
          c._specOpen = !c._specOpen;
          editor.style.display = c._specOpen ? "block" : "none";
          toggle.textContent = c._specOpen ? "− spec" : "+ spec";
        });
        li.appendChild(editor);
      } else {
        head.appendChild(removeBtn);
      }

      removeBtn.addEventListener("click", () => {
        criteria.splice(i, 1); renderCriteria();
      });
      criteriaList.appendChild(li);
    });
  }

  // Which sensor kinds expose enough configuration knobs to be worth
  // an inline editor. Lint / unit_test / build / manual / judge run
  // off the criterion text alone.
  function sensorKindNeedsSpec(kind) {
    return kind === "screenshot" || kind === "gif" || kind === "visual_diff";
  }

  // Concrete starter JSON the user can replace — same field set the Go
  // sensor consumes, so paste-and-tweak is the fast path.
  function defaultSpecHint(kind) {
    if (kind === "gif") {
      return JSON.stringify({
        viewport: { w: 1280, h: 800 },
        frame_delay_ms: 250,
        scenario: [
          { action: "navigate", url: "/" },
          { action: "wait", selector: ".some-element" },
          { action: "hover", selector: ".some-element" },
          { action: "sleep", sleep_ms: 600 },
        ],
      }, null, 2);
    }
    if (kind === "screenshot") {
      return JSON.stringify({
        url: "/",
        selector: "",
        hover_selector: "",
        viewport: { w: 1280, h: 800 },
        full_page: false,
      }, null, 2);
    }
    if (kind === "visual_diff") {
      return JSON.stringify({
        url: "/",
        baseline_url: "/api/uploads/<id>.png",
        tolerance: 16,
        diff_threshold: 0.5,
      }, null, 2);
    }
    return "";
  }

  const newCritText = el("input", { type: "text", placeholder: "User can toggle theme from header" });
  const newCritKind = el("select", {}, SENSOR_KINDS.map((k) => el("option", { value: k }, k)));
  const addCrit = el("button", { class: "secondary" }, t("specNew.btn.addCriterion"));
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

    refinePanel.appendChild(el("h3", { style: "margin-top:0" }, t("specNew.refine.title")));
    refinePanel.appendChild(el("p", { class: "muted" }, t("specNew.refine.help")));

    const refinedTitle = el("input", { type: "text", value: refined.refined_title || "" });
    const refinedIntent = el("textarea", { rows: "4" }, refined.refined_intent || "");

    refinePanel.appendChild(el("label", {}, t("specNew.refine.refTitle")));
    refinePanel.appendChild(refinedTitle);
    refinePanel.appendChild(el("label", {}, t("specNew.refine.refIntent")));
    refinePanel.appendChild(refinedIntent);

    // Each refined question is rendered as: prompt + (optional) row of
    // clickable answer chips + free-text textarea. Clicking a chip
    // writes its text into the textarea (and toggles a selected style);
    // the user can still type their own. We always read the answer
    // from the textarea so chip + custom text is one input path.
    const questions = (refined.questions || []).filter(
      (q) => q && typeof q.question === "string" && q.question.trim());
    const answerEls = [];
    if (questions.length > 0) {
      refinePanel.appendChild(el("h4", { style: "margin-top:14px" }, t("specNew.refine.questions")));
      questions.forEach((q) => {
        refinePanel.appendChild(el("p", { class: "question" }, q.question));
        const a = el("textarea", { rows: "2", placeholder: "Click a chip below or type your answer…" });
        const opts = (q.options || []).filter((o) => typeof o === "string" && o.trim());
        if (opts.length > 0) {
          const chips = el("div", { class: "row", style: "gap:6px;flex-wrap:wrap;margin-bottom:6px" });
          opts.forEach((o) => {
            const chip = el("button", { class: "secondary chip", type: "button" }, o);
            chip.addEventListener("click", () => {
              a.value = o;
              chips.querySelectorAll("button").forEach((b) => b.classList.remove("active"));
              chip.classList.add("active");
            });
            chips.appendChild(chip);
          });
          refinePanel.appendChild(chips);
        }
        refinePanel.appendChild(a);
        answerEls.push({ q: q.question, a });
      });
    } else {
      refinePanel.appendChild(el("p", { class: "muted",
        style: "margin-top:14px" }, t("specNew.refine.empty")));
    }

    // Single combined action: applies the refined draft to the form
    // above AND generates criteria from (refined title + intent + qa).
    const genBtn = el("button", {}, t("specNew.refine.apply"));
    genBtn.addEventListener("click", async () => {
      const titleVal = refinedTitle.value.trim();
      const intentVal = refinedIntent.value.trim();
      if (!titleVal) return toast("Refined title is empty", "error");
      const qa = answerEls
        .map(({ q, a }) => ({ q, a: a.value.trim() }))
        .filter(({ a }) => a !== "");
      genBtn.disabled = true;
      genBtn.textContent = t("specNew.btn.suggestBusy");
      try {
        const resp = await api(`/api/projects/${params.pid}/specs/suggest`, {
          method: "POST",
          body: { title: titleVal, intent: intentVal, qa, lang: getLang() } });
        const added = (resp.criteria || []).filter((s) => s.text && s.sensor_kind);
        // Apply refined draft to the form regardless — that's part of the action.
        titleInput.value = titleVal;
        intentInput.value = intentVal;
        if (added.length === 0) {
          toast("Model returned nothing usable", "error");
          return;
        }
        for (const s of added) {
          criteria.push({ id: genID(), text: s.text, sensor_kind: s.sensor_kind });
        }
        renderCriteria();
        toast(`Applied draft + added ${added.length} criteria — review and edit`);
        refinePanel.style.display = "none";
      } catch (err) {
        toast(err.message, "error");
      } finally {
        genBtn.disabled = false;
        genBtn.textContent = t("specNew.refine.apply");
      }
    });
    const dismissBtn = el("button", { class: "secondary" }, t("specNew.refine.dismiss"));
    dismissBtn.addEventListener("click", () => { refinePanel.style.display = "none"; });
    refinePanel.appendChild(el("div", { class: "row",
      style: "gap:8px;margin-top:14px" }, [genBtn, dismissBtn]));
  }

  const suggestBtn = el("button", { class: "secondary" }, t("specNew.btn.suggest"));
  suggestBtn.addEventListener("click", async () => {
    const title = titleInput.value.trim();
    const intent = intentInput.value.trim();
    if (!title) return toast("Type a title first", "error");
    suggestBtn.disabled = true;
    suggestBtn.textContent = t("specNew.btn.refining");
    try {
      const refined = await api(`/api/projects/${params.pid}/specs/refine`, {
        method: "POST", body: { title, intent, lang: getLang() } });
      renderRefinePanel(refined);
    } catch (err) {
      if (err.status === 503) {
        showRefinePanelError("No LLM provider on the server. Set ANTHROPIC_API_KEY or install the claude CLI.");
      } else {
        toast(err.message, "error");
      }
    } finally {
      suggestBtn.disabled = false;
      suggestBtn.textContent = t("specNew.btn.suggest");
    }
  });

  const submit = el("button", {}, t("specNew.btn.create"));
  submit.addEventListener("click", async () => {
    if (!titleInput.value.trim()) return toast("Title required", "error");
    try {
      const sp = await api(`/api/projects/${params.pid}/specs`, { method: "POST",
        body: { title: titleInput.value.trim(), intent: intentInput.value, priority: priorityInput.value } });
      // Fold the criteria onto the spec via PATCH so we keep one network roundtrip.
      if (criteria.length > 0) {
        await api(`/api/projects/${params.pid}/specs/${sp.id}`, { method: "PATCH",
          // Strip UI-only fields like _specOpen so they don't bloat
          // the row and confuse downstream consumers reading the JSON.
          body: { acceptance_criteria: criteria.map(({_specOpen, ...rest}) => rest) } });
      }
      toast("Created " + sp.title);
      go(`/projects/${params.pid}/specs/${sp.id}`);
    } catch (err) {
      toast(err.message, "error");
    }
  });

  const cancel = el("button", { class: "secondary" }, t("specNew.btn.cancel"));
  cancel.addEventListener("click", () => go(`/projects/${params.pid}/specs`));

  const card = el("div", { class: "card", style: "max-width:720px" }, [
    el("label", { class: "help-tip", title: t("specNew.field.titleHelp") }, t("specNew.field.title")), titleInput,
    el("label", { class: "help-tip", title: t("specNew.field.intentHelp") }, t("specNew.field.intent")), intentInput,
    el("label", { class: "help-tip", title: t("specNew.field.priorityHelp") }, t("specNew.field.priority")), priorityInput,
    el("h2", { class: "help-tip", style: "margin-top:18px", title: t("specNew.criteriaHelp") }, t("specNew.criteria")),
    el("div", { class: "row", style: "gap:8px;margin-bottom:6px" }, [suggestBtn]),
    refinePanel,
    criteriaList,
    el("div", { class: "row", style: "gap:8px;margin-top:8px" }, [newCritKind, newCritText, addCrit]),
    el("div", { class: "row", style: "gap:8px;margin-top:18px" }, [submit, cancel]),
  ]);
  mount.appendChild(card);
}
