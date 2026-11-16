import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiPath } from "../../api/paths";
import type { CreateKeyRequest, CreatedAPIKey, KeyList } from "../../api/types";
import { useApi } from "../../session/SessionProvider";

export const KEYS_PATH = "/v1/keys";
const KEYS_KEY = ["keys"] as const;

/** GET /v1/keys, every key of the tenant. */
export function useKeysQuery() {
  const api = useApi();
  return useQuery({ queryKey: KEYS_KEY, queryFn: () => api.get<KeyList>(KEYS_PATH) });
}

/** POST /v1/keys. The whole key is returned to the caller and not kept by the cache. */
export function useCreateKey() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    gcTime: 0,
    mutationFn: (body: CreateKeyRequest) => api.send<CreatedAPIKey>("POST", KEYS_PATH, body),
    onSettled: () => queryClient.invalidateQueries({ queryKey: KEYS_KEY }),
  });
}

/** DELETE /v1/keys/{id}. */
export function useRevokeKey(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api.send<undefined>("DELETE", apiPath(["v1", "keys", id])),
    onSettled: () => queryClient.invalidateQueries({ queryKey: KEYS_KEY }),
  });
}
