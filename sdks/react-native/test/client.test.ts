// SPDX-License-Identifier: AGPL-3.0-only

import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { gunzipSync } from "node:zlib";

import {
  AppleHealthPartBuilder,
  OhsApiError,
  OhsClient,
  collectAll,
  paginate,
  type OhsClientOptions,
  type Page,
} from "../src/index.ts";
import { fakeFetch, type Reply } from "./fake-fetch.ts";
import * as fx from "./fixtures.ts";

const BASE = "https://ohs.example.org/";

function client(replies: Reply[] | ((r: import("./fake-fetch.ts").Recorded) => Reply), extra: Partial<OhsClientOptions> = {}) {
  const f = fakeFetch(replies);
  const sleeps: number[] = [];
  const c = new OhsClient({
    baseUrl: BASE,
    token: "ohs_sk_test",
    fetch: f.fetch,
    sleep: async (ms) => {
      sleeps.push(ms);
    },
    ...extra,
  });
  return { c, requests: f.requests, sleeps };
}

const page = (data: unknown[], next: string | null = null) => ({ body: { data, next_cursor: next } });

describe("requests", () => {
  it("sends auth, accept and user agent headers", async () => {
    const { c, requests } = client([{ body: { data: [{ slug: "oura", kinds: ["oauth", "push"] }] } }]);
    const providers = await c.listProviders();
    assert.deepEqual(providers, [{ slug: "oura", kinds: ["oauth", "push"] }]);
    const r = requests[0]!;
    assert.equal(r.method, "GET");
    assert.equal(r.url, "https://ohs.example.org/v1/providers");
    assert.equal(r.headers["Authorization"], "Bearer ohs_sk_test");
    assert.equal(r.headers["Accept"], "application/json");
    assert.equal(r.headers["User-Agent"], "open-health-sync-react-native/0.1.0");
    assert.equal(r.body, undefined);
  });

  it("calls the token provider before every request", async () => {
    let n = 0;
    const { c, requests } = client([{ body: fx.user }, { body: fx.user }], {
      token: undefined,
      tokenProvider: async () => `tok-${++n}`,
    });
    await c.getUser(fx.USER_ID);
    await c.getUser(fx.USER_ID);
    assert.equal(n, 2);
    assert.equal(requests[0]!.headers["Authorization"], "Bearer tok-1");
    assert.equal(requests[1]!.headers["Authorization"], "Bearer tok-2");
  });

  it("requires a credential and a base URL", () => {
    assert.throws(() => new OhsClient({ baseUrl: BASE, fetch: fakeFetch([]).fetch }), TypeError);
    assert.throws(() => new OhsClient({ baseUrl: "", token: "x", fetch: fakeFetch([]).fetch }), TypeError);
  });

  it("creates a user with an idempotency key", async () => {
    const { c, requests } = client([{ status: 201, body: fx.user }]);
    const u = await c.createUser({ externalRef: "customer-42", idempotencyKey: "key-1" });
    assert.deepEqual(u, {
      id: fx.USER_ID,
      externalRef: "customer-42",
      status: "active",
      createdAt: "2026-01-01T00:00:00Z",
    });
    const r = requests[0]!;
    assert.equal(r.method, "POST");
    assert.equal(r.path, "/v1/users");
    assert.equal(r.headers["Idempotency-Key"], "key-1");
    assert.equal(r.headers["Content-Type"], "application/json");
    assert.deepEqual(JSON.parse(r.body as string), { external_ref: "customer-42" });
  });

  it("creates a user with no body", async () => {
    const { c, requests } = client([{ status: 201, body: { ...fx.user, external_ref: null } }]);
    const u = await c.createUser();
    assert.equal(u.externalRef, null);
    assert.equal(requests[0]!.body, undefined);
    assert.equal(requests[0]!.headers["Idempotency-Key"], undefined);
  });

  it("lists, reads and deletes users", async () => {
    const { c, requests } = client([
      page([fx.user], "c2"),
      { status: 202, body: { job_id: fx.deletionJob.job_id } },
      { body: fx.deletionJob },
    ]);
    const p = await c.listUsers({ limit: 1 });
    assert.equal(p.nextCursor, "c2");
    assert.equal(p.data[0]!.id, fx.USER_ID);
    assert.deepEqual(requests[0]!.query, { limit: "1" });
    const job = await c.deleteUser(fx.USER_ID);
    assert.equal(job, fx.deletionJob.job_id);
    assert.equal(requests[1]!.method, "DELETE");
    assert.equal(requests[1]!.path, `/v1/users/${fx.USER_ID}`);
    const d = await c.getDeletion(job);
    assert.deepEqual(d, {
      jobId: fx.deletionJob.job_id,
      userId: fx.USER_ID,
      status: "done",
      createdAt: "2026-01-11T00:00:00Z",
      finishedAt: "2026-01-11T00:00:05Z",
    });
    assert.equal(requests[2]!.path, `/v1/deletions/${fx.deletionJob.job_id}`);
  });

  it("mints a user token", async () => {
    const { c, requests } = client([{ status: 201, body: fx.userToken }]);
    const t = await c.mintUserToken(fx.USER_ID, ["upload", "read"], 600);
    assert.deepEqual(t, { token: "ohs_ut_FAKEFAKEFAKE", expiresAt: "2026-01-11T01:00:00Z", scopes: ["upload", "read"] });
    assert.equal(requests[0]!.path, `/v1/users/${fx.USER_ID}/tokens`);
    assert.deepEqual(JSON.parse(requests[0]!.body as string), { scopes: ["upload", "read"], ttl_seconds: 600 });
  });

  it("handles connections", async () => {
    const { c, requests } = client([
      { body: { data: [fx.connection] } },
      { body: fx.freshness },
      { body: { authorize_url: "https://provider.example/authorize?state=abc" } },
      { body: { authorize_url: "https://provider.example/authorize?state=def" } },
      { status: 204 },
    ]);
    const conns = await c.listConnections(fx.USER_ID);
    assert.deepEqual(conns[0], {
      provider: "oura",
      status: "active",
      scopes: ["daily", "heartrate"],
      lastSyncAt: "2026-01-10T15:00:00Z",
      initialBackfillAt: null,
      createdAt: "2026-01-01T00:00:00Z",
      updatedAt: "2026-01-10T15:00:00Z",
    });
    const f = await c.getFreshness(fx.USER_ID);
    assert.equal(f.asOf, "2026-01-10T15:05:00Z");
    assert.deepEqual(f.data[0], {
      provider: "oura",
      kind: "oauth",
      status: "active",
      state: "fresh",
      syncedThrough: "2026-01-10T15:00:00Z",
      lagSeconds: 300,
      lastSyncAt: "2026-01-10T15:00:00Z",
      lastEventAt: null,
      pushHealthy: true,
      pullEverySeconds: 21600,
      nextPullAt: "2026-01-10T21:00:00Z",
      lastAttemptAt: "2026-01-10T15:00:00Z",
      lastError: null,
      consecutiveFailures: 0,
    });
    const url = await c.startConnection(fx.USER_ID, "oura", "myapp://connected");
    assert.equal(url, "https://provider.example/authorize?state=abc");
    assert.equal(requests[2]!.method, "POST");
    assert.equal(requests[2]!.path, `/v1/users/${fx.USER_ID}/connections/oura`);
    assert.deepEqual(JSON.parse(requests[2]!.body as string), { redirect_uri: "myapp://connected" });
    await c.startConnection(fx.USER_ID, "oura");
    assert.equal(requests[3]!.body, undefined);
    await c.deleteConnection(fx.USER_ID, "oura");
    assert.equal(requests[4]!.method, "DELETE");
    assert.equal(requests[4]!.path, `/v1/users/${fx.USER_ID}/connections/oura`);
  });

  it("encodes path segments and query values, sending only set parameters", async () => {
    const { c, requests } = client([page([]), page([])]);
    await c.listSamples("a/b c", {
      type: "heart_rate",
      start: "2026-01-10T00:00:00+01:00",
      end: new Date("2026-01-11T00:00:00Z"),
      cursor: "x=y&z",
      limit: 50,
    });
    const r = requests[0]!;
    assert.ok(r.url.startsWith("https://ohs.example.org/v1/users/a%2Fb%20c/samples?"));
    assert.ok(r.url.includes("start=2026-01-10T00%3A00%3A00%2B01%3A00"));
    assert.deepEqual(r.query, {
      type: "heart_rate",
      start: "2026-01-10T00:00:00+01:00",
      end: "2026-01-11T00:00:00.000Z",
      cursor: "x=y&z",
      limit: "50",
    });
    await c.listWorkouts(fx.USER_ID);
    assert.equal(requests[1]!.url, `https://ohs.example.org/v1/users/${fx.USER_ID}/workouts`);
  });

  it("rejects listSamples without a type", async () => {
    const { c } = client([]);
    await assert.rejects(() => c.listSamples(fx.USER_ID, { type: "" }), TypeError);
  });
});

