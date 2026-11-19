import { useMemo, useState } from "react";

import { ApiError } from "../../api/client";
import type { Webhook } from "../../api/types";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";
import { useOpenCreate } from "../../palette/useOpenCreate";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { SecretOnce } from "../../ui/SecretOnce";
import { useToast } from "../../ui/Toast";
import { WEBHOOKS_PATH, useCreateWebhook, useWebhooksQuery, webhookPath } from "./api";
import { EMPTY_VALUES, EndpointFields, type EndpointValues } from "./EndpointForm";
import { EndpointsTable } from "./EndpointsTable";

function CreateEndpoint({ onCreated, onClose }: { onCreated: (secret: string) => void; onClose: () => void }) {
  const create = useCreateWebhook();
  const [values, setValues] = useState<EndpointValues>(EMPTY_VALUES);
  return (
    <ConfirmDialog
      title="Add a webhook endpoint"
      body={
        <>
          <p>This server sends a signed event to this URL when data is written or deleted, or a connection breaks.</p>
          <EndpointFields values={values} onChange={setValues} />
        </>
      }
      confirmLabel="Create endpoint"
      dirty={values.url.trim() !== "" || values.description.trim() !== "" || values.eventTypes.length > 0}
      onConfirm={async () => {
        const url = values.url.trim();
        if (url === "") throw new Error("Enter the endpoint URL.");
        const description = values.description.trim();
        const hook = await create.mutateAsync({
          url,
          ...(description === "" ? {} : { description }),
          event_types: [...values.eventTypes],
        });
        const value = hook.secret;
        // Keep the secret out of the mutation state: only the parent holds it.
        create.reset();
        onCreated(value);
      }}
      onClose={onClose}
    />
  );
}

export function WebhooksPage() {
  const canWrite = useCanWrite();
  const toast = useToast();
  const query = useWebhooksQuery();
  const { data, error } = query;
  const [selected, setSelected] = useState<Webhook | null>(null);
  const [creating, setCreating] = useState(false);
  useOpenCreate(() => {
    setCreating(true);
  });
  const [secret, setSecret] = useState<string | null>(null);

  const entry = useMemo<InspectorEntry | null>(() => {
    if (selected) {
      return {
        kind: "webhook",
        title: selected.description || selected.url,
        id: selected.id,
        request: { method: "GET", path: webhookPath(selected.id) },
        response: selected,
      };
    }
    const base = { kind: "webhooks" as const, title: "Webhooks", request: { method: "GET" as const, path: WEBHOOKS_PATH } };
    if (data !== undefined) return { ...base, response: data };
    return error instanceof ApiError ? { ...base, response: undefined, error } : null;
  }, [selected, data, error]);
  useInspect(entry);

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">Webhooks</h1>
          <p className="page-sub">Endpoints this server notifies when data is written or deleted, or a connection breaks.</p>
        </div>
        <div className="page-actions">
          {canWrite ? (
            <Button
              kind="primary"
              onClick={() => {
                setCreating(true);
              }}
            >
              Add endpoint
            </Button>
          ) : null}
        </div>
      </header>
      {query.isPending ? <p role="status">Loading</p> : null}
      {error !== null && data === undefined ? <ErrorPanel error={error} retry /> : null}
      {data ? (
        <Card title="Endpoints">
          <EndpointsTable rows={data.data} selectedId={selected?.id} onSelect={setSelected} />
        </Card>
      ) : null}
      {creating ? (
        <CreateEndpoint
          onCreated={(value) => {
            setSecret(value);
            toast("Endpoint created.");
          }}
          onClose={() => {
            setCreating(false);
          }}
        />
      ) : null}
      {secret !== null ? (
        <SecretOnce
          title="Signing secret"
          intro="Verify every delivery with this secret. Store it with your receiver now."
          value={secret}
          onDone={() => {
            setSecret(null);
          }}
        />
      ) : null}
    </div>
  );
}
