import type { APIKey } from "../../api/types";
import { absTime, relTime } from "../../lib/format";
import { Button } from "../../ui/Button";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { Mono } from "../../ui/Mono";
import { Pill } from "../../ui/Pill";

export function scopesLabel(scopes: readonly string[]): string {
  return scopes.includes("*") ? "Everything" : scopes.join(", ");
}

function stamp(iso: string | null, none: string) {
  return iso ? <span title={absTime(iso)}>{relTime(iso)}</span> : <span className="muted">{none}</span>;
}

export interface KeysTableProps {
  rows: readonly APIKey[];
  /** The prefix of the key this session signed in with. */
  sessionPrefix: string;
  selectedId: string | undefined;
  onSelect: (key: APIKey) => void;
  /** Absent when the session may not write: no action column. */
  onRevoke?: (key: APIKey) => void;
}

export function KeysTable({ rows, sessionPrefix, selectedId, onSelect, onRevoke }: KeysTableProps) {
  const columns: Column<APIKey>[] = [
    {
      key: "key",
      header: "Key",
      cell: (k) => (
        <span className="named">
          <Mono>{k.prefix}...</Mono>
          {k.prefix === sessionPrefix ? <Pill tone="ok">This session</Pill> : null}
        </span>
      ),
    },
    { key: "scopes", header: "Scopes", cell: (k) => (k.scopes.includes("*") ? "Everything" : <Mono>{scopesLabel(k.scopes)}</Mono>) },
    { key: "created", header: "Created", cell: (k) => stamp(k.created_at, "-") },
    { key: "used", header: "Last used", cell: (k) => stamp(k.last_used_at, "Never") },
    { key: "expires", header: "Expires", cell: (k) => stamp(k.expires_at, "Never") },
    {
      key: "status",
      header: "Status",
      cell: (k) => {
        if (k.revoked_at) return <Pill tone="off">Revoked</Pill>;
        if (k.expires_at && Date.parse(k.expires_at) <= Date.now()) return <Pill tone="off">Expired</Pill>;
        return <Pill tone="ok">Active</Pill>;
      },
    },
  ];
  if (onRevoke) {
    columns.push({
      key: "actions",
      header: "",
      className: "row-actions",
      cell: (k) =>
        k.revoked_at || k.prefix === sessionPrefix ? null : (
          <Button
            kind="danger"
            onClick={(e) => {
              e.stopPropagation();
              onRevoke(k);
            }}
          >
            Revoke
          </Button>
        ),
    });
  }
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(k) => k.id}
      selectedKey={selectedId}
      onSelect={onSelect}
      empty={<EmptyState title="No key">Create one to call the API.</EmptyState>}
    />
  );
}
