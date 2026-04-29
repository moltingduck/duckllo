// router.js — hash-based URL routing. Each route maps a regex over the
// URL hash (window.location.hash, e.g. "#/projects/abc/specs") to a render
// function. Render functions are async and receive (mountEl, params).

const routes = [];

export function on(pattern, render) {
  // pattern like "/projects/:pid/specs/:sid". Convert to RegExp.
  const keys = [];
  const re = pattern.replace(/:([a-zA-Z]+)/g, (_, k) => {
    keys.push(k);
    return "([^/]+)";
  });
  routes.push({ re: new RegExp("^" + re + "$"), keys, render });
}

let currentMount = null;
let currentRender = null;

export async function start(mountEl) {
  currentMount = mountEl;
  window.addEventListener("hashchange", dispatch);
  await dispatch();
}

export function go(path) {
  if (window.location.hash === "#" + path) return dispatch();
  window.location.hash = path;
}

async function dispatch() {
  const path = window.location.hash.slice(1) || "/";
  for (const r of routes) {
    const m = path.match(r.re);
    if (m) {
      const params = {};
      r.keys.forEach((k, i) => { params[k] = decodeURIComponent(m[i + 1]); });
      currentRender = r.render;
      currentMount.innerHTML = '<p class="loading">Loading…</p>';
      try {
        await r.render(currentMount, params);
      } catch (err) {
        currentMount.innerHTML = `<p class="error">${escapeHTML(err.message)}</p>`;
      }
      return;
    }
  }
  currentMount.innerHTML = `<p class="error">No route for ${escapeHTML(path)}</p>`;
}

export function escapeHTML(s) {
  return String(s)
    .replace(/&/g, "&amp;").replace(/</g, "&lt;")
    .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

export function el(tag, attrs = {}, children = []) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on") && typeof v === "function") {
      node.addEventListener(k.slice(2).toLowerCase(), v);
    } else if (v !== false && v != null) {
      node.setAttribute(k, v);
    }
  }
  for (const c of [].concat(children)) {
    if (c == null || c === false) continue;
    node.appendChild(c instanceof Node ? c : document.createTextNode(c));
  }
  return node;
}
