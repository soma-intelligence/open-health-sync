import type { APIKey, Pipeline, Webhook } from "../api/types";
import { attention, gettingStarted, needsYou, pipelineStages, STAGE_STATES } from "./pipeline";

const quiet: Pipeline = {
  generated_at: "2026-10-13T12:00:00Z",
  users: { active: 2, deleting: 0 },
  sources: [{ slug: "oura", name: "Oura", kinds: ["oauth"], enabled: true, connections: { active: 2 }, broken: 0, records: 0, paused_until: null }],
  sync: { queued: 1, running: 0, retrying: 0, rate_limited: 0, failed_24h: 0, succeeded_24h: 40 },
  processing: { pending: 0, running: 0, parked: 0, rejected_records: 0 },
  storage: { raw_objects_24h: 12, raw_bytes_24h: 2_500_000 },
  delivery: { endpoints: 1, endpoints_failing: 0, endpoints_disabled: 0, pending: 0, parked: 0 },
  data: { records: 0, users: 0, sources: 0, last_synced_at: null },
};

const empty: Pipeline = {
  generated_at: "2026-10-13T12:00:00Z",
  users: { active: 0, deleting: 0 },
  sources: [],
  sync: { queued: 0, running: 0, retrying: 0, rate_limited: 0, failed_24h: 0, succeeded_24h: 0 },
  processing: { pending: 0, running: 0, parked: 0, rejected_records: 0 },
  storage: { raw_objects_24h: 0, raw_bytes_24h: 0 },
  delivery: { endpoints: 0, endpoints_failing: 0, endpoints_disabled: 0, pending: 0, parked: 0 },
  data: { records: 0, users: 0, sources: 0, last_synced_at: null },
};

test("pipelineStages: five stages, each with its state", () => {
  const stages = pipelineStages(quiet);
  expect(stages.map((s) => s.key)).toEqual(["sources", "sync", "processing", "storage", "delivery"]);
  expect(stages.map((s) => s.tone)).toEqual(["ok", "ok", "ok", "ok", "ok"]);
  expect(stages[3]?.figures[0]?.value).toBe("2.5 MB");
  expect(stages.every((s) => s.role && STAGE_STATES[s.tone])).toBe(true);

  const busy = structuredClone(quiet);
  const [oura] = busy.sources as [Pipeline["sources"][number]];
  oura.connections["needs_reauth"] = 1;
  busy.sync.failed_24h = 3;
  busy.processing.rejected_records = 4;
  busy.delivery.endpoints = 0;
  const s = pipelineStages(busy);
  expect(s.map((x) => x.tone)).toEqual(["bad", "bad", "warn", "ok", "off"]);
  expect(s[1]?.figures[3]?.tone).toBe("bad");
  expect(s[1]?.figures[3]?.to).toBe("/console/jobs");
  // No source yet is a quiet start, not a fault: grey, never orange.
  expect(pipelineStages({ ...quiet, sources: [] })[0]?.tone, "no provider on").toBe("off");
  expect(pipelineStages({ ...quiet, sources: [] })[0]?.state).toBe("No source yet");
});

test("attention: what needs the operator, worst first", () => {
  expect(attention(quiet)).toEqual([]);
  const p = structuredClone(quiet);
  p.sources.push({ slug: "withings", name: "Withings", kinds: ["oauth", "push"], enabled: false, connections: { active: 3 }, broken: 0, records: 0, paused_until: null });
  const [oura] = p.sources as [Pipeline["sources"][number]];
  oura.connections["needs_reauth"] = 1;
  p.processing.parked = 2;
  p.delivery.endpoints_failing = 1;
  const items = attention(p);
  expect(items.map((a) => a.tone)).toEqual(["bad", "bad", "warn", "warn"]);
  expect(items[0]?.text).toMatch(/Oura token expired for 1 user/);
  expect(items[0]?.detail).toMatch(/1 user needs to reconnect Oura/);
  expect(items[1]?.text).toMatch(/2 processing jobs are parked after 5 failed attempts/);
  expect(items[1]?.to).toBe("/console/jobs");
  expect(items.map((a) => a.text).join("\n")).toMatch(/Withings is off but 3 connections are kept/);
  expect(attention({ ...quiet, sources: [] })[0]?.text).toMatch(/No cloud provider is on/);
  const devices: Pipeline = {
    ...quiet,
    sources: [{ slug: "apple_health", name: "Apple Health", kinds: ["device"], enabled: true, connections: {}, broken: 0, records: 0, paused_until: null }],
  };
  expect(attention(devices)[0]?.text, "app uploads do not count").toMatch(/No cloud provider is on/);
});

