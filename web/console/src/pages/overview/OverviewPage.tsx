import { useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";

import { ApiError } from "../../api/client";
import { gettingStarted, needsYou } from "../../lib/pipeline";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";
import { useSession } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { Icon } from "../../ui/Icon";
import { Pill } from "../../ui/Pill";
import { RefreshFailed } from "../../ui/RefreshFailed";
import { useKeysQuery } from "../keys/api";
import { useWebhooksQuery } from "../webhooks/api";
import { PIPELINE_KEY, usePipelineQuery, useProbe } from "./api";
import { AttentionList } from "./AttentionList";
import { GettingStarted } from "./GettingStarted";
import { PipelineBand } from "./PipelineBand";
import { SourcesCard } from "./SourcesCard";

function Probe({ path, ok, bad }: { path: "/healthz" | "/readyz"; ok: string; bad: string }) {
  const probe = useProbe(path);
  if (probe.isPending) return null;
  const detail = probe.error instanceof ApiError ? probe.error.detail : undefined;
  return (
    <span title={detail}>
      <Pill tone={probe.isError ? "bad" : "ok"}>{probe.isError ? bad : ok}</Pill>
    </span>
  );
}

export function OverviewPage() {
  const { session } = useSession();
  const queryClient = useQueryClient();
  const pipeline = usePipelineQuery();
  const { data, error } = pipeline;
  // Endpoints are only listed when the pipeline counts one in trouble, so a quiet tenant reads no list.
  const troubled = data !== undefined && data.delivery.endpoints_failing + data.delivery.endpoints_disabled > 0;
  const webhooks = useWebhooksQuery({ enabled: troubled });
  const keys = useKeysQuery({ glance: true });
  const steps = data ? gettingStarted(data) : [];
  const setupLeft = steps.some((i) => !i.done);
  const items = data ? needsYou(data, troubled ? webhooks.data?.data : undefined, keys.data?.data) : [];

  const entry = useMemo<InspectorEntry | null>(() => {
    if (data === undefined && !(error instanceof ApiError)) return null;
    const base = { kind: "pipeline" as const, title: "Pipeline", request: { method: "GET" as const, path: "/v1/pipeline" } };
    return error instanceof ApiError && data === undefined ? { ...base, response: undefined, error } : { ...base, response: data };
  }, [data, error]);
  useInspect(entry);

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: PIPELINE_KEY });
    void queryClient.invalidateQueries({ queryKey: ["probe"] });
  };

  // Until setup is done and nothing is broken, the steps are the one priority: they come first.
  const stepsFirst = setupLeft && items.every((i) => i.chip === "Setup");

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">Overview</h1>
          {session ? <p className="page-sub">{`Everything flowing into ${session.acting_tenant.name}, right now.`}</p> : null}
        </div>
        <div className="page-actions">
          <Probe path="/healthz" ok="API live" bad="API down" />
          <Probe path="/readyz" ok="Ready" bad="Not ready" />
          <Button onClick={refresh} disabled={pipeline.isFetching} aria-busy={pipeline.isFetching}>
            <Icon name="refresh" size="sm" />
            {pipeline.isFetching && !pipeline.isPending ? "Refreshing" : "Refresh"}
          </Button>
        </div>
      </header>
      {pipeline.isPending ? (
        <p role="status" className="loading">
          Loading
        </p>
      ) : null}
      {error !== null && data === undefined ? <ErrorPanel error={error} retry /> : null}
      {error !== null && data !== undefined ? (
        <RefreshFailed error={error} updatedAt={pipeline.dataUpdatedAt} onRetry={refresh} busy={pipeline.isFetching} />
      ) : null}
      {data ? (
        <>
          {stepsFirst ? <GettingStarted items={steps} /> : null}
          <PipelineBand pipeline={data} stale={error !== null} />
          <div className="overview-cards">
            <AttentionList items={items} setupLeft={setupLeft} />
            <SourcesCard sources={data.sources} />
          </div>
          {setupLeft && !stepsFirst ? <GettingStarted items={steps} /> : null}
        </>
      ) : null}
    </div>
  );
}
