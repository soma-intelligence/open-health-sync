import { keepPreviousData, useQuery } from "@tanstack/react-query";

import type { AdminTenantList, AuditList } from "../../api/types";
import { useApi, useIsAdmin } from "../../session/SessionProvider";
import { useSettingsQuery } from "../settings/api";

export const PAGE_SIZE = 50;

export interface AuditFilter {
  action: string;
  /** The id of an API key, or "" for any actor. */
  keyId: string;
}

/** The query string of GET /v1/audit for a filter and a cursor. */
export function auditQuery(filter: AuditFilter, cursor: string | undefined): Record<string, string> {
  const query: Record<string, string> = { limit: String(PAGE_SIZE) };
  if (filter.action !== "") query["action"] = filter.action;
  if (filter.keyId !== "") {
    query["actor_type"] = "api_key";
    query["actor_id"] = filter.keyId;
  }
  if (cursor !== undefined) query["cursor"] = cursor;
  return query;
}

/** GET /v1/audit, 50 a page, newest first. */
export function useAuditQuery(filter: AuditFilter, cursor: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: ["audit", { action: filter.action, keyId: filter.keyId, cursor }],
    queryFn: () =>
      api.get<AuditList>("/v1/audit", {
        action: filter.action || undefined,
        actor_type: filter.keyId ? "api_key" : undefined,
        actor_id: filter.keyId || undefined,
        cursor,
        limit: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  });
}

/** GET /v1/admin/tenants, for an instance admin session only. */
export function useAdminTenantsQuery() {
  const api = useApi();
  const admin = useIsAdmin();
  return useQuery({
    queryKey: ["admin-tenants"],
    queryFn: () => api.get<AdminTenantList>("/v1/admin/tenants"),
    enabled: admin,
  });
}

/** The days the audit log is kept, from the settings (admin only), else undefined. */
export function useRetentionDays(): string | undefined {
  const settings = useSettingsQuery({ passive: true }).data;
  const row = settings?.data.find((s) => s.name === "OHS_AUDIT_RETENTION_DAYS");
  if (row === undefined) return undefined;
  const value = row.value ?? row.default;
  return value === "" ? undefined : value;
}
