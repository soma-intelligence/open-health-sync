import type { WebhookEventType } from "../../api/types";

/** The event types an endpoint may subscribe to. */
export const EVENT_TYPES = [
  "data.written",
  "data.deleted",
  "data.erased",
  "user.deleted",
  "connection.needs_reauth",
  "connection.broken",
  "connection.restored",
  "consent.granted",
  "consent.revoked",
  "tag.created",
  "tag.updated",
  "tag.deleted",
  "share.created",
  "share.revoked",
  "share.accessed",
] as const satisfies readonly WebhookEventType[];

// Fails tsc when the schema has an event type this list does not.
type Missing = Exclude<WebhookEventType, (typeof EVENT_TYPES)[number]>;
export const EVENT_TYPES_COMPLETE: [Missing] extends [never] ? true : never = true;
