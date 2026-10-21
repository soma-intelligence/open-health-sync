// SPDX-License-Identifier: AGPL-3.0-only
//
// Step 3: read it back. Daily summaries, resting heart rate and the latest night from the REST
// API, then one record as REST JSON, FHIR R4 and Open mHealth side by side.

import type { Metadata } from "next";
import Link from "next/link";
import type { FHIRObservation, OMHDataPoint, Sample, SleepSession } from "@open-health-sync/server";
import { AutoRefresh } from "../../../../components/auto-refresh.tsx";
import { Columns, Line } from "../../../../components/charts.tsx";
import { Steps } from "../../../../components/steps.tsx";
import {
  Empty,
  ErrorNotice,
  Icon,
  Json,
  PageHead,
  Route,
  Routes,
  formatDateTime,
  formatDay,
  formatDuration,
  formatNumber,
  formatTime,
} from "../../../../components/ui.tsx";
import { attempt, tenantClient, type Result } from "../../../../lib/ohs.ts";
import { loadUser } from "../../../../lib/user.ts";
import { readWindow } from "../../../../lib/window.ts";

export const dynamic = "force-dynamic";
export const metadata: Metadata = { title: "Read it back" };

interface Props {
  params: Promise<{ id: string }>;
}

export default async function ReadBackPage({ params }: Props) {
  const { id } = await params;
  const user = await loadUser(id);
  const ohs = tenantClient();
  const { start, end } = readWindow();
  const [coverage, daily, resting, sleep] = await Promise.all([
    attempt(() => ohs.getCoverage(id, { start, end })),
    attempt(() => ohs.listDaily(id, { start, end, limit: 100 })),
    attempt(() => ohs.listSamples(id, { type: "resting_heart_rate", start, end, limit: 100 })),
    attempt(() => ohs.listSleep(id, { start, end, limit: 100 })),
  ]);

  const pending = coverage.ok ? coverage.value.pending_jobs : 0;
  const days = coverage.ok ? [...new Set(coverage.value.days.map((d) => d.date))].sort() : [];
  const summaries = daily.ok ? daily.value.data.filter((d) => d.step_count != null).slice(-14) : [];
  const samples = resting.ok ? resting.value.data.slice(-14) : [];
  const night = sleep.ok ? sleep.value.data.at(-1) : undefined;
  const latest = samples.at(-1);

  return (
    <main className="view">
      <Steps current={3} userId={id} />
      <PageHead
        step="Step 03 · Read it back"
        title="Read it back"
        sub={
          <>
            What the server holds for <strong>{user.external_ref ?? user.id}</strong> over the last 30 days, read with
            the tenant key. The same records are served as plain JSON, as FHIR R4 Observations and as Open mHealth data
            points.
          </>
        }
        routes={pending > 0 ? <AutoRefresh label="Processing, refreshing" /> : undefined}
      />

      {!coverage.ok && <ErrorNotice what="Reading coverage" error={coverage.error} />}
      {coverage.ok && (
        <div className="stats">
          <div className="stat">
            <span className="label">Pending jobs</span>
            <span className="numeral">{pending}</span>
            <span className="foot">{pending > 0 ? "uploads still being processed" : "everything uploaded is processed"}</span>
          </div>
          <div className="stat">
            <span className="label">Days with data</span>
            <span className="numeral">{days.length}</span>
            <span className="foot">{days.length > 0 ? `${formatDay(days[0] ?? "")} to ${formatDay(days.at(-1) ?? "")}` : "none yet"}</span>
          </div>
          <div className="stat">
            <span className="label">Rejected lines</span>
            <span className="numeral">{coverage.value.rejected_records}</span>
            <span className="foot">lines the server could not store</span>
          </div>
          <div className="stat">
            <span className="label">Sources</span>
            <span className="numeral word">{[...new Set(coverage.value.days.map((d) => d.provider))].join(", ") || "none"}</span>
            <span className="foot">
              <Route method="GET" path="/v1/users/{id}/coverage" />
            </span>
          </div>
        </div>
      )}

      {coverage.ok && days.length === 0 && pending === 0 ? (
        <section className="card">
          <Empty title="No data yet">
            Upload sample data or connect a source in <Link href={`/users/${id}`}>step 02</Link>, then come back.
          </Empty>
        </section>
      ) : (
        <>
          <section className="card">
            <div className="card-head">
              <h2>Daily summaries: steps</h2>
              <Route method="GET" path={`/v1/users/{id}/daily?start=${start}&end=${end}`} />
              <p className="hint">
                One row per local day. Without a summary from the source, the server derives the day&apos;s totals from
                the step samples.
              </p>
            </div>
            {!daily.ok ? (
              <div className="card-body">
                <ErrorNotice what="Reading daily summaries" error={daily.error} />
              </div>
            ) : summaries.length === 0 ? (
              <Empty title="No daily summary yet">{pending > 0 ? "The upload is being processed." : undefined}</Empty>
            ) : (
              <>
                <div className="card-body">
                  <Columns
                    label={`Steps per day, ${summaries[0]?.date} to ${summaries.at(-1)?.date}`}
                    points={summaries.map((d) => ({
                      day: d.date,
                      value: d.step_count ?? 0,
                      title: `${formatDay(d.date)}: ${formatNumber(d.step_count ?? 0)} steps`,
                    }))}
                  />
                </div>
                <div className="scroll">
                  <table>
                    <thead>
                      <tr>
                        <th>Day</th>
                        <th className="num">Steps</th>
                        <th>Source</th>
                      </tr>
                    </thead>
                    <tbody>
                      {summaries
                        .slice(-7)
                        .reverse()
                        .map((d) => (
                          <tr key={`${d.date}-${d.id}`}>
                            <td className="nowrap">{formatDay(d.date)}</td>
                            <td className="num">{formatNumber(d.step_count ?? 0)}</td>
                            <td>
                              <div className="cell-2">
                                <code>{d.sources?.step_count ?? d.provider}</code>
                                <span className="sub">{d.metadata.derived ? "derived from samples" : "summary from the source"}</span>
                              </div>
                            </td>
                          </tr>
                        ))}
                    </tbody>
                  </table>
                </div>
                <div className="card-body">
                  <Json label="Response, the latest summary" value={summaries.at(-1)} />
                </div>
              </>
            )}
          </section>

          <div className="grid-2">
            <section className="card">
              <div className="card-head">
                <h2>Resting heart rate</h2>
                <Route method="GET" path="/v1/users/{id}/samples?type=resting_heart_rate" />
              </div>
              <div className="card-body">
                {!resting.ok ? (
                  <ErrorNotice what="Reading samples" error={resting.error} />
                ) : samples.length === 0 ? (
                  <Empty title="No resting heart rate yet" />
                ) : (
                  <RestingHeartRate samples={samples} />
                )}
              </div>
            </section>
            <section className="card">
              <div className="card-head">
                <h2>Latest sleep</h2>
                <Route method="GET" path="/v1/users/{id}/sleep" />
              </div>
              <div className="card-body">
                {!sleep.ok ? (
                  <ErrorNotice what="Reading sleep" error={sleep.error} />
                ) : !night ? (
                  <Empty title="No night yet" />
                ) : (
                  <Night night={night} />
                )}
              </div>
            </section>
          </div>

          {latest && <Formats userId={id} sample={latest} />}
        </>
      )}

      <div className="button-row" style={{ justifyContent: "space-between" }}>
        <Link className="button" href={`/users/${id}`}>
          Back to get data in
        </Link>
        <Link className="button primary" href="/webhooks">
          Next: receive webhooks <Icon name="arrow" />
        </Link>
      </div>
    </main>
  );
}

