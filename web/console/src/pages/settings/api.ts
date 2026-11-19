import { useMutation, useQuery } from "@tanstack/react-query";

import { ApiError } from "../../api/client";
import type { ProcessList, SettingsList, SettingsPatch } from "../../api/types";
import { useApi, useIsAdmin } from "../../session/SessionProvider";
import { applyOutcome, type ApplyOutcome } from "./model";

export const SETTINGS_PATH = "/v1/admin/settings";
export const SETTINGS_EXPORT_PATH = "/v1/admin/settings/export";
export const PROCESSES_PATH = "/v1/admin/processes";

export const SETTINGS_KEY = ["settings"] as const;
// Under SETTINGS_KEY, so refreshing the settings refreshes the processes too.
export const PROCESSES_KEY = [...SETTINGS_KEY, "processes"] as const;

const PROCESSES_EVERY_MS = 30_000;
// The inspector mounts a second observer whenever its entry changes: without
// a short stale time each mount would read the settings again.
const SETTINGS_STALE_MS = 15_000;

/**
 * GET /v1/admin/settings. Runs only for an instance admin session.
 *
 * passive: a reader on the side (the inspector, the palette, the audit page)
 * that only shows the settings when they are there. Its mount never starts the
 * load again after a failure: the inspector mounts when the Settings page shows
 * that failure, and a refetch from it would put the page back to Loading and
 * round again for ever. Try again and the Settings page itself still reload.
 */
export function useSettingsQuery({ passive = false }: { passive?: boolean } = {}) {
  const api = useApi();
  const admin = useIsAdmin();
  return useQuery({
    queryKey: SETTINGS_KEY,
    queryFn: () => api.get<SettingsList>(SETTINGS_PATH),
    enabled: admin,
    staleTime: SETTINGS_STALE_MS,
    retryOnMount: !passive,
  });
}

/** GET /v1/admin/processes, every 30 s. Runs only for an instance admin session. */
export function useProcessesQuery() {
  const api = useApi();
  const admin = useIsAdmin();
  return useQuery({
    queryKey: PROCESSES_KEY,
    queryFn: () => api.get<ProcessList>(PROCESSES_PATH),
    enabled: admin,
    refetchInterval: PROCESSES_EVERY_MS,
  });
}

export interface ApplyVariables {
  /** The revision of the list the diff was built on. */
  revision: number;
  patch: SettingsPatch;
}

/**
 * PATCH /v1/admin/settings with If-Match. 412, 409 and 422 answer an outcome,
 * not an error. The body may hold a typed secret: the caller resets the
 * mutation once it has read the outcome, so no copy stays in the cache.
 */
export function useApplySettings() {
  const api = useApi();
  return useMutation({
    gcTime: 0,
    mutationFn: async ({ revision, patch }: ApplyVariables): Promise<ApplyOutcome> => {
      try {
        const res = await api.request<unknown>("PATCH", SETTINGS_PATH, {
          body: patch,
          headers: { "If-Match": `"${String(revision)}"` },
        });
        return applyOutcome(res);
      } catch (e) {
        if (e instanceof ApiError) return applyOutcome(e);
        throw e;
      }
    },
  });
}
