import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type {
  ConnectSession,
  ConsentHistory,
  User,
  UserConnections,
  UserConsents,
  UserCoverage,
  UserFreshness,
  UserShares,
  UserSyncRuns,
} from "../../api/types";
import { apiPath, withQuery } from "../../api/paths";
import { useApi } from "../../session/SessionProvider";

export const LIST_SIZE = 50;
export const PREVIEW_SIZE = 10;

/** The path of one of the user's resources: /v1/users/{id}/{...rest}. */
export function userPath(id: string, ...rest: string[]): string {
  return apiPath(["v1", "users", id, ...rest]);
}

/** The key of every query of one user, so a write refreshes them all. */
const userKey = (id: string) => ["user", id] as const;

export function useUserQuery(id: string) {
  const api = useApi();
  return useQuery({
    queryKey: [...userKey(id), "self"],
    queryFn: () => api.get<User>(userPath(id)),
    retry: false,
  });
}

export function useCoverageQuery(id: string) {
  const api = useApi();
  return useQuery({
    queryKey: [...userKey(id), "coverage"],
    queryFn: () => api.get<UserCoverage>(userPath(id, "coverage")),
  });
}

export function useConnectionsQuery(id: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: [...userKey(id), "connections"],
    queryFn: () => api.get<UserConnections>(userPath(id, "connections")),
    enabled,
  });
}

export function useFreshnessQuery(id: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: [...userKey(id), "freshness"],
    queryFn: () => api.get<UserFreshness>(userPath(id, "freshness")),
    enabled,
  });
}

export function useConsentsQuery(id: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: [...userKey(id), "consents"],
    queryFn: () => api.get<UserConsents>(userPath(id, "consents")),
    enabled,
  });
}

export function useConsentHistoryQuery(id: string, cursor: string | undefined, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: [...userKey(id), "consent-history", cursor],
    queryFn: () => api.get<ConsentHistory>(userPath(id, "consents", "history"), { cursor, limit: LIST_SIZE }),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export function useSyncRunsQuery(id: string, cursor: string | undefined, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: [...userKey(id), "sync-runs", cursor],
    queryFn: () => api.get<UserSyncRuns>(userPath(id, "sync-runs"), { cursor, limit: LIST_SIZE }),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export function useSharesQuery(id: string, cursor: string | undefined, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: [...userKey(id), "shares", cursor],
    queryFn: () => api.get<UserShares>(userPath(id, "shares"), { cursor }),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export type PreviewDataset = "daily" | "sleep" | "workouts";

/** GET one dataset, first 10 rows. A mutation, so nothing is read until Preview is pressed and nothing is kept. */
export function usePreview(id: string) {
  const api = useApi();
  return useMutation({
    gcTime: 0,
    mutationFn: async (dataset: PreviewDataset) => {
      const answer = await api.get<{ data?: unknown[]; entry?: unknown[] }>(userPath(id, dataset), { limit: PREVIEW_SIZE });
      return { dataset, rows: (answer.data ?? answer.entry ?? []).slice(0, PREVIEW_SIZE) };
    },
  });
}

/** POST .../connect-sessions: the link is returned to the caller and not kept by the cache. */
export function useCreateConnectSession(id: string) {
  const api = useApi();
  return useMutation({
    gcTime: 0,
    mutationFn: () => api.send<ConnectSession>("POST", userPath(id, "connect-sessions"), {}),
  });
}

export function useSyncNow(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (provider: string) => api.send<unknown>("POST", userPath(id, "connections", provider, "sync")),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: userKey(id) });
    },
  });
}

export function useDisconnect(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ provider, deleteData }: { provider: string; deleteData: boolean }) =>
      api.send<unknown>("DELETE", withQuery(userPath(id, "connections", provider), deleteData ? { delete_data: true } : undefined)),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: userKey(id) });
    },
  });
}

export function useDeleteUser(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api.send<unknown>("DELETE", userPath(id)),
    onSuccess: async () => {
      queryClient.removeQueries({ queryKey: userKey(id) });
      await queryClient.invalidateQueries({ queryKey: ["users"] });
      await queryClient.invalidateQueries({ queryKey: ["pipeline"] });
    },
  });
}