function RestingHeartRate({ samples }: { samples: Sample[] }) {
  const values = samples.map((s) => s.value);
  const mean = values.reduce((a, b) => a + b, 0) / values.length;
  return (
    <>
      <div className="chart-head">
        <span className="numeral">
          {samples.at(-1)?.value}
          <span className="unit">bpm, latest</span>
        </span>
        <span className="muted small">
          {Math.round(mean)} bpm on average over {samples.length} days
        </span>
      </div>
      <Line
        label="Resting heart rate per day"
        unit="bpm"
        height={150}
        points={samples.map((s) => ({
          day: s.start_date.slice(0, 10),
          value: s.value,
          title: `${formatDay(s.start_date.slice(0, 10))}: ${s.value} ${s.unit ?? "bpm"}`,
        }))}
      />
    </>
  );
}

function Night({ night }: { night: SleepSession }) {
  const stages = [
    { key: "deep", name: "Deep", seconds: night.deep_sleep_seconds ?? 0 },
    { key: "light", name: "Light", seconds: night.light_sleep_seconds ?? 0 },
    { key: "rem", name: "REM", seconds: night.rem_sleep_seconds ?? 0 },
    { key: "awake", name: "Awake", seconds: night.awake_seconds ?? 0 },
  ];
  const total = stages.reduce((sum, s) => sum + s.seconds, 0);
  return (
    <>
      <div className="chart-head">
        <span className="numeral word">{night.total_sleep_seconds ? formatDuration(night.total_sleep_seconds) : "unknown"} asleep</span>
        {night.metadata.synthetic === true && <span className="tag synthetic">Synthetic</span>}
      </div>
      <p className="muted small">
        In bed {formatDateTime(night.bedtime_start)}
        {night.bedtime_end ? ` to ${formatTime(night.bedtime_end)}` : ""}, from <code>{night.provider}</code>
      </p>
      {total > 0 && (
        <>
          <div className="stagebar" role="img" aria-label="Time in each sleep stage">
            {stages.map((s) =>
              s.seconds > 0 ? (
                <span
                  key={s.key}
                  style={{ flexGrow: s.seconds, ["--swatch" as string]: `var(--stage-${s.key})` }}
                  title={`${s.name}: ${formatDuration(s.seconds)}`}
                />
              ) : null,
            )}
          </div>
          <ul className="legend">
            {stages.map((s) => (
              <li key={s.key} style={{ ["--swatch" as string]: `var(--stage-${s.key})` }}>
                <span className="key">{s.name}</span>
                <span className="value">
                  {formatDuration(s.seconds)} <span className="dim">{Math.round((100 * s.seconds) / total)}%</span>
                </span>
              </li>
            ))}
          </ul>
        </>
      )}
      <Json label="Response, this night" value={night} />
    </>
  );
}

