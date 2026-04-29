// components/annotator.js — modal canvas annotator for screenshot,
// visual_diff, and gif verifications. Users click+drag to draw a bbox,
// pick a verdict, type a comment, save. Saved annotations are stored
// with image-relative coordinates so they survive viewport changes.
//
// Posting a fix_required annotation flips the parent run to 'correcting'
// on the server side (see store.CreateAnnotation), which is how the
// human's signal becomes the corrector agent's next prompt.

import { api, get, post, patch } from "/api.js";
import { el } from "/router.js";
import { toast } from "/toast.js";

const VERDICTS = [
  { value: "fix_required", label: "Fix required (drives corrector)" },
  { value: "nit",          label: "Nit (informational)" },
  { value: "acceptable",   label: "Acceptable (no action)" },
];

// openAnnotator(pid, verification, ctx?)
//   ctx.specID    optional — when present plus a screenshot criterion,
//                 the modal shows a "Set as baseline" button.
//   ctx.criterion optional — the matching criterion for the verification;
//                 lets the modal know whether the kind supports baselines.
export async function openAnnotator(pid, verification, ctx = {}) {
  // Backdrop + modal scaffold.
  const backdrop = el("div", { class: "annotator-backdrop" });
  const modal = el("div", { class: "annotator-modal" });
  backdrop.appendChild(modal);
  document.body.appendChild(backdrop);

  function close() { backdrop.remove(); }
  backdrop.addEventListener("click", (e) => { if (e.target === backdrop) close(); });

  const headerActions = el("div", { class: "row", style: "gap:8px" });

  // "Set as baseline" — only meaningful for visual sensors with a known
  // criterion. When clicked we PATCH the spec's acceptance_criteria,
  // setting the criterion's sensor_spec.baseline_url to this verification's
  // artifact URL. Future runs will diff against it.
  const isVisual = ["screenshot", "visual_diff"].includes(verification.kind);
  const hasArtifact = !!verification.artifact_url;
  if (isVisual && hasArtifact && ctx.specID && ctx.criterion) {
    const isCurrent = (ctx.criterion.sensor_spec || {}).baseline_url === verification.artifact_url;
    const baselineBtn = el("button", { class: "secondary" },
      isCurrent ? "Already the baseline" : "Set as baseline");
    if (isCurrent) baselineBtn.disabled = true;
    baselineBtn.addEventListener("click", async () => {
      try {
        const spec = (await get(`/api/projects/${pid}/specs/${ctx.specID}`)).spec;
        const updated = (spec.acceptance_criteria || []).map((c) =>
          c.id === ctx.criterion.id
            ? { ...c, sensor_spec: { ...(c.sensor_spec || {}), baseline_url: verification.artifact_url } }
            : c);
        await patch(`/api/projects/${pid}/specs/${ctx.specID}`, { acceptance_criteria: updated });
        toast("Baseline saved — future runs will diff against this image");
        baselineBtn.textContent = "Already the baseline";
        baselineBtn.disabled = true;
      } catch (err) { toast(err.message, "error"); }
    });
    headerActions.appendChild(baselineBtn);
  }

  headerActions.appendChild(el("button", { class: "secondary", onClick: close }, "Close"));

  modal.appendChild(el("div", { class: "annotator-header" }, [
    el("strong", {}, `${verification.kind} — ${verification.summary || verification.id.slice(0, 8)}`),
    headerActions,
  ]));

  const stage = el("div", { class: "annotator-stage" });
  modal.appendChild(stage);

  const sidebar = el("div", { class: "annotator-sidebar" });
  modal.appendChild(sidebar);

  // Load image + existing annotations in parallel.
  const img = el("img", { src: verification.artifact_url, alt: verification.kind });
  const canvas = el("canvas", { class: "annotator-canvas" });
  stage.appendChild(img);
  stage.appendChild(canvas);

  const existingAnnos = await get(`/api/projects/${pid}/verifications/${verification.id}/annotations`);

  await new Promise((res, rej) => {
    img.onload = res;
    img.onerror = () => rej(new Error("could not load image"));
  });

  // Match canvas to displayed image size; redraw on resize.
  function syncCanvas() {
    const r = img.getBoundingClientRect();
    canvas.width = r.width;
    canvas.height = r.height;
    canvas.style.width = r.width + "px";
    canvas.style.height = r.height + "px";
    drawAnnotations();
  }
  window.addEventListener("resize", syncCanvas);
  syncCanvas();

  const ctx = canvas.getContext("2d");

  // Drawing state.
  let drawing = false, startX = 0, startY = 0, currentBox = null;

  canvas.addEventListener("mousedown", (e) => {
    const r = canvas.getBoundingClientRect();
    drawing = true;
    startX = e.clientX - r.left;
    startY = e.clientY - r.top;
  });
  canvas.addEventListener("mousemove", (e) => {
    if (!drawing) return;
    const r = canvas.getBoundingClientRect();
    const cx = e.clientX - r.left;
    const cy = e.clientY - r.top;
    currentBox = { x: Math.min(cx, startX), y: Math.min(cy, startY),
      w: Math.abs(cx - startX), h: Math.abs(cy - startY) };
    drawAnnotations();
  });
  canvas.addEventListener("mouseup", () => {
    if (!drawing) return;
    drawing = false;
    if (currentBox && currentBox.w > 4 && currentBox.h > 4) {
      promptForCommentAndSave(currentBox);
    }
    currentBox = null;
    drawAnnotations();
  });

  function imgToCanvas(b) {
    // bbox is stored as image-relative {x,y,w,h} in 0..1 range.
    return {
      x: b.x * canvas.width, y: b.y * canvas.height,
      w: b.w * canvas.width, h: b.h * canvas.height,
    };
  }
  function canvasToImg(b) {
    return {
      x: b.x / canvas.width, y: b.y / canvas.height,
      w: b.w / canvas.width, h: b.h / canvas.height,
    };
  }

  function drawAnnotations() {
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    for (const a of existingAnnos) {
      const b = imgToCanvas(parseBbox(a.bbox));
      ctx.lineWidth = 2;
      ctx.strokeStyle = colorFor(a.verdict);
      ctx.fillStyle = colorFor(a.verdict, 0.15);
      ctx.strokeRect(b.x, b.y, b.w, b.h);
      ctx.fillRect(b.x, b.y, b.w, b.h);
      drawLabel(b, labelFor(a));
    }
    if (currentBox) {
      ctx.lineWidth = 2;
      ctx.strokeStyle = "#58a6ff";
      ctx.setLineDash([6, 4]);
      ctx.strokeRect(currentBox.x, currentBox.y, currentBox.w, currentBox.h);
      ctx.setLineDash([]);
    }
  }

  function drawLabel(b, text) {
    ctx.font = "11px ui-monospace, Menlo, monospace";
    const padding = 4;
    const m = ctx.measureText(text);
    const w = m.width + padding * 2;
    const h = 16;
    ctx.fillStyle = "rgba(0,0,0,0.7)";
    ctx.fillRect(b.x, Math.max(b.y - h, 0), w, h);
    ctx.fillStyle = "white";
    ctx.fillText(text, b.x + padding, Math.max(b.y - 4, 12));
  }

  function colorFor(verdict, alpha = 1) {
    const map = {
      fix_required: [248, 81, 73],
      nit:          [210, 153, 34],
      acceptable:   [63, 185, 80],
    };
    const c = map[verdict] || [139, 148, 158];
    return `rgba(${c[0]},${c[1]},${c[2]},${alpha})`;
  }

  function labelFor(a) {
    const body = (a.body || "").slice(0, 30);
    return `[${a.verdict}] ${body}${(a.body || "").length > 30 ? "…" : ""}`;
  }

  function parseBbox(raw) {
    if (!raw) return { x: 0, y: 0, w: 0, h: 0 };
    if (typeof raw === "string") {
      try { return JSON.parse(raw); } catch (_) { return { x: 0, y: 0, w: 0, h: 0 }; }
    }
    return raw;
  }

  async function promptForCommentAndSave(boxCanvas) {
    const verdictSel = el("select", {},
      VERDICTS.map((v) => el("option", { value: v.value }, v.label)));
    const bodyInput = el("textarea", { rows: "3",
      placeholder: "Describe what's wrong here, or what should change" });
    const saveBtn = el("button", {}, "Save");
    const cancelBtn = el("button", { class: "secondary" }, "Cancel");

    const form = el("div", { class: "annotator-form" }, [
      el("label", {}, "Verdict"), verdictSel,
      el("label", {}, "Comment"), bodyInput,
      el("div", { class: "row", style: "gap:8px;margin-top:8px" }, [saveBtn, cancelBtn]),
    ]);
    const formBackdrop = el("div", { class: "annotator-form-backdrop" });
    formBackdrop.appendChild(form);
    modal.appendChild(formBackdrop);

    return new Promise((resolve) => {
      cancelBtn.addEventListener("click", () => { formBackdrop.remove(); resolve(null); });
      saveBtn.addEventListener("click", async () => {
        try {
          const newA = await post(
            `/api/projects/${pid}/verifications/${verification.id}/annotations`,
            { bbox: canvasToImg(boxCanvas), body: bodyInput.value, verdict: verdictSel.value },
          );
          existingAnnos.push(newA);
          drawAnnotations();
          formBackdrop.remove();
          toast("Annotation saved" + (verdictSel.value === "fix_required" ? " — run flipped to correcting" : ""));
          renderSidebar();
          resolve(newA);
        } catch (err) {
          toast(err.message, "error");
        }
      });
      bodyInput.focus();
    });
  }

  function renderSidebar() {
    sidebar.innerHTML = "";
    sidebar.appendChild(el("h3", {}, "Annotations"));
    if (existingAnnos.length === 0) {
      sidebar.appendChild(el("p", { class: "muted" },
        "Click+drag on the image to draw a bbox and add a comment."));
      return;
    }
    for (const a of existingAnnos) {
      sidebar.appendChild(el("div", { class: "annotation-item " + a.verdict }, [
        el("div", { class: "row" }, [
          el("span", { class: "pill " + statusClassForVerdict(a.verdict) }, a.verdict),
          el("span", { class: "muted mono", style: "font-size:11px" }, new Date(a.created_at).toLocaleString()),
        ]),
        el("p", { style: "margin:6px 0 0;white-space:pre-wrap" }, a.body || "(no body)"),
      ]));
    }
  }
  renderSidebar();
}

function statusClassForVerdict(v) {
  switch (v) {
    case "fix_required": return "fail";
    case "nit": return "warn";
    case "acceptable": return "pass";
  }
  return "pending";
}

// Re-export for run.js convenience.
export { api };
