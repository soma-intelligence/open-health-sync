import { formatAgo } from "./format";

export interface KeyField {
  name: string;
  secret: boolean;
  set: boolean;
  /** What the API served (non-secret keys). */
  value: string | null;
  /** What the operator typed. */
  input: string;
  /** A request to remove the key. */
  clear: boolean;
}

/**
 * The keys of a PATCH /v1/admin/providers body: only changes, null removes a
 * key, an empty input keeps one. Null when nothing changed.
 */
export function keysPatch(fields: readonly KeyField[]): Record<string, string | null> | null {
  const out: Record<string, string | null> = {};
  for (const f of fields) {
    const typed = f.input.trim();
    if (f.clear) {
      if (f.set) out[f.name] = null;
      continue;
    }
    if (typed === "") continue;
    if (!f.secret && f.set && typed === (f.value ?? "")) continue;
    out[f.name] = typed;
  }
  return Object.keys(out).length > 0 ? out : null;
}

export interface SyncPolicy {
  pull_every_seconds: number;
  pull_every_without_push_seconds: number;
  lookback_seconds: number;
  max_catch_up_seconds: number;
  backfill_seconds: number;
  backfill_chunk_seconds: number;
  reconcile_seconds: number;
}

/**
 * A provider's sync policy as the rows of the provider page, in plain words.
 * null (a provider the server never pulls) gives no row. Without push, only
 * pull_every_without_push_seconds ever applies.
 */
export function syncSchedule(sync: SyncPolicy | null | undefined, push = false): [string, string][] {
  if (!sync) return [];
  const every = (s: number) => `Every ${formatAgo(s)}`;
  const rows: [string, string][] = [];
  if (!push) {
    rows.push(["Pulled", every(sync.pull_every_without_push_seconds)]);
  } else if (sync.pull_every_seconds === sync.pull_every_without_push_seconds) {
    rows.push(["Pulled", every(sync.pull_every_seconds)]);
  } else {
    rows.push(["Pulled while notifications arrive", every(sync.pull_every_seconds)]);
    rows.push(["Pulled without notifications", every(sync.pull_every_without_push_seconds)]);
  }
  rows.push(["Each pull reads again", `The last ${formatAgo(sync.lookback_seconds)}, for late or revised data`]);
  rows.push(["After an outage", `Catches up to ${formatAgo(sync.max_catch_up_seconds)}`]);
  rows.push([
    "New connections",
    `Backfilled over ${formatAgo(sync.backfill_seconds)}, ${formatAgo(sync.backfill_chunk_seconds)} per job`,
  ]);
  rows.push([
    "Daily reconciliation",
    sync.reconcile_seconds > 0 ? `The first pull after 00:00 UTC reads the last ${formatAgo(sync.reconcile_seconds)}` : "Off",
  ]);
  return rows;
}
