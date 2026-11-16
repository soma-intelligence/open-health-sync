import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiPath } from "../../api/paths";
import type { EtlJob, EtlJobList, RetryParkedJobs, TenantSyncRunList } from "../../api/types";
import { useApi } from "../../session/SessionProvider";

export const FAILED_RUNS_QUERY = { status: "failed", limit: 50 } as const;
export const PARKED_JOBS_QUERY = { status: "parked", limit: 100 } as const;
export const FAILED_RUNS_PATH = "/v1/pipeline/sync-runs";
export const PARKED_JOBS_PATH = "/v1/pipeline/etl-jobs";

/** GET /v1/pipeline/sync-runs?status=failed&limit=50. */
export function useFailedRunsQuery() {
  const api = useApi();
  return useQuery({
    queryKey: ["jobs", "runs"],
    queryFn: () => api.get<TenantSyncRunList>(FAILED_RUNS_PATH, FAILED_RUNS_QUERY),
  });
}

/** GET /v1/pipeline/etl-jobs?status=parked&limit=100. */
export function useParkedJobsQuery() {
  const api = useApi();
  return useQuery({
    queryKey: ["jobs", "parked"],
    queryFn: () => api.get<EtlJobList>(PARKED_JOBS_PATH, PARKED_JOBS_QUERY),
  });
}

/** POST /v1/pipeline/etl-jobs/{id}/retry. */
export function useRetryJob() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.send<EtlJob>("POST", apiPath(["v1", "pipeline", "etl-jobs", String(id), "retry"])),
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

/** POST /v1/pipeline/etl-jobs/retry, every parked job of the tenant. */
export function useRetryAllJobs() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api.send<RetryParkedJobs>("POST", "/v1/pipeline/etl-jobs/retry", {}),
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}
