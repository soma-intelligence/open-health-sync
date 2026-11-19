import { ErrorPanel } from "../../ui/ErrorPanel";
import { useCoverageQuery, userPath } from "./api";
import { CoveragePanel } from "./CoveragePanel";
import { DownloadsPanel } from "./DownloadsPanel";
import { useTabInspect } from "./inspect";
import { PreviewPanel } from "./PreviewPanel";

/** Coverage on open. Values stay behind Preview and the downloads. */
export function DataTab({ userId }: { userId: string }) {
  const coverage = useCoverageQuery(userId);
  useTabInspect("Coverage", userId, userPath(userId, "coverage"), undefined, coverage.data, coverage.error);
  return (
    <div className="stack">
      {coverage.isPending ? <p role="status">Loading</p> : null}
      {coverage.error !== null && coverage.data === undefined ? <ErrorPanel error={coverage.error} retry /> : null}
      {coverage.data ? <CoveragePanel coverage={coverage.data} /> : null}
      <PreviewPanel userId={userId} />
      <DownloadsPanel userId={userId} />
    </div>
  );
}
