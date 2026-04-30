// preview.js — assembled-prompt viewer for one (run, phase). Each
// segment is labeled with its source so the user can trace which
// document is contributing each section, and most segments carry an
// "Edit source" link that jumps straight to the page where it can be
// changed (spec composer, harness rule editor, run dashboard, etc.).
//
// After editing, the user can come back here and hit "Refresh" — it
// re-fetches the assembled prompt with the new content so the
// edit-then-preview loop is short.

import { api } from "/api.js";
import { go, el } from "/router.js";
import { toast } from "/toast.js";

const PHASES = ["plan", "execute", "validate", "correct"];

let currentMount = null;
let currentParams = null;
let currentPhase = "plan";

export async function render(mount, params) {
  currentMount = mount;
  currentParams = params;
  currentPhase = (new URLSearchParams(window.location.hash.split("?")[1] || "")).get("phase") || "plan";
  await draw();
}

async function draw() {
  const { pid, rid } = currentParams;
  const mount = currentMount;
  mount.innerHTML = "";

  mount.appendChild(el("h1", {}, "Prompt preview"));
  mount.appendChild(el("p", { class: "muted" },
    "What the agent sees if it claims this phase right now. Each block is tagged with its source — click 'Edit source' to jump to the page that controls that text."));

  // Phase tabs.
  const tabs = el("div", { class: "row", style: "gap:6px;margin-bottom:12px" });
  PHASES.forEach((p) => {
    const btn = el("button", {
      class: p === currentPhase ? "" : "secondary",
      title: phaseTitle(p),
    }, p);
    btn.addEventListener("click", () => {
      currentPhase = p;
      draw();
    });
    tabs.appendChild(btn);
  });
  const refreshBtn = el("button", {
    class: "secondary",
    title: "Re-fetch the assembled prompt — useful after editing a source document",
  }, "Refresh");
  refreshBtn.addEventListener("click", () => draw());
  tabs.appendChild(refreshBtn);
  const backBtn = el("button", {
    class: "secondary",
    style: "margin-left:auto",
  }, "Back to run");
  backBtn.addEventListener("click", () => go(`/projects/${pid}/runs/${rid}`));
  tabs.appendChild(backBtn);
  mount.appendChild(tabs);

  let preview;
  try {
    preview = await api(`/api/projects/${pid}/runs/${rid}/preview?phase=${currentPhase}`);
  } catch (err) {
    mount.appendChild(el("p", { class: "error" }, "preview: " + err.message));
    return;
  }

  // System prompt block.
  const sysCard = el("div", { class: "card" }, [
    el("div", { class: "preview-header" }, [
      el("strong", {}, "System prompt"),
      el("span", { class: "muted" }, " — role contract for the " + (preview.role || "agent")),
    ]),
    el("pre", { class: "preview-content" }, preview.system || "(empty)"),
  ]);
  mount.appendChild(sysCard);

  // User-message segments.
  const segHeader = el("h2", {}, "User message — labeled segments");
  segHeader.classList.add("help-tip");
  segHeader.title = "Each card below is one labeled chunk of the user message the agent receives. The order on this page is the same order the runner concatenates them into the actual prompt.";
  mount.appendChild(segHeader);

  const segs = preview.user || [];
  if (segs.length === 0) {
    mount.appendChild(el("p", { class: "empty" }, "No user-message content for this phase."));
    return;
  }
  segs.forEach((seg) => mount.appendChild(renderSegment(seg)));
}

function renderSegment(seg) {
  const sourceTag = el("span", {
    class: "preview-source-tag pill mono",
    title: sourceHelp(seg.source),
  }, seg.source);
  const head = el("div", { class: "preview-header" }, [
    sourceTag,
    el("strong", { style: "margin-left:8px" }, seg.heading || seg.source),
  ]);
  if (seg.edit_url) {
    const link = el("a", {
      href: seg.edit_url,
      class: "preview-edit-link",
      title: "Open the page where this content can be edited",
      style: "margin-left:auto",
    }, "Edit source →");
    head.appendChild(link);
  }
  return el("div", { class: "card preview-segment" }, [
    head,
    el("pre", { class: "preview-content" }, seg.content),
  ]);
}

function sourceHelp(src) {
  return {
    spec: "The spec's title + intent. Edit on the spec page (only while status is draft / proposed).",
    criteria: "The spec's typed acceptance criteria. Each criterion's sensor_kind decides what verifies it. Frozen on approval.",
    plan: "The plan steps the executor follows. Edit on the spec page; the planner agent drafts a new revision otherwise.",
    harness_rule: "Project-level guides the runner concatenates into every relevant phase's prompt. Edit on the steering page.",
    workspace_diff: "The output of `git diff` after the executor finished. Ground truth for the validator's verdict — not editable directly; re-run the executor to change it.",
    annotation: "Open fix_required annotations the corrector must address. Edit on the run dashboard's verification modal.",
    failed_sensor: "Sensors whose status is fail or warn. Re-run the validator to refresh.",
  }[src] || "Source of this prompt segment.";
}

function phaseTitle(p) {
  return {
    plan: "Plan phase: planner agent drafts a list of steps. Output: a JSON plan the executor will follow.",
    execute: "Execute phase: executor agent edits files step by step. Output: the workspace diff.",
    validate: "Validate phase: judge reasons over the diff to decide pass/fail per criterion. Sensors run separately and post their own verdicts.",
    correct: "Correct phase: corrector reads open annotations + failed sensors and proposes the next round of edits.",
  }[p];
}
