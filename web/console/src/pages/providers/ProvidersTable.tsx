import type { AdminProvider, AdminTenantProvider } from "../../api/types";
import { classifyProvider, providerStatusLabel } from "../../lib/classify";
import { DataTable, type Column } from "../../ui/DataTable";
import { Pill } from "../../ui/Pill";

/** Where a provider's keys come from, in the words of the table. */
export function keysFrom(p: Pick<AdminProvider, "status" | "source">): string {
  if (p.status === "built_in") return "Built-in";
  if (p.source === "env") return "Environment";
  if (p.source === "console") return "Console";
  return "-";
}

function tenantCell(t: AdminTenantProvider | undefined) {
  if (t === undefined) return <span className="muted">-</span>;
  if (!t.enabled) return <Pill tone="off">Off</Pill>;
  return t.available ? <Pill tone="ok">On</Pill> : <Pill tone="warn">Unavailable</Pill>;
}

export interface ProvidersTableProps {
  rows: readonly AdminProvider[];
  tenant: ReadonlyMap<string, AdminTenantProvider>;
  selected: string | undefined;
  onSelect: (p: AdminProvider) => void;
  empty: React.ReactNode;
}

export function ProvidersTable({ rows, tenant, selected, onSelect, empty }: ProvidersTableProps) {
  const columns: Column<AdminProvider>[] = [
    {
      key: "provider",
      header: "Provider",
      cell: (p) => (
        <div className="cell-stack">
          <strong>{p.name}</strong>
          <span className="muted mono small">{p.slug}</span>
        </div>
      ),
    },
    { key: "status", header: "Status", cell: (p) => <Pill tone={classifyProvider(p)}>{providerStatusLabel(p.status)}</Pill> },
    { key: "keys", header: "Keys from", cell: (p) => keysFrom(p) },
    { key: "tenant", header: "This tenant", cell: (p) => tenantCell(tenant.get(p.slug)) },
  ];
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(p) => p.slug}
      selectedKey={selected}
      onSelect={onSelect}
      to={(p) => `/console/providers/${encodeURIComponent(p.slug)}`}
      empty={empty}
    />
  );
}
