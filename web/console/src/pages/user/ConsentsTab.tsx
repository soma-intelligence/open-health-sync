import type { ConsentEvent } from "../../api/types";
import { absTime, humanize, providerLabel, relTime } from "../../lib/format";
import { Card } from "../../ui/Card";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { Pager, usePager } from "../../ui/Pager";
import { Pill } from "../../ui/Pill";
import { useConsentHistoryQuery, useConsentsQuery, userPath } from "./api";
import { useTabInspect } from "./inspect";

const history: Column<ConsentEvent>[] = [
  { key: "at", header: "When", cell: (e) => <span title={absTime(e.at)}>{relTime(e.at)}</span> },
  { key: "provider", header: "Provider", cell: (e) => providerLabel(null, e.provider) },
  { key: "category", header: "Category", cell: (e) => humanize(e.category) },
  { key: "action", header: "Action", cell: (e) => <Pill tone={e.action === "granted" ? "ok" : "off"}>{humanize(e.action)}</Pill> },
  { key: "evidence", header: "Evidence", cell: (e) => <span className="mono">{e.evidence}</span> },
];

/** What the user agreed to, and how that changed. Read only: consent is the user's to give. */
export function ConsentsTab({ userId }: { userId: string }) {
  const pager = usePager();
  const consents = useConsentsQuery(userId, true);
  const events = useConsentHistoryQuery(userId, pager.cursor, true);
  useTabInspect("Consents", userId, userPath(userId, "consents"), undefined, consents.data, consents.error);

  return (
    <div className="stack">
      <Card title="Current consents">
        {consents.error !== null && consents.data === undefined ? <ErrorPanel error={consents.error} /> : null}
        {consents.isPending ? <p role="status">Loading</p> : null}
        {consents.data && consents.data.data.length === 0 ? <EmptyState title="No consent recorded yet" /> : null}
        {consents.data?.data.map((source) => (
          <div key={source.provider} className="consent-source">
            <h4>{providerLabel(null, source.provider)}</h4>
            <ul className="chips">
              {source.categories.map((c) => (
                <li key={c.category}>
                  <Pill tone={c.granted ? "ok" : "off"}>{`${humanize(c.category)}: ${c.granted ? "Granted" : "Not granted"}`}</Pill>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </Card>
      <Card title="History">
        {events.error !== null && events.data === undefined ? <ErrorPanel error={events.error} /> : null}
        {events.isPending ? <p role="status">Loading</p> : null}
        {events.data ? (
          <>
            <DataTable columns={history} rows={events.data.data} rowKey={(e) => String(e.id)} empty={<EmptyState title="No consent change yet" />} />
            <Pager pager={pager} nextCursor={events.data.next_cursor} loading={events.isPlaceholderData} />
          </>
        ) : null}
      </Card>
    </div>
  );
}
