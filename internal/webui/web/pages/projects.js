import { api } from "/api.js";
import { go, el } from "/router.js";
import { toast } from "/toast.js";

export async function render(mount) {
  const projects = await api("/api/projects");
  mount.innerHTML = "";
  mount.appendChild(el("h1", {}, "Projects"));

  if (projects.length === 0) {
    mount.appendChild(el("p", { class: "empty" },
      "You're not a member of any project yet. Create one below."));
  } else {
    const list = el("div", { class: "spec-list" });
    for (const p of projects) {
      const row = el("div", { class: "spec-row" }, [
        el("div", {}, [
          el("div", { class: "spec-title" }, p.name),
          el("div", { class: "spec-meta" }, p.description || "—"),
        ]),
        el("span", { class: "pill mono" }, p.id.slice(0, 8)),
        el("button", { class: "secondary" }, "Open"),
      ]);
      row.addEventListener("click", () => go(`/projects/${p.id}/specs`));
      list.appendChild(row);
    }
    mount.appendChild(list);
  }

  // New project form.
  mount.appendChild(el("h2", { style: "margin-top:32px" }, "New project"));
  const nameInput = el("input", { type: "text", placeholder: "Project name" });
  const descInput = el("input", { type: "text", placeholder: "Description (optional)" });
  const create = el("button", {}, "Create");
  create.addEventListener("click", async () => {
    if (!nameInput.value.trim()) return toast("Name required", "error");
    try {
      const p = await api("/api/projects", { method: "POST",
        body: { name: nameInput.value.trim(), description: descInput.value.trim() } });
      toast("Created " + p.name);
      go(`/projects/${p.id}/specs`);
    } catch (err) {
      toast(err.message, "error");
    }
  });
  const card = el("div", { class: "card", style: "max-width:520px" }, [
    el("label", {}, "Name"), nameInput,
    el("label", {}, "Description"), descInput,
    el("div", { style: "margin-top:10px" }, create),
  ]);
  mount.appendChild(card);
}
