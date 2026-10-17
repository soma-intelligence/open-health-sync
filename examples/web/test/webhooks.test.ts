// SPDX-License-Identifier: AGPL-3.0-only
//
// The webhook receiver against the vectors the server's own code signs
// (sdks/server/testdata/webhook-vectors.json), and the route handler with a fresh signature.

import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { readFileSync } from "node:fs";
import { beforeEach, test } from "node:test";
import { POST } from "../app/api/webhooks/route.ts";
import { INBOX_SIZE, clearEntries, receiveWebhook, recentEntries } from "../lib/webhooks.ts";

interface Vector {
  name: string;
  key_text: string;
  id: string;
  timestamp: string;
  now: number;
  body: string;
  signature: string;
  valid: boolean;
}

const file = new URL("../../../sdks/server/testdata/webhook-vectors.json", import.meta.url);
const { vectors } = JSON.parse(readFileSync(file, "utf8")) as { vectors: Vector[] };

const secretOf = (keyText: string) => `whsec_${Buffer.from(keyText, "utf8").toString("base64")}`;

function request(headers: Record<string, string>, body: string): Request {
  return new Request("http://localhost:3000/api/webhooks", {
    method: "POST",
    headers: { "content-type": "application/json", ...headers },
    body,
  });
}

beforeEach(() => {
  clearEntries();
  delete process.env.OHS_WEBHOOK_SECRET;
});

for (const v of vectors) {
  test(`vector: ${v.name}`, async () => {
    const headers = { "webhook-id": v.id, "webhook-timestamp": v.timestamp, "webhook-signature": v.signature };
    const res = await receiveWebhook(request(headers, v.body), {
      secret: secretOf(v.key_text),
      now: new Date(v.now * 1000),
    });
    const [entry] = recentEntries();
    assert.ok(entry);
    if (v.valid) {
      assert.equal(res.status, 204);
      assert.equal(entry.verified, true);
      assert.equal(entry.event?.id, JSON.parse(v.body).id);
    } else {
      assert.equal(res.status, 400);
      assert.equal(entry.verified, false);
      assert.equal(entry.event, undefined, "nothing of a refused request is kept but the reason");
      assert.ok(entry.reason);
    }
  });
}

function sign(secret: string, id: string, timestamp: number, body: string): string {
  const key = Buffer.from(secret.slice("whsec_".length), "base64");
  const mac = createHmac("sha256", key).update(`${id}.${timestamp}.${body}`).digest("base64");
  return `v1,${mac}`;
}

const secret = secretOf("web example test secret");
const event = {
  id: "0199d6c4-8a5e-7d3c-9f1e-6b2a4c8d0e17",
  type: "webhook.test",
  timestamp: "2026-10-12T08:30:12.345Z",
  test: true,
  data: { endpoint_id: "7d1c2b3a-0000-4000-8000-000000000001" },
};

test("the route accepts a request signed now with OHS_WEBHOOK_SECRET", async () => {
  process.env.OHS_WEBHOOK_SECRET = secret;
  const body = JSON.stringify(event);
  const ts = Math.floor(Date.now() / 1000);
  const headers = { "webhook-id": event.id, "webhook-timestamp": String(ts), "webhook-signature": sign(secret, event.id, ts, body) };
  const res = await POST(request(headers, body));
  assert.equal(res.status, 204);
  assert.equal(recentEntries()[0]?.event?.type, "webhook.test");

  // A retry repeats the event id: kept once, counted twice.
  await POST(request(headers, body));
  assert.equal(recentEntries().length, 1);
  assert.equal(recentEntries()[0]?.deliveries, 2);
});

test("the route refuses a body changed after signing", async () => {
  process.env.OHS_WEBHOOK_SECRET = secret;
  const body = JSON.stringify(event);
  const ts = Math.floor(Date.now() / 1000);
  const headers = { "webhook-id": event.id, "webhook-timestamp": String(ts), "webhook-signature": sign(secret, event.id, ts, body) };
  const res = await POST(request(headers, body.replace("webhook.test", "data.written")));
  assert.equal(res.status, 400);
  assert.equal(recentEntries()[0]?.verified, false);
});

test("the route refuses another secret's signature", async () => {
  process.env.OHS_WEBHOOK_SECRET = secret;
  const body = JSON.stringify(event);
  const ts = Math.floor(Date.now() / 1000);
  const other = secretOf("another secret");
  const headers = { "webhook-id": event.id, "webhook-timestamp": String(ts), "webhook-signature": sign(other, event.id, ts, body) };
  assert.equal((await POST(request(headers, body))).status, 400);
});

test("without a secret the route answers 503, so the server retries later", async () => {
  const res = await POST(request({ "webhook-id": "x", "webhook-timestamp": "1", "webhook-signature": "v1,AA==" }, "{}"));
  assert.equal(res.status, 503);
});

test("the inbox keeps the last 20 requests", async () => {
  process.env.OHS_WEBHOOK_SECRET = secret;
  const ts = Math.floor(Date.now() / 1000);
  for (let i = 0; i < INBOX_SIZE + 5; i++) {
    const id = `event-${i}`;
    const body = JSON.stringify({ ...event, id });
    await POST(request({ "webhook-id": id, "webhook-timestamp": String(ts), "webhook-signature": sign(secret, id, ts, body) }, body));
  }
  const entries = recentEntries();
  assert.equal(entries.length, INBOX_SIZE);
  assert.equal(entries[0]?.event?.id, `event-${INBOX_SIZE + 4}`, "newest first");
});
