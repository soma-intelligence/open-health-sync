import { useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { ApiError } from "../../api/client";
import type { Delivery, Webhook } from "../../api/types";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";
import { classifyWebhook, webhookLabel } from "../../lib/classify";
import { absTime, relTime } from "../../lib/format";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { EmptyState } from "../../ui/EmptyState";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { Mono } from "../../ui/Mono";
import { Pill } from "../../ui/Pill";
import { SecretOnce } from "../../ui/SecretOnce";
import { useToast } from "../../ui/Toast";
import {
  DELIVERIES_QUERY,
  useDeleteWebhook,
  useDeliveriesQuery,
  useRedeliver,
  useRotateWebhookSecret,
  useTestWebhook,
  useUpdateWebhook,
  useWebhookQuery,
  webhookPath,
} from "./api";
import { DeliveriesTable } from "./DeliveriesTable";
import { EndpointFields, changedFields, valuesOf, type EndpointValues } from "./EndpointForm";
import { eventsLabel } from "./EndpointsTable";
import { Skeleton } from "../../ui/Skeleton";

function Crumbs() {
  return (
    <nav className="crumbs" aria-label="Breadcrumb">
      <Link to="/webhooks">Webhooks</Link>
    </nav>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="fact">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function when(iso: string | null, none: string): ReactNode {
  return iso ? <span title={absTime(iso)}>{relTime(iso)}</span> : <span className="muted">{none}</span>;
}

/** Another endpoint is a fresh page: nothing local carries over. */
export function WebhookPage() {
  const { webhookId = "" } = useParams();
  return <WebhookView key={webhookId} />;
}

function WebhookView() {
  const { webhookId = "" } = useParams();
  const query = useWebhookQuery(webhookId);
  const hook = query.data;
  const error = query.error;

  const entry = useMemo<InspectorEntry | null>(() => {
    const base = {
      kind: "webhook" as const,
      title: hook ? hook.description || hook.url : webhookId,
      id: webhookId,
      request: { method: "GET" as const, path: webhookPath(webhookId) },
    };
    if (hook !== undefined) return { ...base, response: hook };
    return error instanceof ApiError ? { ...base, response: undefined, error } : null;
  }, [hook, error, webhookId]);
  useInspect(entry);

  if (query.isPending) {
    return (
      <div className="page">
        <Skeleton kind="page" />
      </div>
    );
  }
  if (hook === undefined) {
    if (error instanceof ApiError && error.status === 404) {
      return (
        <div className="page">
          <EmptyState title="Not found">
            No webhook endpoint with this id. Back to <Link to="/webhooks">Webhooks</Link>.
          </EmptyState>
        </div>
      );
    }
    return (
      <div className="page">
        <ErrorPanel error={error} retry />
      </div>
    );
  }
  return <Detail hook={hook} />;
}

type Action =
  | { kind: "test" | "rotate" | "toggle" | "delete" }
  | { kind: "retry" | "replay"; delivery: Delivery };

function Detail({ hook }: { hook: Webhook }) {
  const canWrite = useCanWrite();
  const toast = useToast();
  const navigate = useNavigate();
  const deliveries = useDeliveriesQuery(hook.id);
  const update = useUpdateWebhook(hook.id);
  const remove = useDeleteWebhook(hook.id);
  const test = useTestWebhook(hook.id);
  const rotate = useRotateWebhookSecret(hook.id);
  const retry = useRedeliver(hook.id, "retry");
  const replay = useRedeliver(hook.id, "replay");
  const [action, setAction] = useState<Action | null>(null);
  const [secret, setSecret] = useState<{ value: string; until: string | null } | null>(null);
  const [expireNow, setExpireNow] = useState(false);

  const close = () => {
    rotate.reset();
    setAction(null);
    setExpireNow(false);
  };
  const title = hook.description || "Endpoint";
  const on = hook.status === "active";
  const rows = deliveries.data?.data ?? [];

  return (
    <div className="page">
      <Crumbs />
      <header className="page-head">
        <div>
          <div className="user-title">
            <h1 className="page-title">{title}</h1>
            <Pill tone={classifyWebhook(hook)}>{webhookLabel(hook)}</Pill>
          </div>
          <p className="page-sub">
            <Mono>{hook.url}</Mono>
          </p>
        </div>
        {canWrite ? (
          <div className="page-actions">
            <Button
              onClick={() => {
                setAction({ kind: "test" });
              }}
            >
              Send test
            </Button>
            <Button
              onClick={() => {
                setAction({ kind: "rotate" });
              }}
            >
              Rotate secret
            </Button>
            <Button
              {...(on ? {} : { kind: "primary" as const })}
              onClick={() => {
                setAction({ kind: "toggle" });
              }}
            >
              {on ? "Pause" : "Resume"}
            </Button>
            <Button
              kind="danger"
              onClick={() => {
                setAction({ kind: "delete" });
              }}
            >
              Delete
            </Button>
          </div>
        ) : null}
      </header>
      {hook.disabled_reason ? <p className="notice warn">Turned off: {hook.disabled_reason}</p> : null}
      <div className="stack">
        <Card title="Status">
          <dl className="facts">
            <Fact label="Last success">{when(hook.last_success_at, "Never")}</Fact>
            <Fact label="Failing since">{hook.failing_since ? <span className="err" title={absTime(hook.failing_since)}>{relTime(hook.failing_since)}</span> : <span className="muted">Not failing</span>}</Fact>
            {hook.consecutive_failures > 0 ? <Fact label="Failed attempts in a row">{String(hook.consecutive_failures)}</Fact> : null}
            {hook.paused_until ? <Fact label="Retries again">{when(hook.paused_until, "-")}</Fact> : null}
            {hook.previous_secret_expires_at ? <Fact label="Previous secret also signs until">{when(hook.previous_secret_expires_at, "-")}</Fact> : null}
            <Fact label="Event types">{hook.event_types.length === 0 ? eventsLabel(hook) : <span className="mono">{hook.event_types.join(", ")}</span>}</Fact>
          </dl>
        </Card>
        <Card title="Recent deliveries">
          <p className="muted">The last 30 days, newest first. Retry sends a delivery again; Replay sends its event as a new delivery.</p>
          {deliveries.isPending ? <Skeleton count={3} /> : null}
          {deliveries.error !== null && deliveries.data === undefined ? <ErrorPanel error={deliveries.error} retry /> : null}
          {deliveries.data ? (
            <DeliveriesTable
              rows={rows}
              onAgain={
                canWrite
                  ? (delivery, how) => {
                      setAction({ kind: how, delivery });
                    }
                  : undefined
              }
            />
          ) : null}
          {deliveries.data?.next_cursor ? <p className="muted">{`Showing the latest ${String(DELIVERIES_QUERY.limit)}.`}</p> : null}
        </Card>
        {canWrite ? <EditEndpoint hook={hook} /> : null}
      </div>

      {action?.kind === "test" ? (
        <ConfirmDialog
          title="Send a test event?"
          body="A test event is queued for this endpoint alone, whatever its event types. It shows up in the deliveries below."
          confirmLabel="Send test"
          onConfirm={async () => {
            await test.mutateAsync();
            toast("A test event is queued for delivery.");
          }}
          onClose={close}
        />
      ) : null}
      {action?.kind === "rotate" ? (
        <ConfirmDialog
          title="Rotate the signing secret?"
          body={
            <>
              <p>Deliveries are signed with the new secret at once. The old one also signs for a grace period, so you can update your receiver without refusing a request.</p>
              <label className="check">
                <input
                  type="checkbox"
                  checked={expireNow}
                  onChange={(e) => {
                    setExpireNow(e.target.checked);
                  }}
                />
                <span>End the old secret now (after a leak)</span>
              </label>
            </>
          }
          confirmLabel="Rotate"
          onConfirm={async () => {
            const answer = await rotate.mutateAsync(expireNow);
            const value = answer.secret;
            const until = answer.previous_secret_expires_at;
            // The secret now lives in this component's state only, not in the mutation.
            rotate.reset();
            setSecret({ value, until });
          }}
          onClose={close}
        />
      ) : null}
      {action?.kind === "toggle" ? (
        <ConfirmDialog
          title={on ? "Pause this endpoint?" : "Resume this endpoint?"}
          body={
            on
              ? "Nothing is sent to it while it is paused, and its pending deliveries are parked. Resume it when the receiver is ready."
              : "The endpoint receives events again and its failure state is cleared."
          }
          confirmLabel={on ? "Pause" : "Resume"}
          danger={on}
          onConfirm={async () => {
            await update.mutateAsync({ enabled: !on });
            toast(on ? "Endpoint paused." : "Endpoint resumed.");
          }}
          onClose={close}
        />
      ) : null}
      {action?.kind === "delete" ? (
        <ConfirmDialog
          title="Delete this endpoint?"
          body="It stops receiving events and its delivery history is deleted. This cannot be undone."
          confirmLabel="Delete endpoint"
          danger
          typed={hook.url}
          onConfirm={async () => {
            await remove.mutateAsync();
            toast("Endpoint deleted.");
            void navigate("/webhooks");
          }}
          onClose={close}
        />
      ) : null}
      {action?.kind === "retry" || action?.kind === "replay" ? (
        <ConfirmDialog
          title={`${action.kind === "retry" ? "Retry" : "Replay"} delivery #${String(action.delivery.id)}?`}
          body={
            action.kind === "retry"
              ? "The delivery is sent again from its first attempt, with its original id and payload."
              : "The event is sent again as a new delivery, with the same event id and payload. The original keeps its history."
          }
          confirmLabel={action.kind === "retry" ? "Retry" : "Replay"}
          onConfirm={async () => {
            const id = action.delivery.id;
            if (action.kind === "retry") {
              await retry.mutateAsync(id);
              toast(`Delivery #${String(id)} queued again.`);
            } else {
              await replay.mutateAsync(id);
              toast(`The event of delivery #${String(id)} is sent again as a new delivery.`);
            }
          }}
          onClose={close}
        />
      ) : null}
      {secret !== null ? (
        <SecretOnce
          title="New signing secret"
          intro={
            secret.until
              ? `Update your receiver with it now. The old secret also signs until ${absTime(secret.until)}.`
              : "Update your receiver with it now. The old secret stopped signing."
          }
          value={secret.value}
          onDone={() => {
            setSecret(null);
          }}
        />
      ) : null}
    </div>
  );
}

/** URL, description and event types; Save sends only what changed, after a confirmation. */
function EditEndpoint({ hook }: { hook: Webhook }) {
  const toast = useToast();
  const update = useUpdateWebhook(hook.id);
  const [values, setValues] = useState<EndpointValues>(() => valuesOf(hook));
  const [asking, setAsking] = useState(false);
  // The endpoint changes under the form (pause, resume, a test, a refresh): the
  // form follows it only while nothing is being edited, so typed changes stay.
  const [base, setBase] = useState(hook);
  if (hook !== base) {
    if (hook.updated_at !== base.updated_at && Object.keys(changedFields(base, values)).length === 0) setValues(valuesOf(hook));
    setBase(hook);
  }
  const body = changedFields(hook, values);
  const names = Object.keys(body).map((k) => (k === "event_types" ? "event types" : k));
  const dirty = names.length > 0;

  return (
    <Card title="Settings">
      <form
        className="confirm-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (dirty) setAsking(true);
        }}
      >
        <EndpointFields values={values} onChange={setValues} />
        <div className="row-actions">
          <Button type="submit" kind="primary" disabled={!dirty}>
            Save changes
          </Button>
        </div>
      </form>
      {asking ? (
        <ConfirmDialog
          title="Save these changes?"
          body={`Changes: ${names.join(", ")}. They apply to the next delivery.`}
          confirmLabel="Save"
          onConfirm={async () => {
            await update.mutateAsync(body);
            toast("Endpoint saved.");
          }}
          onClose={() => {
            setAsking(false);
          }}
        />
      ) : null}
    </Card>
  );
}
