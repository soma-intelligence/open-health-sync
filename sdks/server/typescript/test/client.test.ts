// SPDX-License-Identifier: AGPL-3.0-only
//
// The generated client against a local HTTP server that records each request and answers
// what the test queued.

import assert from "node:assert/strict";
import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { after, before, beforeEach, test } from "node:test";
import { OhsError, OpenHealthSync } from "../src/index.ts";

interface Seen {
  method: string;
  url: URL;
  headers: IncomingMessage["headers"];
  body: Buffer;
}

interface Answer {
  status: number;
  headers?: Record<string, string>;
  body?: string | Buffer;
}

let server: Server;
let baseUrl: string;
let seen: Seen[] = [];
let answers: Answer[] = [];

function json(status: number, value: unknown, headers: Record<string, string> = {}): Answer {
  return { status, headers: { "Content-Type": "application/json", ...headers }, body: JSON.stringify(value) };
}

before(async () => {
  server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      seen.push({
        method: req.method ?? "",
        url: new URL(req.url ?? "/", "http://localhost"),
        headers: req.headers,
        body: Buffer.concat(chunks),
      });
      const answer = answers.shift() ?? json(500, { title: "no answer queued" });
      res.writeHead(answer.status, answer.headers);
      res.end(answer.body);
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  baseUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}/`;
});

after(() => {
  server.close();
});

beforeEach(() => {
  seen = [];
  answers = [];
});

const userId = "3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11";
const client = () => new OpenHealthSync({ baseUrl, apiKey: "ohs_sk_EXAMPLE" });

test("sends the key as a bearer token and reads JSON", async () => {
  answers.push(json(200, { data: [{ slug: "withings", kinds: ["oauth"] }] }));
  const providers = await client().listProviders();
  assert.deepEqual(providers.data[0]?.slug, "withings");
  assert.equal(seen[0]?.method, "GET");
  assert.equal(seen[0]?.url.pathname, "/v1/providers");
  assert.equal(seen[0]?.headers.authorization, "Bearer ohs_sk_EXAMPLE");
  assert.equal(seen[0]?.headers.accept, "application/json");
});

test("posts a JSON body with an Idempotency-Key", async () => {
  const user = { id: userId, external_ref: "customer-42", status: "active", created_at: "2026-01-01T00:00:00Z" };
  answers.push(json(201, user));
  const created = await client().createUser({ external_ref: "customer-42" }, { idempotencyKey: "k-1" });
  assert.deepEqual(created, user);
  assert.equal(seen[0]?.method, "POST");
  assert.equal(seen[0]?.headers["content-type"], "application/json");
  assert.equal(seen[0]?.headers["idempotency-key"], "k-1");
  assert.deepEqual(JSON.parse(seen[0]?.body.toString() ?? ""), { external_ref: "customer-42" });
});

test("sends no body when an optional body is left out", async () => {
  answers.push(json(200, { authorize_url: "https://provider.example/consent" }));
  const res = await client().startConnection(userId, "withings");
  assert.equal(res.authorize_url, "https://provider.example/consent");
  assert.equal(seen[0]?.url.pathname, `/v1/users/${userId}/connections/withings`);
  assert.equal(seen[0]?.body.length, 0);
  assert.equal(seen[0]?.headers["content-type"], undefined);
});

test("encodes path parameters and drops absent query parameters", async () => {
  answers.push(json(200, { data: [], next_cursor: null }));
  await client().listSamples("a/b", { type: "heart_rate", start: "2026-01-01", limit: 10 });
  const url = seen[0]?.url;
  assert.equal(url?.pathname, "/v1/users/a%2Fb/samples");
  assert.deepEqual(Object.fromEntries(url?.searchParams ?? []), { type: "heart_rate", start: "2026-01-01", limit: "10" });
});

test("an error answer is an OhsError with the problem and the request id", async () => {
  answers.push({
    status: 404,
    headers: { "Content-Type": "application/problem+json", "X-Request-Id": "req-123" },
    body: JSON.stringify({ type: "about:blank", title: "Not Found", status: 404, detail: "no such user", request_id: "req-123" }),
  });
  const err = await client().getUser(userId).catch((e: unknown) => e);
  assert.ok(err instanceof OhsError);
  assert.equal(err.status, 404);
  assert.equal(err.title, "Not Found");
  assert.equal(err.detail, "no such user");
  assert.equal(err.requestId, "req-123");
  assert.equal(err.message, "404 Not Found: no such user");
});

test("an error answer that is not a problem keeps the status", async () => {
  answers.push({ status: 502, headers: { "Content-Type": "text/plain" }, body: "bad gateway" });
  const err = await client().listProviders().catch((e: unknown) => e);
  assert.ok(err instanceof OhsError);
  assert.equal(err.status, 502);
  assert.equal(err.body, "bad gateway");
});

test("204 resolves to undefined", async () => {
  answers.push({ status: 204 });
  assert.equal(await client().deleteWebhook("w1"), undefined);
  assert.equal(seen[0]?.method, "DELETE");
});

test("an iterator follows next_cursor across pages", async () => {
  answers.push(json(200, { data: [{ id: 1 }, { id: 2 }], next_cursor: "c2" }));
  answers.push(json(200, { data: [{ id: 3 }], next_cursor: null }));
  const ids: number[] = [];
  for await (const s of client().iterDaily(userId, { start: "2026-01-01", limit: 2 })) ids.push(s.id);
  assert.deepEqual(ids, [1, 2, 3]);
  assert.equal(seen[0]?.url.searchParams.get("cursor"), null);
  assert.equal(seen[1]?.url.searchParams.get("cursor"), "c2");
  assert.equal(seen[1]?.url.searchParams.get("start"), "2026-01-01");
});

test("an export is returned as the Response, to stream", async () => {
  answers.push({ status: 200, headers: { "Content-Type": "text/csv" }, body: "id,type\n1,heart_rate\n" });
  const res = await client().exportDataset(userId, "samples", { format: "csv" });
  assert.equal(await res.text(), "id,type\n1,heart_rate\n");
  assert.equal(seen[0]?.url.pathname, `/v1/users/${userId}/exports/samples`);
});

test("an upload sends its headers and the raw bytes", async () => {
  answers.push(json(200, { key: "raw/key", duplicate: false }));
  const part = new Uint8Array([0x1f, 0x8b, 0x08]);
  const res = await client().ingestAppleHealth(part, {
    ohsType: "HKQuantityTypeIdentifierHeartRate",
    ohsDay: "2026-01-01",
    ohsBatch: "b1",
    ohsPart: 0,
  });
  assert.equal(res.duplicate, false);
  assert.equal(seen[0]?.headers["content-type"], "application/gzip");
  assert.equal(seen[0]?.headers["x-ohs-part"], "0");
  assert.equal(seen[0]?.headers["x-ohs-type"], "HKQuantityTypeIdentifierHeartRate");
  assert.deepEqual([...(seen[0]?.body ?? [])], [0x1f, 0x8b, 0x08]);
});

test("a custom fetch is used", async () => {
  const calls: string[] = [];
  const ohs = new OpenHealthSync({
    baseUrl: "https://health.example.com",
    apiKey: "ohs_sk_EXAMPLE",
    fetch: async (input) => {
      calls.push(String(input));
      return new Response(JSON.stringify({ status: "ok" }), { headers: { "Content-Type": "application/json" } });
    },
  });
  assert.deepEqual(await ohs.healthz(), { status: "ok" });
  assert.deepEqual(calls, ["https://health.example.com/healthz"]);
});
