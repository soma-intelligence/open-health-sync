// SPDX-License-Identifier: AGPL-3.0-only

// The read-only admin console. It calls the existing API with GET only, on
// the same origin, with the tenant key the operator pasted (kept in
// sessionStorage). Every value from the API reaches the page through
// textContent or an attribute set on a created element, never as markup.
// The helpers above main() are pure and tested with `node --test`.

export const KEY_STORAGE = "ohs.console.key";

// ReadOnlyError is thrown by the fetch wrapper for anything but a GET.
export class ReadOnlyError extends Error {}

// ApiError is a non-2xx answer; detail comes from the problem+json body.
export class ApiError extends Error {
  constructor(status, detail) {
    super(`HTTP ${status}${detail ? ": " + detail : ""}`);
    this.status = status;
    this.detail = detail;
  }
}

// makeGet returns get(path, opts), the only way the console talks to the
// server. It refuses any method other than GET, any body, and any URL that
// is not a path on this origin; it sends the key as a bearer token unless
// opts.auth is false.
export function makeGet(fetchImpl, getKey) {
  return async function get(path, opts = {}) {
    const method = String(opts.method ?? "GET").toUpperCase();
    if (method !== "GET") {
      throw new ReadOnlyError(`the console is read-only: ${method} refused`);
    }
    if (opts.body !== undefined && opts.body !== null) {
      throw new ReadOnlyError("the console is read-only: a request body is refused");
    }
    if (typeof path !== "string" || !path.startsWith("/") || path.startsWith("//") || path.includes("\\")) {
      throw new ReadOnlyError("the console only calls paths on this server");
    }
    const headers = { Accept: "application/json" };
    if (opts.auth !== false) {
      const key = getKey();
      if (key) headers.Authorization = "Bearer " + key;
    }
    const res = await fetchImpl(path, {
      method: "GET",
      headers,
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      referrerPolicy: "no-referrer",
    });
    let body = null;
    try {
      body = await res.json();
    } catch {
      body = null;
    }
    if (!res.ok) {
      const detail = body && typeof body === "object" ? body.detail || body.title || "" : "";
      throw new ApiError(res.status, String(detail));
    }
    return body;
  };
}

// formatAgo turns a duration in seconds into "45 s", "12 min", "3 h 5 min",
// "2 d 4 h". Negative durations (a time in the future) read "in ...".
export function formatAgo(seconds) {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds)) return "-";
  const future = seconds < 0;
  let s = Math.round(Math.abs(seconds));
  let out;
  if (s < 60) out = `${s} s`;
  else if (s < 3600) out = `${Math.floor(s / 60)} min`;
  else if (s < 86400) {
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    out = m ? `${h} h ${m} min` : `${h} h`;
  } else {
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    out = h ? `${d} d ${h} h` : `${d} d`;
  }
  return future ? `in ${out}` : out;
}

// formatTime renders an RFC 3339 time as "2026-10-13 08:30 UTC (3 h ago)".
// null or an unparsable value reads "-" or the raw text.
export function formatTime(iso, now = Date.now()) {
  if (iso === null || iso === undefined || iso === "") return "-";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return String(iso);
  const stamp = new Date(t).toISOString().slice(0, 16).replace("T", " ") + " UTC";
  const ago = (now - t) / 1000;
  const rel = ago >= 0 ? `${formatAgo(ago)} ago` : formatAgo(ago);
  return `${stamp} (${rel})`;
}

// Classes, used as CSS class names: ok (green), warn (amber), bad (red),
// off (grey).

// classifyFreshness maps a SourceFreshness to a class: stale, failing and
// needs_reauth are bad, syncing is warn, disconnected is off.
export function classifyFreshness(src) {
  switch (src && src.state) {
    case "fresh":
      return (src.consecutive_failures || 0) > 0 ? "warn" : "ok";
    case "syncing":
      return "warn";
    case "disconnected":
      return "off";
    case "stale":
    case "failing":
    case "needs_reauth":
      return "bad";
    default:
      return "warn";
  }
}

// classifyConnection maps a Connection status to a class.
export function classifyConnection(conn) {
  switch (conn && conn.status) {
    case "active":
      return "ok";
    case "needs_reauth":
      return "bad";
    case "disconnected":
      return "off";
    default:
      return "warn";
  }
}

