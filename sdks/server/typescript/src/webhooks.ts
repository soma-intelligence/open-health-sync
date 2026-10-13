// SPDX-License-Identifier: AGPL-3.0-only
//
// Verification of the webhooks Open Health Sync sends, per Standard Webhooks:
// webhook-signature is "v1," + base64 HMAC-SHA256 of "{webhook-id}.{webhook-timestamp}.{body}",
// keyed by the base64-decoded secret after "whsec_". Written by hand (not generated) and
// tested against vectors signed by the server's own code (sdks/server/testdata).

import { createHmac, timingSafeEqual } from "node:crypto";
import type { WebhookEvent } from "./types.ts";

/** How far webhook-timestamp may be from the local clock, in seconds. */
export const WEBHOOK_TOLERANCE_SECONDS = 300;

/** A webhook request that must be refused. */
export class WebhookVerificationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "WebhookVerificationError";
  }
}

/** Request headers: a fetch Headers, or a plain object such as Node's IncomingHttpHeaders. */
export type WebhookHeaders = Headers | Record<string, string | string[] | undefined>;

export interface VerifyWebhookOptions {
  /** The time to check webhook-timestamp against; now by default. */
  now?: Date;
  /** Seconds of tolerance on webhook-timestamp; 300 by default. */
  toleranceSeconds?: number;
}

function header(headers: WebhookHeaders, name: string): string | undefined {
  if (typeof (headers as Headers).get === "function") {
    return (headers as Headers).get(name) ?? undefined;
  }
  const record = headers as Record<string, string | string[] | undefined>;
  for (const [key, value] of Object.entries(record)) {
    if (key.toLowerCase() === name) return Array.isArray(value) ? value.join(" ") : value;
  }
  return undefined;
}

const base64 = /^[A-Za-z0-9+/]*={0,2}$/;

function secretKey(secret: string): Buffer {
  const raw = secret.startsWith("whsec_") ? secret.slice("whsec_".length) : "";
  if (!raw || raw.length % 4 !== 0 || !base64.test(raw)) {
    throw new WebhookVerificationError("malformed secret: expected whsec_ followed by base64");
  }
  return Buffer.from(raw, "base64");
}

/**
 * Checks a webhook request and returns its event. Pass the raw body, exactly as received:
 * a body parsed and serialized again does not verify. Throws WebhookVerificationError when
 * a header is missing, the timestamp is more than five minutes off, or no v1 signature
 * matches. Deduplicate on the event id (the webhook-id header): a retry repeats it.
 */
export function verifyWebhook(
  secret: string,
  headers: WebhookHeaders,
  body: string | Uint8Array,
  options: VerifyWebhookOptions = {},
): WebhookEvent {
  const key = secretKey(secret);
  const id = header(headers, "webhook-id");
  const timestamp = header(headers, "webhook-timestamp");
  const signatures = header(headers, "webhook-signature");
  if (!id || !timestamp || !signatures) {
    throw new WebhookVerificationError("missing webhook-id, webhook-timestamp or webhook-signature");
  }
  if (!/^-?\d+$/.test(timestamp)) {
    throw new WebhookVerificationError("webhook-timestamp is not a number of seconds");
  }
  const now = Math.floor((options.now ?? new Date()).getTime() / 1000);
  const tolerance = options.toleranceSeconds ?? WEBHOOK_TOLERANCE_SECONDS;
  if (Math.abs(now - Number(timestamp)) > tolerance) {
    throw new WebhookVerificationError("webhook-timestamp is too far from now");
  }

  const bytes = typeof body === "string" ? Buffer.from(body, "utf8") : Buffer.from(body);
  const expected = createHmac("sha256", key).update(`${id}.${timestamp}.`).update(bytes).digest();
  const matched = signatures.trim().split(/\s+/).some((signature) => {
    const [version, value] = signature.split(",", 2);
    if (version !== "v1" || !value || !base64.test(value)) return false;
    const given = Buffer.from(value, "base64");
    return given.length === expected.length && timingSafeEqual(given, expected);
  });
  if (!matched) throw new WebhookVerificationError("no matching v1 signature");
  return JSON.parse(bytes.toString("utf8")) as WebhookEvent;
}
