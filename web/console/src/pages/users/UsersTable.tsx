import type { UserListItem } from "../../api/types";
import { classifyUser } from "../../lib/classify";
import { isDemoUser } from "../../lib/demo";
import { absTime, formatCount, humanize, relTime } from "../../lib/format";
import { DataTable, type Column } from "../../ui/DataTable";
import { Mono } from "../../ui/Mono";
import { Pill } from "../../ui/Pill";

const columns: Column<UserListItem>[] = [
  {
    key: "user",
    header: "User",
    cell: (u) => (
      <div className="cell-stack">
        <span className="named">
          {u.external_ref === null ? <span className="muted">No external id</span> : <Mono>{u.external_ref}</Mono>}
          {isDemoUser(u) ? <Pill tone="info">Demo</Pill> : null}
        </span>
        <span className="muted mono small">{u.id}</span>
      </div>
    ),
  },
  { key: "status", header: "Status", cell: (u) => <Pill tone={classifyUser(u)}>{humanize(u.status)}</Pill> },
  { key: "sources", header: "Sources", className: "num", cell: (u) => formatCount(u.sources) },
  {
    key: "last-data",
    header: "Last data",
    cell: (u) =>
      u.last_data_at ? <span title={absTime(u.last_data_at)}>{relTime(u.last_data_at)}</span> : <span className="muted">None yet</span>,
  },
  {
    key: "created",
    header: "Created",
    cell: (u) => <span title={absTime(u.created_at)}>{relTime(u.created_at)}</span>,
  },
];

export interface UsersTableProps {
  rows: readonly UserListItem[];
  selectedId: string | undefined;
  onSelect: (user: UserListItem) => void;
  empty: React.ReactNode;
}

export function UsersTable({ rows, selectedId, onSelect, empty }: UsersTableProps) {
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(u) => u.id}
      selectedKey={selectedId}
      onSelect={onSelect}
      to={(u) => `/console/users/${u.id}`}
      empty={empty}
    />
  );
}
