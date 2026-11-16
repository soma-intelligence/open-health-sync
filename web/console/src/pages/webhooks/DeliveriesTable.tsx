import type { Delivery } from "../../api/types";
import { classifyDelivery } from "../../lib/classify";
import { absTime, formatAgo, humanize, relTime } from "../../lib/format";
import { Button } from "../../ui/Button";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { Pill } from "../../ui/Pill";

/** Creation to delivery: the list does not carry the time of one request. */
function duration(d: Delivery): string {
  if (!d.delivered_at) return "-";
  return formatAgo((Date.parse(d.delivered_at) - Date.parse(d.created_at)) / 1000);
}

export interface DeliveriesTableProps {
  rows: readonly Delivery[];
  /** Set when the session may write: Retry and Replay on finished deliveries. */
  onAgain: ((d: Delivery, how: "retry" | "replay") => void) | undefined;
}

export function DeliveriesTable({ rows, onAgain }: DeliveriesTableProps) {
  const columns: Column<Delivery>[] = [
    {
      key: "event",
      header: "Event",
      cell: (d) => (
        <div>
          <span className="mono">{d.event_type}</span>
          <div className="mono small muted">{`#${String(d.id)}`}{d.replay_of === null ? "" : ` replays #${String(d.replay_of)}`}</div>
        </div>
      ),
    },
    {
      key: "response",
      header: "Response",
      cell: (d) => (
        <div>
          <Pill tone={classifyDelivery(d)}>{humanize(d.status)}</Pill>{" "}
          {d.last_status_code === null ? null : <span className="mono">{String(d.last_status_code)}</span>}
          {d.last_error ? <div className="err small">{d.last_error}</div> : null}
        </div>
      ),
    },
    { key: "duration", header: "Duration", cell: duration },
    { key: "attempt", header: "Attempt", className: "num", cell: (d) => String(d.attempts) },
    { key: "time", header: "Time", cell: (d) => <span title={absTime(d.created_at)}>{relTime(d.created_at)}</span> },
  ];
  if (onAgain) {
    columns.push({
      key: "actions",
      header: "Actions",
      cell: (d) =>
        d.status === "done" || d.status === "parked" ? (
          <div className="row-actions">
            <Button
              size="sm"
              onClick={() => {
                onAgain(d, "retry");
              }}
            >
              Retry
            </Button>
            <Button
              size="sm"
              onClick={() => {
                onAgain(d, "replay");
              }}
            >
              Replay
            </Button>
          </div>
        ) : null,
    });
  }
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(d) => String(d.id)}
      empty={<EmptyState title="No delivery in the last 30 days">{onAgain ? "Send a test event to check the endpoint." : "Nothing was sent to this endpoint lately."}</EmptyState>}
    />
  );
}
