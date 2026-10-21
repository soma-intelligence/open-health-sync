// SPDX-License-Identifier: AGPL-3.0-only

// Tests of the console's pure helpers: node --test internal/console/app.test.mjs
// (no dependency, no DOM; app.mjs only starts the page when document exists).

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  ApiError,
  HEALTH_RANGES,
  ReadOnlyError,
  STAGE_STATES,
  absTime,
  apiPath,
  attention,
  barPath,
  classifyConnection,
  classifyDelivery,
  classifyFreshness,
  classifyProvider,
  classifyRun,
  classifyWebhook,
  clip,
  compact,
  confirmMatches,
  dailySeries,
  dayLabel,
  filenameFrom,
  formatAgo,
  formatBytes,
  formatCount,
  formatDuration,
  formatTime,
  formatWindow,
  healthRange,
  humanize,
  initials,
  keysPatch,
  labelDays,
  latest,
  linePath,
  localDate,
  makeApi,
  makeGet,
  mean,
  niceTicks,
  parseRoute,
  pipelineStages,
  providerLabel,
  providerStatusLabel,
  recordZone,
  relTime,
  sleepNights,
  splitFigure,
  statSeries,
  webhookLabel,
  workoutSeconds,
  workoutSummary,
  workoutTimeline,
} from "./static/lib.mjs";

function fakeFetch(status = 200, body = { data: [] }, headers = {}) {
  const calls = [];
  const impl = async (url, init) => {
    calls.push({ url, init });
    return {
      ok: status >= 200 && status < 300,
      status,
      headers: { get: (h) => headers[h] ?? null },
      json: async () => {
        if (body === undefined) throw new SyntaxError("no body");
        return body;
      },
      blob: async () => ({ size: 3 }),
    };
  };
  return { impl, calls };
}

test("app.mjs loads without a DOM", async () => {
  await import("./static/app.mjs");
});

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

test("absTime, relTime and formatWindow", () => {
  const now = Date.parse("2026-10-13T12:00:00Z");
  assert.equal(absTime("2026-10-13T09:00:00Z"), "2026-10-13 09:00 UTC");
  assert.equal(absTime(null), "-");
  assert.equal(relTime("2026-10-13T11:59:40Z", now), "just now");
  assert.equal(relTime("2026-10-13T11:48:00Z", now), "12 min ago");
  assert.equal(relTime("2026-10-13T09:00:00Z", now), "3 h ago");
  assert.equal(relTime("2026-10-09T12:00:00Z", now), "4 d ago");
  assert.equal(relTime("2026-10-13T12:05:00Z", now), "in 5 min");
  assert.equal(relTime("2026-08-01T12:00:00Z", now), "2026-08-01");
  assert.equal(relTime("soon", now), "soon");
  assert.equal(formatWindow("2026-10-16T03:33:00Z", "2026-10-16T04:33:00Z"), "2026-10-16 03:33 to 04:33");
  assert.equal(formatWindow("2026-10-16T22:00:00Z", "2026-10-17T00:00:00Z"), "2026-10-16 22:00 to 2026-10-17 00:00");
});

