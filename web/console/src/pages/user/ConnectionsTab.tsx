import { useMemo, useState } from "react";

import type { UserConnection } from "../../api/types";
import { classifyConnection, classifyFreshness } from "../../lib/classify";
import { absTime, humanize, providerLabel, relTime } from "../../lib/format";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { DataTable, type Column } from "../../ui/DataTable";
import { EmptyState } from "../../ui/EmptyState";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { Pill } from "../../ui/Pill";
import { useToast } from "../../ui/Toast";
import { useConnectionsQuery, useDisconnect, useFreshnessQuery, useSyncNow, userPath } from "./api";
import { useTabInspect } from "./inspect";

type Action = { kind: "sync" | "disconnect"; provider: string };

export function ConnectionsTab({ userId }: { userId: string }) {
  const canWrite = useCanWrite();
  const toast = useToast();
  const connections = useConnectionsQuery(userId, true);
  const freshness = useFreshnessQuery(userId, true);
  const sync = useSyncNow(userId);
  const disconnect = useDisconnect(userId);
  const [action, setAction] = useState<Action | null>(null);
  const [deleteData, setDeleteData] = useState(false);
  useTabInspect("Connections", userId, userPath(userId, "connections"), undefined, connections.data, connections.error);

  const states = useMemo(() => new Map((freshness.data?.data ?? []).map((s) => [s.provider, s])), [freshness.data]);

  const columns: Column<UserConnection>[] = [
    { key: "provider", header: "Provider", cell: (c) => providerLabel(null, c.provider) },
    { key: "status", header: "Status", cell: (c) => <Pill tone={classifyConnection(c)}>{humanize(c.status)}</Pill> },
    {
      key: "freshness",
      header: "Freshness",
      cell: (c) => {
        const f = states.get(c.provider);
        return f ? <Pill tone={classifyFreshness(f)}>{humanize(f.state)}</Pill> : <span className="muted">-</span>;
      },
    },
    { key: "last", header: "Last sync", cell: (c) => <span title={absTime(c.last_sync_at)}>{relTime(c.last_sync_at)}</span> },
  ];
  if (canWrite) {
    columns.push({
      key: "actions",
      header: "Actions",
      cell: (c) => (
        <div className="row-actions">
          {c.status === "active" ? (
            <Button
              size="sm"
              onClick={() => {
                setAction({ kind: "sync", provider: c.provider });
              }}
            >
              Sync now
            </Button>
          ) : null}
          {c.status !== "disconnected" ? (
            <Button
              size="sm"
              kind="danger"
              onClick={() => {
                setDeleteData(false);
                setAction({ kind: "disconnect", provider: c.provider });
              }}
            >
              Disconnect
            </Button>
          ) : null}
        </div>
      ),
    });
  }

  const close = () => {
    setAction(null);
  };
  const name = action ? providerLabel(null, action.provider) : "";

  return (
    <>
      {connections.isPending ? <p role="status">Loading</p> : null}
      {connections.error !== null && connections.data === undefined ? <ErrorPanel error={connections.error} retry /> : null}
      {connections.data ? (
        <DataTable
          columns={columns}
          rows={connections.data.data}
          rowKey={(c) => c.provider}
          empty={<EmptyState title="No provider connected yet">{canWrite ? "Create a connection link and send it to the user." : undefined}</EmptyState>}
        />
      ) : null}
      {action?.kind === "sync" ? (
        <ConfirmDialog
          title={`Sync ${name} now?`}
          body="A pull of this provider is queued now, outside its usual schedule."
          confirmLabel="Sync now"
          onConfirm={async () => {
            await sync.mutateAsync(action.provider);
            toast(`A sync of ${name} is queued.`);
          }}
          onClose={close}
        />
      ) : null}
      {action?.kind === "disconnect" ? (
        <ConfirmDialog
          title={`Disconnect ${name}?`}
          body={
            <>
              <p>Access is revoked at the provider and pulls stop. The user can connect again later.</p>
              <label className="check">
                <input
                  type="checkbox"
                  checked={deleteData}
                  onChange={(e) => {
                    setDeleteData(e.target.checked);
                  }}
                />
                Also delete its data
              </label>
            </>
          }
          confirmLabel="Disconnect"
          danger
          onConfirm={async () => {
            await disconnect.mutateAsync({ provider: action.provider, deleteData });
            toast(deleteData ? `${name} disconnected; its data is being erased.` : `${name} disconnected.`);
          }}
          onClose={close}
        />
      ) : null}
    </>
  );
}
