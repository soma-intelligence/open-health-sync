// The overview's model: GET /v1/pipeline as five stages, what needs the
// operator, and the getting started checklist. Links are absolute console
// paths; a component strips the basename for the router.
import type { APIKey, Pipeline, Webhook } from "../api/types";
import type { Tone } from "./classify";
import { isCloud } from "./classify";
import { formatBytes, formatCount, formatTime, relTime } from "./format";

/**
 * The words of a stage's state chip: green ok, orange warning, red blocked, grey idle. A stage with
 * nothing going through it is idle, never "ok": an empty tenant does not read as a working one.
 */
export const STAGE_STATES: Record<Tone, string> = { ok: "OK", warn: "Warning", bad: "Blocked", off: "Idle" };

export interface StageFigure {
  label: string;
  value: string;
  tone?: Tone;
  /** The page that shows it. */
  to?: string;
}

export interface Stage {
  key: "sources" | "sync" | "processing" | "storage" | "delivery";
  title: string;
  role: string;
  tone: Tone;
  /** The words on the state chip: STAGE_STATES of the tone, or a more precise word for an idle stage. */
  state: string;
  /** The first figure is the headline. */
  figures: StageFigure[];
}

export interface AttentionItem {
  tone: "bad" | "warn";
  /** The words of the state chip: what kind of problem it is. */
  chip: string;
  text: string;
  /** A second line: since when, how many. */
  detail?: string;
  /** The page that fixes it, when there is one. */
  to?: string;
  /** The words of the one button that goes there. */
  action?: string;
}

export interface ChecklistItem {
  id: "provider" | "user" | "connection" | "data" | "webhook";
  title: string;
  done: boolean;
  to: string;
}

const PROVIDERS = "/console/providers";
const USERS = "/console/users";
const JOBS = "/console/jobs";
const WEBHOOKS = "/console/webhooks";
const KEYS = "/console/keys";

const worst = (...tones: Tone[]): Tone => (tones.includes("bad") ? "bad" : tones.includes("warn") ? "warn" : "ok");
const when = (cond: boolean, tone: Tone): Tone => (cond ? tone : "ok");
const conn = (s: Pipeline["sources"][number], status: string): number => s.connections[status] ?? 0;

type StageDraft = Omit<Stage, "state"> & { idle?: string };

/** The chip words: an idle stage may name why (no source yet, not set up). */
const withState = ({ idle, ...stage }: StageDraft): Stage => ({
  ...stage,
  state: stage.tone === "off" && idle !== undefined ? idle : STAGE_STATES[stage.tone],
});

