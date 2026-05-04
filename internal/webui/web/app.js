// app.js — bootstrap, top-bar wiring, route table.

import { api, auth } from "/api.js";
import { on, start, go, el } from "/router.js";
import { t, getLang, setLang, LANGS } from "/i18n.js";

import * as login    from "/pages/login.js";
import * as projects from "/pages/projects.js";
import * as specs    from "/pages/specs.js";
import * as specNew  from "/pages/spec-new.js";
import * as spec     from "/pages/spec.js";
import * as run      from "/pages/run.js";
import * as preview  from "/pages/preview.js";
import * as steering from "/pages/steering.js";
import * as projectBar from "/components/project-bar.js";

on("/",                              gateRequiringAuth(projects.render));
on("/login",                         login.render);
on("/projects",                      gateRequiringAuth(projects.render));
on("/projects/:pid/specs",           gateRequiringAuth(specs.render));
on("/projects/:pid/specs/new",       gateRequiringAuth(specNew.render));
on("/projects/:pid/specs/:sid",      gateRequiringAuth(spec.render));
on("/projects/:pid/runs/:rid",       gateRequiringAuth(run.render));
on("/projects/:pid/runs/:rid/preview", gateRequiringAuth(preview.render));
on("/projects/:pid/steering",        gateRequiringAuth(steering.render));

function gateRequiringAuth(render) {
  return async (mount, params) => {
    if (!auth.token) return go("/login");
    try {
      // Probe identity. Any 401 means our token is dead.
      await api("/api/auth/me");
    } catch (err) {
      if (err.status === 401) {
        auth.clear();
        return go("/login");
      }
      throw err;
    }
    return render(mount, params);
  };
}

function renderLangPicker() {
  // Inline dropdown rendered as a tiny <select>. Every change writes
  // localStorage and broadcasts a `langchange` event the pages listen
  // to, so switching language re-renders the current view without a
  // page refresh.
  const sel = el("select", { class: "lang-picker", title: "UI language" });
  for (const l of LANGS) {
    const opt = el("option", { value: l.code }, l.short);
    if (l.code === getLang()) opt.setAttribute("selected", "");
    sel.appendChild(opt);
  }
  sel.addEventListener("change", () => setLang(sel.value));
  return sel;
}

async function paintTopbar() {
  const userbox = document.getElementById("userbox");
  if (!auth.token) {
    userbox.innerHTML = "";
    userbox.appendChild(renderLangPicker());
    userbox.appendChild(document.createTextNode(" "));
    userbox.appendChild(el("a", { href: "#/login" }, t("nav.signin")));
    return;
  }
  try {
    const me = await api("/api/auth/me");
    userbox.innerHTML = "";
    userbox.appendChild(renderLangPicker());
    userbox.appendChild(document.createTextNode(" "));
    userbox.appendChild(el("span", { class: "me" }, me.username));
    userbox.appendChild(document.createTextNode(" · "));
    const out = el("a", { href: "#" }, t("nav.logout"));
    out.addEventListener("click", async (e) => {
      e.preventDefault();
      try { await api("/api/auth/logout", { method: "POST" }); } catch (_) {}
      auth.clear();
      go("/login");
      paintTopbar();
    });
    userbox.appendChild(out);
  } catch (_) {
    auth.clear();
    userbox.innerHTML = "";
    userbox.appendChild(renderLangPicker());
    userbox.appendChild(document.createTextNode(" "));
    userbox.appendChild(el("a", { href: "#/login" }, t("nav.signin")));
  }
}

// Language change re-renders the topbar AND triggers the current page's
// dispatch (router.dispatch is exposed via a simple reload-route trick:
// fire a hashchange-equivalent so the active page module re-renders
// from scratch with the new strings.).
window.addEventListener("langchange", () => {
  paintTopbar();
  projectBar.refresh();
  window.dispatchEvent(new Event("hashchange"));
});

window.addEventListener("hashchange", () => { paintTopbar(); projectBar.refresh(); });
paintTopbar();
projectBar.refresh();
start(document.getElementById("app"));
