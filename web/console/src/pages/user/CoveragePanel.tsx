import type { SourceFreshness, UserConnection, UserCoverage } from "../../api/types";
import { classifyConnection, classifyFreshness, type Tone } from "../../lib/classify";
import { absTime, formatCount, humanize, providerLabel, relTime } from "../../lib/format";
import { Card } from "../../ui/Card";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { Pill } from "../../ui/Pill";

interface ProviderCoverage {
  provider: string;
  days: number;
  records: number;
  first: string;
  last: string;
  synced: string;
}

/** Totals of the coverage report: per provider, and across all of them. */
export function summarizeCoverage(c: UserCoverage): { rows: ProviderCoverage[]; days: number; records: number } {
  const by = new Map<string, ProviderCoverage>();
  const dates = new Set<string>();
  let records = 0;
  for (const d of c.days) {
    const p = by.get(d.provider) ?? { provider: d.provider, days: 0, records: 0, first: d.date, last: d.date, synced: d.last_synced_at };
    p.days += 1;
    p.records += d.records;
    records += d.records;
    dates.add(d.date);
    if (d.date < p.first) p.first = d.date;
    if (d.date > p.last) p.last = d.date;
    if (d.last_synced_at > p.synced) p.synced = d.last_synced_at;
    by.set(d.provider, p);
  }
  return { rows: [...by.values()].sort((a, b) => b.records - a.records), days: dates.size, records };
}

/** Up to three figures the page already holds: counts of the coverage report, never a health value. */
export function MetricTiles({ coverage }: { coverage: UserCoverage }) {
  const sum = summarizeCoverage(coverage);
  const earliest = sum.rows.map((r) => r.first).sort()[0];
  const tiles: [string, string, string][] = [
    ["Days with data", formatCount(sum.days), earliest === undefined ? "None yet" : `Since ${earliest}`],
    ["Records", formatCount(sum.records), "Across every source"],
    ["Sources with data", formatCount(sum.rows.length), sum.rows.map((r) => providerLabel(null, r.provider)).join(", ") || "None yet"],
  ];
  return (
    <div className="metric-row">
      {tiles.map(([label, value, hint]) => (
        <section key={label} className="card metric" aria-label={label}>
          <span className="metric-label">{label}</span>
          <span className="metric-value">{value}</span>
          <span className="metric-hint">{hint}</span>
        </section>
      ))}
    </div>
  );
}

interface SourceRow {
  provider: string;
  records: number;
  /** The latest of the last sync and the last day synced. */
  last: string | null;
  tone: Tone;
  state: string;
}

/** One row per source the user has, connected or with data, with its last data and its state. */
export function sourceRows(
  coverage: UserCoverage,
  connections: readonly UserConnection[] | undefined,
  freshness: readonly SourceFreshness[] | undefined,
): SourceRow[] {
  const cov = new Map(summarizeCoverage(coverage).rows.map((r) => [r.provider, r]));
  const conn = new Map((connections ?? []).map((c) => [c.provider, c]));
  const fresh = new Map((freshness ?? []).map((f) => [f.provider, f]));
  const providers = [...new Set([...conn.keys(), ...cov.keys(), ...fresh.keys()])];
  const latest = (...stamps: (string | null | undefined)[]) =>
    stamps
      .filter((s): s is string => typeof s === "string" && s !== "")
      .sort()
      .at(-1) ?? null;
  return providers
    .map((provider) => {
      const c = conn.get(provider);
      const f = fresh.get(provider);
      const r = cov.get(provider);
      let tone: Tone = "off";
      let state = "No connection";
      if (f) {
        tone = classifyFreshness(f);
        state = humanize(f.state);
      } else if (c) {
        tone = classifyConnection(c);
        state = humanize(c.status);
      } else if (r) {
        state = "Uploaded";
        tone = "ok";
      }
      return { provider, records: r?.records ?? 0, last: latest(c?.last_sync_at, f?.last_sync_at, r?.synced), tone, state };
    })
    .sort((a, b) => b.records - a.records || a.provider.localeCompare(b.provider));
}

const columns: Column<SourceRow>[] = [
  { key: "provider", header: "Source", cell: (s) => <strong>{providerLabel(null, s.provider)}</strong> },
  {
    key: "last",
    header: "Last data",
    cell: (s) => (s.last ? <span title={absTime(s.last)}>{relTime(s.last)}</span> : <span className="muted">None yet</span>),
  },
  { key: "records", header: "Records", className: "num", cell: (s) => formatCount(s.records) },
  { key: "state", header: "State", cell: (s) => <Pill tone={s.tone}>{s.state}</Pill> },
];

/** The user's sources: last data and state, from the coverage report, the connections and their freshness. */
export function CoveragePanel({
  coverage,
  connections,
  freshness,
}: {
  coverage: UserCoverage;
  connections?: readonly UserConnection[] | undefined;
  freshness?: readonly SourceFreshness[] | undefined;
}) {
  const rows = sourceRows(coverage, connections, freshness);
  return (
    <Card title="Sources">
      <DataTable columns={columns} rows={rows} rowKey={(s) => s.provider} empty={<EmptyState title="No data has landed yet" />} />
      {coverage.pending_jobs > 0 ? <p className="card-hint">{`${formatCount(coverage.pending_jobs)} jobs of this user are still waiting.`}</p> : null}
      {coverage.parked_jobs > 0 ? <p className="card-hint err">{`${formatCount(coverage.parked_jobs)} jobs of this user are parked.`}</p> : null}
      {coverage.rejected_records > 0 ? (
        <p className="card-hint">{`${formatCount(coverage.rejected_records)} records were rejected by processing.`}</p>
      ) : null}
      {coverage.truncated ? <p className="card-hint">More than 10,000 days: the totals cover the earliest ones.</p> : null}
    </Card>
  );
}
