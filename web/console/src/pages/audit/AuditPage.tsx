import { useMemo, useState } from "react";

import { ApiError } from "../../api/client";
import type { AdminTenant, AuditEvent } from "../../api/types";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";
import { useSession } from "../../session/SessionProvider";
import { Card } from "../../ui/Card";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { Pager, usePager } from "../../ui/Pager";
import { useKeysQuery } from "../keys/api";
import { auditQuery, useAdminTenantsQuery, useAuditQuery, useRetentionDays, type AuditFilter } from "./api";
import { AuditFilters } from "./AuditFilters";
import { AuditTable } from "./AuditTable";
import { Skeleton } from "../../ui/Skeleton";

export function AuditPage() {
  const { session } = useSession();
  const pager = usePager();
  const [filter, setFilter] = useState<AuditFilter>({ action: "", keyId: "" });
  const [selected, setSelected] = useState<AuditEvent | null>(null);
  const audit = useAuditQuery(filter, pager.cursor);
  const keys = useKeysQuery();
  const tenants = useAdminTenantsQuery();
  const retention = useRetentionDays();
  const { data, error } = audit;

  const tenantNames = useMemo(() => {
    // PageBase types data as unknown[], so a map on the intersection sees unknown rows; reading it as AdminTenant[] is an assignment.
    const rows: readonly AdminTenant[] = tenants.data?.data ?? [];
    return new Map(rows.map((t) => [t.id, t.name]));
  }, [tenants.data]);

  const entry = useMemo<InspectorEntry | null>(() => {
    if (selected) {
      return {
        kind: "audit-event",
        title: selected.action,
        id: String(selected.id),
        request: { method: "GET", path: "/v1/audit" },
        response: selected,
      };
    }
    const base = {
      kind: "audit" as const,
      title: "Audit log",
      request: { method: "GET" as const, path: "/v1/audit", query: auditQuery(filter, pager.cursor) },
    };
    if (data !== undefined) return { ...base, response: data };
    return error instanceof ApiError ? { ...base, response: undefined, error } : null;
  }, [selected, data, error, filter, pager.cursor]);
  useInspect(entry);

  const change = (next: AuditFilter) => {
    pager.reset();
    setSelected(null);
    setFilter(next);
  };

  const kept = retention === undefined ? "" : ` Kept ${retention} days.`;

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">Audit log</h1>
          <p className="page-sub">Who read, exported, changed or deleted what, with which key.{kept}</p>
        </div>
        <div className="page-actions">
          <AuditFilters filter={filter} keys={keys.data?.data ?? []} onChange={change} />
        </div>
      </header>
      <Card title="Events">
        {audit.isPending ? <Skeleton count={8} /> : null}
        {error !== null && data === undefined ? <ErrorPanel title="The audit log did not load." error={error} retry /> : null}
        {data ? (
          <>
            <AuditTable
              rows={data.data}
              keys={keys.data?.data ?? []}
              actingTenantId={session?.acting_tenant.id ?? ""}
              tenantNames={tenantNames}
              selectedId={selected?.id}
              onSelect={setSelected}
              filtered={filter.action !== "" || filter.keyId !== ""}
            />
            <Pager pager={pager} nextCursor={data.next_cursor} loading={audit.isPlaceholderData} />
          </>
        ) : null}
      </Card>
    </div>
  );
}
