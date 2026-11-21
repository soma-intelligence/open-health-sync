import type { TenantSyncRun } from "../../api/types";
import { classifyRun } from "../../lib/classify";
import { absTime, formatCount, humanize, providerLabel, relTime } from "../../lib/format";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { Pill } from "../../ui/Pill";

const columns: Column<TenantSyncRun>[] = [
  { key: "user", header: "User", cell: (r) => <span className="mono small">{r.user_id}</span> },
  { key: "provider", header: "Provider", cell: (r) => providerLabel(null, r.provider) },
  { key: "trigger", header: "Trigger", cell: (r) => humanize(r.trigger) },
  { key: "status", header: "Status", cell: (r) => <Pill tone={classifyRun(r)}>{humanize(r.status)}</Pill> },
  { key: "attempts", header: "Attempts", className: "num", cell: (r) => formatCount(r.attempts) },
  { key: "error", header: "Error", cell: (r) => (r.error ? <span className="err">{r.error}</span> : <span className="muted">-</span>) },
  {
    key: "ended",
    header: "Ended",
    cell: (r) => <span title={absTime(r.finished_at ?? r.created_at)}>{relTime(r.finished_at ?? r.created_at)}</span>,
  },
];

export function FailedRunsTable({ rows }: { rows: readonly TenantSyncRun[] }) {
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(r) => String(r.id)}
      to={(r) => `/console/users/${r.user_id}`}
      empty={<EmptyState title="No failed sync run">Every pull is going through.</EmptyState>}
    />
  );
}