/** The five stages of the overview, in the order data flows. */
export function pipelineStages(p: Pipeline): Stage[] {
  const sources = p.sources;
  const enabled = sources.filter((s) => s.enabled && isCloud(s));
  const sum = (f: (s: Pipeline["sources"][number]) => number) => sources.reduce((n, s) => n + f(s), 0);
  const active = sum((s) => conn(s, "active"));
  const reauth = sum((s) => conn(s, "needs_reauth"));
  const broken = sum((s) => s.broken);
  const paused = sources.filter((s) => s.paused_until).length;
  const { sync, processing: proc, storage: store, delivery: del, users, data } = p;
  const nbsp = " ";
  const day = `last 24${nbsp}h`;
  const inFlight = sync.queued + sync.running + sync.retrying + sync.rate_limited;
  const drafts: StageDraft[] = [
    {
      key: "sources",
      title: "Sources",
      role: "Accounts users connect, app uploads and imports",
      // No provider on, nothing connected and no record is a tenant still being set up: idle, not a fault.
      tone:
        active + reauth === 0 && enabled.length === 0 && data.sources === 0
          ? "off"
          : worst(when(reauth > 0, "bad"), when(broken > 0, "warn")),
      idle: "No source yet",
      figures: [
        // The same count as the user page's "Sources with data", over every user.
        { label: "Sources with data", value: formatCount(data.sources) },
        { label: "Connections", value: formatCount(active) },
        { label: "Cloud providers on", value: formatCount(enabled.length), to: PROVIDERS },
        { label: "Users", value: formatCount(users.active), to: USERS },
        { label: "Need reconnecting", value: formatCount(reauth), ...(reauth > 0 ? { tone: "bad" as const } : {}) },
      ],
    },
    {
      key: "sync",
      title: "Sync",
      role: "Pulls from each provider",
      tone:
        inFlight + sync.succeeded_24h + sync.failed_24h === 0
          ? "off"
          : worst(when(sync.failed_24h > 0, "bad"), when(sync.retrying > 0 || paused > 0 || sync.rate_limited > 0, "warn")),
      figures: [
        { label: `Runs succeeded, ${day}`, value: formatCount(sync.succeeded_24h) },
        { label: "Queued", value: formatCount(inFlight - sync.running) },
        { label: "Running", value: formatCount(sync.running) },
        { label: `Failed, ${day}`, value: formatCount(sync.failed_24h), to: JOBS, ...(sync.failed_24h > 0 ? { tone: "bad" as const } : {}) },
      ],
    },
    {
      key: "processing",
      title: "Processing",
      role: "Raw files into records",
      tone:
        proc.pending + proc.running + proc.parked + proc.rejected_records === 0 && store.raw_objects_24h === 0
          ? "off"
          : worst(when(proc.parked > 0, "bad"), when(proc.rejected_records > 0, "warn")),
      figures: [
        { label: "Jobs waiting", value: formatCount(proc.pending) },
        { label: "Running", value: formatCount(proc.running) },
        { label: "Parked", value: formatCount(proc.parked), to: JOBS, ...(proc.parked > 0 ? { tone: "bad" as const } : {}) },
        { label: "Rejected records", value: formatCount(proc.rejected_records), ...(proc.rejected_records > 0 ? { tone: "warn" as const } : {}) },
      ],
    },
    {
      key: "storage",
      title: "Storage",
      role: "Records kept, and the raw archive for replay",
      tone: data.records === 0 && store.raw_objects_24h === 0 ? "off" : "ok",
      idle: "Empty",
      figures: [
        // A total, since the first record: the 24 hour figures below are activity.
        { label: "Records stored", value: formatCount(data.records) },
        { label: "Users with data", value: formatCount(data.users), to: USERS },
        { label: `Raw data, ${day}`, value: formatBytes(store.raw_bytes_24h) },
        { label: `Raw objects, ${day}`, value: formatCount(store.raw_objects_24h) },
      ],
    },
    {
      key: "delivery",
      title: "Delivery",
      role: "Webhooks to your systems",
      tone: del.endpoints === 0 ? "off" : worst(when(del.parked > 0 || del.endpoints_disabled > 0, "bad"), when(del.endpoints_failing > 0, "warn")),
      idle: "Not set up",
      figures: [
        { label: "Webhook endpoints", value: formatCount(del.endpoints), to: WEBHOOKS },
        { label: "Failing", value: formatCount(del.endpoints_failing), ...(del.endpoints_failing > 0 ? { tone: "warn" as const } : {}) },
        { label: "Deliveries pending", value: formatCount(del.pending) },
        { label: "Deliveries parked", value: formatCount(del.parked), ...(del.parked > 0 ? { tone: "bad" as const } : {}) },
      ],
    },
  ];
  return drafts.map(withState);
}

/** What needs the operator, worst first, each with the page that fixes it. */
export function attention(p: Pipeline): AttentionItem[] {
  const out: AttentionItem[] = [];
  const { sources, sync, processing: proc, delivery: del } = p;
  const plural = (n: number, one: string, many: string) => `${formatCount(n)} ${n === 1 ? one : many}`;
  if (!sources.some((s) => s.enabled && isCloud(s))) {
    out.push({
      tone: "warn",
      chip: "Setup",
      text: "No cloud provider is on yet: turn one on so users can connect their accounts. App uploads work already.",
      to: PROVIDERS,
      action: "Open providers",
    });
  }
  for (const s of sources) {
    const reauth = conn(s, "needs_reauth");
    if (reauth > 0) {
      out.push({
        tone: "bad",
        chip: "Expired",
        text: `${s.name} token expired for ${plural(reauth, "user", "users")}.`,
        detail: `${plural(reauth, "user needs", "users need")} to reconnect ${s.name}.`,
        to: USERS,
        action: "Find users",
      });
    }
    const kept = conn(s, "active");
    if (!s.enabled && kept > 0) {
      out.push({
        tone: "warn",
        chip: "Off",
        text: `${s.name} is off but ${plural(kept, "connection is", "connections are")} kept: they sync again when it is on.`,
        to: PROVIDERS,
        action: "Open providers",
      });
    }
    if (s.paused_until) {
      out.push({ tone: "warn", chip: "Paused", text: `${s.name} asked to slow down: its pulls are paused until ${formatTime(s.paused_until)}.` });
    }
  }
  if (sync.failed_24h > 0) {
    out.push({
      tone: "bad",
      chip: "Failed",
      text: `${plural(sync.failed_24h, "sync run", "sync runs")} failed in the last 24 hours.`,
      to: JOBS,
      action: "Open jobs",
    });
  }
  if (proc.parked > 0) {
    out.push({
      tone: "bad",
      chip: "Parked",
      text: `${plural(proc.parked, "processing job is", "processing jobs are")} parked after 5 failed attempts.`,
      to: JOBS,
      action: "Open jobs",
    });
  }
  if (del.endpoints_disabled > 0) {
    out.push({
      tone: "bad",
      chip: "Disabled",
      text: `${plural(del.endpoints_disabled, "webhook endpoint is", "webhook endpoints are")} disabled.`,
      to: WEBHOOKS,
      action: "Open webhooks",
    });
  }
  if (del.parked > 0) {
    out.push({
      tone: "bad",
      chip: "Parked",
      text: `${plural(del.parked, "webhook delivery is", "webhook deliveries are")} parked.`,
      to: WEBHOOKS,
      action: "Open webhooks",
    });
  }
  if (del.endpoints_failing > 0) {
    out.push({
      tone: "warn",
      chip: "Failing",
      text: `${plural(del.endpoints_failing, "webhook endpoint is", "webhook endpoints are")} failing.`,
      to: WEBHOOKS,
      action: "Open webhooks",
    });
  }
  if (proc.rejected_records > 0) {
    out.push({
      tone: "warn",
      chip: "Rejected",
      text: `${plural(proc.rejected_records, "record was", "records were")} rejected by processing (see each user's coverage).`,
    });
  }
  // Array.sort is stable: the order above holds within a tone.
  const rank = { bad: 0, warn: 1 };
  return out.sort((a, b) => rank[a.tone] - rank[b.tone]);
}

