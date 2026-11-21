import { useQuery } from "@tanstack/react-query";

import type { Pipeline } from "../../api/types";
import { useApi } from "../../session/SessionProvider";

export const PIPELINE_KEY = ["pipeline"] as const;
const LIVE_MS = 30_000;

/** GET /v1/pipeline for the acting tenant, again every 30 seconds. */
export function usePipelineQuery() {
  const api = useApi();
  return useQuery({
    queryKey: PIPELINE_KEY,
    queryFn: () => api.get<Pipeline>("/v1/pipeline"),
    refetchInterval: LIVE_MS,
  });
}

/** /healthz or /readyz: sent without the session, answers with the text of a 200. */
export function useProbe(path: "/healthz" | "/readyz") {
  const api = useApi();
  return useQuery({
    queryKey: ["probe", path],
    queryFn: async () => (await api.request<string>("GET", path, { anonymous: true, as: "text" })).data,
    refetchInterval: LIVE_MS,
    retry: false,
  });
}
