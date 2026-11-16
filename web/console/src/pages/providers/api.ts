import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiPath } from "../../api/paths";
import type {
  AdminProvider,
  AdminProviderList,
  AdminProviderPatch,
  AdminTenantProvider,
  AdminTenantProviderList,
  AdminTenantProviderPatch,
  ProviderList,
} from "../../api/types";
import { useApi } from "../../session/SessionProvider";

export const adminProvidersPath = "/v1/admin/providers";

export const adminProviderPath = (slug: string): string => apiPath(["v1", "admin", "providers", slug]);

export const tenantProvidersPath = (tenantId: string): string => apiPath(["v1", "admin", "tenants", tenantId, "providers"]);

export const tenantProviderPath = (tenantId: string, slug: string): string =>
  apiPath(["v1", "admin", "tenants", tenantId, "providers", slug]);

/** GET /v1/admin/providers: every connector of this build, for the instance admin. */
export function useAdminProvidersQuery(enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: ["providers", "admin"],
    queryFn: () => api.get<AdminProviderList>(adminProvidersPath),
    enabled,
  });
}

/** GET /v1/providers: the connectors on for the caller's tenant. */
export function useProvidersQuery(enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: ["providers", "public"],
    queryFn: () => api.get<ProviderList>("/v1/providers"),
    enabled,
  });
}

/** GET /v1/admin/providers/{slug}. */
export function useAdminProviderQuery(slug: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: ["providers", "admin", slug],
    queryFn: () => api.get<AdminProvider>(adminProviderPath(slug)),
    enabled,
    retry: false,
  });
}

/** GET /v1/admin/tenants/{id}/providers: the per tenant switches. */
export function useTenantProvidersQuery(tenantId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: ["providers", "tenant", tenantId],
    queryFn: () => api.get<AdminTenantProviderList>(tenantProvidersPath(tenantId)),
    enabled,
  });
}

/** PATCH /v1/admin/providers/{slug}: keys and the instance switch. */
export function useUpdateProvider(slug: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (patch: AdminProviderPatch) => api.send<AdminProvider>("PATCH", adminProviderPath(slug), patch),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["providers"] });
    },
  });
}

/** DELETE /v1/admin/providers/{slug}: forget what the console saved. */
export function useForgetProvider(slug: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api.send<AdminProvider>("DELETE", adminProviderPath(slug)),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["providers"] });
    },
  });
}

/** PATCH /v1/admin/tenants/{id}/providers/{slug}: the switch of one tenant. */
export function useUpdateTenantProvider(tenantId: string, slug: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (patch: AdminTenantProviderPatch) =>
      api.send<AdminTenantProvider>("PATCH", tenantProviderPath(tenantId, slug), patch),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["providers"] });
    },
  });
}
