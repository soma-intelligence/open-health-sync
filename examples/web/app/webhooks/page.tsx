// SPDX-License-Identifier: AGPL-3.0-only
//
// Step 4: receive webhooks. The endpoint is app/api/webhooks/route.ts; this page registers it,
// sends test events to it, and shows the last 20 requests it received.

import type { Metadata } from "next";
import type { Webhook, WebhookDelivery } from "@open-health-sync/server";
import { ActionForm } from "../../components/action-form.tsx";
import { AutoRefresh } from "../../components/auto-refresh.tsx";
import { Steps } from "../../components/steps.tsx";
import { Empty, ErrorNotice, Json, Notice, PageHead, Route, Routes, formatDateTime } from "../../components/ui.tsx";
import { readConfig, webhookUrl } from "../../lib/config.ts";
import { attempt, tenantClient, type Result } from "../../lib/ohs.ts";
import { INBOX_SIZE, recentEntries, webhookSecret, type InboxEntry } from "../../lib/webhooks.ts";
import { clearInbox, registerWebhook, rotateSecret, sendTestEvent } from "../actions.ts";

export const dynamic = "force-dynamic";
export const metadata: Metadata = { title: "Webhooks" };

export default async function WebhooksPage() {
  const { config, missing } = readConfig();
  return (
    <main className="view">
      <Steps current={4} />
      <PageHead
        step="Step 04 · Webhooks"
        title="Receive webhooks"
        sub="Open Health Sync posts a signed event when something changes: data written, a user erased, a connection that needs the person again. Events say what changed and for whom, never a health value. This app verifies each request and keeps the last 20 in memory."
        routes={
          <Routes>
            <Route method="POST" path="/v1/webhooks" />
            <Route method="POST" path="/v1/webhooks/{id}/test" />
          </Routes>
        }
      />
      {config ? (
        <Content receiver={webhookUrl(config)} />
      ) : (
        <Notice kind="warn">
          Set {missing.join(" and ")} in <code>.env.local</code> first (see step 01).
        </Notice>
      )}
    </main>
  );
}

