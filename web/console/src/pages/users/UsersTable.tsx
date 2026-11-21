import type { User } from "../../api/types";
import { classifyUser } from "../../lib/classify";
import { absTime, humanize, relTime } from "../../lib/format";
import { DataTable, type Column } from "../../ui/DataTable";
import { Mono } from "../../ui/Mono";
import { Pill } from "../../ui/Pill";

const columns: Column<User>[] = [
  {
    key: "user",
    header: "User",
    cell: (u) => (
      <div className="cell-stack">
        {u.external_ref === null ? <span className="muted">No external id</span> : <Mono>{u.external_ref}</Mono>}
        <span className="muted mono small">{u.id}</span>
      </div>
    ),
  },
  { key: "status", header: "Status", cell: (u) => <Pill tone={classifyUser(u)}>{humanize(u.status)}</Pill> },
  {
    key: "created",
    header: "Created",
    cell: (u) => <span title={absTime(u.created_at)}>{relTime(u.created_at)}</span>,
  },
];

export interface UsersTableProps {
  rows: readonly User[];
  selectedId: string | undefined;
  onSelect: (user: User) => void;
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
