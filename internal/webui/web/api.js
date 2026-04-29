// api.js — thin wrapper around fetch + bearer auth + JSON.
// Auth token lives in localStorage as "duckllo.token". An API key issued
// via /api/projects/{pid}/api-keys can also be pasted in to drive the UI
// as an agent (handy during development).

const TOKEN_KEY = "duckllo.token";

export const auth = {
  get token() { return localStorage.getItem(TOKEN_KEY) || ""; },
  set(t) { localStorage.setItem(TOKEN_KEY, t || ""); },
  clear() { localStorage.removeItem(TOKEN_KEY); },
};

export async function api(path, opts = {}) {
  const headers = new Headers(opts.headers || {});
  if (auth.token) headers.set("Authorization", "Bearer " + auth.token);
  if (opts.body && !(opts.body instanceof FormData)) {
    headers.set("Content-Type", "application/json");
    if (typeof opts.body !== "string") opts.body = JSON.stringify(opts.body);
  }
  const res = await fetch(path, { ...opts, headers });
  if (res.status === 204) return null;
  const ct = res.headers.get("Content-Type") || "";
  const body = ct.includes("application/json") ? await res.json() : await res.text();
  if (!res.ok) {
    const err = new Error((body && body.error) || res.statusText);
    err.status = res.status;
    err.body = body;
    throw err;
  }
  return body;
}

// Convenience helpers.
export const get   = (p)        => api(p);
export const post  = (p, body)  => api(p, { method: "POST", body });
export const patch = (p, body)  => api(p, { method: "PATCH", body });
export const del   = (p)        => api(p, { method: "DELETE" });

// Server-Sent Events: returns an EventSource. Token goes in the query
// string because EventSource cannot set custom headers.
export function events(projectID) {
  const url = `/api/projects/${projectID}/events?token=${encodeURIComponent(auth.token)}`;
  return new EventSource(url);
}
