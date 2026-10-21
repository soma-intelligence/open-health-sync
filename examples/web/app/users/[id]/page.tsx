// SPDX-License-Identifier: AGPL-3.0-only
//
// Step 2: get data in. Either the user connects a cloud source on the hosted connection page,
// or this server uploads synthetic data through the route a phone app uses.

import type { Metadata } from "next";
import Link from "next/link";
import { ActionForm } from "../../../components/action-form.tsx";
import { Steps } from "../../../components/steps.tsx";
import {
  ErrorNotice,
  Icon,
  Notice,
  PageHead,
  Route,
  Routes,
  formatDateTime,
  formatDay,
  formatNumber,
  formatTime,
} from "../../../components/ui.tsx";
import { requireConfig } from "../../../lib/config.ts";
import { attempt, tenantClient } from "../../../lib/ohs.ts";
import { SAMPLE_DAYS, sampleOptions } from "../../../lib/upload.ts";
import { RESTING_HEART_RATE, batchId, dayPlans, syntheticParts } from "../../../lib/synthetic.ts";
import { loadUser } from "../../../lib/user.ts";
import { connectSource, uploadSample } from "../../actions.ts";

export const dynamic = "force-dynamic";
export const metadata: Metadata = { title: "Get data in" };

interface Props {
  params: Promise<{ id: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}

export default async function GetDataInPage({ params, searchParams }: Props) {
  const { id } = await params;
  const query = await searchParams;
  const user = await loadUser(id);
  const ohs = tenantClient();
  const [providers, connections] = await Promise.all([
    attempt(() => ohs.listProviders()),
    attempt(() => ohs.listConnections(id)),
  ]);
  const { appUrl } = requireConfig();
  const oauth = providers.ok ? providers.value.data.filter((p) => p.kinds.includes("oauth")) : [];

  const options = sampleOptions();
  const plans = dayPlans(options);
  const parts = syntheticParts(options);
  const samplePart = parts.find((part) => part.type === RESTING_HEART_RATE);

  // The hosted page sends the user back here with ?status=connected|error&provider=<slug>.
  const status = typeof query.status === "string" ? query.status : undefined;
  const provider = typeof query.provider === "string" ? query.provider : "the source";

  return (
    <main className="view">
      <Steps current={2} userId={id} />
      <PageHead
        step="Step 02 · Get data in"
        title={user.external_ref ?? "User"}
        sub={
          <>
            User <code>{user.id}</code>, created {formatDateTime(user.created_at)}. Data comes in two ways: the
            person connects a cloud source on the hosted page, or a device uploads what it recorded.
          </>
        }
        routes={
          <Routes>
            <Route method="GET" path="/v1/users/{id}" />
          </Routes>
        }
      />

      {status === "connected" && (
        <Notice kind="ok">
          <strong>{provider} is connected.</strong> The first sync of its history is queued; read it back in step 03 once it
          has run.
        </Notice>
      )}
      {status === "error" && (
        <Notice kind="bad">
          <strong>Connecting {provider} did not finish.</strong> The person declined, or the provider refused the
          request. Nothing was stored; open the connection page again to retry.
        </Notice>
      )}

      <div className="grid-2">
        <section className="card">
          <div className="card-head">
            <div className="title">
              <h2>Connect a source</h2>
              <span className="tag">Hosted page</span>
            </div>
            <p className="hint">For cloud sources such as Withings or Oura, which the server then syncs on its own.</p>
          </div>
          <div className="card-body">
            <div className="prose">
              <p>
                This server asks Open Health Sync for a connection link and sends the browser there. The page lists the
                sources, runs the provider&apos;s consent, then sends the person back to this app.
              </p>
            </div>
            <dl className="facts compact" style={{ margin: "14px 0 16px" }}>
              <dt>Sources</dt>
              <dd>
                {!providers.ok ? (
                  <span className="dim">unknown</span>
                ) : oauth.length === 0 ? (
                  <span className="dim">none enabled on this instance</span>
                ) : (
                  <span className="chips">
                    {oauth.map((p) => (
                      <span key={p.slug} className="tag">
                        {p.name ?? p.slug}
                      </span>
                    ))}
                  </span>
                )}
              </dd>
              <dt>Comes back to</dt>
              <dd>
                <code>
                  {appUrl}/users/{id}
                </code>
              </dd>
              <dt>Link lifetime</dt>
              <dd>15 minutes, for this person only</dd>
            </dl>
            {providers.ok && oauth.length === 0 && (
              <Notice kind="warn">
                No cloud source is enabled on this instance. An instance admin turns one on in the admin console or with{" "}
                <code>OHS_&lt;PROVIDER&gt;_CLIENT_ID</code> and <code>_CLIENT_SECRET</code>. Until then, upload sample
                data instead.
              </Notice>
            )}
            {!providers.ok && <ErrorNotice what="Listing providers" error={providers.error} />}
            <ActionForm action={connectSource.bind(null, id)} submit="Open the connection page" pending="Creating the link..." />
          </div>
          {connections.ok && connections.value.data.length > 0 && (
            <div className="scroll">
              <table>
                <thead>
                  <tr>
                    <th>Connected source</th>
                    <th>Status</th>
                    <th>Last sync</th>
                  </tr>
                </thead>
                <tbody>
                  {connections.value.data.map((c) => (
                    <tr key={c.provider}>
                      <td>{c.provider}</td>
                      <td>
                        <span className={`pill ${c.status === "active" ? "ok" : "warn"}`}>{c.status}</span>
                      </td>
                      <td className="dim nowrap">{c.last_sync_at ? formatDateTime(c.last_sync_at) : "not yet"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div className="card-foot">
            <Routes>
              <Route method="GET" path="/v1/providers" />
              <Route method="POST" path="/v1/users/{id}/connect-sessions" />
              <Route method="GET" path="/v1/users/{id}/connections" />
            </Routes>
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <div className="title">
              <h2>Upload sample data</h2>
              <span className="tag synthetic">Synthetic</span>
            </div>
            <p className="hint">No wearable needed. The data is made up in code; it is not a real person&apos;s.</p>
          </div>
          <div className="card-body">
            <div className="prose">
              <p>
                With the tenant key, this server mints a token for this user that can only upload and lives 5 minutes.
                With that token alone it sends {SAMPLE_DAYS} days of steps, resting heart rate and sleep through the
                route a phone app uses: {parts.length} gzip NDJSON parts, one type and one day each.
              </p>
              <p className="small muted">
                Every line says <code>&quot;synthetic&quot;: true</code> in its metadata, and the server keeps it. Uploading
                again sends the same bytes, and the server answers <code>duplicate</code> for each part.
              </p>
            </div>
            <div style={{ marginTop: 16 }}>
              <ActionForm
                action={uploadSample.bind(null, id)}
                submit={`Upload ${SAMPLE_DAYS} days of sample data`}
                pending={`Uploading ${parts.length} parts...`}
                primary
              />
            </div>
            <details className="json">
              <summary>
                What is sent
                <span className="meta">
                  {plans[0]?.day} to {plans.at(-1)?.day}
                </span>
              </summary>
              <div className="scroll">
                <table>
                  <thead>
                    <tr>
                      <th>Day</th>
                      <th className="num">Steps</th>
                      <th className="num">Resting HR</th>
                      <th>Night</th>
                    </tr>
                  </thead>
                  <tbody>
                    {plans.map((plan) => (
                      <tr key={plan.day}>
                        <td className="nowrap">{formatDay(plan.day)}</td>
                        <td className="num">{formatNumber(plan.steps)}</td>
                        <td className="num">{plan.restingHeartRate} bpm</td>
                        <td className="nowrap dim">
                          {formatTime(plan.bedtime)} to {formatTime(plan.wake)}, {Math.floor(plan.asleepMinutes / 60)} h{" "}
                          {String(plan.asleepMinutes % 60).padStart(2, "0")} asleep
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </details>
            {samplePart && (
              <details className="json">
                <summary>One part, as sent</summary>
                <pre>
                  {[
                    "POST /v1/ingest/apple-health",
                    "Authorization: Bearer <the upload-only user token>",
                    "Content-Type: application/gzip",
                    `X-OHS-Type: ${samplePart.type}`,
                    `X-OHS-Day: ${samplePart.day}`,
                    `X-OHS-Batch: ${batchId(options)}`,
                    `X-OHS-Part: ${samplePart.part}`,
                    "",
                    `gzip of ${samplePart.lines.length} NDJSON line${samplePart.lines.length === 1 ? "" : "s"}, the first:`,
                    JSON.stringify(samplePart.lines[0], null, 2),
                  ].join("\n")}
                </pre>
              </details>
            )}
          </div>
          <div className="card-foot">
            <Routes>
              <Route method="POST" path="/v1/users/{id}/tokens" />
              <Route method="POST" path="/v1/ingest/apple-health" />
            </Routes>
          </div>
        </section>
      </div>

      <div className="button-row" style={{ justifyContent: "flex-end" }}>
        <Link className="button primary" href={`/users/${id}/data`}>
          Next: read it back <Icon name="arrow" />
        </Link>
      </div>
    </main>
  );
}
