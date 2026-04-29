import { api } from "/api.js";
import { go, el } from "/router.js";
import { toast } from "/toast.js";

const SENSOR_KINDS = ["lint", "unit_test", "e2e_test", "build", "screenshot", "judge", "manual"];

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
    criteria.push({ id: crypto.randomUUID(), text, sensor_kind: newCritKind.value });
    newCritText.value = "";
    renderCriteria();
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
    criteriaList,
    el("div", { class: "row", style: "gap:8px;margin-top:8px" }, [newCritKind, newCritText, addCrit]),
    el("div", { class: "row", style: "gap:8px;margin-top:18px" }, [submit, cancel]),
  ]);
  mount.appendChild(card);
}
