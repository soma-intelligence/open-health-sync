import type { APIKey, AuditEvent } from "../../api/types";
import { absTime, humanize, relTime } from "../../lib/format";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { Mono } from "../../ui/Mono";
import { Pill } from "../../ui/Pill";

function statusTone(status: number | null): "ok" | "warn" | "bad" | "off" {
  if (status === null) return "off";
  if (status >= 500) return "bad";
  if (status >= 400) return "warn";
  return "ok";
}

/**
 * The route as the table shows it. The server records the Go route pattern,
 * which already starts with the method ("GET /v1/keys"); the method is added
 * only to a pattern that lacks it, so it never shows twice.
 */
export function routeLabel(method: string | undefined, route: string): string {
  if (!method || route.startsWith(`${method} `)) return route;
  return `${method} ${route}`;
}

export interface AuditTableProps {
  rows: readonly AuditEvent[];
  keys: readonly APIKey[];
  /** The tenant the console acts as. */
  actingTenantId: string;
  /** Names of tenants by id, when the session may list them. */
  tenantNames: ReadonlyMap<string, string>;
  selectedId: number | undefined;
  onSelect: (event: AuditEvent) => void;
  filtered: boolean;
}

export function AuditTable({ rows, keys, actingTenantId, tenantNames, selectedId, onSelect, filtered }: AuditTableProps) {
  const prefixes = new Map(keys.map((k) => [k.id, k.prefix]));
  const columns: Column<AuditEvent>[] = [
    { key: "time", header: "Time", cell: (e) => <span title={absTime(e.at)}>{relTime(e.at)}</span> },
    { key: "action", header: "Action", cell: (e) => <Mono>{e.action}</Mono> },
    {
      key: "route",
      header: "Route",
      cell: (e) => (e.route ? <Mono>{routeLabel(e.method, e.route)}</Mono> : <span className="muted">-</span>),
    },
    {
      key: "actor",
      header: "Actor",
      cell: (e) => {
        const prefix = e.actor.type === "api_key" ? prefixes.get(e.actor.id) : undefined;
        const via = e.actor_tenant_id;
        return (
          <div className="cell-stack">
            <span>
              {humanize(e.actor.type)} <Mono>{prefix === undefined ? e.actor.id : `${prefix}...`}</Mono>
            </span>
            {via !== null && via !== actingTenantId ? (
              <span className="muted small">via console session of {tenantNames.get(via) ?? via}</span>
            ) : null}
          </div>
        );
      },
    },
    {
      key: "status",
      header: "Status",
      cell: (e) => (e.status === null ? <span className="muted">-</span> : <Pill tone={statusTone(e.status)}>{String(e.status)}</Pill>),
    },
  ];
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(e) => String(e.id)}
      selectedKey={selectedId === undefined ? undefined : String(selectedId)}
      onSelect={onSelect}
      empty={
        filtered ? (
          <EmptyState title="No event matches">Try another action or actor.</EmptyState>
        ) : (
          <EmptyState title="No event yet">Every authenticated request is recorded here.</EmptyState>
        )
      }
    />
  );
}