async function Content({ receiver }: { receiver: string }) {
  const ohs = tenantClient();
  const endpoints = await attempt(() => ohs.listWebhooks());
  const endpoint = endpoints.ok ? endpoints.value.data.find((w) => w.url === receiver) : undefined;
  const deliveries: Result<{ data: WebhookDelivery[] }> | undefined = endpoint
    ? await attempt(() => ohs.listWebhookDeliveries(endpoint.id, { limit: 5 }))
    : undefined;
  const secret = webhookSecret();
  const entries = recentEntries();
  const local = /^http:\/\/(localhost|127\.0\.0\.1)[:/]/.test(receiver);

  return (
    <>
      {!endpoints.ok && <ErrorNotice what="Listing endpoints" error={endpoints.error} />}
      <div className="grid-2">
        <section className="card">
          <div className="card-head">
            <h2>This app&apos;s endpoint</h2>
            {endpoint ? <EndpointStatus endpoint={endpoint} /> : <span className="pill">Not registered</span>}
          </div>
          <div className="card-body">
            <dl className="facts compact">
              <dt>Receiver</dt>
              <dd>
                <code>POST {receiver}</code>
              </dd>
              <dt>Secret</dt>
              <dd>
                {secret?.from === "env" ? (
                  <span className="pill ok">From OHS_WEBHOOK_SECRET</span>
                ) : secret ? (
                  <span className="pill warn">Registered here, in memory</span>
                ) : (
                  <span className="pill bad">Not set</span>
                )}
              </dd>
              <dt>Checks</dt>
              <dd>
                <code>verifyWebhook</code> of the server SDK: HMAC-SHA256 of id, timestamp and raw body, timestamp within 5
                minutes
              </dd>
              {endpoint && (
                <>
                  <dt>Endpoint</dt>
                  <dd>
                    <code>{endpoint.id}</code>
                  </dd>
                  <dt>Last success</dt>
                  <dd>{endpoint.last_success_at ? formatDateTime(endpoint.last_success_at) : "none yet"}</dd>
                </>
              )}
            </dl>
            <div style={{ marginTop: 16 }}>
              {!endpoint && endpoints.ok && local && (
                <Notice>
                  The instance posts only to https and public addresses, unless it runs with{" "}
                  <code>OHS_WEBHOOK_ALLOW_PRIVATE=true</code> (a local instance). Otherwise open a tunnel to this app and
                  set <code>APP_URL</code> to its https address.
                </Notice>
              )}
              {endpoint && !secret && (
                <Notice kind="warn">
                  The endpoint exists but this app has no secret to check it with. Put the secret shown at creation in{" "}
                  <code>OHS_WEBHOOK_SECRET</code>, or rotate it.
                </Notice>
              )}
              {/* Always mounted, so the secret shown once stays on screen when the page refreshes. */}
              <ActionForm
                action={registerWebhook}
                show={!endpoint && endpoints.ok}
                submit="Register this endpoint"
                pending="Registering..."
                primary
              />
              <ActionForm
                action={rotateSecret.bind(null, endpoint?.id ?? "")}
                show={Boolean(endpoint && !secret)}
                submit="Rotate the secret"
                pending="Rotating..."
              />
            </div>
          </div>
          {endpoints.ok && endpoints.value.data.length > 0 && (
            <div className="card-foot">
              <span>
                {endpoints.value.data.length} endpoint{endpoints.value.data.length > 1 ? "s" : ""} on this tenant
              </span>
              <Route method="GET" path="/v1/webhooks" />
            </div>
          )}
        </section>

        <section className="card">
          <div className="card-head">
            <h2>Send a test event</h2>
            <Route method="POST" path="/v1/webhooks/{id}/test" />
            <p className="hint">
              A sample of the event, about no real user (the nil UUID), marked <code>&quot;test&quot;: true</code>.
            </p>
          </div>
          <div className="card-body">
            {endpoint ? (
              <ActionForm action={sendTestEvent.bind(null, endpoint.id)} submit="Send" pending="Sending..." inline primary>
                <select name="event_type" aria-label="Event type" defaultValue="data.written">
                  <option value="data.written">data.written</option>
                  <option value="webhook.test">webhook.test</option>
                  <option value="connection.needs_reauth">connection.needs_reauth</option>
                  <option value="user.deleted">user.deleted</option>
                </select>
              </ActionForm>
            ) : (
              <p className="muted">Register the endpoint first.</p>
            )}
          </div>
          {deliveries && (
            <>
              {!deliveries.ok ? (
                <div className="card-body">
                  <ErrorNotice what="Listing deliveries" error={deliveries.error} />
                </div>
              ) : deliveries.value.data.length > 0 ? (
                <div className="scroll">
                  <table>
                    <thead>
                      <tr>
                        <th>Delivery</th>
                        <th>Status</th>
                        <th className="num">Answer</th>
                      </tr>
                    </thead>
                    <tbody>
                      {deliveries.value.data.map((d) => (
                        <tr key={d.id}>
                          <td>
                            <div className="cell-2">
                              <code>{d.event_type}</code>
                              <span className="sub">
                                {formatDateTime(d.created_at)}, {d.attempts} attempt{d.attempts === 1 ? "" : "s"}
                              </span>
                            </div>
                          </td>
                          <td>
                            <span className={`pill ${d.status === "done" ? "ok" : d.status === "parked" ? "bad" : "warn"}`}>{d.status}</span>
                          </td>
                          <td className="num dim">{d.last_status_code ?? "none"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : null}
              <div className="card-foot">
                <Route method="GET" path="/v1/webhooks/{id}/deliveries" />
              </div>
            </>
          )}
        </section>
      </div>

      <section className="card">
        <div className="card-head">
          <div className="title">
            <h2>Received</h2>
            <span className="tag">
              {entries.length} / {INBOX_SIZE}
            </span>
          </div>
          <div className="button-row">
            <AutoRefresh label="Live" />
            {entries.length > 0 && (
              <form action={clearInbox}>
                <button type="submit" className="small">
                  Clear
                </button>
              </form>
            )}
          </div>
          <p className="hint">
            Requests to <code>/api/webhooks</code>, newest first, kept in this process&apos;s memory. A retry repeats the
            event id, so it is counted, not listed twice.
          </p>
        </div>
        {entries.length === 0 ? (
          <Empty title="Nothing received yet">Send a test event, or upload sample data: each processed part sends data.written.</Empty>
        ) : (
          <ul className="events">
            {entries.map((entry, i) => (
              <EventItem key={`${entry.webhookId}-${i}`} entry={entry} />
            ))}
          </ul>
        )}
      </section>
    </>
  );
}

function EndpointStatus({ endpoint }: { endpoint: Webhook }) {
  if (endpoint.status === "disabled") return <span className="pill bad">Disabled</span>;
  if (endpoint.failing_since) return <span className="pill warn">Failing</span>;
  return <span className="pill ok">Registered</span>;
}

function EventItem({ entry }: { entry: InboxEntry }) {
  const event = entry.event;
  const data = event?.data;
  const meta = data
    ? [
        data.external_ref ? `user ${data.external_ref}` : data.user_id && `user ${data.user_id}`,
        data.provider && `from ${data.provider}`,
        data.first_day && `days ${data.first_day} to ${data.last_day}`,
        data.records && `rows ${Object.entries(data.records).map(([table, n]) => `${table} ${n}`).join(", ")}`,
      ].filter(Boolean)
    : [];
  return (
    <li>
      <div className="event-row">
        {entry.verified ? <span className="pill ok">Verified</span> : <span className="pill bad">Refused</span>}
        <span className="type">{event?.type ?? "unknown"}</span>
        {event?.test && <span className="tag">Test</span>}
        {entry.deliveries > 1 && <span className="tag">Received {entry.deliveries} times</span>}
        <time dateTime={entry.receivedAt}>{formatDateTime(entry.receivedAt)}</time>
      </div>
      <p className="event-meta">
        {entry.verified ? meta.join(" · ") || `event ${entry.webhookId}` : `${entry.reason} (webhook-id ${entry.webhookId || "missing"})`}
      </p>
      {event && <Json label="Event" value={event} />}
    </li>
  );
}
