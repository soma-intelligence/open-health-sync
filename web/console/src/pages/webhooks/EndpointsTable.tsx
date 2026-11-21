import type { Webhook } from "../../api/types";
import { classifyWebhook, webhookLabel } from "../../lib/classify";
import { absTime, relTime } from "../../lib/format";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { Pill } from "../../ui/Pill";

export function eventsLabel(w: Webhook): string {
  const n = w.event_types.length;
  if (n === 0) return "Every type";
  return n === 1 ? "1 type" : `${String(n)} types`;
}

const columns: Column<Webhook>[] = [
  {
    key: "endpoint",
    header: "Endpoint",
    cell: (w) => (
      <div>
        <div>{w.description || "Endpoint"}</div>
        <div className="mono small muted">{w.url}</div>
      </div>
    ),
  },
  { key: "events", header: "Events", cell: (w) => <span title={w.event_types.join(", ")}>{eventsLabel(w)}</span> },
  { key: "status", header: "Status", cell: (w) => <Pill tone={classifyWebhook(w)}>{webhookLabel(w)}</Pill> },
  {
    key: "success",
    header: "Last success",
    cell: (w) => (w.last_success_at ? <span title={absTime(w.last_success_at)}>{relTime(w.last_success_at)}</span> : <span className="muted">Never</span>),
  },
  {
    key: "failing",
    header: "Failing since",
    cell: (w) => (w.failing_since ? <span className="err" title={absTime(w.failing_since)}>{relTime(w.failing_since)}</span> : <span className="muted">-</span>),
  },
];

export interface EndpointsTableProps {
  rows: readonly Webhook[];
  selectedId: string | undefined;
  onSelect: (w: Webhook) => void;
}

export function EndpointsTable({ rows, selectedId, onSelect }: EndpointsTableProps) {
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(w) => w.id}
      selectedKey={selectedId}
      onSelect={onSelect}
      to={(w) => `/console/webhooks/${w.id}`}
      empty={<EmptyState title="No webhook endpoint">Add one to push new data to your systems as it lands.</EmptyState>}
    />
  );
}
