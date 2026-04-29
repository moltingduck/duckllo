// toast.js — minimal status notifications. Intentionally CSS-driven; no
// animation library, no per-toast lifecycle queue. Last call wins.

const toastEl = document.getElementById("toast");
let timer = null;

export function toast(msg, kind = "info") {
  toastEl.textContent = msg;
  toastEl.classList.toggle("error", kind === "error");
  toastEl.classList.add("show");
  clearTimeout(timer);
  timer = setTimeout(() => toastEl.classList.remove("show"), 3500);
}
