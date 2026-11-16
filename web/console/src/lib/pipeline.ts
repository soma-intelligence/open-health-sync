// The overview's model: GET /v1/pipeline as five stages, what needs the
// operator, and the getting started checklist. Links are absolute console
// paths; a component strips the basename for the router.
import type { Pipeline } from "../api/types";
import type { Tone } from "./classify";
import { isCloud } from "./classify";
import { formatBytes, formatCount, formatTime } from "./format";

/** The state of a stage on the overview. */
export const STAGE_STATES: Record<Tone, string> = { ok: "Healthy", warn: "Check", bad: "Needs you", off: "Not set up" };

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
  /** The first figure is the headline. */
  figures: StageFigure[];
}

export interface AttentionItem {
  tone: "bad" | "warn";
  text: string;
  /** The page that fixes it, when there is one. */
  to?: string;
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

const worst = (...tones: Tone[]): Tone => (tones.includes("bad") ? "bad" : tones.includes("warn") ? "warn" : "ok");
const when = (cond: boolean, tone: Tone): Tone => (cond ? tone : "ok");
const conn = (s: Pipeline["sources"][number], status: string): number => s.connections[status] ?? 0;

/** The five stages of the overview, in the order data flows. */
export function pipelineStages(p: Pipeline): Stage[] {
  const sources = p.sources;
  const enabled = sources.filter((s) => s.enabled && isCloud(s));
  const sum = (f: (s: Pipeline["sources"][number]) => number) => sources.reduce((n, s) => n + f(s), 0);
  const active = sum((s) => conn(s, "active"));
  const reauth = sum((s) => conn(s, "needs_reauth"));
  const broken = sum((s) => s.broken);
  const paused = sources.filter((s) => s.paused_until).length;
  const { sync, processing: proc, storage: store, delivery: del, users } = p;
  const nbsp = " ";
  return [
    {
      key: "sources",
      title: "Sources",
      role: "Accounts users connect, and app uploads",
      tone: enabled.length === 0 ? "warn" : worst(when(reauth > 0, "bad"), when(broken > 0, "warn")),
      figures: [
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
      tone: worst(when(sync.failed_24h > 0, "bad"), when(sync.retrying > 0 || paused > 0 || sync.rate_limited > 0, "warn")),
      figures: [
        { label: `Runs succeeded, 24${nbsp}h`, value: formatCount(sync.succeeded_24h) },
        { label: "Queued", value: formatCount(sync.queued + sync.retrying + sync.rate_limited) },
        { label: "Running", value: formatCount(sync.running) },
        { label: `Failed, 24${nbsp}h`, value: formatCount(sync.failed_24h), to: JOBS, ...(sync.failed_24h > 0 ? { tone: "bad" as const } : {}) },
      ],
    },
    {
      key: "processing",
      title: "Processing",
      role: "Raw files into records",
      tone: worst(when(proc.parked > 0, "bad"), when(proc.rejected_records > 0, "warn")),
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
      role: "The raw archive, kept for replay",
      tone: "ok",
      figures: [
        { label: `Raw data, 24${nbsp}h`, value: formatBytes(store.raw_bytes_24h) },
        { label: `Raw objects, 24${nbsp}h`, value: formatCount(store.raw_objects_24h) },
      ],
    },
    {
      key: "delivery",
      title: "Delivery",
      role: "Webhooks to your systems",
      tone:
        del.endpoints === 0
          ? "off"
          : worst(when(del.parked > 0 || del.endpoints_disabled > 0, "bad"), when(del.endpoints_failing > 0, "warn")),
      figures: [
        { label: "Webhook endpoints", value: formatCount(del.endpoints), to: WEBHOOKS },
        { label: "Failing", value: formatCount(del.endpoints_failing), ...(del.endpoints_failing > 0 ? { tone: "warn" as const } : {}) },
        { label: "Deliveries pending", value: formatCount(del.pending) },
        { label: "Deliveries parked", value: formatCount(del.parked), ...(del.parked > 0 ? { tone: "bad" as const } : {}) },
      ],
    },
  ];
}

/** What needs the operator, worst first, each with the page that fixes it. */
export function attention(p: Pipeline): AttentionItem[] {
  const out: AttentionItem[] = [];
  const { sources, sync, processing: proc, delivery: del } = p;
  const plural = (n: number, one: string, many: string) => `${formatCount(n)} ${n === 1 ? one : many}`;
  if (!sources.some((s) => s.enabled && isCloud(s))) {
    out.push({
      tone: "warn",
      text: "No cloud provider is on yet: turn one on so users can connect their accounts. App uploads work already.",
      to: PROVIDERS,
    });
  }
  for (const s of sources) {
    const reauth = conn(s, "needs_reauth");
    if (reauth > 0) {
      out.push({ tone: "bad", text: `${plural(reauth, "user needs", "users need")} to reconnect ${s.name}.`, to: USERS });
    }
    const kept = conn(s, "active");
    if (!s.enabled && kept > 0) {
      out.push({
        tone: "warn",
        text: `${s.name} is off but ${plural(kept, "connection is", "connections are")} kept: they sync again when it is on.`,
        to: PROVIDERS,
      });
    }
    if (s.paused_until) {
      out.push({ tone: "warn", text: `${s.name} asked to slow down: its pulls are paused until ${formatTime(s.paused_until)}.` });
    }
  }
  if (sync.failed_24h > 0) {
    out.push({ tone: "bad", text: `${plural(sync.failed_24h, "sync run", "sync runs")} failed in the last 24 hours.`, to: JOBS });
  }
  if (proc.parked > 0) {
    out.push({ tone: "bad", text: `${plural(proc.parked, "processing job is", "processing jobs are")} parked after 5 failed attempts.`, to: JOBS });
  }
  if (del.endpoints_disabled > 0) {
    out.push({ tone: "bad", text: `${plural(del.endpoints_disabled, "webhook endpoint is", "webhook endpoints are")} disabled.`, to: WEBHOOKS });
  }
  if (del.parked > 0) {
    out.push({ tone: "bad", text: `${plural(del.parked, "webhook delivery is", "webhook deliveries are")} parked.`, to: WEBHOOKS });
  }
  if (del.endpoints_failing > 0) {
    out.push({ tone: "warn", text: `${plural(del.endpoints_failing, "webhook endpoint is", "webhook endpoints are")} failing.`, to: WEBHOOKS });
  }
  if (proc.rejected_records > 0) {
    out.push({ tone: "warn", text: `${plural(proc.rejected_records, "record was", "records were")} rejected by processing (see each user's coverage).` });
  }
  // Array.sort is stable: the order above holds within a tone.
  const rank = { bad: 0, warn: 1 };
  return out.sort((a, b) => rank[a.tone] - rank[b.tone]);
}

/** The five steps from an empty install to data flowing out. */
export function gettingStarted(p: Pipeline): ChecklistItem[] {
  const connected = p.sources.some((s) => Object.values(s.connections).some((n) => n > 0));
  return [
    { id: "provider", title: "Turn on a cloud provider", done: p.sources.some((s) => s.enabled && isCloud(s)), to: PROVIDERS },
    { id: "user", title: "Add a user", done: p.users.active > 0, to: USERS },
    { id: "connection", title: "Connect an account or an app", done: connected, to: USERS },
    { id: "data", title: "Receive data", done: p.sync.succeeded_24h > 0 || p.storage.raw_objects_24h > 0, to: USERS },
    { id: "webhook", title: "Add a webhook endpoint", done: p.delivery.endpoints > 0, to: WEBHOOKS },
  ];
}
