import type { EtlJob } from "../../api/types";
import { absTime, formatCount, providerLabel, relTime } from "../../lib/format";
import { Button } from "../../ui/Button";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";

export interface ParkedJobsTableProps {
  rows: readonly EtlJob[];
  /** Set when the session may write: shows a Retry button per row. */
  onRetry: ((job: EtlJob) => void) | undefined;
}

export function ParkedJobsTable({ rows, onRetry }: ParkedJobsTableProps) {
  const columns: Column<EtlJob>[] = [
    { key: "job", header: "Job", cell: (j) => <span className="mono">{`#${String(j.id)}`}</span> },
    { key: "user", header: "User", cell: (j) => <span className="mono small">{j.user_id}</span> },
    { key: "provider", header: "Provider", cell: (j) => providerLabel(null, j.provider) },
    { key: "attempts", header: "Attempts", className: "num", cell: (j) => formatCount(j.attempts) },
    { key: "error", header: "Error", cell: (j) => (j.error ? <span className="err">{j.error}</span> : <span className="muted">-</span>) },
    { key: "created", header: "Created", cell: (j) => <span title={absTime(j.created_at)}>{relTime(j.created_at)}</span> },
  ];
  if (onRetry) {
    columns.push({
      key: "actions",
      header: "Actions",
      cell: (j) => (
        <div className="row-actions">
          <Button
            size="sm"
            onClick={() => {
              onRetry(j);
            }}
          >
            Retry
          </Button>
        </div>
      ),
    });
  }
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(j) => String(j.id)}
      to={(j) => `/console/users/${j.user_id}`}
      empty={<EmptyState title="Nothing is stuck">No parked job. Processing is keeping up.</EmptyState>}
    />
  );
}