describe("decoding", () => {
  const prov = {
    provider: "apple_health",
    source: "Apple Watch",
    device: "Watch7,1",
    ingestedAt: "2026-01-10T15:00:00.123456789Z",
  };

  it("decodes samples, keeping metadata keys and ignoring unknown fields", async () => {
    const { c } = client([page([fx.sample])]);
    const p = await c.listSamples(fx.USER_ID, { type: "heart_rate" });
    assert.deepEqual(p, {
      nextCursor: null,
      data: [
        {
          ...prov,
          id: 1234,
          type: "heart_rate",
          value: 72.5,
          unit: "bpm",
          startDate: "2026-01-10T14:30:00.123456789Z",
          endDate: "2026-01-10T14:30:00Z",
          sourceName: "Apple Watch",
          sourceBundleId: "com.example.health",
          deviceModel: "Watch",
          productType: "Watch7,1",
          externalId: "A1B2",
          isPrimarySource: true,
          wasUserEntered: false,
          metadata: { timezone: "Europe/Paris", HKTimeZone: "Europe/Paris", nested_key: { a_b: 1 } },
        },
      ],
    });
  });

  it("decodes every other record type", async () => {
    const { c, requests } = client([
      page([fx.categorySample]),
      page([fx.sleepSession]),
      page([fx.sleepPhase]),
      page([fx.workout]),
      page([fx.routePoint]),
      page([fx.nutritionLog]),
      page([fx.dailySummary]),
      { body: fx.coverage },
    ]);
    const cat = await c.listCategorySamples(fx.USER_ID, { type: "mindful_session" });
    assert.deepEqual(cat.data[0], {
      ...prov,
      id: 55,
      type: "mindful_session",
      valueInt: 0,
      valueLabel: null,
      startDate: "2026-01-10T08:00:00Z",
      endDate: "2026-01-10T08:10:00Z",
      sourceName: "iPhone",
      externalId: null,
      metadata: {},
    });
    assert.equal(requests[0]!.path, `/v1/users/${fx.USER_ID}/category-samples`);

    const sleep = await c.listSleep(fx.USER_ID, { start: "2026-01-09" });
    assert.deepEqual(sleep.data[0], {
      ...prov,
      source: null,
      id: 77,
      bedtimeStart: "2026-01-09T22:00:00Z",
      bedtimeEnd: "2026-01-10T06:00:00Z",
      totalSleepSeconds: 26100,
      deepSleepSeconds: 5400,
      remSleepSeconds: 6300,
      lightSleepSeconds: 14400,
      awakeSeconds: 900,
      sleepScore: 84,
      hrvAvgMs: 48.2,
      restingHr: 52,
      respiratoryRate: 14.1,
      spo2Avg: 96.5,
      externalId: "s-77",
      metadata: { timezone: "Europe/Paris" },
    });
    assert.deepEqual(requests[1]!.query, { start: "2026-01-09" });

    const phases = await c.listSleepPhases(fx.USER_ID, 77);
    assert.deepEqual(phases.data[0], {
      ...prov,
      id: 9001,
      sessionId: 77,
      phase: "deep",
      startDate: "2026-01-09T22:15:00Z",
      endDate: "2026-01-09T23:45:00Z",
      durationS: 5400,
    });
    assert.equal(requests[2]!.path, `/v1/users/${fx.USER_ID}/sleep/77/phases`);

    const w = await c.listWorkouts(fx.USER_ID, { type: "running" });
    assert.deepEqual(w.data[0], {
      ...prov,
      id: 4242,
      activityType: "running",
      startDate: "2026-01-10T07:00:00Z",
      endDate: "2026-01-10T07:45:00Z",
      durationS: 2700,
      activeEnergyKcal: 480,
      distanceM: 7200.5,
      avgHeartRate: 132,
      maxHeartRate: 165,
      avgSpeedMps: 2.67,
      elevationGainM: 35,
      vo2Max: null,
      externalId: "w-1",
      metadata: { indoor: false },
    });

    const route = await c.getWorkoutRoute(fx.USER_ID, 4242, { limit: 1000 });
    assert.deepEqual(route.data[0], {
      ...prov,
      id: 1,
      workoutId: 4242,
      timestamp: "2026-01-10T07:00:01Z",
      latitude: 48.8566,
      longitude: 2.3522,
      altitudeM: 35.2,
      speedMps: 2.7,
      courseDeg: 180,
    });
    assert.equal(requests[4]!.path, `/v1/users/${fx.USER_ID}/workouts/4242/route`);

    const n = await c.listNutrition(fx.USER_ID);
    assert.deepEqual(n.data[0], {
      ...prov,
      id: 31,
      startDate: "2026-01-10T12:00:00Z",
      endDate: null,
      mealType: "lunch",
      energyKcal: 650,
      proteinG: 32,
      carbsG: 70,
      fatG: 22,
      fiberG: 9,
      sugarG: 12,
      sodiumMg: 800,
      nutrients: { vitamin_c_mg: 45 },
      externalId: null,
      metadata: {},
    });

    const d = await c.listDaily(fx.USER_ID);
    assert.deepEqual(d.data[0], {
      ...prov,
      provider: "oura",
      id: 12,
      date: "2026-01-10",
      readinessScore: 81,
      activityScore: 77,
      sleepScore: 84,
      recoveryScore: null,
      strainScore: null,
      stepCount: 9832,
      activeKcal: 520.5,
      totalKcal: 2400,
      distanceM: 7400,
      floorsClimbed: 6,
      restingHr: 52,
      hrvAvgMs: 48,
      metadata: {},
    });

    const cov = await c.getCoverage(fx.USER_ID, { provider: "apple_health", start: "2026-01-01" });
    assert.deepEqual(cov, {
      pendingJobs: 0,
      parkedJobs: 1,
      rejectedRecords: 2,
      truncated: false,
      days: [
        {
          provider: "apple_health",
          date: "2026-01-10",
          records: 3,
          metrics: { heart_rate: 2, step_count: 1 },
          lastSyncedAt: "2026-01-10T15:00:00.5Z",
        },
      ],
    });
    assert.deepEqual(requests[7]!.query, { provider: "apple_health", start: "2026-01-01" });
  });
});

