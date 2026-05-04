import { api, events } from "/api.js";
import { go, el, escapeHTML } from "/router.js";
import { toast } from "/toast.js";
import { openAnnotator } from "/components/annotator.js";
import { t } from "/i18n.js";

let currentSource = null;

export async function render(mount, params) {
  if (currentSource) { currentSource.close(); currentSource = null; }
  const { pid, rid } = params;
  await refresh(mount, pid, rid);
  currentSource = events(pid);
  ["iteration.appended", "iteration.updated", "verification.posted",
    "verification.updated", "annotation.added", "run.advanced",
    "run.workspace_set"].forEach((t) => {
    currentSource.addEventListener(t, () => refresh(mount, pid, rid));
  });
}

async function refresh(mount, pid, rid) {
  const [data, verifications] = await Promise.all([
    api(`/api/projects/${pid}/runs/${rid}`),
    api(`/api/projects/${pid}/runs/${rid}/verifications`),
  ]);
  const run = data.run;
  const iterations = data.iterations || [];

  // Fetch the spec so the annotator can offer "Set as baseline" when a
  // verification corresponds to a screenshot/visual_diff criterion.
  let spec = null;
  let critByID = {};
  try {
    spec = (await api(`/api/projects/${pid}/specs/${run.spec_id}`)).spec;
    for (const c of spec.acceptance_criteria || []) critByID[c.id] = c;
  } catch (_) { /* tolerable; annotator just won't show the button */ }

  mount.innerHTML = "";
  mount.appendChild(el("h1", {}, "Run " + run.id.slice(0, 8)));
  mount.appendChild(el("p", { class: "muted mono" },
    `status=${run.status} · turns=${run.turns_used}/${run.turn_budget} · tokens=${run.token_usage}`));

  // Phase 2 visibility: when the runner has provisioned a Docker
  // workspace (and optionally a Tailscale sidecar), the dev URL,
  // container id, and tailscale host all live in run.workspace_meta.
  // Surface them so an operator can click straight through to the
  // dev server instead of digging through the API.
  const wm = run.workspace_meta || {};
  const wmKeys = Object.keys(wm).filter(k => wm[k]);
  if (wmKeys.length > 0) {
    const card = el("div", { class: "card", style: "margin-top:6px" });
    card.appendChild(el("h3", { style: "margin:0 0 6px;font-size:13px" }, "Workspace"));
    const tbl = el("table", { class: "mono", style: "font-size:12px;width:100%" });
    for (const k of wmKeys) {
      const tr = el("tr");
      tr.appendChild(el("td", { class: "muted", style: "padding:2px 8px 2px 0;width:140px" }, k));
      const v = String(wm[k]);
      let cell;
      if (k === "dev_url" && /^https?:\/\//.test(v)) {
        cell = el("td", {}, [el("a", { href: v, target: "_blank", rel: "noopener" }, v)]);
      } else {
        cell = el("td", {}, v);
      }
      tr.appendChild(cell);
      tbl.appendChild(tr);
    }
    card.appendChild(tbl);
    mount.appendChild(card);
  }

  const actionRow = el("div", { class: "row", style: "gap:8px" });
  if (!["done", "failed", "aborted"].includes(run.status)) {
    const abort = el("button", { class: "danger" }, t("run.btn.abort"));
    abort.addEventListener("click", async () => {
      try { await api(`/api/projects/${pid}/runs/${rid}/abort`, { method: "POST" });
        toast("Run aborted"); refresh(mount, pid, rid);
      } catch (err) { toast(err.message, "error"); }
    });
    actionRow.appendChild(abort);
  }
  // Runs parked in 'validating' or 'correcting' are awaiting human input.
  // Offer "Mark complete" so a PM can force-finish without bouncing
  // through the runner's claim/advance machinery.
  if (run.status === "validating" || run.status === "correcting") {
    const complete = el("button", {}, t("run.btn.complete"));
    complete.addEventListener("click", async () => {
      if (!confirm("Force this run to 'done' and mark the spec validated?")) return;
      try {
        await api(`/api/projects/${pid}/runs/${rid}/complete`, { method: "POST" });
        toast("Run marked complete");
        refresh(mount, pid, rid);
      } catch (err) { toast(err.message, "error"); }
    });
    actionRow.appendChild(complete);
  }
  const previewBtn = el("button", {
    class: "secondary",
    title: t("run.btn.previewHelp"),
  }, t("run.btn.preview"));
  previewBtn.addEventListener("click", () => go(`/projects/${pid}/runs/${rid}/preview`));
  actionRow.appendChild(previewBtn);
  const back = el("button", { class: "secondary" }, t("run.btn.backToSpec"));
  back.addEventListener("click", () => go(`/projects/${pid}/specs/${run.spec_id}`));
  actionRow.appendChild(back);
  mount.appendChild(actionRow);

  // Surface a clear "awaiting review" hint when the run is parked.
  if (run.status === "validating") {
    mount.appendChild(el("p", { class: "muted", style: "margin:8px 0 0;font-size:12px" },
      "↳ Validator finished but not all criteria passed. Review the sensor grid below; post fix_required annotations to send the run back through the corrector, or click 'Mark complete' if you accept the result."));
  } else if (run.status === "correcting") {
    mount.appendChild(el("p", { class: "muted", style: "margin:8px 0 0;font-size:12px" },
      "↳ Run is correcting based on annotations. Wait for the runner to claim and re-execute, or 'Mark complete' to abandon the correction loop."));
  }

  // Two-column dashboard.
  const grid = el("div", { class: "run-grid", style: "margin-top:14px" });

  // Left: iteration timeline.
  const leftCol = el("div");
  leftCol.appendChild(el("h2", {}, "Iterations"));
  if (iterations.length === 0) {
    leftCol.appendChild(el("p", { class: "empty" },
      "No iterations yet. Once a runner claims this run, you'll see the planner output here first."));
  } else {
    for (const it of iterations) {
      const card = el("div", { class: "iteration " + it.phase }, [
        el("h4", {}, `#${it.idx} · ${it.phase} · ${it.agent_role}`),
        el("div", { class: "stamp" },
          `${new Date(it.started_at).toLocaleTimeString()} · ${it.provider}/${it.model || "?"} · in=${it.prompt_tokens} out=${it.completion_tokens}`),
        el("p", { style: "margin-top:6px" }, it.summary || el("span", { class: "muted" }, "(no summary)")),
      ]);
      // Expandable transcript so a human can read the full prompt + response
      // (or multi-turn conversation for the executor) without hitting the API.
      if (it.transcript) {
        const det = el("details", { style: "margin-top:6px" });
        det.appendChild(el("summary", { style: "cursor:pointer;color:var(--accent);font-size:12px" },
          "View transcript"));
        const pre = el("pre", { class: "mono", style: "white-space:pre-wrap;font-size:11px;max-height:360px;overflow:auto;background:var(--bg-elev);padding:8px;border-radius:4px;margin-top:6px" });
        pre.textContent = it.transcript;
        det.appendChild(pre);
        card.appendChild(det);
      }
      leftCol.appendChild(card);
    }
  }
  grid.appendChild(leftCol);

  // Right: sensor grid.
  const rightCol = el("div");
  rightCol.appendChild(el("h2", {}, "Sensors"));
  if (verifications.length === 0) {
    rightCol.appendChild(el("p", { class: "empty" },
      "No verifications posted yet. Sensors fire during the validate phase."));
  } else {
    const sg = el("div", { class: "sensor-grid" });
    for (const v of verifications) {
      const isImage = v.artifact_url && (v.kind === "screenshot" || v.kind === "visual_diff" || v.kind === "gif");
      const tile = el("div", { class: "sensor-tile" + (isImage ? " clickable" : "") }, [
        el("div", { class: "row" }, [
          el("span", { class: "kind" }, v.kind),
          el("span", { class: "spacer" }),
          el("span", { class: "pill " + statusClass(v.status) }, v.status),
        ]),
        el("p", {}, v.summary || el("span", { class: "muted" }, "(no summary)")),
      ]);
      if (isImage) {
        const img = el("img", { src: v.artifact_url, alt: v.kind });
        tile.appendChild(img);
        tile.appendChild(el("p", { class: "muted", style: "font-size:11px;margin:6px 0 0" },
          "Click to annotate"));
        tile.style.cursor = "pointer";
        const ctx = spec ? { specID: spec.id, criterion: critByID[v.criterion_id] } : {};
        tile.addEventListener("click", () => openAnnotator(pid, v, ctx));
      }

      // workspace_changes carries a git diff in details_json.diff that
      // makes the validator's verdict explicable. Show it inline in a
      // collapsible block so humans see what actually changed without
      // hitting the API directly.
      if (v.kind === "workspace_changes" && v.details_json && v.details_json.diff) {
        const det = el("details", { style: "margin-top:8px" });
        det.appendChild(el("summary", { style: "cursor:pointer;color:var(--accent);font-size:12px" },
          "View diff"));
        const pre = el("pre", { class: "mono", style: "white-space:pre-wrap;font-size:11px;max-height:320px;overflow:auto;background:var(--bg-elev);padding:8px;border-radius:4px;margin-top:6px" });
        pre.textContent = v.details_json.diff;
        det.appendChild(pre);
        tile.appendChild(det);
      }

      sg.appendChild(tile);
    }
    rightCol.appendChild(sg);
  }
  grid.appendChild(rightCol);

  mount.appendChild(grid);
}

function statusClass(s) {
  switch (s) {
    case "pass": return "pass";
    case "fail": return "fail";
    case "warn": return "warn";
    default: return "pending";
  }
}

// silence unused import lint via direct reference (escapeHTML may be used in future updates)
const _ = escapeHTML;
