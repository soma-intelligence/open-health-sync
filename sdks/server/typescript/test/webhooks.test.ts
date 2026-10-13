// SPDX-License-Identifier: AGPL-3.0-only
//
// verifyWebhook against the vectors the server's Go code signs (sdks/server/testdata).

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { verifyWebhook, WebhookVerificationError } from "../src/index.ts";

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

const file = new URL("../../testdata/webhook-vectors.json", import.meta.url);
const { vectors } = JSON.parse(readFileSync(file, "utf8")) as { vectors: Vector[] };

const secretOf = (keyText: string) => `whsec_${Buffer.from(keyText, "utf8").toString("base64")}`;

for (const v of vectors) {
  test(`vector: ${v.name}`, () => {
    const headers = { "webhook-id": v.id, "webhook-timestamp": v.timestamp, "webhook-signature": v.signature };
    const verify = () => verifyWebhook(secretOf(v.key_text), headers, v.body, { now: new Date(v.now * 1000) });
    if (v.valid) {
      const event = verify();
      assert.equal(event.id, JSON.parse(v.body).id);
    } else {
      assert.throws(verify, WebhookVerificationError);
    }
  });
}

test("reads fetch Headers and byte bodies", () => {
  const v = vectors.find((x) => x.valid);
  assert.ok(v);
  const headers = new Headers({ "Webhook-Id": v.id, "Webhook-Timestamp": v.timestamp, "Webhook-Signature": v.signature });
  const event = verifyWebhook(secretOf(v.key_text), headers, new TextEncoder().encode(v.body), {
    now: new Date(v.now * 1000),
  });
  assert.equal(event.type, "data.written");
});

test("refuses a malformed secret", () => {
  const v = vectors[0];
  assert.ok(v);
  const headers = { "webhook-id": v.id, "webhook-timestamp": v.timestamp, "webhook-signature": v.signature };
  assert.throws(() => verifyWebhook("not-a-secret", headers, v.body), WebhookVerificationError);
});
