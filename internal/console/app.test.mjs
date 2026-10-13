// SPDX-License-Identifier: AGPL-3.0-only

// Tests of the console's pure helpers: node --test internal/console/app.test.mjs
// (no dependency, no DOM; the module only starts the page when document exists).

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  ApiError,
  ReadOnlyError,
  apiPath,
  classifyConnection,
  classifyDelivery,
  classifyFreshness,
  classifyWebhook,
  formatAgo,
  formatTime,
  makeGet,
  parseRoute,
} from "./static/app.mjs";

function fakeFetch(status = 200, body = { data: [] }) {
  const calls = [];
  const impl = async (url, init) => {
    calls.push({ url, init });
    return { ok: status >= 200 && status < 300, status, json: async () => body };
  };
  return { impl, calls };
}

test("get refuses every method but GET, before any request", async () => {
  const f = fakeFetch();
  const get = makeGet(f.impl, () => "ohs_sk_test");
  for (const method of ["POST", "PUT", "PATCH", "DELETE", "post", "OPTIONS", "HEAD"]) {
    await assert.rejects(get("/v1/users", { method }), ReadOnlyError, method);
  }
  await assert.rejects(get("/v1/users", { body: "{}" }), ReadOnlyError);
  assert.equal(f.calls.length, 0);
});

test("get refuses URLs off this origin", async () => {
  const f = fakeFetch();
  const get = makeGet(f.impl, () => "k");
  for (const url of ["https://evil.example/v1/users", "//evil.example/x", "v1/users", "/\\evil.example", 42]) {
    await assert.rejects(get(url), ReadOnlyError, String(url));
  }
  assert.equal(f.calls.length, 0);
});

test("get sends a GET with the bearer key and no cookies", async () => {
  const f = fakeFetch(200, { data: [1] });
  const get = makeGet(f.impl, () => "ohs_sk_test");
  assert.deepEqual(await get("/v1/users"), { data: [1] });
  assert.equal(f.calls.length, 1);
  const { url, init } = f.calls[0];
  assert.equal(url, "/v1/users");
  assert.equal(init.method, "GET");
  assert.equal(init.body, undefined);
  assert.equal(init.headers.Authorization, "Bearer ohs_sk_test");
  assert.equal(init.credentials, "omit");
  assert.equal(init.redirect, "error");

  await get("/healthz", { auth: false });
  assert.equal(f.calls[1].init.headers.Authorization, undefined);
});

test("get turns a problem answer into an ApiError", async () => {
  const f = fakeFetch(401, { title: "Unauthorized", detail: "missing or invalid credentials" });
  const get = makeGet(f.impl, () => "bad");
  await assert.rejects(get("/v1/users"), (err) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, 401);
    assert.equal(err.detail, "missing or invalid credentials");
    return true;
  });
});

test("formatAgo", () => {
  assert.equal(formatAgo(null), "-");
  assert.equal(formatAgo(undefined), "-");
  assert.equal(formatAgo(0), "0 s");
  assert.equal(formatAgo(59), "59 s");
  assert.equal(formatAgo(60), "1 min");
  assert.equal(formatAgo(3599), "59 min");
  assert.equal(formatAgo(3600), "1 h");
  assert.equal(formatAgo(3 * 3600 + 5 * 60), "3 h 5 min");
  assert.equal(formatAgo(86400), "1 d");
  assert.equal(formatAgo(2 * 86400 + 4 * 3600), "2 d 4 h");
  assert.equal(formatAgo(-120), "in 2 min");
});

test("formatTime", () => {
  const now = Date.parse("2026-10-13T12:00:00Z");
  assert.equal(formatTime(null, now), "-");
  assert.equal(formatTime("2026-10-13T09:00:00Z", now), "2026-10-13 09:00 UTC (3 h ago)");
  assert.equal(formatTime("2026-10-13T12:30:00Z", now), "2026-10-13 12:30 UTC (in 30 min)");
  assert.equal(formatTime("not a date", now), "not a date");
});

test("classifyFreshness: stale, failing and needs_reauth are red", () => {
  assert.equal(classifyFreshness({ state: "fresh", consecutive_failures: 0 }), "ok");
  assert.equal(classifyFreshness({ state: "fresh", consecutive_failures: 1 }), "warn");
  assert.equal(classifyFreshness({ state: "syncing" }), "warn");
  assert.equal(classifyFreshness({ state: "stale" }), "bad");
  assert.equal(classifyFreshness({ state: "failing" }), "bad");
  assert.equal(classifyFreshness({ state: "needs_reauth" }), "bad");
  assert.equal(classifyFreshness({ state: "disconnected" }), "off");
  assert.equal(classifyFreshness({ state: "something new" }), "warn");
});

test("classifyConnection, classifyWebhook, classifyDelivery", () => {
  assert.equal(classifyConnection({ status: "active" }), "ok");
  assert.equal(classifyConnection({ status: "needs_reauth" }), "bad");
  assert.equal(classifyConnection({ status: "disconnected" }), "off");

  assert.equal(classifyWebhook({ status: "active", failing_since: null }), "ok");
  assert.equal(classifyWebhook({ status: "active", failing_since: "2026-10-13T00:00:00Z" }), "bad");
  assert.equal(classifyWebhook({ status: "disabled", failing_since: null }), "bad");

  assert.equal(classifyDelivery({ status: "done", attempts: 1 }), "ok");
  assert.equal(classifyDelivery({ status: "pending", attempts: 0 }), "ok");
  assert.equal(classifyDelivery({ status: "pending", attempts: 2, last_error: "HTTP 500" }), "warn");
  assert.equal(classifyDelivery({ status: "parked", attempts: 8 }), "bad");
});

test("apiPath escapes ids and drops empty query values", () => {
  assert.equal(apiPath(["v1", "users", "a/b?c"]), "/v1/users/a%2Fb%3Fc");
  assert.equal(apiPath(["v1", "users", "u1", "connections"]), "/v1/users/u1/connections");
  assert.equal(apiPath(["v1", "users"], { limit: 50, cursor: null }), "/v1/users?limit=50");
  assert.equal(apiPath(["v1", "users"], { cursor: "a&b" }), "/v1/users?cursor=a%26b");
});

test("parseRoute", () => {
  assert.deepEqual(parseRoute(""), { view: "overview" });
  assert.deepEqual(parseRoute("#/users"), { view: "users" });
  assert.deepEqual(parseRoute("#/users/abc%2F1"), { view: "user", id: "abc/1" });
  assert.deepEqual(parseRoute("#/webhooks/w1"), { view: "webhook", id: "w1" });
  assert.deepEqual(parseRoute("#/nope"), { view: "overview" });
  assert.deepEqual(parseRoute("#/users/%E0"), { view: "user", id: "%E0" });
});