describe("FHIR", () => {
  it("returns the raw bundle, its observations and the next cursor", async () => {
    const { c, requests } = client([
      { body: fx.fhirBundle, headers: { "Content-Type": "application/fhir+json" } },
      { body: { resourceType: "Bundle", type: "searchset", link: [{ relation: "self", url: "https://x/y" }] } },
      { body: fx.fhirBundle.entry[0]!.resource },
    ]);
    const b = await c.searchFhirObservations(fx.USER_ID, { code: "http://loinc.org|8867-4", category: "vital-signs", limit: 1 });
    assert.equal(b.nextCursor, "eyJ0IjoiMjAyNi0wMS0xMCJ9==");
    assert.equal(b.observations.length, 1);
    assert.equal(b.observations[0]!["id"], "qs-1234");
    assert.equal(b.raw["resourceType"], "Bundle");
    const r = requests[0]!;
    assert.equal(r.path, `/v1/users/${fx.USER_ID}/fhir/Observation`);
    assert.equal(r.headers["Accept"], "application/fhir+json");
    assert.deepEqual(r.query, { code: "http://loinc.org|8867-4", category: "vital-signs", limit: "1" });

    const last = await c.searchFhirObservations(fx.USER_ID);
    assert.equal(last.nextCursor, null);
    assert.deepEqual(last.observations, []);

    const obs = await c.readFhirObservation(fx.USER_ID, "qs-1234");
    assert.equal(obs["id"], "qs-1234");
    assert.equal(requests[2]!.path, `/v1/users/${fx.USER_ID}/fhir/Observation/qs-1234`);
    assert.equal(requests[2]!.headers["Accept"], "application/fhir+json");
  });
});

