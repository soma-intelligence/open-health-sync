import { useMemo, useState } from "react";

import { ApiError } from "../../api/client";
import type { APIKey } from "../../api/types";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";
import { useCanWrite, useSession } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { SecretOnce } from "../../ui/SecretOnce";
import { useToast } from "../../ui/Toast";
import { KEYS_PATH, useKeysQuery, useRevokeKey } from "./api";
import { CreateKeyDialog } from "./CreateKeyDialog";
import { KeysTable } from "./KeysTable";

function RevokeKey({ target, onClose }: { target: APIKey; onClose: () => void }) {
  const toast = useToast();
  const revoke = useRevokeKey(target.id);
  return (
    <ConfirmDialog
      title="Revoke this key?"
      body="Every service using it is refused at once, and so is every user token it minted. This cannot be undone."
      confirmLabel="Revoke key"
      danger
      typed={target.prefix}
      onConfirm={async () => {
        await revoke.mutateAsync();
        toast("Key revoked.");
      }}
      onClose={onClose}
    />
  );
}

export function KeysPage() {
  const canWrite = useCanWrite();
  const { session } = useSession();
  const toast = useToast();
  const query = useKeysQuery();
  const { data, error } = query;
  const [selected, setSelected] = useState<APIKey | null>(null);
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<APIKey | null>(null);
  const [secret, setSecret] = useState<string | null>(null);

  const entry = useMemo<InspectorEntry | null>(() => {
    if (selected) {
      return {
        kind: "keys",
        title: `${selected.prefix}...`,
        id: selected.id,
        request: { method: "GET", path: KEYS_PATH },
        response: selected,
      };
    }
    const base = { kind: "keys" as const, title: "API keys", request: { method: "GET" as const, path: KEYS_PATH } };
    if (data !== undefined) return { ...base, response: data };
    return error instanceof ApiError ? { ...base, response: undefined, error } : null;
  }, [selected, data, error]);
  useInspect(entry);

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">API keys</h1>
          <p className="page-sub">
            Keys of {session?.tenant.name ?? "this tenant"}. A key created here can do at most what this session&apos;s key does.
          </p>
        </div>
        <div className="page-actions">
          {canWrite ? (
            <Button
              kind="primary"
              onClick={() => {
                setCreating(true);
              }}
            >
              Create key
            </Button>
          ) : null}
        </div>
      </header>
      {query.isPending ? <p role="status">Loading</p> : null}
      {error !== null && data === undefined ? <ErrorPanel error={error} /> : null}
      {data ? (
        <Card title="Keys">
          <KeysTable
            rows={data.data}
            sessionPrefix={session?.key_prefix ?? ""}
            selectedId={selected?.id}
            onSelect={setSelected}
            {...(canWrite ? { onRevoke: setRevoking } : {})}
          />
          {canWrite ? <p className="muted small">The key of this session cannot revoke itself: sign in with another key first.</p> : null}
        </Card>
      ) : null}
      {creating ? (
        <CreateKeyDialog
          onCreated={(value) => {
            setSecret(value);
            toast("Key created.");
          }}
          onClose={() => {
            setCreating(false);
          }}
        />
      ) : null}
      {revoking ? (
        <RevokeKey
          target={revoking}
          onClose={() => {
            setRevoking(null);
          }}
        />
      ) : null}
      {secret !== null ? (
        <SecretOnce
          title="New API key"
          intro="Store it in your secret manager now. Only a hash is kept on the server."
          value={secret}
          onDone={() => {
            setSecret(null);
          }}
        />
      ) : null}
    </div>
  );
}
