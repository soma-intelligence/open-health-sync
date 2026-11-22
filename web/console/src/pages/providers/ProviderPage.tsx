import { useMemo } from "react";
import { Link, useParams } from "react-router";

import { ApiError } from "../../api/client";
import type { AdminProvider } from "../../api/types";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";
import { classifyProvider, providerStatusLabel } from "../../lib/classify";
import { absTime, humanize, relTime } from "../../lib/format";
import { syncSchedule } from "../../lib/providers";
import { useIsAdmin } from "../../session/SessionProvider";
import { Card } from "../../ui/Card";
import { EmptyState } from "../../ui/EmptyState";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { Mono } from "../../ui/Mono";
import { Pill } from "../../ui/Pill";
import { adminProviderPath, useAdminProviderQuery, useProvidersQuery } from "./api";
import { ForgetProviderCard } from "./ForgetProviderCard";
import { ProviderKeysForm } from "./ProviderKeysForm";
import { ProviderSwitch } from "./ProviderSwitch";
import { keysFrom } from "./ProvidersTable";
import { RegisterUrls } from "./RegisterUrls";
import { TenantProviderSwitch } from "./TenantProviderSwitch";
import { Skeleton } from "../../ui/Skeleton";

function Crumbs() {
  return (
    <nav className="crumbs" aria-label="Breadcrumb">
      <Link to="/providers">Providers</Link>
    </nav>
  );
}

export function ProviderPage() {
  const { slug = "" } = useParams();
  // Another provider is a fresh page: no typed key carries over.
  return useIsAdmin() ? <AdminProviderView key={slug} slug={slug} /> : <TenantProvider key={slug} slug={slug} />;
}

function Facts({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="facts">
      {rows.map(([k, v]) => (
        <div key={k} className="fact">
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function AdminProviderView({ slug }: { slug: string }) {
  const query = useAdminProviderQuery(slug, true);
  const p = query.data;
  const error = query.error;

  const entry = useMemo<InspectorEntry | null>(() => {
    const base = { kind: "provider" as const, title: p?.name ?? slug, id: slug, request: { method: "GET" as const, path: adminProviderPath(slug) } };
    if (p !== undefined) return { ...base, response: p };
    return error instanceof ApiError ? { ...base, response: undefined, error } : null;
  }, [p, error, slug]);
  useInspect(entry);

  if (query.isPending) {
    return (
      <div className="page">
        <Skeleton kind="page" />
      </div>
    );
  }
  if (p === undefined) {
    if (error instanceof ApiError && error.status === 404) {
      return (
        <div className="page">
          <EmptyState title="Not found">
            No provider by this name in this build. Back to <Link to="/providers">Providers</Link>.
          </EmptyState>
        </div>
      );
    }
    return (
      <div className="page">
        <ErrorPanel error={error} retry />
      </div>
    );
  }
  return <AdminDetail provider={p} />;
}

/** Changes whenever the keys the server holds change, so the form starts again from them. */
function keysSignature(p: AdminProvider): string {
  return `${p.updated_at ?? "none"}|${p.keys.map((k) => `${k.name}:${String(k.set)}:${k.value ?? ""}`).join(",")}`;
}

function AdminDetail({ provider: p }: { provider: AdminProvider }) {
  const builtIn = p.status === "built_in";
  const schedule = syncSchedule(p.sync, p.kinds.includes("push"));
  return (
    <div className="page">
      <Crumbs />
      <header className="page-head">
        <div>
          <div className="user-title">
            <h1 className="page-title">{p.name}</h1>
            <Pill tone={classifyProvider(p)}>{providerStatusLabel(p.status)}</Pill>
          </div>
          <p className="page-sub">
            <Mono>{p.slug}</Mono>
          </p>
        </div>
      </header>
      {p.error ? <ErrorPanel error={new Error(`The provider refused these keys. ${p.error}`)} /> : null}
      <div className="stack">
        <Card title="Facts">
          <Facts
            rows={[
              ["Kind", p.kinds.map(humanize).join(", ")],
              ["Keys from", keysFrom(p)],
              [
                "Last change",
                p.updated_at ? (
                  <span title={absTime(p.updated_at)}>
                    {relTime(p.updated_at)}
                    {p.updated_by ? <> by key <Mono>{p.updated_by}</Mono></> : null}
                  </span>
                ) : (
                  "-"
                ),
              ],
            ]}
          />
        </Card>
        {schedule.length > 0 ? (
          <Card title="Sync schedule">
            <Facts rows={schedule} />
          </Card>
        ) : null}
        {builtIn ? (
          <Card title="Nothing to set up">
            <p>{p.name} needs no key and no app registration: the SDK or app uploads to this server, which is always ready to receive it.</p>
          </Card>
        ) : (
          <>
            <RegisterUrls provider={p} />
            <ProviderKeysForm key={keysSignature(p)} provider={p} />
            <ProviderSwitch provider={p} />
          </>
        )}
        <TenantProviderSwitch provider={p} />
        {builtIn ? null : <ForgetProviderCard provider={p} />}
      </div>
    </div>
  );
}

function TenantProvider({ slug }: { slug: string }) {
  const list = useProvidersQuery(true);
  const entry = useMemo<InspectorEntry | null>(() => {
    const base = { kind: "providers" as const, title: "Providers", request: { method: "GET" as const, path: "/v1/providers" } };
    if (list.data !== undefined) return { ...base, response: list.data };
    return list.error instanceof ApiError ? { ...base, response: undefined, error: list.error } : null;
  }, [list.data, list.error]);
  useInspect(entry);

  if (list.isPending) {
    return (
      <div className="page">
        <Skeleton kind="page" />
      </div>
    );
  }
  if (list.data === undefined) {
    return (
      <div className="page">
        <ErrorPanel error={list.error} retry />
      </div>
    );
  }
  const p = list.data.data.find((x) => x.slug === slug);
  if (p === undefined) {
    return (
      <div className="page">
        <EmptyState title="Not found">
          This provider is not on for this tenant. Back to <Link to="/providers">Providers</Link>.
        </EmptyState>
      </div>
    );
  }
  return (
    <div className="page">
      <Crumbs />
      <header className="page-head">
        <div>
          <h1 className="page-title">{p.name ?? p.slug}</h1>
          <p className="page-sub">Only the instance admin tenant can change a provider.</p>
        </div>
      </header>
      <Card title="Facts">
        <Facts rows={[["Kind", p.kinds.map(humanize).join(", ")]]} />
      </Card>
    </div>
  );
}