describe("errors", () => {
  it("maps a problem+json body", async () => {
    const { c } = client([
      {
        status: 404,
        text: JSON.stringify({ type: "about:blank", title: "Not Found", status: 404, detail: "user not found", request_id: "req-123" }),
        headers: { "Content-Type": "application/problem+json", "X-Request-Id": "req-header" },
      },
    ]);
    const err = await c.getUser(fx.USER_ID).then(
      () => assert.fail("expected an error"),
      (e: unknown) => e,
    );
    assert.ok(err instanceof OhsApiError);
    assert.equal(err.status, 404);
    assert.equal(err.title, "Not Found");
    assert.equal(err.detail, "user not found");
    assert.equal(err.type, "about:blank");
    assert.equal(err.requestId, "req-123");
    assert.equal(err.retryAfterSeconds, undefined);
    assert.match(err.message, /404/);
    assert.match(err.message, /req-123/);
    assert.doesNotMatch(err.message, /ohs_sk_test/);
  });

  it("maps a body that is not JSON, taking the request id from the header", async () => {
    const { c } = client([{ status: 502, text: "<html>bad gateway</html>", headers: { "X-Request-Id": "rid-9" } }]);
    const err = await c.listProviders().catch((e: unknown) => e);
    assert.ok(err instanceof OhsApiError);
    assert.equal(err.status, 502);
    assert.equal(err.detail, undefined);
    assert.equal(err.requestId, "rid-9");
  });
});