/** A key expiring within this many days is listed under Needs you. */
export const KEY_EXPIRY_DAYS = 14;

/**
 * Needs you: the pipeline's problems, with each failing or disabled webhook endpoint and each key
 * that expires within 14 days named on its own line. Lists that did not load are left out: their
 * pipeline totals stand in for the endpoints, and keys are skipped.
 */
export function needsYou(p: Pipeline, webhooks?: readonly Webhook[], keys?: readonly APIKey[], now: number = Date.now()): AttentionItem[] {
  let items = attention(p);
  if (webhooks !== undefined) {
    // Named endpoints replace the totals of disabled and failing ones; parked deliveries stay.
    items = items.filter((i) => !(i.to === WEBHOOKS && (i.chip === "Disabled" || i.chip === "Failing")));
    for (const w of webhooks) {
      const to = `${WEBHOOKS}/${encodeURIComponent(w.id)}`;
      if (w.status === "disabled") {
        items.push({
          tone: "bad",
          chip: "Disabled",
          text: `Webhook to ${hostOf(w.url)}`,
          detail: w.disabled_reason ?? "Turned off after failed deliveries.",
          to,
          action: "Open",
        });
      } else if (w.failing_since) {
        const tries = w.consecutive_failures;
        items.push({
          tone: "warn",
          chip: "Retrying",
          text: `Webhook to ${hostOf(w.url)}`,
          detail: `${formatCount(tries)} failed ${tries === 1 ? "attempt" : "attempts"} in a row, the first ${relTime(w.failing_since, now)}`,
          to,
          action: "Open",
        });
      }
    }
  }
  for (const k of keys ?? []) {
    if (k.revoked_at || k.expires_at === null) continue;
    const at = Date.parse(k.expires_at);
    if (at <= now || at - now > KEY_EXPIRY_DAYS * 86_400_000) continue;
    items.push({
      tone: "warn",
      chip: "Expires soon",
      text: `API key ${k.prefix}...`,
      detail: `Expires ${relTime(k.expires_at, now)}`,
      to: KEYS,
      action: "Open keys",
    });
  }
  const rank = { bad: 0, warn: 1 };
  return items.sort((a, b) => rank[a.tone] - rank[b.tone]);
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

/** The five steps from an empty install to data flowing out. */
export function gettingStarted(p: Pipeline): ChecklistItem[] {
  // An app upload or an import is a source as much as a connected account: records from it count.
  const connected = p.sources.some((s) => s.records > 0 || Object.values(s.connections).some((n) => n > 0));
  return [
    { id: "provider", title: "Turn on a cloud provider", done: p.sources.some((s) => s.enabled && isCloud(s)), to: PROVIDERS },
    { id: "user", title: "Add a user", done: p.users.active > 0, to: USERS },
    { id: "connection", title: "Connect an account or an app", done: connected, to: USERS },
    { id: "data", title: "Receive data", done: p.data.records > 0 || p.sync.succeeded_24h > 0 || p.storage.raw_objects_24h > 0, to: USERS },
    { id: "webhook", title: "Add a webhook endpoint", done: p.delivery.endpoints > 0, to: WEBHOOKS },
  ];
}
