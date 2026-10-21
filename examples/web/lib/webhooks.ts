// SPDX-License-Identifier: AGPL-3.0-only
//
// The webhook receiver: checks each request with the SDK's verifyWebhook (Standard Webhooks,
// HMAC-SHA256, five minutes of tolerance) and keeps the last 20 requests in memory for the
// webhooks page. A real app would queue the work and answer at once, as this one does.

import { verifyWebhook, WebhookVerificationError, type WebhookEvent } from "@open-health-sync/server";

export const INBOX_SIZE = 20;

export interface InboxEntry {
  receivedAt: string;
  /** The webhook-id header: the event id, the same on every retry. */
  webhookId: string;
  verified: boolean;
  /** The event, for a verified request only. */
  event?: WebhookEvent;
  /** Why a request was refused. Nothing else of a refused request is kept. */
  reason?: string;
  /** How many times this event arrived (a retry or a replay repeats the id). */
  deliveries: number;
}

interface Inbox {
  entries: InboxEntry[];
  /** A secret registered from the webhooks page, used while OHS_WEBHOOK_SECRET is not set. */
  runtimeSecret?: string;
}

// On globalThis: Next.js builds each route into its own bundle, and the route handler and the
// page must see the same inbox. It also survives a reload in development.
const KEY = Symbol.for("open-health-sync.web-example.webhooks");

function inbox(): Inbox {
  const holder = globalThis as typeof globalThis & { [KEY]?: Inbox };
  holder[KEY] ??= { entries: [] };
  return holder[KEY];
}

/** The requests received, newest first. */
export function recentEntries(): InboxEntry[] {
  return inbox().entries.map((entry) => ({ ...entry }));
}

export function clearEntries(): void {
  inbox().entries = [];
}

export function setRuntimeSecret(secret: string): void {
  inbox().runtimeSecret = secret;
}

export interface SecretSource {
  secret: string;
  from: "env" | "registered";
}

/** The secret to verify with: OHS_WEBHOOK_SECRET, else one registered from this app. */
export function webhookSecret(env: Record<string, string | undefined> = process.env): SecretSource | undefined {
  const fromEnv = env.OHS_WEBHOOK_SECRET?.trim();
  if (fromEnv) return { secret: fromEnv, from: "env" };
  const registered = inbox().runtimeSecret;
  return registered ? { secret: registered, from: "registered" } : undefined;
}

function remember(entry: Omit<InboxEntry, "deliveries">): void {
  const box = inbox();
  const seen = entry.verified ? box.entries.find((e) => e.verified && e.webhookId === entry.webhookId) : undefined;
  if (seen) {
    // Deduplicate on the event id: delivery is at least once.
    seen.deliveries += 1;
    seen.receivedAt = entry.receivedAt;
    return;
  }
  box.entries.unshift({ ...entry, deliveries: 1 });
  box.entries.length = Math.min(box.entries.length, INBOX_SIZE);
}

export interface ReceiveOptions {
  /** The whsec_ secret; without one every request is refused with 503, so it is retried later. */
  secret: string | undefined;
  /** The time to check webhook-timestamp against; now by default (tests pass a fixed time). */
  now?: Date;
}

/** Handles one POST of Open Health Sync. Answers 204 when the signature holds, 400 when not. */
export async function receiveWebhook(request: Request, options: ReceiveOptions): Promise<Response> {
  // The raw bytes, as received: a body parsed and serialized again would not verify.
  const body = new Uint8Array(await request.arrayBuffer());
  const webhookId = (request.headers.get("webhook-id") ?? "").slice(0, 100);
  const receivedAt = (options.now ?? new Date()).toISOString();

  if (!options.secret) {
    remember({ receivedAt, webhookId, verified: false, reason: "no secret configured: set OHS_WEBHOOK_SECRET" });
    return Response.json({ error: "webhook secret not configured" }, { status: 503 });
  }
  try {
    const event = verifyWebhook(options.secret, request.headers, body, options.now ? { now: options.now } : {});
    remember({ receivedAt, webhookId, verified: true, event });
    return new Response(null, { status: 204 });
  } catch (err) {
    if (!(err instanceof WebhookVerificationError)) throw err;
    remember({ receivedAt, webhookId, verified: false, reason: err.message });
    return Response.json({ error: err.message }, { status: 400 });
  }
}