describe("upload", () => {
  const part = () =>
    new AppleHealthPartBuilder("batch-1")
      .add("HKQuantityTypeIdentifierHeartRate", "2026-01-10", {
        quantityType: "HKQuantityTypeIdentifierHeartRate",
        startDate: "2026-01-10T14:30:00Z",
        endDate: "2026-01-10T14:30:00Z",
        value: 72,
        unit: "count/min",
      })
      .build()[0]!;

  it("sends the part headers and the gzip body", async () => {
    const { c, requests } = client([{ body: { key: "raw/t/u/apple_health/2026-01-10/batch-1_0.ndjson.gz", duplicate: false } }]);
    const res = await c.uploadAppleHealthPart(part());
    assert.deepEqual(res, { key: "raw/t/u/apple_health/2026-01-10/batch-1_0.ndjson.gz", duplicate: false });
    const r = requests[0]!;
    assert.equal(r.method, "POST");
    assert.equal(r.path, "/v1/ingest/apple-health");
    assert.equal(r.headers["Content-Type"], "application/gzip");
    assert.equal(r.headers["X-OHS-Type"], "HKQuantityTypeIdentifierHeartRate");
    assert.equal(r.headers["X-OHS-Day"], "2026-01-10");
    assert.equal(r.headers["X-OHS-Batch"], "batch-1");
    assert.equal(r.headers["X-OHS-Part"], "0");
    assert.ok(r.body instanceof Uint8Array);
    const lines = gunzipSync(r.body).toString("utf8");
    assert.equal(
      lines,
      '{"quantityType":"HKQuantityTypeIdentifierHeartRate","startDate":"2026-01-10T14:30:00Z","endDate":"2026-01-10T14:30:00Z","value":72,"unit":"count/min"}\n',
    );
  });

  it("retries a 503 after Retry-After, then succeeds", async () => {
    let tokens = 0;
    const { c, requests, sleeps } = client(
      [
        { status: 503, body: { title: "Service Unavailable", status: 503 }, headers: { "Retry-After": "7" } },
        { body: { key: "k", duplicate: true } },
      ],
      { token: undefined, tokenProvider: () => `t${++tokens}` },
    );
    const res = await c.uploadAppleHealthPart(part());
    assert.deepEqual(res, { key: "k", duplicate: true });
    assert.equal(requests.length, 2);
    assert.deepEqual(sleeps, [7000]);
    assert.equal(requests[1]!.headers["Authorization"], "Bearer t2");
  });

  it("retries 429, 500 and network errors with exponential backoff", async () => {
    const { c, sleeps } = client([
      { status: 429 },
      { networkError: true },
      { status: 500, text: "oops" },
      { body: { key: "k", duplicate: false } },
    ]);
    await c.uploadAppleHealthPart(part());
    assert.deepEqual(sleeps, [1000, 2000, 4000]);
  });

  it("does not retry a 400", async () => {
    const { c, requests, sleeps } = client([
      { status: 400, body: { title: "Bad Request", status: 400, detail: "X-OHS-Day is invalid", request_id: "r1" } },
    ]);
    const err = await c.uploadAppleHealthPart(part()).catch((e: unknown) => e);
    assert.ok(err instanceof OhsApiError);
    assert.equal(err.status, 400);
    assert.equal(requests.length, 1);
    assert.deepEqual(sleeps, []);
  });

  it("gives up after 5 attempts", async () => {
    const { c, requests, sleeps } = client(() => ({ status: 503, headers: { "X-Request-Id": "busy" } }));
    const err = await c.uploadAppleHealthPart(part()).catch((e: unknown) => e);
    assert.ok(err instanceof OhsApiError);
    assert.equal(err.status, 503);
    assert.equal(err.requestId, "busy");
    assert.equal(requests.length, 5);
    assert.deepEqual(sleeps, [1000, 2000, 4000, 8000]);
  });

  it("caps the backoff at 30 seconds", async () => {
    const { c, sleeps } = client(() => ({ status: 503 }), { maxUploadAttempts: 8 });
    await c.uploadAppleHealthPart(part()).catch(() => undefined);
    assert.deepEqual(sleeps, [1000, 2000, 4000, 8000, 16000, 30000, 30000]);
  });

  it("uploads parts in order", async () => {
    const parts = new AppleHealthPartBuilder("b")
      .add("HKQuantityTypeIdentifierStepCount", "2026-01-10", { value: 1 })
      .add("HKQuantityTypeIdentifierHeartRate", "2026-01-10", { value: 2 })
      .build();
    const { c, requests } = client([
      { body: { key: "k0", duplicate: false } },
      { body: { key: "k1", duplicate: false } },
    ]);
    const res = await c.uploadParts(parts);
    assert.deepEqual(res.map((r) => r.key), ["k0", "k1"]);
    assert.deepEqual(requests.map((r) => r.headers["X-OHS-Part"]), ["0", "1"]);
  });
});

describe("pagination", () => {
  it("walks every page", async () => {
    const { c, requests } = client([
      page([fx.sample, { ...fx.sample, id: 2 }], "c1"),
      page([{ ...fx.sample, id: 3 }], "c2"),
      page([{ ...fx.sample, id: 4 }]),
    ]);
    const ids: number[] = [];
    for await (const s of paginate((cursor) => c.listSamples(fx.USER_ID, { type: "heart_rate", cursor }))) {
      ids.push(s.id);
    }
    assert.deepEqual(ids, [1234, 2, 3, 4]);
    assert.deepEqual(
      requests.map((r) => r.query["cursor"]),
      [undefined, "c1", "c2"],
    );
  });

  it("collects and stops on a repeated cursor", async () => {
    let calls = 0;
    const pages: Page<number>[] = [{ data: [1], nextCursor: "a" }, { data: [2], nextCursor: null }];
    assert.deepEqual(await collectAll(async () => pages[calls++]!), [1, 2]);
    await assert.rejects(() => collectAll(async () => ({ data: [1], nextCursor: "same" })), /same cursor/);
  });
});
