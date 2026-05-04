// components/report-panel.js — "report not met" panel attached to a
// passed criterion card. Renders sensor-specific preset reasons as
// multi-select chips plus a free-text comment, then POSTs the result
// as a fix_required annotation against the criterion's latest
// verification. The server flips the run to 'correcting' and enqueues
// a 'correct' work item so the corrector picks up the human signal
// without any extra plumbing here.
//
// Why a separate module: the same panel is useful from any surface
// that lists criterion cards (spec page today, run dashboard tomorrow),
// and the preset map should not be duplicated.

import { post } from "/api.js";
import { el } from "/router.js";
import { toast } from "/toast.js";

// Preset reasons by sensor_kind. Each entry is a short, sensor-specific
// phrasing of a common false-positive failure mode — picked so a human
// can scan a checklist instead of writing prose. Free text is still
// supported for everything these don't cover.
const PRESET_REASONS = {
  gif: [
    "Recording doesn't actually show the change in action",
    "Wrong screen / route — captured area missed the feature",
    "Action sequence is incomplete (cut too early or too late)",
    "Visible glitch (flash, jank, layout shift) while recording",
    "Frames out of order or skipped — flow looks wrong",
    "Recorded viewport / device size doesn't match the requirement",
  ],
  screenshot: [
    "Required UI element is missing or off-screen",
    "Layout / spacing is wrong",
    "Theme or color does not match",
    "Text content is wrong or stale",
    "Captured before the UI finished rendering",
    "Wrong viewport / device size",
  ],
  visual_diff: [
    "Highlighted regions aren't the actual regression",
    "Baseline is stale and should be re-captured",
    "Anti-aliasing noise — not a real visual change",
    "Diff missed a regression that's clearly visible",
  ],
  judge: [
    "Judge missed an obvious regression in the diff",
    "Verdict doesn't reflect the spec's intent",
    "Reasoning was vague or contradicted itself",
    "Edge case the spec calls out wasn't considered",
    "Failed sensor was ignored when it should have weighed",
  ],
  unit_test: [
    "Passes but the feature is still broken (false positive)",
    "Assertion too loose — passes for the wrong output",
    "Coverage missed the path that actually changed",
    "The regression we just hit is not exercised",
    "Test ran but didn't actually invoke the changed code",
  ],
  e2e_test: [
    "Passes but the user flow is still broken in the browser",
    "Selector matched the old UI by accident — wrong element under test",
    "Flow stopped before the assertion that matters",
    "Scenario differs from how a user actually triggers this",
    "Timing flake — test passed by luck, not behaviour",
  ],
  build: [
    "Build succeeds but the binary doesn't run",
    "Warnings hide real errors that should be treated as failure",
    "Artifact is missing a file the change required",
    "Wrong target / arch built",
    "Dependencies are unpinned and will drift",
  ],
  lint: [
    "Rule is too lax for the kind of change we made",
    "New file is excluded from the lint scope",
    "Should have caught the smell here but didn't",
  ],
  typecheck: [
    "Type narrowing is missing where it would catch the bug",
    "any / unknown leaks the unsafe path past the checker",
    "Generic boundary lost at the seam the change introduced",
  ],
  manual: [
    "Manual verification was not actually performed",
    "Manual step is too vague to reproduce",
    "Reported outcome doesn't match what actually happens",
  ],
};

const DEFAULT_REASONS = [
  "Result doesn't actually meet the criterion",
  "Verification missed the regression",
  "Coverage too thin to call this a pass",
];

export function presetReasonsFor(sensorKind) {
  return PRESET_REASONS[sensorKind] || DEFAULT_REASONS;
}

// buildReportPanel mounts an inline panel inside the criterion card
// body. onSubmitted is called after a successful POST so callers can
// trigger a refresh — we don't navigate or re-render here, the caller
// owns the parent view.
//
//   pid           project id
//   verificationID id of the latest verification on this criterion
//   sensorKind    drives which preset list shows
//   onSubmitted() optional callback after annotation posted
//   onCancel()    optional callback when the user closes the panel
export function buildReportPanel({ pid, verificationID, sensorKind, onSubmitted, onCancel }) {
  const reasons = presetReasonsFor(sensorKind);
  const checkboxes = reasons.map((text, i) => {
    const id = `report-reason-${verificationID}-${i}`;
    const cb = el("input", { type: "checkbox", id, value: text });
    const label = el("label", { for: id, class: "report-panel__reason" }, [
      cb,
      el("span", {}, text),
    ]);
    return { cb, label };
  });

  const textarea = el("textarea", {
    rows: "3",
    class: "report-panel__text",
    placeholder: "Anything else the corrector should know? (optional)",
  });

  const submit = el("button", { class: "report-panel__submit" }, "Send to corrector");
  const cancel = el("button", { class: "secondary report-panel__cancel" }, "Cancel");

  const errorLine = el("p", { class: "report-panel__error muted", style: "display:none" }, "");

  submit.addEventListener("click", async (e) => {
    e.preventDefault();
    const checkedReasons = checkboxes.filter(({ cb }) => cb.checked).map(({ cb }) => cb.value);
    const freeText = textarea.value.trim();
    if (checkedReasons.length === 0 && !freeText) {
      errorLine.textContent = "Pick at least one reason or write a comment.";
      errorLine.style.display = "";
      return;
    }
    submit.disabled = true;
    submit.textContent = "Sending…";
    try {
      const body = formatBody(checkedReasons, freeText);
      await post(`/api/projects/${pid}/verifications/${verificationID}/annotations`, {
        verdict: "fix_required", body,
      });
      toast("Reported — run flipped to correcting, corrector will pick this up");
      if (onSubmitted) onSubmitted();
    } catch (err) {
      submit.disabled = false;
      submit.textContent = "Send to corrector";
      errorLine.textContent = err.message;
      errorLine.style.display = "";
    }
  });

  cancel.addEventListener("click", (e) => {
    e.preventDefault();
    if (onCancel) onCancel();
  });

  return el("div", { class: "report-panel" }, [
    el("p", { class: "report-panel__lead" },
      "Tell the corrector what's actually wrong. Pick any reasons that fit; add detail below."),
    el("div", { class: "report-panel__reasons" }, checkboxes.map((c) => c.label)),
    textarea,
    errorLine,
    el("div", { class: "row report-panel__actions" }, [submit, cancel]),
  ]);
}

// formatBody packs the structured selection + free text into the
// annotation body string. Kept human-readable so the corrector prompt
// (which dumps body verbatim) is still legible to anyone reading
// transcripts later.
function formatBody(reasons, freeText) {
  const lines = ["Reported as not met."];
  if (reasons.length > 0) {
    lines.push("");
    lines.push("Reasons:");
    for (const r of reasons) lines.push(`- ${r}`);
  }
  if (freeText) {
    lines.push("");
    lines.push("Notes:");
    lines.push(freeText);
  }
  return lines.join("\n");
}
