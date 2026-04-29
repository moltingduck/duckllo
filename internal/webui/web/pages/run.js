import { api, events } from "/api.js";
import { go, el, escapeHTML } from "/router.js";
import { toast } from "/toast.js";
import { openAnnotator } from "/components/annotator.js";

let currentSource = null;

export async function render(mount, params) {
  if (currentSource) { currentSource.close(); currentSource = null; }
  const { pid, rid } = params;
  await refresh(mount, pid, rid);
  currentSource = events(pid);
  ["iteration.appended", "iteration.updated", "verification.posted", "annotation.added", "run.advanced"].forEach((t) => {
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

  mount.innerHTML = "";
  mount.appendChild(el("h1", {}, "Run " + run.id.slice(0, 8)));
  mount.appendChild(el("p", { class: "muted mono" },
    `status=${run.status} · turns=${run.turns_used}/${run.turn_budget} · tokens=${run.token_usage}`));

  const actionRow = el("div", { class: "row", style: "gap:8px" });
  if (!["done", "failed", "aborted"].includes(run.status)) {
    const abort = el("button", { class: "danger" }, "Abort run");
    abort.addEventListener("click", async () => {
      try { await api(`/api/projects/${pid}/runs/${rid}/abort`, { method: "POST" });
        toast("Run aborted"); refresh(mount, pid, rid);
      } catch (err) { toast(err.message, "error"); }
    });
    actionRow.appendChild(abort);
  }
  const back = el("button", { class: "secondary" }, "Back to spec");
  back.addEventListener("click", () => go(`/projects/${pid}/specs/${run.spec_id}`));
  actionRow.appendChild(back);
  mount.appendChild(actionRow);

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
        tile.addEventListener("click", () => openAnnotator(pid, v));
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
