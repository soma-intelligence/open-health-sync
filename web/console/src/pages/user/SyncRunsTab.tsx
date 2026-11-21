import type { SyncRun } from "../../api/types";
import { classifyRun } from "../../lib/classify";
import { absTime, formatCount, formatWindow, humanize, providerLabel, relTime } from "../../lib/format";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { Pager, usePager } from "../../ui/Pager";
import { Pill } from "../../ui/Pill";
import { LIST_SIZE, useSyncRunsQuery, userPath } from "./api";
import { useTabInspect } from "./inspect";

const columns: Column<SyncRun>[] = [
  { key: "provider", header: "Provider", cell: (r) => providerLabel(null, r.provider) },
  { key: "trigger", header: "Trigger", cell: (r) => humanize(r.trigger) },
  { key: "status", header: "Status", cell: (r) => <Pill tone={classifyRun(r)}>{humanize(r.status)}</Pill> },
  { key: "window", header: "Window", cell: (r) => <span className="nowrap">{formatWindow(r.window_start, r.window_end)}</span> },
  { key: "objects", header: "Objects", className: "num", cell: (r) => formatCount(r.objects_stored) },
  { key: "error", header: "Error", cell: (r) => (r.error ? <span className="err">{r.error}</span> : <span className="muted">-</span>) },
  { key: "when", header: "When", cell: (r) => <span title={absTime(r.created_at)}>{relTime(r.created_at)}</span> },
];

export function SyncRunsTab({ userId }: { userId: string }) {
  const pager = usePager();
  const runs = useSyncRunsQuery(userId, pager.cursor, true);
  useTabInspect("Sync runs", userId, userPath(userId, "sync-runs"), { limit: LIST_SIZE, cursor: pager.cursor }, runs.data, runs.error);
  return (
    <>
      {runs.isPending ? <p role="status">Loading</p> : null}
      {runs.error !== null && runs.data === undefined ? <ErrorPanel error={runs.error} retry /> : null}
      {runs.data ? (
        <>
          <DataTable columns={columns} rows={runs.data.data} rowKey={(r) => String(r.id)} empty={<EmptyState title="No sync run yet" />} />
          <Pager pager={pager} nextCursor={runs.data.next_cursor} loading={runs.isPlaceholderData} />
        </>
      ) : null}
    </>
  );
}
