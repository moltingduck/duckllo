import { api, events } from "/api.js";
import { go, el } from "/router.js";
import { toast } from "/toast.js";

const STATUSES = ["draft", "proposed", "approved", "running", "validated", "merged", "rejected"];

let currentSource = null;

export async function render(mount, params) {
  if (currentSource) { currentSource.close(); currentSource = null; }
  const pid = params.pid;
  const project = await api(`/api/projects/${pid}`);

  mount.innerHTML = "";
  mount.appendChild(el("h1", {}, project.name));
  mount.appendChild(el("p", { class: "muted" }, project.description || "No description"));

  const filterRow = el("div", { class: "row", style: "gap:8px;margin-bottom:14px;flex-wrap:wrap" });
  const allBtn = el("button", { class: "secondary", "data-status": "" }, "All");
  filterRow.appendChild(allBtn);
  for (const s of STATUSES) {
    filterRow.appendChild(el("button", { class: "secondary", "data-status": s }, s));
  }
  filterRow.appendChild(el("span", { class: "spacer" }));
  // Steering = the project's harness rules + recurring-failures view.
  // Naming it "Steering" is opaque if you haven't read CLAUDE.md, so a
  // help tooltip explains the concept.
  const steeringBtn = el("button", {
    class: "secondary help-tip",
    title: "Steering: edit the project's harness rules — guides the runner concatenates into every iteration's prompt — and review recurring failure patterns. This is where you teach the harness 'don't make this mistake again' instead of correcting it inline each time.",
  }, "Steering");
  steeringBtn.addEventListener("click", () => go(`/projects/${pid}/steering`));
  filterRow.appendChild(steeringBtn);
  const newBtn = el("button", {
    title: "Compose a new spec: title, intent, and typed acceptance criteria. Approving the spec freezes the criteria and unlocks 'Start run'.",
  }, "New spec");
  newBtn.addEventListener("click", () => go(`/projects/${pid}/specs/new`));
  filterRow.appendChild(newBtn);
  mount.appendChild(filterRow);

  const list = el("div", { class: "spec-list" });
  mount.appendChild(list);

  let activeStatus = "";

  async function refresh() {
    const path = activeStatus
      ? `/api/projects/${pid}/specs?status=${activeStatus}`
      : `/api/projects/${pid}/specs`;
    const specs = await api(path);
    list.innerHTML = "";
    if (specs.length === 0) {
      list.appendChild(el("p", { class: "empty" }, "No specs yet. Create one to start."));
      return;
    }
    for (const sp of specs) {
      const row = el("div", { class: "spec-row" }, [
        el("div", {}, [
          el("div", { class: "spec-title" }, sp.title),
          el("div", { class: "spec-meta" },
            `${sp.priority} · updated ${new Date(sp.updated_at).toLocaleString()}`),
        ]),
        el("span", { class: "pill" }, sp.status),
        el("button", { class: "secondary" }, "Open"),
      ]);
      row.addEventListener("click", () => go(`/projects/${pid}/specs/${sp.id}`));
      list.appendChild(row);
    }
  }
  await refresh();

  filterRow.querySelectorAll("button[data-status]").forEach((btn) => {
    btn.addEventListener("click", () => {
      activeStatus = btn.getAttribute("data-status");
      filterRow.querySelectorAll("button[data-status]").forEach((b) => {
        b.classList.toggle("secondary", b !== btn);
      });
      refresh();
    });
  });

  // Live updates.
  currentSource = events(pid);
  ["spec.created", "spec.updated", "spec.criteria_changed", "run.queued", "run.advanced"].forEach((t) => {
    currentSource.addEventListener(t, () => refresh());
  });
  currentSource.onerror = () => toast("Live updates dropped — page will still work", "error");
}