test("humanize, initials and providerLabel", () => {
  assert.equal(humanize("needs_reauth"), "Needs reconnecting");
  assert.equal(humanize("rate_limited"), "Rate limited");
  assert.equal(humanize("succeeded"), "Succeeded");
  assert.equal(humanize(null), "-");
  assert.equal(humanize("constructor"), "Constructor", "no prototype lookup");
  assert.equal(initials("Apple Health"), "AH");
  assert.equal(initials("intervals.icu"), "I");
  assert.equal(initials("self_report"), "SR");
  assert.equal(initials(""), "?");
  assert.equal(providerLabel({ oura: "Oura" }, "oura"), "Oura");
  assert.equal(providerLabel({}, "apple_health"), "Apple health");
  assert.equal(providerLabel(null, "toString"), "ToString");
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

test("webhookLabel agrees with the endpoint's color", () => {
  assert.equal(webhookLabel({ status: "active", failing_since: null }), "Active");
  assert.equal(webhookLabel({ status: "active", failing_since: "2026-10-13T09:00:00Z" }), "Failing");
  assert.equal(webhookLabel({ status: "disabled", failing_since: "2026-10-13T09:00:00Z" }), "Turned off");
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
  assert.deepEqual(parseRoute(""), { view: "overview", section: "overview" });
  assert.deepEqual(parseRoute("#/users"), { view: "users", section: "users" });
  assert.deepEqual(parseRoute("#/users/abc%2F1"), { view: "user", section: "users", id: "abc/1" });
  assert.deepEqual(parseRoute("#/webhooks/w1"), { view: "webhook", section: "webhooks", id: "w1" });
  assert.deepEqual(parseRoute("#/providers/oura"), { view: "provider", section: "providers", id: "oura" });
  assert.deepEqual(parseRoute("#/jobs/x"), { view: "jobs", section: "jobs" });
  assert.deepEqual(parseRoute("#/keys"), { view: "keys", section: "keys" });
  assert.deepEqual(parseRoute("#/audit"), { view: "audit", section: "audit" });
  assert.deepEqual(parseRoute("#/nope"), { view: "overview", section: "overview" });
  assert.deepEqual(parseRoute("#/constructor"), { view: "overview", section: "overview" });
  assert.deepEqual(parseRoute("#/users/%E0"), { view: "user", section: "users", id: "%E0" });
});

test("api sends writes as JSON with the key, and only the methods it knows", async () => {
  const f = fakeFetch(201, { id: "u1" });
  const api = makeApi(f.impl, () => "ohs_sk_test");
  assert.deepEqual(await api("/v1/users", { method: "post", body: { external_ref: "r" } }), { id: "u1" });
  const { init } = f.calls[0];
  assert.equal(init.method, "POST");
  assert.equal(init.body, JSON.stringify({ external_ref: "r" }));
  assert.equal(init.headers["Content-Type"], "application/json");
  assert.equal(init.headers.Authorization, "Bearer ohs_sk_test");
  assert.equal(init.credentials, "omit");
  for (const method of ["PATCH", "PUT", "DELETE"]) {
    await api("/v1/x", { method });
  }
  assert.equal(f.calls.length, 4);
  assert.equal(f.calls[1].init.body, undefined, "no body, no Content-Type");
  assert.equal(f.calls[1].init.headers["Content-Type"], undefined);
  for (const method of ["OPTIONS", "HEAD", "TRACE", "CONNECT"]) {
    await assert.rejects(api("/v1/users", { method }), ReadOnlyError, method);
  }
  await assert.rejects(api("/v1/users", { body: {} }), ReadOnlyError, "a GET carries no body");
  await assert.rejects(api("https://evil.example/v1/users", { method: "POST" }), ReadOnlyError);
  assert.equal(f.calls.length, 4);
});

test("api in read-only mode sends GET only", async () => {
  const f = fakeFetch();
  const api = makeApi(f.impl, () => "k", { readOnly: true });
  for (const method of ["POST", "PATCH", "PUT", "DELETE"]) {
    await assert.rejects(api("/v1/users", { method, body: {} }), ReadOnlyError, method);
  }
  await api("/v1/users");
  assert.equal(f.calls.length, 1);
});

test("api reads 204 as null and downloads as a blob with a safe name", async () => {
  const none = fakeFetch(204, undefined);
  assert.equal(await makeApi(none.impl, () => "k")("/v1/keys/1", { method: "DELETE" }), null);
  const file = fakeFetch(200, undefined, { "Content-Disposition": 'attachment; filename="samples-u1.csv"' });
  const got = await makeApi(file.impl, () => "k")("/v1/users/u1/exports/samples", { download: true });
  assert.equal(got.filename, "samples-u1.csv");
  assert.equal(got.blob.size, 3);
  assert.equal(file.calls[0].init.headers.Accept, "*/*");
});

test("filenameFrom", () => {
  assert.equal(filenameFrom('attachment; filename="a b/../c.zip"'), "a_b_.._c.zip");
  assert.equal(filenameFrom(null), "download");
  assert.equal(filenameFrom('attachment; filename=".."'), "download");
  assert.equal(filenameFrom("attachment; filename=x.json"), "x.json");
});

test("formatCount and formatBytes", () => {
  assert.equal(formatCount(0), "0");
  assert.equal(formatCount(1234567), "1,234,567");
  assert.equal(formatCount(null), "-");
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatBytes(950), "950 B");
  assert.equal(formatBytes(1500), "1.5 kB");
  assert.equal(formatBytes(12_000_000), "12 MB");
  assert.equal(formatBytes(undefined), "-");
});

test("classifyProvider, providerStatusLabel and classifyRun", () => {
  assert.equal(classifyProvider({ status: "active" }), "ok");
  assert.equal(classifyProvider({ status: "built_in" }), "ok");
  assert.equal(classifyProvider({ status: "error" }), "bad");
  assert.equal(classifyProvider({ status: "not_configured" }), "off");
  assert.equal(classifyProvider({ status: "off" }), "off");
  assert.equal(providerStatusLabel("not_configured"), "Not configured");
  assert.equal(providerStatusLabel("new_one"), "new_one");
  assert.equal(classifyRun({ status: "failed" }), "bad");
  assert.equal(classifyRun({ status: "rate_limited" }), "warn");
  assert.equal(classifyRun({ status: "succeeded" }), "ok");
  assert.equal(classifyRun({ status: "cancelled" }), "off");
});

const quiet = {
  users: { active: 2, deleting: 0 },
  sources: [{ slug: "oura", name: "Oura", kinds: ["oauth"], enabled: true, connections: { active: 2 }, broken: 0, paused_until: null }],
  sync: { queued: 1, running: 0, retrying: 0, rate_limited: 0, failed_24h: 0, succeeded_24h: 40 },
  processing: { pending: 0, running: 0, parked: 0, rejected_records: 0 },
  storage: { raw_objects_24h: 12, raw_bytes_24h: 2_500_000 },
  delivery: { endpoints: 1, endpoints_failing: 0, endpoints_disabled: 0, pending: 0, parked: 0 },
};

test("pipelineStages: five stages, each with its state", () => {
  const stages = pipelineStages(quiet);
  assert.deepEqual(stages.map((s) => s.key), ["sources", "sync", "processing", "storage", "delivery"]);
  assert.deepEqual(stages.map((s) => s.cls), ["ok", "ok", "ok", "ok", "ok"]);
  assert.equal(stages[3].figures[0].value, "2.5 MB");
  assert.ok(stages.every((s) => s.role && STAGE_STATES[s.cls]));

  const busy = structuredClone(quiet);
  busy.sources[0].connections.needs_reauth = 1;
  busy.sync.failed_24h = 3;
  busy.processing.rejected_records = 4;
  busy.delivery.endpoints = 0;
  const s = pipelineStages(busy);
  assert.deepEqual(s.map((x) => x.cls), ["bad", "bad", "warn", "ok", "off"]);
  assert.equal(s[1].figures[3].cls, "bad");
  assert.equal(s[1].figures[3].href, "#/jobs");
  assert.equal(pipelineStages({ ...quiet, sources: [] })[0].cls, "warn", "no provider on");
});

test("attention: what needs the operator, worst first", () => {
  assert.deepEqual(attention(quiet), []);
  const p = structuredClone(quiet);
  p.sources.push({ slug: "withings", name: "Withings", kinds: ["oauth", "push"], enabled: false, connections: { active: 3 }, paused_until: null });
  p.sources[0].connections.needs_reauth = 1;
  p.processing.parked = 2;
  p.delivery.endpoints_failing = 1;
  const items = attention(p);
  assert.deepEqual(items.map((a) => a.cls), ["bad", "bad", "warn", "warn"]);
  assert.match(items[0].text, /1 user needs to reconnect Oura/);
  assert.match(items[1].text, /2 processing jobs are parked after 5 failed attempts/);
  assert.equal(items[1].href, "#/jobs");
  assert.match(items.map((a) => a.text).join("\n"), /Withings is off but 3 connections are kept/);
  assert.match(attention({ ...quiet, sources: [] })[0].text, /No cloud provider is on/);
  const devices = { ...quiet, sources: [{ slug: "apple_health", name: "Apple Health", kinds: ["device"], enabled: true, connections: {} }] };
  assert.match(attention(devices)[0].text, /No cloud provider is on/, "app uploads do not count");
});

test("keysPatch sends only what changed", () => {
  const fields = [
    { name: "CLIENT_ID", secret: false, set: true, value: "abc", input: "abc", clear: false },
    { name: "CLIENT_SECRET", secret: true, set: true, value: null, input: "", clear: false },
    { name: "REGION", secret: false, set: false, value: null, input: "", clear: false },
  ];
  assert.equal(keysPatch(fields), null, "nothing typed: nothing changes");
  fields[0].input = " new-id ";
  fields[1].input = "s3cret";
  assert.deepEqual(keysPatch(fields), { CLIENT_ID: "new-id", CLIENT_SECRET: "s3cret" });
  fields[0].input = "abc";
  fields[1].input = "";
  fields[1].clear = true;
  fields[2].clear = true;
  assert.deepEqual(keysPatch(fields), { CLIENT_SECRET: null }, "clearing an unset key sends nothing");
});

test("confirmMatches", () => {
  assert.equal(confirmMatches("oura", "oura"), true);
  assert.equal(confirmMatches(" oura ", "oura"), true);
  assert.equal(confirmMatches("Oura", "oura"), false);
  assert.equal(confirmMatches("", ""), false);
  assert.equal(confirmMatches(undefined, "x"), false);
});

test("splitFigure", () => {
  assert.deepEqual(splitFigure("3 of 9"), { figure: "3", unit: "of 9", word: false });
  assert.deepEqual(splitFigure("1.2 MB"), { figure: "1.2", unit: "MB", word: false });
  assert.deepEqual(splitFigure("1,234"), { figure: "1,234", unit: "", word: false });
  assert.deepEqual(splitFigure("12 min ago"), { figure: "12", unit: "min ago", word: false });
  assert.deepEqual(splitFigure("-"), { figure: "-", unit: "", word: false });
  assert.deepEqual(splitFigure(null), { figure: "-", unit: "", word: false });
  assert.deepEqual(splitFigure("Never"), { figure: "Never", unit: "", word: true });
  assert.deepEqual(splitFigure("just now"), { figure: "just now", unit: "", word: true });
});

test("healthRange: the last n days, today included, end exclusive", () => {
  const now = Date.parse("2026-10-18T01:30:00Z");
  assert.deepEqual(HEALTH_RANGES, [30, 90]);
  const r = healthRange(30, now);
  assert.equal(r.days.length, 30);
  assert.equal(r.start, "2026-09-19");
  assert.equal(r.days[29], "2026-10-18");
  assert.equal(r.end, "2026-10-19");
  assert.equal(r.before, "2026-09-18");
  assert.equal(healthRange(90, now).start, "2026-07-21");
  assert.equal(healthRange(3, Date.parse("2026-03-01T12:00:00Z")).days.join(","), "2026-02-27,2026-02-28,2026-03-01");
});

test("localDate and recordZone", () => {
  assert.equal(localDate("2026-10-17T22:30:00Z", "Asia/Tokyo"), "2026-10-18");
  assert.equal(localDate("2026-10-17T05:30:00Z", "America/Los_Angeles"), "2026-10-16");
  assert.equal(localDate("2026-10-17T22:30:00Z", "Not/AZone"), "2026-10-17", "an unknown zone is UTC");
  assert.equal(localDate("2026-10-17T22:30:00Z", null), "2026-10-17");
  assert.equal(localDate("soon", "UTC"), null);
  assert.equal(recordZone([{ metadata: { timezone: "Europe/Paris" } }, { metadata: {} }]), "Europe/Paris");
  assert.equal(recordZone([{ metadata: { timezone: "Nope" } }], [{ metadata: { timezone: "Asia/Tokyo" } }]), "Asia/Tokyo");
  assert.equal(recordZone([], null, [{}]), "UTC");
});

test("sleepNights: by the day the user woke up, stages summed, no stage held as asleep", () => {
  const days = ["2026-10-16", "2026-10-17", "2026-10-18"];
  const nights = sleepNights(
    [
      { bedtime_start: "2026-10-15T21:30:00Z", bedtime_end: "2026-10-16T05:30:00Z", deep_sleep_seconds: 3600, light_sleep_seconds: 14400, rem_sleep_seconds: 5400, awake_seconds: 1200 },
      { bedtime_start: "2026-10-16T12:00:00Z", bedtime_end: "2026-10-16T12:30:00Z", light_sleep_seconds: 1800 },
      { bedtime_start: "2026-10-17T21:00:00Z", bedtime_end: "2026-10-18T05:00:00Z", total_sleep_seconds: 25200, metadata: { timezone: "Asia/Tokyo" } },
      { bedtime_start: "2026-10-01T21:00:00Z", bedtime_end: "2026-10-02T05:00:00Z", total_sleep_seconds: 25200 },
    ],
    days,
    "Europe/Paris",
  );
  assert.deepEqual(nights[0], { date: "2026-10-16", deep: 3600, light: 16200, rem: 5400, asleep: 0, awake: 1200, total: 25200 }, "a nap adds to its day");
  assert.equal(nights[1], null);
  assert.equal(nights[2].asleep, 25200);
  assert.equal(nights[2].total, 25200);
  assert.deepEqual(sleepNights(null, days, "UTC"), [null, null, null]);
});

test("dailySeries and statSeries: one value per day, null where none", () => {
  const days = ["2026-10-16", "2026-10-17", "2026-10-18"];
  assert.deepEqual(dailySeries([{ date: "2026-10-16", step_count: 8000 }, { date: "2026-10-18", step_count: null }, { date: "2026-10-01", step_count: 3 }], days, "step_count"), [8000, null, null]);
  const stats = {
    buckets: [
      { start: "2026-10-16T00:00:00+02:00", count: 1, sum: 54 },
      { start: "2026-10-18T00:00:00+02:00", count: 2, sum: 120 },
      { start: "2026-10-18T00:00:00+02:00", count: 1, sum: 63 },
    ],
  };
  assert.deepEqual(statSeries(stats, days), [54, null, 61], "rows of a day pool by count");
  assert.deepEqual(statSeries(null, days), [null, null, null]);
});

test("workoutTimeline: lanes by frequency, the tail folded into Other", () => {
  const days = ["2026-10-16", "2026-10-17", "2026-10-18"];
  const w = (type, start) => ({ activity_type: type, start_date: start, duration_s: 1800 });
  const t = workoutTimeline(
    [w("running", "2026-10-16T07:00:00Z"), w("running", "2026-10-18T07:00:00Z"), w("cycling", "2026-10-17T07:00:00Z"), w("yoga", "2026-10-17T18:00:00Z"), w("swimming", "2026-10-18T18:00:00Z"), w("running", "2026-09-01T07:00:00Z")],
    days,
    "UTC",
    3,
  );
  assert.deepEqual(t.rows.map((r) => [r.label, r.count]), [["Running", 2], ["Cycling", 1], ["Other", 2]]);
  assert.deepEqual(t.marks.map((m) => [m.day, m.row]), [[0, 0], [2, 0], [1, 1], [1, 2], [2, 2]]);
  assert.equal(t.seconds, 5 * 1800, "outside the range counts for nothing");
  assert.equal(workoutTimeline([], days, "UTC").rows.length, 0);
});

test("workoutSeconds and workoutSummary", () => {
  assert.equal(workoutSeconds({ duration_s: 600 }), 600);
  assert.equal(workoutSeconds({ start_date: "2026-10-17T07:00:00Z", end_date: "2026-10-17T07:40:00Z" }), 2400);
  assert.equal(workoutSeconds({ start_date: "2026-10-17T07:00:00Z" }), 0);
  assert.equal(workoutSummary({ duration_s: 2700, distance_m: 8234, active_energy_kcal: 412.4 }), "45 min · 8.2 km · 412 kcal");
  assert.equal(workoutSummary({ duration_s: 0, distance_m: 800 }), "800 m");
});

test("mean and latest skip missing days", () => {
  assert.equal(mean([null, 2, 4, null]), 3);
  assert.equal(mean([null]), null);
  assert.deepEqual(latest([1, 5, null]), { value: 5, index: 1 });
  assert.equal(latest([null, null]), null);
});

test("niceTicks: round steps covering the values", () => {
  assert.deepEqual(niceTicks(0, 12345).ticks, [0, 5000, 10000, 15000]);
  assert.deepEqual(niceTicks(0, 9.1).ticks, [0, 2, 4, 6, 8, 10]);
  assert.deepEqual(niceTicks(52.3, 61.8).ticks, [50, 55, 60, 65]);
  assert.deepEqual(niceTicks(71.3, 74.9).ticks, [71, 72, 73, 74, 75]);
  assert.deepEqual(niceTicks(0, 0), { ticks: [0, 0.5, 1], lo: 0, hi: 1 }, "nothing but zeros");
  const flat = niceTicks(78.2, 78.2);
  assert.ok(flat.lo < 78.2 && flat.hi > 78.2, "a flat series gets room");
  assert.deepEqual(niceTicks(NaN, 3), { ticks: [0, 1], lo: 0, hi: 1 });
});

test("barPath rounds the data end only, linePath joins points", () => {
  assert.equal(barPath(0, 10, 8, 30), "M0 40V14Q0 10 4 10H4Q8 10 8 14V40Z");
  assert.equal(barPath(0, 10, 8, 30, 0), "M0 40V10H8V40Z");
  assert.equal(barPath(0, 10, 2, 30), "M0 40V11Q0 10 1 10H1Q2 10 2 11V40Z", "no more than half the width");
  assert.equal(linePath([[0, 1], [2.555, 3]]), "M0 1L2.56 3");
  assert.equal(linePath([]), "");
});

test("formatDuration, compact, dayLabel, labelDays and clip", () => {
  assert.equal(formatDuration(27000), "7 h 30 min");
  assert.equal(formatDuration(2700), "45 min");
  assert.equal(formatDuration(28800), "8 h");
  assert.equal(formatDuration(null), "-");
  assert.equal(compact(12500), "12.5K");
  assert.equal(compact(2000000), "2M");
  assert.equal(compact(500), "500");
  assert.equal(compact(0.5), "0.5");
  assert.equal(dayLabel("2026-10-17"), "Oct 17");
  assert.equal(dayLabel("2026-10-17", true), "Sat, Oct 17");
  assert.equal(dayLabel("soon"), "soon");
  assert.deepEqual(labelDays(30, 5), [5, 11, 17, 23, 29], "the last day is always named");
  assert.deepEqual(labelDays(3, 10), [0, 1, 2]);
  assert.equal(clip("High intensity interval training", 16), "High intensity…");
  assert.equal(clip("Running", 16), "Running");
});
