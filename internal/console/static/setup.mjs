// SPDX-License-Identifier: AGPL-3.0-only

// The first-run setup page (/console/setup). It is the one console page that
// writes: a single POST /v1/setup with the setup token the operator typed,
// which creates the instance's first tenant and returns its API key once.
// It sends no cookie and no bearer key, calls only that fixed path on this
// origin, and shows the key with textContent. The helpers above main() are
// pure and tested with `node --test`.

// KEY_STORAGE is the console's sessionStorage key (lib.mjs): the new key is
// kept there so /console opens signed in, in this tab only.
export const KEY_STORAGE = "ohs.console.key";

// SETUP_PATH is the only URL this page calls.
export const SETUP_PATH = "/v1/setup";

// MAX_TENANT_NAME mirrors the server's limit, in bytes.
export const MAX_TENANT_NAME = 100;

// SetupError is a refused or failed setup call; message is for the operator.
export class SetupError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

// setupMessage explains a non-2xx answer of /v1/setup.
export function setupMessage(status, detail) {
  switch (status) {
    case 401:
      return "This setup token was refused. It is the OHS_SETUP_TOKEN of this instance.";
    case 409:
      return "This instance is already set up.";
    case 404:
      return "The first-run setup is off on this instance (OHS_SETUP_TOKEN is not set).";
    case 400:
      return `The request was refused${detail ? ": " + detail : "."}`;
    case 413:
      return "The tenant name is too long.";
    default:
      return `Setup failed (HTTP ${status})${detail ? ": " + detail : ""}. Try again.`;
  }
}

// buildSetupRequest checks the form values and returns the JSON body, or
// throws a SetupError naming the problem. Surrounding spaces are trimmed
// (pasted tokens often carry some); an empty name is left out, so the
// server picks "default".
export function buildSetupRequest(token, name) {
  const t = String(token ?? "").trim();
  if (!t) throw new SetupError(0, "Enter the setup token.");
  const n = String(name ?? "").trim();
  if (new TextEncoder().encode(n).length > MAX_TENANT_NAME) {
    throw new SetupError(0, `The tenant name must be at most ${MAX_TENANT_NAME} bytes.`);
  }
  if (/[\u0000-\u001f\u007f-\u009f]/.test(n)) {
    throw new SetupError(0, "The tenant name must not contain control characters.");
  }
  const body = { setup_token: t };
  if (n) body.tenant_name = n;
  return JSON.stringify(body);
}

// isApiKey accepts what the server returns as a tenant key, nothing else.
export function isApiKey(v) {
  return typeof v === "string" && /^ohs_sk_[A-Za-z0-9_-]{16,}$/.test(v);
}

// setupState reads GET /v1/setup: "needed", "done", or "off" (404, the
// route is not mounted). Anything else throws a SetupError.
export function setupState(status, body) {
  if (status === 404) return "off";
  if (status !== 200 || !body || typeof body.needed !== "boolean") {
    throw new SetupError(status, setupMessage(status, ""));
  }
  return body.needed ? "needed" : "done";
}

// makeSetupClient returns {state(), create(token, name)} over fetchImpl. Both
// call SETUP_PATH on this origin only, with no cookie, no cache and no
// redirect followed.
export function makeSetupClient(fetchImpl) {
  const base = { credentials: "omit", cache: "no-store", redirect: "error", referrerPolicy: "no-referrer" };
  async function readJSON(res) {
    try {
      return await res.json();
    } catch {
      return null;
    }
  }
  return {
    async state() {
      const res = await fetchImpl(SETUP_PATH, { ...base, method: "GET", headers: { Accept: "application/json" } });
      return setupState(res.status, await readJSON(res));
    },
    async create(token, name) {
      const body = buildSetupRequest(token, name);
      const res = await fetchImpl(SETUP_PATH, {
        ...base,
        method: "POST",
        headers: { Accept: "application/json", "Content-Type": "application/json" },
        body,
      });
      const out = await readJSON(res);
      if (res.status !== 201) {
        const detail = out && typeof out === "object" ? String(out.detail || "") : "";
        throw new SetupError(res.status, setupMessage(res.status, detail));
      }
      if (!out || !isApiKey(out.api_key) || typeof out.tenant_id !== "string") {
        throw new SetupError(res.status, "The server answered with no API key. Create one with `ohs admin key create`.");
      }
      return { tenantId: out.tenant_id, apiKey: out.api_key };
    },
  };
}

// ---------------------------------------------------------------------------
// DOM

function storeKey(key) {
  try {
    window.sessionStorage.setItem(KEY_STORAGE, key);
  } catch {
    // Storage blocked: the operator signs in to the console by hand.
  }
}

function main() {
  const client = makeSetupClient((u, o) => window.fetch(u, o));
  const $ = (id) => document.getElementById(id);
  const loading = $("setup-loading");
  const done = $("setup-done");
  const off = $("setup-off");
  const form = $("setup-form");
  const tokenInput = $("setup-token");
  const nameInput = $("tenant-name");
  const submit = $("setup-submit");
  const error = $("setup-error");
  const result = $("setup-result");
  const keyOut = $("result-key");
  const copyStatus = $("copy-status");

  function show(which) {
    loading.hidden = true;
    done.hidden = which !== "done";
    off.hidden = which !== "off";
    form.hidden = which !== "needed";
    result.hidden = which !== "result";
  }

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    error.textContent = "";
    submit.disabled = true;
    try {
      const { tenantId, apiKey } = await client.create(tokenInput.value, nameInput.value);
      tokenInput.value = "";
      storeKey(apiKey);
      $("result-tenant").textContent = tenantId;
      keyOut.textContent = apiKey;
      show("result");
    } catch (err) {
      if (err instanceof SetupError && err.status === 409) {
        tokenInput.value = "";
        show("done");
        return;
      }
      error.textContent = err instanceof SetupError ? err.message : `Setup failed: ${err.message}`;
    } finally {
      submit.disabled = false;
    }
  });

  $("copy-key").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(keyOut.textContent);
      copyStatus.textContent = "Copied.";
    } catch {
      // No clipboard access (plain http, for example): select the key.
      const range = document.createRange();
      range.selectNodeContents(keyOut);
      const sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
      copyStatus.textContent = "Selected: copy it with your keyboard.";
    }
  });

  client.state().then(
    (state) => {
      show(state);
      if (state === "needed") tokenInput.focus();
    },
    (err) => {
      loading.hidden = true;
      error.textContent = err instanceof SetupError ? err.message : `Cannot reach this instance: ${err.message}`;
    },
  );
}

if (typeof document !== "undefined") {
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", main);
  else main();
}
