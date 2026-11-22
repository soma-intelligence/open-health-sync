import type { Share } from "../../api/types";
import { absTime, formatBytes, formatCount, humanize, relTime } from "../../lib/format";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { Pager, usePager } from "../../ui/Pager";
import { Pill } from "../../ui/Pill";
import { useSharesQuery, userPath } from "./api";
import { useTabInspect } from "./inspect";
import { Skeleton } from "../../ui/Skeleton";

const columns: Column<Share>[] = [
  { key: "label", header: "Share", cell: (s) => <div className="cell-stack"><span>{s.label}</span><span className="muted small">{s.categories.map(humanize).join(", ")}</span></div> },
  { key: "status", header: "Status", cell: (s) => <Pill tone={s.status === "active" ? "ok" : s.status === "disabled" ? "bad" : "off"}>{humanize(s.status)}</Pill> },
  { key: "range", header: "Covers", cell: (s) => <span className="mono nowrap">{`${s.start} to ${s.end}`}</span> },
  { key: "passcode", header: "Passcode", cell: (s) => (s.passcode ? "Yes" : "No") },
  { key: "opens", header: "Opens", className: "num", cell: (s) => formatCount(s.accesses) },
  { key: "size", header: "Size", className: "num", cell: (s) => formatBytes(s.file_bytes) },
  { key: "expires", header: "Expires", cell: (s) => <span title={absTime(s.expires_at)}>{relTime(s.expires_at)}</span> },
];

/** The share links the user made. Read only: a link is the user's to make and to revoke. */
export function SharesTab({ userId }: { userId: string }) {
  const pager = usePager();
  const shares = useSharesQuery(userId, pager.cursor, true);
  useTabInspect("Shares", userId, userPath(userId, "shares"), { cursor: pager.cursor }, shares.data, shares.error);
  return (
    <>
      {shares.isPending ? <Skeleton count={3} /> : null}
      {shares.error !== null && shares.data === undefined ? <ErrorPanel error={shares.error} retry /> : null}
      {shares.data ? (
        <>
          <DataTable columns={columns} rows={shares.data.data} rowKey={(s) => s.id} empty={<EmptyState title="No shares yet">Links the user makes to share a slice of their data appear here.</EmptyState>} />
          <Pager pager={pager} nextCursor={shares.data.next_cursor} loading={shares.isPlaceholderData} />
        </>
      ) : null}
    </>
  );
}
