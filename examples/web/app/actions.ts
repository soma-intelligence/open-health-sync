// SPDX-License-Identifier: AGPL-3.0-only
"use server";

// The Server Actions behind the forms. They run on the server with the tenant API key; the
// browser posts the form and gets an ActionResult back, never the key.

import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import type { WebhookEventType } from "@open-health-sync/server";
import type { ActionResult } from "../lib/action-result.ts";
import { requireConfig, webhookUrl } from "../lib/config.ts";
import { formatDateTime, formatDay } from "../components/ui.tsx";
import { showError, tenantClient } from "../lib/ohs.ts";
import { uploadSampleData } from "../lib/upload.ts";
import { clearEntries, setRuntimeSecret } from "../lib/webhooks.ts";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function failed(what: string, err: unknown): ActionResult {
  const e = showError(err);
  const where = [e.status && `HTTP ${e.status}`, e.requestId && `request ${e.requestId}`].filter(Boolean).join(", ");
  return { ok: false, message: `${what} failed: ${e.message}${where ? ` (${where})` : ""}` };
}

/** Step 1: POST /v1/users, then open the new user's page. */
export async function createUser(_: ActionResult | null, form: FormData): Promise<ActionResult | null> {
  const ref = String(form.get("external_ref") ?? "").trim();
  // The API takes up to 255 characters; 200 leaves room for the idempotency key's prefix.
  if (!ref || ref.length > 200) return { ok: false, message: "Give the user a reference of 1 to 200 characters." };
  let id: string;
  try {
    // The idempotency key makes a double submit create one user, not two.
    id = (await tenantClient().createUser({ external_ref: ref }, { idempotencyKey: `web-example-${ref}` })).id;
  } catch (err) {
    return failed("Creating the user", err);
  }
  revalidatePath("/");
  redirect(`/users/${id}`);
}

/** Step 2a: POST /v1/users/{id}/connect-sessions, then send the browser to the hosted page. */
export async function connectSource(userId: string, _: ActionResult | null): Promise<ActionResult | null> {
  if (!UUID.test(userId)) return { ok: false, message: "Unknown user." };
  let url: string;
  try {
    const { appUrl } = requireConfig();
    const session = await tenantClient().createConnectSession(userId, {
      // After each consent the user comes back here, with ?status=connected&provider=<slug>.
      redirect_uri: `${appUrl}/users/${userId}`,
      app_name: "Open Health Sync web example",
      ttl_seconds: 900,
    });
    url = session.url;
  } catch (err) {
    return failed("Creating the connection link", err);
  }
  redirect(url);
}

/** Step 2b: mint an upload-only token, upload the synthetic parts with it. */
export async function uploadSample(userId: string, _: ActionResult | null): Promise<ActionResult | null> {
  if (!UUID.test(userId)) return { ok: false, message: "Unknown user." };
  try {
    const s = await uploadSampleData(userId);
    revalidatePath(`/users/${userId}`);
    const types = Object.entries(s.types)
      .map(([type, lines]) => `${type.replace(/^HK(Quantity|Category)TypeIdentifier/, "")} ${lines}`)
      .join(", ");
    return {
      ok: true,
      message:
        s.duplicates === s.parts
          ? `All ${s.parts} parts were already stored: the server answered "duplicate" for each, so nothing is written twice.`
          : `Uploaded ${s.parts} parts (${s.lines} lines): ${s.stored} stored, ${s.duplicates} already there. The server processes them in the background.`,
      facts: [
        ["Days", `${formatDay(s.firstDay)} to ${formatDay(s.lastDay)}`],
        ["Lines", types],
        ["Batch", s.batch],
        ["First key", s.firstKey],
        ["Token", `upload scope only, expires ${formatDateTime(s.tokenExpiresAt)}`],
      ],
    };
  } catch (err) {
    return failed("The upload", err);
  }
}

/** Step 4: POST /v1/webhooks for this app's receiver. The secret is kept in memory and shown once. */
export async function registerWebhook(_: ActionResult | null): Promise<ActionResult | null> {
  try {
    const config = requireConfig();
    const created = await tenantClient().createWebhook({
      url: webhookUrl(config),
      description: "Open Health Sync web example",
    });
    setRuntimeSecret(created.secret);
    revalidatePath("/webhooks");
    return {
      ok: true,
      message: `Registered endpoint ${created.id} for every event type. This app verifies with its secret until it restarts.`,
      secret: created.secret,
    };
  } catch (err) {
    return failed("Registering the endpoint", err);
  }
}

/** Step 4: POST /v1/webhooks/{id}/rotate-secret, when this app no longer has the secret. */
export async function rotateSecret(webhookId: string, _: ActionResult | null): Promise<ActionResult | null> {
  if (!UUID.test(webhookId)) return { ok: false, message: "Unknown endpoint." };
  try {
    const { secret } = await tenantClient().rotateWebhookSecret(webhookId);
    setRuntimeSecret(secret);
    revalidatePath("/webhooks");
    return { ok: true, message: "New secret, used from the next request on. This app verifies with it until it restarts.", secret };
  } catch (err) {
    return failed("Rotating the secret", err);
  }
}

const EVENT_TYPES = new Set<string>(["webhook.test", "data.written", "connection.needs_reauth", "user.deleted"]);

/** Step 4: POST /v1/webhooks/{id}/test, a sample event about no real user. */
export async function sendTestEvent(webhookId: string, _: ActionResult | null, form: FormData): Promise<ActionResult | null> {
  if (!UUID.test(webhookId)) return { ok: false, message: "Unknown endpoint." };
  const type = String(form.get("event_type") ?? "webhook.test");
  if (!EVENT_TYPES.has(type)) return { ok: false, message: "Pick an event type from the list." };
  try {
    const delivery = await tenantClient().testWebhook(
      webhookId,
      type === "webhook.test" ? {} : { event_type: type as WebhookEventType },
    );
    return {
      ok: true,
      message: `Queued delivery ${delivery.id} (${delivery.event_type}). The worker posts it within seconds; it appears below.`,
    };
  } catch (err) {
    return failed("Sending the test event", err);
  }
}

export async function clearInbox(): Promise<void> {
  clearEntries();
  revalidatePath("/webhooks");
}
