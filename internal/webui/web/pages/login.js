import { api, auth } from "/api.js";
import { go, el } from "/router.js";
import { toast } from "/toast.js";

export async function render(mount) {
  const status = await api("/api/status");
  const firstUser = status.needs_first_user;

  mount.innerHTML = "";
  const card = el("div", { class: "card", style: "max-width:380px;margin:60px auto" });
  card.appendChild(el("h1", {}, firstUser ? "Welcome — set up duckllo" : "Sign in"));
  if (firstUser) {
    card.appendChild(el("p", { class: "muted" },
      "No users yet. The first account becomes the admin."));
  } else if (!status.gin_present) {
    card.appendChild(el("p", { class: "muted" },
      "Heads-up: gin steward account is missing. Phase 1 still works, but project auto-attach won't fire."));
  }

  const usernameInput = el("input", { type: "text", placeholder: "Username", autocomplete: "username" });
  const passwordInput = el("input", { type: "password", placeholder: "Password (≥6 chars)", autocomplete: "current-password" });
  const displayInput = el("input", { type: "text", placeholder: "Display name (optional)" });
  const errBox = el("div", { class: "error" });

  const btn = el("button", {}, firstUser ? "Create admin" : "Sign in");
  const altBtn = el("button", { class: "secondary" }, firstUser ? "I already have an account" : "Register a new user");

  let mode = firstUser ? "register" : "login";

  altBtn.addEventListener("click", () => {
    mode = mode === "login" ? "register" : "login";
    btn.textContent = mode === "register" ? "Create account" : "Sign in";
    altBtn.textContent = mode === "register" ? "I already have an account" : "Register a new user";
    displayInput.style.display = mode === "register" ? "" : "none";
  });
  if (!firstUser) displayInput.style.display = "none";

  btn.addEventListener("click", async () => {
    errBox.textContent = "";
    try {
      const path = mode === "register" ? "/api/auth/register" : "/api/auth/login";
      const body = mode === "register"
        ? { username: usernameInput.value.trim(), password: passwordInput.value, display_name: displayInput.value.trim() }
        : { username: usernameInput.value.trim(), password: passwordInput.value };
      const resp = await api(path, { method: "POST", body });
      auth.set(resp.token);
      toast("Signed in as " + resp.username);
      go("/projects");
    } catch (err) {
      errBox.textContent = err.message;
    }
  });

  card.appendChild(el("label", {}, "Username"));
  card.appendChild(usernameInput);
  card.appendChild(el("label", {}, "Password"));
  card.appendChild(passwordInput);
  card.appendChild(el("label", { id: "display-label" }, "Display name"));
  card.appendChild(displayInput);
  card.appendChild(errBox);
  card.appendChild(el("div", { class: "row", style: "margin-top:14px;gap:8px" }, [btn, altBtn]));

  mount.appendChild(card);
  mount.appendChild(el("p", { class: "muted mono", style: "text-align:center;margin-top:16px;font-size:11px" },
    `duckllo ${status.version} · schema ${status.schema_version || "uninitialised"} · uptime ${status.uptime_seconds}s`));
}