test("gettingStarted: five steps, each done by its own sign", () => {
  const items = gettingStarted(empty);
  expect(items.map((i) => i.id)).toEqual(["provider", "user", "connection", "data", "webhook"]);
  expect(items.every((i) => !i.done)).toBe(true);
  expect(items.map((i) => i.to)).toEqual(["/console/providers", "/console/users", "/console/users", "/console/users", "/console/webhooks"]);

  const done = (p: Pipeline) => gettingStarted(p).filter((i) => i.done).map((i) => i.id);
  const device = { slug: "apple_health", name: "Apple Health", kinds: ["device" as const], enabled: true, connections: {}, broken: 0, records: 0, paused_until: null };
  const oura = { slug: "oura", name: "Oura", kinds: ["oauth" as const], enabled: true, connections: {}, broken: 0, records: 0, paused_until: null };
  expect(done({ ...empty, sources: [device] }), "an app upload source is not a provider").toEqual([]);
  expect(done({ ...empty, sources: [oura] })).toEqual(["provider"]);
  expect(done({ ...empty, sources: [{ ...oura, enabled: false }] })).toEqual([]);
  expect(done({ ...empty, users: { active: 1, deleting: 0 } })).toEqual(["user"]);
  expect(done({ ...empty, sources: [{ ...device, connections: { active: 1 } }] })).toEqual(["connection"]);
  expect(done({ ...empty, sync: { ...empty.sync, succeeded_24h: 1 } })).toEqual(["data"]);
  expect(done({ ...empty, storage: { raw_objects_24h: 1, raw_bytes_24h: 10 } })).toEqual(["data"]);
  expect(done({ ...empty, delivery: { ...empty.delivery, endpoints: 1 } })).toEqual(["webhook"]);
});

test("needsYou: names failing endpoints and keys that expire within 14 days, one action each", () => {
  const now = Date.parse("2026-10-13T12:00:00Z");
  const p: Pipeline = { ...quiet, delivery: { ...quiet.delivery, endpoints: 2, endpoints_failing: 1, endpoints_disabled: 1 } };
  const hook = (over: Partial<Webhook>) =>
    ({ id: "w1", url: "https://a.example/hook", status: "active", failing_since: null, consecutive_failures: 0, ...over }) as Webhook;
  const webhooks = [
    hook({ id: "w1", status: "disabled", url: "https://off.example/in" }),
    hook({ id: "w2", failing_since: "2026-10-13T10:00:00Z", consecutive_failures: 3, url: "https://slow.example/in" }),
    hook({ id: "w3" }),
  ];
  const key = (prefix: string, expires_at: string | null, revoked_at: string | null = null) =>
    ({ id: prefix, prefix, scopes: ["*"], expires_at, revoked_at }) as unknown as APIKey;
  const keys = [
    key("ohs_sk_soon", "2026-10-20T00:00:00Z"),
    key("ohs_sk_late", "2026-12-01T00:00:00Z"),
    key("ohs_sk_gone", "2026-10-01T00:00:00Z"),
    key("ohs_sk_rev", "2026-10-15T00:00:00Z", "2026-10-02T00:00:00Z"),
    key("ohs_sk_none", null),
  ];
  const items = needsYou(p, webhooks, keys, now);
  expect(items.map((i) => i.text)).toEqual(["Webhook to off.example", "Webhook to slow.example", "API key ohs_sk_soon..."]);
  expect(items.map((i) => i.chip)).toEqual(["Disabled", "Retrying", "Expires soon"]);
  expect(items.map((i) => i.to)).toEqual(["/console/webhooks/w1", "/console/webhooks/w2", "/console/keys"]);
  expect(items.every((i) => i.action !== undefined)).toBe(true);
  expect(
    needsYou(p, undefined, [], now).map((i) => i.to),
    "without the list, the totals stay",
  ).toContain("/console/webhooks");
});
