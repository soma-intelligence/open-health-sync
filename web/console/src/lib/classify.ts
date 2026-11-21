// What a status looks like: ok (green), warn (amber), bad (red), off (grey).
import { humanize } from "./format";

export type Tone = "ok" | "warn" | "bad" | "off";

type Maybe<T> = T | null | undefined;

/** A source's freshness: stale, failing and needs_reauth are bad, syncing is warn, disconnected is off. */
export function classifyFreshness(src: Maybe<{ state: string; consecutive_failures?: number }>): Tone {
  switch (src?.state) {
    case "fresh":
      return (src.consecutive_failures ?? 0) > 0 ? "warn" : "ok";
    case "syncing":
      return "warn";
    case "disconnected":
      return "off";
    case "stale":
    case "failing":
    case "needs_reauth":
      return "bad";
    default:
      return "warn";
  }
}

export function classifyConnection(conn: Maybe<{ status: string }>): Tone {
  switch (conn?.status) {
    case "active":
      return "ok";
    case "needs_reauth":
      return "bad";
    case "disconnected":
      return "off";
    default:
      return "warn";
  }
}

interface WebhookState {
  status: string;
  failing_since?: string | null;
}

/** A disabled endpoint, or one failing right now, is bad. */
export function classifyWebhook(w: Maybe<WebhookState>): Tone {
  if (!w) return "warn";
  if (w.status === "disabled" || w.failing_since) return "bad";
  if (w.status === "active") return "ok";
  return "warn";
}

/** An endpoint's state: a failing one reads "Failing" even while it is on, so the label agrees with its tone. */
export function webhookLabel(w: Maybe<WebhookState>): string {
  if (!w) return "-";
  if (w.status === "disabled") return "Turned off";
  if (w.failing_since) return "Failing";
  return humanize(w.status);
}

/** Parked is bad, a retrying delivery is warn, done is ok. */
export function classifyDelivery(d: Maybe<{ status: string; attempts?: number; last_error?: string | null }>): Tone {
  if (!d) return "warn";
  if (d.status === "parked") return "bad";
  if (d.status === "done") return "ok";
  if (d.last_error || (d.attempts ?? 0) > 0) return "warn";
  return "ok";
}

/** A user being deleted is off. */
export function classifyUser(u: Maybe<{ status: string }>): Tone {
  return u?.status === "active" ? "ok" : "off";
}

export function classifyProvider(p: Maybe<{ status: string }>): Tone {
  switch (p?.status) {
    case "active":
    case "built_in":
      return "ok";
    case "error":
      return "bad";
    default:
      // Off, or not configured: a provider nobody set up is not a fault.
      return "off";
  }
}

const PROVIDER_STATUS: Record<string, string> = {
  active: "On",
  built_in: "Built in",
  off: "Off",
  not_configured: "Not configured",
  error: "Error",
};

export function providerStatusLabel(status: Maybe<string>): string {
  if (status === null || status === undefined) return "-";
  return Object.hasOwn(PROVIDER_STATUS, status) ? (PROVIDER_STATUS[status] ?? status) : status;
}

export function classifyRun(run: Maybe<{ status: string }>): Tone {
  switch (run?.status) {
    case "succeeded":
      return "ok";
    case "failed":
      return "bad";
    case "retrying":
    case "rate_limited":
      return "warn";
    case "cancelled":
      return "off";
    default:
      return "ok";
  }
}

/** True for a provider users connect to or that notifies this server; false for a source an app uploads. */
export function isCloud(source: Maybe<{ kinds?: readonly string[] }>): boolean {
  const kinds = source?.kinds ?? [];
  return kinds.includes("oauth") || kinds.includes("push");
}
