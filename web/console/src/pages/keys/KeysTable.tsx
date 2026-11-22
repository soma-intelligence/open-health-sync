import { useState } from "react";

import type { APIKey } from "../../api/types";
import { absTime, relTime } from "../../lib/format";
import { KEY_EXPIRY_DAYS } from "../../lib/pipeline";
import { Button } from "../../ui/Button";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { Mono } from "../../ui/Mono";
import { Pill } from "../../ui/Pill";

export function scopesLabel(scopes: readonly string[]): string {
  return scopes.includes("*") ? "Everything" : scopes.join(", ");
}

/** The access of a key in a word: everything, read only, or the scopes picked. */
export function accessLabel(scopes: readonly string[]): string {
  if (scopes.includes("*")) return "Everything";
  if (scopes.length > 0 && scopes.every((s) => s.endsWith(":read"))) return "Read only";
  return scopes.length === 1 ? "1 scope" : `${String(scopes.length)} scopes`;
}

function stamp(iso: string | null, none: string) {
  return iso ? <span title={absTime(iso)}>{relTime(iso)}</span> : <span className="muted">{none}</span>;
}

/** The expiry, with an orange chip when it falls within the next 14 days. */
function expiry(k: APIKey, now: number) {
  if (!k.expires_at || k.revoked_at) return stamp(k.expires_at, "Never");
  const left = Date.parse(k.expires_at) - now;
  if (left > 0 && left <= KEY_EXPIRY_DAYS * 86_400_000) {
    return (
      <span title={absTime(k.expires_at)}>
        <Pill tone="warn">{relTime(k.expires_at, now)}</Pill>
      </span>
    );
  }
  return stamp(k.expires_at, "Never");
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
  // The page time, read once: a render must not read the clock.
  const [now] = useState(() => Date.now());
  const columns: Column<APIKey>[] = [
    {
      key: "name",
      header: "Name",
      cell: (k) => (
        <span className="named">
          {k.name ? <span className="key-name">{k.name}</span> : <span className="muted">No name</span>}
          {k.prefix === sessionPrefix ? <Pill tone="ok">This session</Pill> : null}
        </span>
      ),
    },
    { key: "key", header: "Key", cell: (k) => <Mono>{k.prefix}...</Mono> },
    {
      key: "scopes",
      header: "Access",
      cell: (k) =>
        k.scopes.includes("*") ? (
          "Everything"
        ) : (
          <span className="cell-stack">
            <span>{accessLabel(k.scopes)}</span>
            <span className="mono small muted">{scopesLabel(k.scopes)}</span>
          </span>
        ),
    },
    { key: "used", header: "Last used", cell: (k) => stamp(k.last_used_at, "Never") },
    { key: "expires", header: "Expires", cell: (k) => expiry(k, now) },
    {
      key: "status",
      header: "Status",
      cell: (k) => {
        if (k.revoked_at) return <Pill tone="off">Revoked</Pill>;
        if (k.expires_at && Date.parse(k.expires_at) <= now) return <Pill tone="off">Expired</Pill>;
        return <Pill tone="ok">Active</Pill>;
      },
    },
  ];
  if (onRevoke) {
    columns.push({
      key: "actions",
      header: "",
      actions: true,
      cell: (k) =>
        k.revoked_at || k.prefix === sessionPrefix ? null : (
          <Button
            kind="danger"
            size="sm"
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
      empty={<EmptyState title="No key yet">Create one to call the API.</EmptyState>}
    />
  );
}
