import { useMemo, useState } from "react";

import { ApiError } from "../../api/client";
import type { AdminProvider, AdminTenantProvider, ProviderListItem } from "../../api/types";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";
import { isCloud } from "../../lib/classify";
import { useIsAdmin, useSession } from "../../session/SessionProvider";
import { Card } from "../../ui/Card";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { Pill } from "../../ui/Pill";
import { adminProvidersPath, useAdminProvidersQuery, useProvidersQuery, useTenantProvidersQuery } from "./api";
import { ProvidersTable } from "./ProvidersTable";
import { Skeleton } from "../../ui/Skeleton";

const SUBTITLE = "Keys saved here are sealed with the instance encryption key.";

export function ProvidersPage() {
  return useIsAdmin() ? <AdminProviders /> : <TenantProviders />;
}

function AdminProviders() {
  const { session } = useSession();
  const tenantId = session?.acting_tenant.id ?? "";
  const providers = useAdminProvidersQuery(true);
  const tenantList = useTenantProvidersQuery(tenantId, tenantId !== "");
  const [selected, setSelected] = useState<AdminProvider | null>(null);
  const { data, error } = providers;

  const entry = useMemo<InspectorEntry | null>(() => {
    if (selected) {
      return {
        kind: "provider",
        title: selected.name,
        id: selected.slug,
        request: { method: "GET", path: `/v1/admin/providers/${selected.slug}` },
        response: selected,
      };
    }
    const base = { kind: "providers" as const, title: "Providers", request: { method: "GET" as const, path: adminProvidersPath } };
    if (data !== undefined) return { ...base, response: data };
    return error instanceof ApiError ? { ...base, response: undefined, error } : null;
  }, [selected, data, error]);
  useInspect(entry);

  const tenant = useMemo(() => {
    const m = new Map<string, AdminTenantProvider>();
    for (const t of tenantList.data?.data ?? []) m.set(t.slug, t);
    return m;
  }, [tenantList.data]);

  const all = data?.data ?? [];
  const cloud = all.filter(isCloud);
  const uploads = all.filter((p) => !isCloud(p));
  const on = cloud.filter((p) => p.status === "active").length;

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">Providers</h1>
          <p className="page-sub">
            {data ? `${String(on)} of ${String(cloud.length)} on. ` : ""}
            {SUBTITLE}
          </p>
        </div>
      </header>
      {providers.isPending ? <Skeleton count={6} /> : null}
      {error !== null && data === undefined ? <ErrorPanel error={error} retry /> : null}
      {tenantList.isError ? <ErrorPanel error={tenantList.error} retry /> : null}
      {data && !data.public_url ? (
        <p className="notice warn">
          <span className="mono">OHS_PUBLIC_URL</span> is not set: the redirect and notification addresses name localhost, which no provider can reach.
        </p>
      ) : null}
      {data ? (
        <div className="stack">
          <Card title="Cloud providers">
            <ProvidersTable
              rows={cloud}
              tenant={tenant}
              selected={selected?.slug}
              onSelect={setSelected}
              empty={<EmptyState title="No cloud provider in this build" />}
            />
          </Card>
          <Card title="App uploads">
            <ProvidersTable
              rows={uploads}
              tenant={tenant}
              selected={selected?.slug}
              onSelect={setSelected}
              empty={<EmptyState title="No app upload source in this build" />}
            />
          </Card>
        </div>
      ) : null}
    </div>
  );
}

const readOnlyColumns: Column<ProviderListItem>[] = [
  { key: "provider", header: "Provider", cell: (p) => <strong>{p.name ?? p.slug}</strong> },
  { key: "kinds", header: "Kind", cell: (p) => p.kinds.map((k) => <Pill key={k} tone="off">{k}</Pill>) },
];

function TenantProviders() {
  const providers = useProvidersQuery(true);
  const { data, error } = providers;
  const entry = useMemo<InspectorEntry | null>(() => {
    const base = { kind: "providers" as const, title: "Providers", request: { method: "GET" as const, path: "/v1/providers" } };
    if (data !== undefined) return { ...base, response: data };
    return error instanceof ApiError ? { ...base, response: undefined, error } : null;
  }, [data, error]);
  useInspect(entry);

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">Providers</h1>
          <p className="page-sub">{data ? `${String(data.data.length)} on for this tenant. ` : ""}Only the instance admin tenant can change them.</p>
        </div>
      </header>
      {providers.isPending ? <Skeleton count={6} /> : null}
      {error !== null && data === undefined ? <ErrorPanel error={error} retry /> : null}
      {data ? (
        <Card title="On for this tenant">
          <DataTable
            columns={readOnlyColumns}
            rows={data.data}
            rowKey={(p) => p.slug}
            empty={<EmptyState title="No provider is on">Ask the instance admin to turn one on.</EmptyState>}
          />
        </Card>
      ) : null}
    </div>
  );
}