// classifyWebhook: a disabled endpoint or one failing right now is bad.
export function classifyWebhook(w) {
  if (!w) return "warn";
  if (w.status === "disabled" || w.failing_since) return "bad";
  if (w.status === "active") return "ok";
  return "warn";
}

// classifyDelivery: parked is bad, a retrying delivery is warn, done is ok.
export function classifyDelivery(d) {
  if (!d) return "warn";
  if (d.status === "parked") return "bad";
  if (d.status === "done") return "ok";
  if (d.last_error || (d.attempts || 0) > 0) return "warn";
  return "ok";
}

// classifyUser: a user being deleted is off.
export function classifyUser(u) {
  return u && u.status === "active" ? "ok" : "off";
}

// apiPath joins segments, each escaped, into a path, with an
// optional query object (null and undefined values are left out).
export function apiPath(segments, query) {
  const path = "/" + segments.map((s) => encodeURIComponent(String(s))).join("/");
  if (!query) return path;
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v !== null && v !== undefined && v !== "") qs.set(k, String(v));
  }
  const s = qs.toString();
  return s ? `${path}?${s}` : path;
}

// parseRoute reads the location hash: #/overview, #/users, #/users/<id>,
// #/webhooks, #/webhooks/<id>.
export function parseRoute(hash) {
  const parts = String(hash || "")
    .replace(/^#\/?/, "")
    .split("/")
    .filter(Boolean)
    .map((p) => {
      try {
        return decodeURIComponent(p);
      } catch {
        return p;
      }
    });
  const [view, id] = parts;
  if (view === "users") return id ? { view: "user", id } : { view: "users" };
  if (view === "webhooks") return id ? { view: "webhook", id } : { view: "webhooks" };
  return { view: "overview" };
}

// ---------------------------------------------------------------------------
// DOM

function storage() {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

function getKey() {
  try {
    return storage()?.getItem(KEY_STORAGE) || "";
  } catch {
    return "";
  }
}

function setKey(v) {
  try {
    if (v) storage()?.setItem(KEY_STORAGE, v);
    else storage()?.removeItem(KEY_STORAGE);
  } catch {
    // Storage blocked: the key lives only until the next reload.
  }
}

// el builds an element. Text children become text nodes; attrs are set as
// attributes (no event handler attributes: listeners are added in code).
function el(tag, attrs, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") e.className = v;
    else e.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

function badge(cls, text) {
  return el("span", { class: `badge ${cls}` }, text ?? "-");
}

function table(headers, rows, empty) {
  if (!rows.length) return el("p", { class: "muted" }, empty);
  return el(
    "div",
    { class: "scroll" },
    el("table", {}, el("thead", {}, el("tr", {}, headers.map((h) => el("th", {}, h)))), el("tbody", {}, rows)),
  );
}

function link(href, text) {
  return el("a", { href }, text);
}

function errorBox(err) {
  return el("p", { class: "bad", role: "alert" }, err instanceof Error ? err.message : String(err));
}

function main() {
  const get = makeGet((u, o) => window.fetch(u, o), getKey);
  const view = document.getElementById("view");
  const signin = document.getElementById("signin");
  const nav = document.getElementById("nav");
  const signout = document.getElementById("signout");
  const form = document.getElementById("signin-form");
  const keyInput = document.getElementById("key");
  const signinError = document.getElementById("signin-error");
  let renderSeq = 0;
  // Cursor stack of the users view: page n starts at cursors[n].
  let userCursors = [null];

  function showSignin(message) {
    signin.hidden = false;
    nav.hidden = true;
    signout.hidden = true;
    view.replaceChildren();
    signinError.textContent = message || "";
    keyInput.focus();
  }

  function signOut() {
    setKey("");
    userCursors = [null];
    keyInput.value = "";
    showSignin("");
  }

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const key = keyInput.value.trim();
    keyInput.value = "";
    if (!key) return;
    setKey(key);
    try {
      await get("/v1/providers");
    } catch (err) {
      setKey("");
      showSignin(err instanceof ApiError && (err.status === 401 || err.status === 403)
        ? "This key was refused. A tenant API key is needed (not a user token)."
        : `Sign in failed: ${err.message}`);
      return;
    }
    render();
  });
  signout.addEventListener("click", signOut);
  window.addEventListener("hashchange", render);

  async function render() {
    if (!getKey()) {
      showSignin("");
      return;
    }
    signin.hidden = true;
    nav.hidden = false;
    signout.hidden = false;
    const seq = ++renderSeq;
    const route = parseRoute(window.location.hash);
    for (const a of nav.querySelectorAll("a")) {
      const target = parseRoute(a.getAttribute("href")).view;
      a.classList.toggle("active", route.view.startsWith(target.replace(/s$/, "")));
    }
    view.replaceChildren(el("p", { class: "muted" }, "Loading..."));
    let content;
    try {
      if (route.view === "users") content = await usersView();
      else if (route.view === "user") content = await userView(route.id);
      else if (route.view === "webhooks") content = await webhooksView();
      else if (route.view === "webhook") content = await webhookView(route.id);
      else content = await overviewView();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setKey("");
        showSignin("The key is no longer accepted. Sign in again.");
        return;
      }
      content = errorBox(err);
    }
    if (seq !== renderSeq) return; // a later navigation won
    view.replaceChildren(content);
  }

  // settle runs a GET and returns {ok, value} or {ok: false, error}, so one
  // failing call does not blank a whole view.
  async function settle(path, opts) {
    try {
      return { ok: true, value: await get(path, opts) };
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) throw error;
      return { ok: false, error };
    }
  }

  async function countUsers() {
    let n = 0;
    let cursor = null;
    for (let page = 0; page < 10; page++) {
      const body = await get(apiPath(["v1", "users"], { limit: 1000, cursor }));
      n += (body.data || []).length;
      cursor = body.next_cursor;
      if (!cursor) return String(n);
    }
    return `${n}+`;
  }

  async function overviewView() {
    const [healthz, readyz, providers, users, hooks] = await Promise.all([
      settle("/healthz", { auth: false }),
      settle("/readyz", { auth: false }),
      settle("/v1/providers"),
      countUsers().then((v) => ({ ok: true, value: v }), (error) => ({ ok: false, error })),
      settle("/v1/webhooks"),
    ]);
    const health = (r, okText) =>
      r.ok ? badge("ok", okText) : badge("bad", r.error.message || "failing");
    const failingHooks = hooks.ok ? (hooks.value.data || []).filter((w) => classifyWebhook(w) === "bad") : [];
    return el(
      "div",
      {},
      el("h1", {}, "Overview"),
      el(
        "dl",
        { class: "facts" },
        el("dt", {}, "Liveness (/healthz)"),
        el("dd", {}, health(healthz, "ok")),
        el("dt", {}, "Readiness (/readyz)"),
        el("dd", {}, health(readyz, "ready")),
        el("dt", {}, "Users"),
        el("dd", {}, users.ok ? link("#/users", users.value) : errorBox(users.error)),
        el("dt", {}, "Webhook endpoints"),
        el(
          "dd",
          {},
          hooks.ok
            ? [
                link("#/webhooks", String((hooks.value.data || []).length)),
                failingHooks.length ? [" ", badge("bad", `${failingHooks.length} failing or disabled`)] : null,
              ]
            : errorBox(hooks.error),
        ),
      ),
      el("h2", {}, "Providers enabled"),
      providers.ok
        ? table(
            ["Provider", "Kinds"],
            (providers.value.data || []).map((p) => el("tr", {}, el("td", {}, p.slug), el("td", {}, (p.kinds || []).join(", ")))),
            "No provider is enabled on this instance.",
          )
        : errorBox(providers.error),
    );
  }

  async function usersView() {
    const page = Math.max(0, userCursors.length - 1);
    const body = await get(apiPath(["v1", "users"], { limit: 50, cursor: userCursors[page] }));
    const rows = (body.data || []).map((u) =>
      el(
        "tr",
        {},
        el("td", {}, link(`#/users/${encodeURIComponent(u.id)}`, u.id)),
        el("td", {}, u.external_ref ?? "-"),
        el("td", {}, formatTime(u.created_at)),
        el("td", {}, badge(classifyUser(u), u.status)),
      ),
    );
    const prev = el("button", { type: "button", disabled: page === 0 }, "Previous");
    const next = el("button", { type: "button", disabled: !body.next_cursor }, "Next");
    prev.addEventListener("click", () => {
      if (userCursors.length > 1) userCursors.pop();
      render();
    });
    next.addEventListener("click", () => {
      if (body.next_cursor) userCursors.push(body.next_cursor);
      render();
    });
    return el(
      "div",
      {},
      el("h1", {}, "Users"),
      table(["ID", "External ref", "Created", "Status"], rows, "No user yet."),
      el("p", { class: "pager" }, prev, ` Page ${page + 1} `, next),
    );
  }

  async function userView(id) {
    const user = await get(apiPath(["v1", "users", id]));
    const [conns, fresh, cov] = await Promise.all([
      settle(apiPath(["v1", "users", id, "connections"])),
      settle(apiPath(["v1", "users", id, "freshness"])),
      settle(apiPath(["v1", "users", id, "coverage"])),
    ]);
    const connRows = conns.ok
      ? (conns.value.data || []).map((c) =>
          el(
            "tr",
            { class: c.status === "needs_reauth" ? "row-bad" : null },
            el("td", {}, c.provider),
            el("td", {}, badge(classifyConnection(c), c.status)),
            el("td", {}, formatTime(c.last_sync_at)),
            el("td", {}, formatTime(c.initial_backfill_at)),
            el("td", {}, formatTime(c.created_at)),
          ),
        )
      : null;
    const freshRows = fresh.ok
      ? (fresh.value.data || []).map((s) => {
          const cls = classifyFreshness(s);
          return el(
            "tr",
            { class: cls === "bad" ? "row-bad" : null },
            el("td", {}, s.provider),
            el("td", {}, s.kind),
            el("td", {}, badge(cls, s.state)),
            el("td", {}, formatTime(s.synced_through)),
            el("td", {}, formatAgo(s.lag_seconds)),
            el("td", {}, formatTime(s.last_attempt_at)),
            el("td", {}, String(s.consecutive_failures ?? 0)),
            el("td", { class: s.last_error ? "bad" : "muted" }, s.last_error ?? "-"),
          );
        })
      : null;
    let coverage;
    if (cov.ok) {
      const c = cov.value;
      const byProvider = new Map();
      for (const d of c.days || []) {
        const p = byProvider.get(d.provider) || { days: 0, records: 0, first: d.date, last: d.date, synced: d.last_synced_at };
        p.days++;
        p.records += d.records || 0;
        if (d.date < p.first) p.first = d.date;
        if (d.date > p.last) p.last = d.date;
        if (d.last_synced_at > p.synced) p.synced = d.last_synced_at;
        byProvider.set(d.provider, p);
      }
      coverage = el(
        "div",
        {},
        el(
          "dl",
          { class: "facts" },
          el("dt", {}, "Pending ETL jobs"),
          el("dd", {}, badge(c.pending_jobs > 0 ? "warn" : "ok", String(c.pending_jobs))),
          el("dt", {}, "Parked ETL jobs"),
          el("dd", {}, badge(c.parked_jobs > 0 ? "bad" : "ok", String(c.parked_jobs))),
          el("dt", {}, "Rejected records"),
          el("dd", {}, badge(c.rejected_records > 0 ? "bad" : "ok", String(c.rejected_records))),
        ),
        table(
          ["Provider", "Days", "Records", "First day", "Last day", "Last synced"],
          [...byProvider.entries()].map(([prov, p]) =>
            el(
              "tr",
              {},
              el("td", {}, prov),
              el("td", {}, String(p.days)),
              el("td", {}, String(p.records)),
              el("td", {}, p.first),
              el("td", {}, p.last),
              el("td", {}, formatTime(p.synced)),
            ),
          ),
          "No data has landed yet.",
        ),
        c.truncated ? el("p", { class: "warn" }, "More than 10000 days: the totals cover the earliest ones.") : null,
      );
    }
    return el(
      "div",
      {},
      el("p", {}, link("#/users", "All users")),
      el("h1", {}, "User ", el("code", {}, user.id)),
      el(
        "dl",
        { class: "facts" },
        el("dt", {}, "External ref"),
        el("dd", {}, user.external_ref ?? "-"),
        el("dt", {}, "Status"),
        el("dd", {}, badge(classifyUser(user), user.status)),
        el("dt", {}, "Created"),
        el("dd", {}, formatTime(user.created_at)),
      ),
      el("h2", {}, "Connections"),
      conns.ok
        ? table(["Provider", "Status", "Last sync", "Backfill done", "Connected"], connRows, "No provider connection.")
        : errorBox(conns.error),
      el("h2", {}, "Freshness"),
      fresh.ok
        ? [
            el("p", { class: "muted" }, `As of ${formatTime(fresh.value.as_of)}`),
            table(
              ["Source", "Kind", "State", "Synced through", "Lag", "Last attempt", "Failures", "Last error"],
              freshRows,
              "No source yet.",
            ),
          ]
        : errorBox(fresh.error),
      el("h2", {}, "Coverage"),
      cov.ok ? coverage : errorBox(cov.error),
    );
  }

  async function webhooksView() {
    const body = await get("/v1/webhooks");
    const rows = (body.data || []).map((w) =>
      el(
        "tr",
        { class: classifyWebhook(w) === "bad" ? "row-bad" : null },
        el("td", {}, link(`#/webhooks/${encodeURIComponent(w.id)}`, w.url)),
        el("td", {}, w.description || "-"),
        el("td", {}, badge(classifyWebhook(w), w.status)),
        el("td", { class: w.failing_since ? "bad" : "muted" }, formatTime(w.failing_since)),
        el("td", {}, formatTime(w.last_success_at)),
        el("td", {}, w.disabled_reason ?? "-"),
      ),
    );
    return el(
      "div",
      {},
      el("h1", {}, "Webhooks"),
      table(["URL", "Description", "Status", "Failing since", "Last success", "Disabled reason"], rows, "No webhook endpoint."),
    );
  }

  async function webhookView(id) {
    const [hook, deliveries] = await Promise.all([
      get(apiPath(["v1", "webhooks", id])).catch((err) => {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }),
      settle(apiPath(["v1", "webhooks", id, "deliveries"], { limit: 50 })),
    ]);
    if (!hook) return errorBox("Webhook endpoint not found.");
    const rows = deliveries.ok
      ? (deliveries.value.data || []).map((d) => {
          const cls = classifyDelivery(d);
          return el(
            "tr",
            { class: cls === "bad" ? "row-bad" : null },
            el("td", {}, String(d.id)),
            el("td", {}, d.event_type),
            el("td", {}, badge(cls, d.status)),
            el("td", {}, String(d.attempts ?? 0)),
            el("td", {}, d.last_status_code === null || d.last_status_code === undefined ? "-" : String(d.last_status_code)),
            el("td", { class: d.last_error ? "bad" : "muted" }, d.last_error ?? "-"),
            el("td", {}, formatTime(d.created_at)),
            el("td", {}, formatTime(d.next_attempt_at)),
            el("td", {}, formatTime(d.delivered_at)),
          );
        })
      : null;
    return el(
      "div",
      {},
      el("p", {}, link("#/webhooks", "All webhooks")),
      el("h1", {}, "Webhook ", el("code", {}, hook.url)),
      el(
        "dl",
        { class: "facts" },
        el("dt", {}, "Status"),
        el("dd", {}, badge(classifyWebhook(hook), hook.status)),
        el("dt", {}, "Event types"),
        el("dd", {}, (hook.event_types || []).length ? hook.event_types.join(", ") : "every type"),
        el("dt", {}, "Failing since"),
        el("dd", { class: hook.failing_since ? "bad" : null }, formatTime(hook.failing_since)),
        el("dt", {}, "Last success"),
        el("dd", {}, formatTime(hook.last_success_at)),
        el("dt", {}, "Disabled reason"),
        el("dd", {}, hook.disabled_reason ?? "-"),
      ),
      el("h2", {}, "Recent deliveries"),
      deliveries.ok
        ? table(
            ["ID", "Event", "Status", "Attempts", "Last code", "Last error", "Created", "Next attempt", "Delivered"],
            rows,
            "No delivery in the last 30 days.",
          )
        : errorBox(deliveries.error),
    );
  }

  render();
}

if (typeof document !== "undefined") {
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", main);
  else main();
}