/** The latest resting heart rate as each output format serves it. */
async function Formats({ userId, sample }: { userId: string; sample: Sample }) {
  const ohs = tenantClient();
  const observationId = `qs-${sample.id}`;
  const at = sample.start_date;
  const after = new Date(Date.parse(at) + 1000).toISOString();
  const [fhir, omh]: [Result<FHIRObservation>, Result<OMHDataPoint | undefined>] = await Promise.all([
    attempt(() => ohs.readFHIRObservation(userId, observationId)),
    attempt(async () => (await ohs.listOMHDataPoints(userId, { schema: "omh:heart-rate", start: at, end: after, limit: 1 })).data[0]),
  ]);
  const coding = fhir.ok ? fhir.value.code.coding?.filter((c) => c.system === "http://loinc.org") : [];
  return (
    <section className="card">
      <div className="card-head">
        <h2>One record, three formats</h2>
        <span className="tag accent">Same data</span>
        <p className="hint">
          The latest resting heart rate as the REST API, FHIR R4 and Open mHealth serve it: one storage, three standard
          shapes, no mapping code in your app.
        </p>
      </div>
      <div className="grid-3">
        <div>
          <p className="subhead">REST JSON</p>
          <Route method="GET" path="/v1/users/{id}/samples" />
          <dl className="facts compact" style={{ marginTop: 14 }}>
            <dt>Type</dt>
            <dd>
              <code>{sample.type}</code>
            </dd>
            <dt>Value</dt>
            <dd>
              {sample.value} {sample.unit}
            </dd>
            <dt>Provenance</dt>
            <dd>
              <code>{sample.provider}</code>, {sample.source_name}
            </dd>
          </dl>
          <Json label="Sample" value={sample} />
          <p className="small" style={{ marginTop: 10 }}>
            <a href={`/users/${userId}/raw/samples`} target="_blank" rel="noreferrer">
              Open the full list <Icon name="external" />
            </a>
          </p>
        </div>
        <div>
          <p className="subhead">FHIR R4</p>
          <Route method="GET" path={`/v1/users/{id}/fhir/Observation/${observationId}`} />
          {!fhir.ok ? (
            <ErrorNotice what="Reading the Observation" error={fhir.error} />
          ) : (
            <>
              <dl className="facts compact" style={{ marginTop: 14 }}>
                <dt>Resource</dt>
                <dd>
                  <code>
                    {fhir.value.resourceType}/{fhir.value.id}
                  </code>
                </dd>
                <dt>Code</dt>
                <dd>
                  {coding?.map((c) => (
                    <code key={c.code}>LOINC {c.code} </code>
                  ))}
                </dd>
                <dt>Value</dt>
                <dd>
                  {fhir.value.valueQuantity?.value} {fhir.value.valueQuantity?.code}{" "}
                  <span className="dim">(UCUM)</span>
                </dd>
              </dl>
              <Json label="Observation" value={fhir.value} />
            </>
          )}
          <p className="small" style={{ marginTop: 10 }}>
            <a href={`/users/${userId}/raw/fhir`} target="_blank" rel="noreferrer">
              Open the searchset Bundle <Icon name="external" />
            </a>
          </p>
        </div>
        <div>
          <p className="subhead">Open mHealth</p>
          <Route method="GET" path="/v1/users/{id}/omh/data-points?schema=omh:heart-rate" />
          {!omh.ok ? (
            <ErrorNotice what="Reading the data point" error={omh.error} />
          ) : !omh.value ? (
            <p className="muted small" style={{ marginTop: 14 }}>
              No data point for this record.
            </p>
          ) : (
            <>
              <dl className="facts compact" style={{ marginTop: 14 }}>
                <dt>Schema</dt>
                <dd>
                  <code>
                    {omh.value.header.schema_id.namespace}:{omh.value.header.schema_id.name}:{omh.value.header.schema_id.version}
                  </code>
                </dd>
                <dt>Id</dt>
                <dd>
                  <code>{omh.value.header.id}</code>
                </dd>
                <dt>Context</dt>
                <dd>{String(omh.value.body.temporal_relationship_to_physical_activity ?? "none")}</dd>
              </dl>
              <Json label="Data point" value={omh.value} />
            </>
          )}
          <p className="small" style={{ marginTop: 10 }}>
            <a href={`/users/${userId}/raw/omh`} target="_blank" rel="noreferrer">
              Open the data points <Icon name="external" />
            </a>
          </p>
        </div>
      </div>
    </section>
  );
}
