import { useMutation, useQuery } from "@tanstack/react-query";

import { ApiError } from "../../api/client";
import type { SetupResult } from "../../api/types";
import { useApi } from "../../session/SessionProvider";
import { SETUP_PATH, SetupError, buildSetupRequest, isApiKey, setupMessage, setupState } from "./setup";

/**
 * GET /v1/setup, anonymous: "needed", "done" or "off". The one reader of the
 * ["setup"] cache entry, so the sign in page and the setup page share its shape.
 */
export function useSetupState(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: ["setup"],
    enabled,
    retry: false,
    refetchOnWindowFocus: false,
    queryFn: async () => {
      try {
        const res = await api.request<unknown>("GET", SETUP_PATH, { anonymous: true });
        return setupState(res.status, res.data);
      } catch (e) {
        if (e instanceof ApiError) return setupState(e.status, null);
        throw e;
      }
    },
  });
}

/** POST /v1/setup, anonymous. The key is returned to the caller; the cache keeps nothing. */
export function useRunSetup() {
  const api = useApi();
  return useMutation({
    gcTime: 0,
    mutationFn: async (input: { token: string; name: string }): Promise<SetupResult> => {
      const body = buildSetupRequest(input.token, input.name);
      let out: SetupResult;
      try {
        out = (await api.request<SetupResult>("POST", SETUP_PATH, { body, anonymous: true })).data;
      } catch (e) {
        if (e instanceof ApiError) throw new SetupError(e.status, setupMessage(e.status, e.detail));
        throw e;
      }
      if (!isApiKey(out.api_key) || typeof out.tenant_id !== "string") {
        throw new SetupError(201, "The server answered with no API key. Create one with `ohs admin key create`.");
      }
      return out;
    },
  });
}
