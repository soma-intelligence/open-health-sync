// SPDX-License-Identifier: AGPL-3.0-only

// Tests of the setup page's pure helpers: node --test internal/console/setup.test.mjs
// (no dependency, no DOM; the module only starts the page when document exists).

import { test } from "node:test";
import assert from "node:assert/strict";

import { KEY_STORAGE as CONSOLE_KEY_STORAGE } from "./static/lib.mjs";
import {
  KEY_STORAGE,
  SETUP_PATH,
  SetupError,
  buildSetupRequest,
  isApiKey,
  makeSetupClient,
  setupMessage,
  setupState,
} from "./static/setup.mjs";

const KEY = "ohs_sk_" + "A".repeat(43);

function fakeFetch(status, body) {
  const calls = [];
  const impl = async (url, init) => {
    calls.push({ url, init });
    return { ok: status >= 200 && status < 300, status, json: async () => body };
  };
  return { impl, calls };
}

test("the key lands where the console reads it", () => {
  assert.equal(KEY_STORAGE, CONSOLE_KEY_STORAGE);
});

test("buildSetupRequest trims, leaves an empty name out and refuses bad values", () => {
  assert.deepEqual(JSON.parse(buildSetupRequest("  tok  ", "")), { setup_token: "tok" });
  assert.deepEqual(JSON.parse(buildSetupRequest("tok", "  Home ")), { setup_token: "tok", tenant_name: "Home" });
  assert.throws(() => buildSetupRequest("   ", "x"), SetupError);
  assert.throws(() => buildSetupRequest(undefined, "x"), SetupError);
  assert.throws(() => buildSetupRequest("tok", "x".repeat(101)), SetupError);
  assert.doesNotThrow(() => buildSetupRequest("tok", "x".repeat(100)));
  assert.throws(() => buildSetupRequest("tok", "é".repeat(51)), SetupError, "the limit is in bytes");
  assert.throws(() => buildSetupRequest("tok", "a\nb"), SetupError);
  assert.throws(() => buildSetupRequest("tok", "a\u0085b"), SetupError);
});

test("isApiKey accepts a tenant key only", () => {
  assert.ok(isApiKey(KEY));
  for (const v of ["", "ohs_sk_", "ohs_ut_" + "A".repeat(43), KEY + "<b>", " " + KEY, null, 42, {}]) {
    assert.equal(isApiKey(v), false, String(v));
  }
});

test("setupState", () => {
  assert.equal(setupState(200, { needed: true }), "needed");
  assert.equal(setupState(200, { needed: false }), "done");
  assert.equal(setupState(404, null), "off");
  assert.throws(() => setupState(200, { needed: "yes" }), SetupError);
  assert.throws(() => setupState(500, null), SetupError);
});

test("setupMessage names the cause", () => {
  assert.match(setupMessage(401, ""), /token was refused/);
  assert.match(setupMessage(409, ""), /already set up/);
  assert.match(setupMessage(404, ""), /off/);
  assert.match(setupMessage(400, "tenant_name must not contain control characters"), /control characters/);
  assert.match(setupMessage(503, ""), /HTTP 503/);
});

test("state reads the fixed path with no cookie and no redirect", async () => {
  const f = fakeFetch(200, { needed: true });
  assert.equal(await makeSetupClient(f.impl).state(), "needed");
  assert.equal(f.calls.length, 1);
  const { url, init } = f.calls[0];
  assert.equal(url, SETUP_PATH);
  assert.equal(init.method, "GET");
  assert.equal(init.credentials, "omit");
  assert.equal(init.redirect, "error");
  assert.equal(init.headers.Authorization, undefined);
});

test("create posts the token once and returns the key", async () => {
  const f = fakeFetch(201, { tenant_id: "t1", api_key: KEY });
  const out = await makeSetupClient(f.impl).create(" tok ", "Home");
  assert.deepEqual(out, { tenantId: "t1", apiKey: KEY });
  assert.equal(f.calls.length, 1);
  const { url, init } = f.calls[0];
  assert.equal(url, SETUP_PATH);
  assert.equal(init.method, "POST");
  assert.equal(init.credentials, "omit");
  assert.equal(init.redirect, "error");
  assert.equal(init.cache, "no-store");
  assert.equal(init.headers["Content-Type"], "application/json");
  assert.equal(init.headers.Authorization, undefined);
  assert.deepEqual(JSON.parse(init.body), { setup_token: "tok", tenant_name: "Home" });
});

test("create refuses bad input before any request", async () => {
  const f = fakeFetch(201, {});
  await assert.rejects(makeSetupClient(f.impl).create("", ""), SetupError);
  assert.equal(f.calls.length, 0);
});

test("create turns refusals and odd answers into a SetupError", async () => {
  for (const [status, body] of [
    [401, { detail: "the setup token is wrong" }],
    [409, { detail: "already set up" }],
    [500, null],
  ]) {
    await assert.rejects(makeSetupClient(fakeFetch(status, body).impl).create("tok", ""), (err) => {
      assert.ok(err instanceof SetupError);
      assert.equal(err.status, status);
      return true;
    });
  }
  // A 201 without a well formed key is never shown as one.
  await assert.rejects(makeSetupClient(fakeFetch(201, { tenant_id: "t", api_key: "<img>" }).impl).create("tok", ""), SetupError);
});
