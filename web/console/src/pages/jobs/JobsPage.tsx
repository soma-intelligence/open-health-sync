import { useMemo, useState } from "react";

import { ApiError } from "../../api/client";
import type { EtlJob } from "../../api/types";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";
import { formatCount } from "../../lib/format";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { useToast } from "../../ui/Toast";
import {
  PARKED_JOBS_PATH,
  PARKED_JOBS_QUERY,
  useFailedRunsQuery,
  useParkedJobsQuery,
  useRetryAllJobs,
  useRetryJob,
} from "./api";
import { FailedRunsTable } from "./FailedRunsTable";
import { ParkedJobsTable } from "./ParkedJobsTable";

type Action = { kind: "one"; job: EtlJob } | { kind: "all" };

export function JobsPage() {
  const canWrite = useCanWrite();
  const toast = useToast();
  const runs = useFailedRunsQuery();
  const jobs = useParkedJobsQuery();
  const retryOne = useRetryJob();
  const retryAll = useRetryAllJobs();
  const [action, setAction] = useState<Action | null>(null);

  const entry = useMemo<InspectorEntry | null>(() => {
    const query = Object.fromEntries(Object.entries(PARKED_JOBS_QUERY).map(([k, v]) => [k, String(v)]));
    const base = { kind: "jobs" as const, title: "Jobs", request: { method: "GET" as const, path: PARKED_JOBS_PATH, query } };
    if (jobs.data !== undefined) return { ...base, response: jobs.data };
    return jobs.error instanceof ApiError ? { ...base, response: undefined, error: jobs.error } : null;
  }, [jobs.data, jobs.error]);
  useInspect(entry);

  const parked = jobs.data?.data ?? [];
  const failed = runs.data?.data ?? [];
  const close = () => {
    setAction(null);
  };

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">Jobs</h1>
          <p className="page-sub">Work that stopped and waits for you. Everything else retries on its own.</p>
        </div>
      </header>
      <Card title="Failed sync runs">
        <p className="muted">Pulls that gave up. The next scheduled sync tries again; Sync now on the user's page tries at once.</p>
        {runs.isPending ? <p role="status">Loading</p> : null}
        {runs.error !== null && runs.data === undefined ? <ErrorPanel error={runs.error} retry /> : null}
        {runs.data ? <FailedRunsTable rows={failed} /> : null}
      </Card>
      <Card
        title="Parked processing jobs"
        actions={
          canWrite && parked.length > 0 ? (
            <Button
              kind="primary"
              onClick={() => {
                setAction({ kind: "all" });
              }}
            >
              Retry all
            </Button>
          ) : undefined
        }
      >
        <p className="muted">
          A raw file that failed to process 5 times. Fix the cause (often a provider change or a full disk), then retry. The raw file is kept.
        </p>
        {jobs.isPending ? <p role="status">Loading</p> : null}
        {jobs.error !== null && jobs.data === undefined ? <ErrorPanel error={jobs.error} retry /> : null}
        {jobs.data ? (
          <ParkedJobsTable
            rows={parked}
            onRetry={
              canWrite
                ? (job) => {
                    setAction({ kind: "one", job });
                  }
                : undefined
            }
          />
        ) : null}
      </Card>
      {action?.kind === "one" ? (
        <ConfirmDialog
          title={`Retry job #${String(action.job.id)}?`}
          body="The job is queued again with fresh attempts. A job that fails for the same reason parks again after 5 attempts."
          confirmLabel="Retry"
          onConfirm={async () => {
            try {
              await retryOne.mutateAsync(action.job.id);
            } catch (e) {
              if (e instanceof ApiError && e.status === 409) {
                const detail = e.detail ? ` ${e.detail}` : "";
                throw new ApiError(409, `This job is no longer parked.${detail}`, e.requestId, e.body);
              }
              throw e;
            }
            toast(`Job #${String(action.job.id)} queued again.`);
          }}
          onClose={close}
        />
      ) : null}
      {action?.kind === "all" ? (
        <ConfirmDialog
          title="Retry every parked job?"
          body="Each job is queued again with fresh attempts. A job that fails for the same reason parks again after 5 attempts."
          confirmLabel="Retry all"
          onConfirm={async () => {
            const answer = await retryAll.mutateAsync();
            toast(`${formatCount(answer.retried)} ${answer.retried === 1 ? "job" : "jobs"} queued again.`);
          }}
          onClose={close}
        />
      ) : null}
    </div>
  );
}
