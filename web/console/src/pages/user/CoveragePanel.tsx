import type { UserCoverage } from "../../api/types";
import { providerLabel, formatCount, relTime, absTime } from "../../lib/format";
import { Card } from "../../ui/Card";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";

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

const columns: Column<ProviderCoverage>[] = [
  { key: "provider", header: "Provider", cell: (p) => providerLabel(null, p.provider) },
  { key: "days", header: "Days", className: "num", cell: (p) => formatCount(p.days) },
  { key: "records", header: "Records", className: "num", cell: (p) => formatCount(p.records) },
  { key: "first", header: "First day", cell: (p) => <span className="mono">{p.first}</span> },
  { key: "last", header: "Last day", cell: (p) => <span className="mono">{p.last}</span> },
  { key: "synced", header: "Last synced", cell: (p) => <span title={absTime(p.synced)}>{relTime(p.synced)}</span> },
];

function Stat({ label, value, tone }: { label: string; value: string; tone?: "warn" | "bad" }) {
  return (
    <div className={tone ? `stat ${tone}` : "stat"}>
      <span className="stat-value">{value}</span>
      <span className="stat-label">{label}</span>
    </div>
  );
}

/** Days with data, per provider. Counts only: no health value is read. */
export function CoveragePanel({ coverage }: { coverage: UserCoverage }) {
  const sum = summarizeCoverage(coverage);
  return (
    <Card title="Coverage">
      <div className="stats">
        <Stat label="Days with data" value={formatCount(sum.days)} />
        <Stat label="Records" value={formatCount(sum.records)} />
        <Stat label="Jobs waiting" value={formatCount(coverage.pending_jobs)} {...(coverage.pending_jobs > 0 ? { tone: "warn" as const } : {})} />
        <Stat label="Parked jobs" value={formatCount(coverage.parked_jobs)} {...(coverage.parked_jobs > 0 ? { tone: "bad" as const } : {})} />
      </div>
      <DataTable columns={columns} rows={sum.rows} rowKey={(p) => p.provider} empty={<EmptyState title="No data has landed yet" />} />
      {coverage.rejected_records > 0 ? <p className="muted">{formatCount(coverage.rejected_records)} records were rejected by processing.</p> : null}
      {coverage.truncated ? <p className="muted">More than 10,000 days: the totals cover the earliest ones.</p> : null}
    </Card>
  );
}
