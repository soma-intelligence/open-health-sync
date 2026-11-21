import type { User } from "../../api/types";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { useConnectionsQuery, useCoverageQuery, useFreshnessQuery, userPath } from "./api";
import { CoveragePanel, MetricTiles } from "./CoveragePanel";
import { useTabInspect } from "./inspect";
import { PreviewPanel } from "./PreviewPanel";
import { PrivacyCard } from "./PrivacyCard";
import { Skeleton } from "../../ui/Skeleton";

/** The user at a glance: counts, sources and their state, consent and privacy. Values stay behind Preview and the exports. */
export function DataTab({ user }: { user: User }) {
  const userId = user.id;
  const coverage = useCoverageQuery(userId);
  const connections = useConnectionsQuery(userId, true);
  const freshness = useFreshnessQuery(userId, true);
  useTabInspect("Coverage", userId, userPath(userId, "coverage"), undefined, coverage.data, coverage.error);
  return (
    <div className="stack">
      {coverage.isPending ? (
        <Skeleton kind="tiles" />
      ) : null}
      {coverage.error !== null && coverage.data === undefined ? <ErrorPanel error={coverage.error} retry /> : null}
      {coverage.data ? <MetricTiles coverage={coverage.data} /> : null}
      <div className="user-cards">
        {coverage.data ? <CoveragePanel coverage={coverage.data} connections={connections.data?.data} freshness={freshness.data?.data} /> : <div />}
        <PrivacyCard user={user} />
      </div>
      <PreviewPanel userId={userId} />
    </div>
  );
}
