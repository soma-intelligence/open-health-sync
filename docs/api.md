# API guide

[`api/openapi.yaml`](../api/openapi.yaml) is the source of truth: every route, parameter and
response shape. This guide explains how the pieces fit. Examples assume
`OHS=http://localhost:8080` and a tenant key in `KEY`.

A running server serves the description of its own version at `GET /openapi.yaml`, without
credentials and readable from any origin. Import `$OHS/openapi.yaml` by URL into Postman
(Import, Link), Insomnia or [editor.swagger.io](https://editor.swagger.io) (File, Import URL),
or render a local page with `npx @redocly/cli build-docs $OHS/openapi.yaml`. The server has no
documentation page of its own, so it never loads third party scripts next to health data.

## Credentials

Every call carries `Authorization: Bearer <secret>`.

| Secret | Looks like | Used by | Calls |
|---|---|---|---|
| Tenant API key | `ohs_sk_` + 43 characters | your server | tenant routes: users, tokens, connections, reads, time tags |
| User token | `ohs_ut_` + 43 characters | one end user's app | user routes: uploads (`upload`), reads over MCP and the user archive (`read`) |

The first key comes from `ohs admin tenant create --name <name>` (or `OHS_BOOTSTRAP_TENANT`,
which logs a key once on first start). Both secrets are stored as SHA-256 hashes: a lost one
cannot be shown again. The connection routes (`/v1/users/{id}/connections...`) take the tenant key. The `connect` scope
exists but no route uses it yet.
A tenant key on a user route, or a user token on a tenant route, gets 403.

A user token is minted for one active user with `POST /v1/users/{id}/tokens`:

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/tokens -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"scopes":["upload","connect"],"ttl_seconds":3600}'
# {"token":"ohs_ut_...","expires_at":"...","scopes":["upload","connect"]}
```

The lifetime is one hour by default and at most 24 hours. A token whose only scope is `read`
may live up to 30 days (2592000 seconds) so a person can put it in their own agent
configuration. The response is `Cache-Control: no-store`; the token is shown once.

## Conventions

- **JSON and time.** Bodies are JSON. Times are RFC 3339 in UTC. Values are in the storage
  unit of their quantity name, see [data-model.md](data-model.md).
- **Lists** answer `{"data": [...], "next_cursor": "..." | null}`, oldest first. Pass
  `next_cursor` back as `cursor` until it is null. `limit` defaults to 100 and is at most 1000;
  a larger value is a 400.
- **Time windows** (`start`, `end`) are half open: `start <= time < end`. Both take an RFC 3339
  time or a `YYYY-MM-DD` date (midnight UTC). `end` must not be before `start`.
- **Errors** are RFC 9457 `application/problem+json`:
  `{"type":"about:blank","title":"Bad Request","status":400,"detail":"..."}`.
  A user, workout or sleep session of another tenant is a 404, never a 403. A 401 carries
  `WWW-Authenticate: Bearer`.
- **Request ids.** Every response carries `X-Request-Id`, and a problem body repeats it as
  `request_id`. Send your own `X-Request-Id` (1 to 128 letters, digits, `-`, `_`, `.`) to have
  it kept, for example your proxy's. Every log line about the call carries it as `request_id`,
  so quote it when you ask the operator about a failed call.
- **Trace context.** When the operator has turned tracing on, a W3C `traceparent` header is
  honoured: the instance's span for the call joins your trace. Without tracing it is ignored,
  and nothing is added to the response either way. See [observability.md](observability.md).
- **Idempotency.** Send `Idempotency-Key` (1 to 255 characters) on `POST /v1/users` or
  `POST /v1/users/{id}/tags`. A retry with the same key and request within
  24 hours gets the first response back with `Idempotent-Replayed: true`. The same key with a
  different request is a 422; a retry while the first call still runs is a 409. Other routes
  ignore the header (token minting would have to store the token, and each connect call mints a
  fresh state); uploads are idempotent by their own headers.

## Users

```sh
curl -s -X POST $OHS/v1/users -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"external_ref":"customer-42"}'
```

`external_ref` is your own identifier, unique per tenant (a second user with the same one is a
409); it may be omitted. `GET /v1/users` pages through the tenant's users, `GET /v1/users/{id}`
returns one.

`DELETE /v1/users/{id}` answers 202 with a `job_id`. The user's status becomes `deleting` at
once: reads return 404 and the user's tokens stop working, so uploads get 401 (410 only for an
upload whose token was accepted just before the deletion started). A stored `Idempotency-Key`
response that created the user is deleted with them. A background job then revokes provider connections (best
effort), deletes the user's raw files and every row, then the user.

`GET /v1/deletions/{job_id}` reads that job with the same tenant key (another tenant's job is a
404). Poll it until `status` is `done`:

```json
{
  "job_id": "5f0c2b8e-8d1a-4c3e-9a57-0b6f1d2e3a4b",
  "user_id": "0d9e7c1a-2b3f-4e5d-8a6b-7c8d9e0f1a2b",
  "status": "done",
  "created_at": "2026-03-01T14:31:02Z",
  "finished_at": "2026-03-01T14:31:06Z"
}
```

`status` is `pending`, `running`, `done` or `failed`; `finished_at` is null until the job ends.
`provider` is null for a user deletion and names the provider for the erasure of one provider's
data (below).

### Erasing one provider's data

Some providers' terms ask that a user's data from them be erased when the user disconnects them.
Two routes do it, each answering 202 with a `job_id` that `GET /v1/deletions/{job_id}` reads
like a user deletion:

- `DELETE /v1/users/{id}/connections/{provider}?delete_data=true` disconnects the provider as a
  plain disconnect does, then erases its data. Without `delete_data` (or with `false`) the
  answer is still 204 and the data already collected is kept, as before.
- `DELETE /v1/users/{id}/data/{provider}` erases a provider's data without touching any
  connection: for device providers (`apple_health`, `health_connect`), which have no
  connection, and for a provider already disconnected. A provider the user is still connected
  to is a 409 (disconnect it with `delete_data=true`); a provider unknown to this build is a 404.

The job deletes everything of that user and provider: the normalized records (samples, sleep
sessions and phases, workouts with their routes and track summaries, nutrition, ECG recordings
and voltages, heartbeat series, daily summaries, observations), the provider's tombstones, the
raw files and their rows, failed records, ETL and pull jobs, pull coverage, the sync log, push
subscriptions, unused consent states and the disconnected connection itself. Daily summaries are
kept per provider, so the other providers' summaries need no recompute. The user, their other
providers and their [order of sources](#several-sources-of-one-metric-the-merged-view) stay. Asking again while the job runs returns the same job.

Until the job is done the provider cannot be connected again (409) and its uploads for the user
are refused (409, nothing kept); afterwards new data is accepted as usual. Re-processing old raw
objects cannot bring the data back, since the raw files and their registry rows are gone, and a
pull still running when the job is due delays it until the pull ends. The `data.erased`
[webhook](webhooks.md) announces the end.

## Connecting a provider

`GET /v1/providers` lists the connectors enabled on this instance and how they work:
`oauth` (the server pulls), `push` (the provider notifies), `device` (an SDK uploads).

For an OAuth provider, your server asks for a consent URL and sends the user there:

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/withings -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"redirect_uri":"https://app.example.org/connected"}'
# {"authorize_url":"https://account.withings.com/oauth2_user/authorize2?..."}
```

After consent the provider calls `GET /oauth/{provider}/callback` on Open Health Sync, which
sends the user to `redirect_uri?status=connected&provider=withings` (`status=error` on failure).
Without a `redirect_uri` the user sees a small built-in result page. `redirect_uri` must be an
absolute https URL (http is allowed for localhost). The `state` of a consent link is single use
and expires after 10 minutes.

To skip building this UI, create a connect session with `POST /v1/users/{id}/connect-sessions`
and send the user to the link it returns: Open Health Sync serves a page that lists the providers
with the user's status and starts the same consent flow. See
[hosted connection page](connect-page.md).

`GET /v1/users/{id}/connections` shows each connection's `status` (`active`, `needs_reauth`,
`disconnected`), `last_sync_at`, `initial_backfill_at` and `health` (whether it syncs, why not,
and whether the user must `reconnect` or you only `wait`). A connection in `needs_reauth` needs
the user to go through consent again. `GET /v1/users/{id}/sync-runs` is the history of its pulls,
with the provider's error when one failed, and `POST /v1/users/{id}/connections/{provider}/sync`
pulls it now. See [sync history and connection health](sync-health.md). `DELETE /v1/users/{id}/connections/{provider}` revokes the
grant (best effort) and stops pulling; data already collected is kept, unless you add
`?delete_data=true`, which also erases it (see [erasing one provider's data](#erasing-one-providers-data)).

`GET /v1/users/{id}/freshness` says how up to date each source of the user is, Apple Health
uploads included: `synced_through`, `lag_seconds`, the last provider notification, the next
pull, the last error, and a `state` (`fresh`, `stale`, `failing`, `syncing`, `needs_reauth`,
`disconnected`). Show it in your app, or alert on it. See [background sync](background-sync.md).

## Uploading device data

`POST /v1/ingest/apple-health` takes one gzip NDJSON part with a user token that has the `upload`
scope. The contract (headers, size, idempotency) is in
[connectors/apple-health.md](connectors/apple-health.md). `POST /v1/ingest/health-connect` takes
Android Health Connect records the same way; its line format is in
[connectors/health-connect.md](connectors/health-connect.md). Both routes share the limit of
uploads in flight.

## Reading data

All reads are `GET /v1/users/{id}/<resource>` with a tenant key, paged as above.

| Resource | Filters | Ordered by |
|---|---|---|
| `samples` | `type` (required), `start`, `end`, `view` | `start_date` |
| `category-samples` | `type` (optional), `start`, `end`, `view` | `start_date` |
| `sleep` | `start`, `end`, `view` | `bedtime_start` |
| `sleep/{session_id}/phases` | `start`, `end` | `start_date` |
| `workouts` | `type` (activity, optional), `start`, `end`, `view` | `start_date` |
| `workouts/{workout_id}/route` | `start`, `end` | `timestamp` |
| `nutrition` | `start`, `end` | `start_date` |
| `daily` | `start`, `end` (compared as dates), `view` | `date` |
| `rr-intervals` | `series_id`, `start`, `end` | `timestamp` (the beat) |
| `hrv` | `series_id`, `start`, `end` (the series' first beat) | the series' first beat |
| `menstrual-cycles` | `start`, `end` (compared as dates, on the cycle's start) | `start_date` |
| `coverage` | `provider`, `start`, `end` | not paged |

`view` is `merged` (the default) or `all`; see
[the merged view](#several-sources-of-one-metric-the-merged-view).

`samples` answers 400 when `type` is not a quantity type of the catalog (a typo such as
`heartrate`), so a wrong name is not mistaken for missing data. The names are listed in
[data-model.md](data-model.md) and by the MCP tool `list_quantity_types`.

```sh
curl -s "$OHS/v1/users/$USER_ID/samples?type=heart_rate&start=2026-03-01&end=2026-03-02&limit=500" \
  -H "Authorization: Bearer $KEY"
```

```json
{
  "data": [
    {
      "id": 1,
      "type": "heart_rate",
      "value": 72.5,
      "unit": "bpm",
      "start_date": "2026-03-01T14:30:00Z",
      "end_date": "2026-03-01T14:30:00Z",
      "source_name": "Apple Watch",
      "source_bundle_id": "com.apple.health.ABC",
      "device_model": "Watch",
      "product_type": "Watch7,1",
      "external_id": "",
      "is_primary_source": false,
      "was_user_entered": false,
      "metadata": {
        "device": "Apple Watch",
        "timezone": "Europe/Paris",
        "HKTimeZone": "Europe/Paris",
        "sourceName": "Apple Watch",
        "original_unit": "count/min",
        "original_value": 72.5,
        "sourceBundleId": "com.apple.health.ABC"
      },
      "provider": "apple_health",
      "source": "apple_health",
      "device": "Apple Watch",
      "ingested_at": "2026-03-01T14:31:02.418Z"
    }
  ],
  "next_cursor": null
}
```

This is the heart rate line of [connectors/apple-health.md](connectors/apple-health.md#line-formats)
read back. `metadata` keeps the provider fields as sent, plus `timezone` and the value and unit
before conversion.

Every record carries its provenance: `provider` (the integration it came through), `source` (the
vendor that produced it, when known), `device` and `ingested_at`. The quantity names and their
units are listed in [data-model.md](data-model.md). The ETL derives step, energy and distance
totals per provider and local day from samples when a provider sends no daily summary of its
own. `rr-intervals` serves the beat-to-beat intervals of heartbeat series and `hrv` the HRV
derived from each series; the rules are in [hrv.md](hrv.md).

### Several sources of one metric: the merged view

A user often has several sources for one metric: a ring and a watch both record the night, a
phone and a watch both count steps, a sample arrives directly and again through an aggregator.
Every source's records are stored as received. The reads (`samples`, `category-samples`,
`sleep`, `workouts`, `daily` and the FHIR search) default to `view=merged`, which lists a record
only when no better ranked source has a matching record, so a sum or an average counts nothing
twice. `view=all` lists every stored record.

Two records of one metric match when they are close in time, judged per kind of metric:

| Kind | Example metrics | Match when |
|---|---|---|
| counts | `step_count`, `active_energy_burned`, distances | the intervals overlap (a day total overlaps the whole day) |
| readings | `heart_rate`, `oxygen_saturation`, `respiratory_rate`, blood pressure | within 5 minutes |
| nightly or daily figures | `resting_heart_rate`, `hrv_rmssd`, `hrv_sdnn`, scores | within 12 hours |
| body measurements | `body_mass`, `body_fat_percentage`, `body_temperature` | within an hour |
| sessions | sleep sessions, workouts | they share at least half of the shorter one (a nap beside the night stays) |
| anything else | | the intervals overlap |

Sources are ranked by their `source` (the vendor; the provider when unknown). Inside one source,
a stream worn on the body (a watch) ranks before the others (a phone); a remaining tie goes to
the provider name, so a sample seen twice is listed once. The order of sources is resolved per
metric, most specific first: the user's order for the metric, for its group (`activity`,
`heart`, `recovery`, `body`, `sleep`, `workouts`), for every metric (`*`), then the built-in
order of the group. A source missing from an order ranks after every listed one.

```bash
curl -s -X PUT "$OHS/v1/users/$USER_ID/source-priorities" -H "Authorization: Bearer $OHS_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"priorities": {"sleep": ["oura", "whoop"], "step_count": ["apple_health"]}}'
```

The answer, like `GET .../source-priorities`, holds the user's `priorities` and the built-in
`defaults`. `{"priorities": {}}` goes back to the defaults.

In the merged view `daily` holds one summary per local day. Each figure comes from the best
ranked source that has it, and `sources` names that source per figure:

```json
{"date": "2026-03-02", "step_count": 10000, "readiness_score": 80, "resting_hr": 50,
 "sources": {"step_count": "apple_health", "readiness_score": "oura", "resting_hr": "whoop"}, ...}
```

`id`, the provenance and `metadata` are those of the day's best ranked row. With `view=all`,
`daily` holds one row per provider and day.

`menstrual-cycles` is derived at read time from the `menstrual_flow` category samples of every
provider: each cycle's start day, its period's flow days (the heaviest flow of each local day,
with the providers that recorded it) and, once the next period is recorded, its length. The
latest cycle is open, with `end_date` and `cycle_length_days` null. Nothing is predicted. The
rules (local days, gaps inside a period, the read limit) are in
[menstrual-cycles.md](menstrual-cycles.md).

### Knowing when an upload is processed

Processing is asynchronous. `GET /v1/users/{id}/coverage` returns record counts per provider and
local day, `pending_jobs` (work not finished yet) and `parked_jobs` (work that exhausted its
retries and needs an operator). After an upload, poll until `pending_jobs` is 0. A server that
would rather not poll can subscribe to the `data.written` [webhook](webhooks.md). Coverage is not
paged: at most 10000 days, the earliest, with `truncated: true` when more matched.

A finished job can still have dropped some lines: a value out of range, a missing `startDate`,
a unit that does not convert. `rejected_records` counts the user's lines in that state (all
providers and days, not narrowed by the filters), each line once even if the object was
processed again; an object that failed as a whole, with no line singled out, counts as one. A
line the server later recovers, after a fix, stops counting. A non-zero value after `pending_jobs` reaches 0 means some uploaded lines were
not stored; an operator finds each line and its reason in `ohs_data.failed_records`.

## Time tags

A time tag gives context to a user's data: an interval with a `kind` from a fixed vocabulary
(`illness`, `injury`, `travel`, `jet_lag`, `vaccination`, `medication`, `alcohol`, `other`), an
optional `label`, `note` and IANA `timezone`, and an `end_at` that stays null while the tag is
ongoing. `POST /v1/users/{id}/tags` records one, `PATCH` changes it (for example
`{"end_at":"2026-03-05"}` to close it), `DELETE` removes it. `GET /v1/users/{id}/tags` lists them
by `start_at`; `start` and `end` keep the tags that overlap the window, ongoing tags included, and
`kind` keeps one kind. The server computes nothing from them. Vocabulary, overlap rules and
examples: [time-tags.md](time-tags.md).

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/tags -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"kind":"illness","label":"Flu","start_at":"2026-03-02"}'
curl -s "$OHS/v1/users/$USER_ID/tags?start=2026-03-01&end=2026-04-01" -H "Authorization: Bearer $KEY"
```

## FHIR R4

`GET /v1/users/{id}/fhir/Observation` serves the same quantity samples, blood pressure readings
(a systolic and a diastolic sample paired into one panel), sleep sessions, workouts and daily
summaries as FHIR R4 Observations, in one `searchset` Bundle (`application/fhir+json`) in time
order, paged by its `next` link, and `GET /v1/users/{id}/fhir/Observation/{observation_id}` reads
one back. Filters: `type`, `code` (`http://loinc.org|8867-4`), `category` (`vital-signs`,
`laboratory`, `activity`), `start`, `end`. The user is `GET /v1/users/{id}/fhir/Patient/{id}`,
the devices the records came from `/fhir/Device`, who produced an Observation
`/fhir/Provenance?target=Observation/{observation_id}`, and
`/fhir/Patient/{id}/$everything` pages through all of them in one Bundle. The mapping, the
profiles met and the limits are in [fhir.md](fhir.md).

```sh
curl -s "$OHS/v1/users/$USER_ID/fhir/Observation?type=heart_rate&start=2026-03-01&limit=50" \
  -H "Authorization: Bearer $KEY"
```

## Open mHealth

`GET /v1/users/{id}/omh/data-points` serves heart rate, steps, blood pressure, body
measurements, vital signs, blood glucose, active energy, sleep sessions and workouts as
[Open mHealth](https://www.openmhealth.org/) data points (a `header` and a `body` that follows one
schema of the catalog, checked against the official schemas in CI), in the order of the FHIR
search, paged like every list. Filters: `schema` (`omh:heart-rate`), `start`, `end`. The mapping
and its limits are in [open-mhealth.md](open-mhealth.md).

```sh
curl -s "$OHS/v1/users/$USER_ID/omh/data-points?schema=omh:blood-pressure&start=2026-03-01" \
  -H "Authorization: Bearer $KEY"
```

## Exports

`GET /v1/users/{id}/exports/{dataset}` streams every record of one list (`samples`,
`category-samples`, `sleep`, `workouts`, `nutrition` or `daily`) in `[start, end)` as one CSV
(`format=csv`, the default) or Parquet (`format=parquet`) file, with the columns of the list's
JSON records. Not paged. See [exports.md](exports.md).

```sh
curl -sOJ "$OHS/v1/users/$USER_ID/exports/samples?format=parquet&start=2026-01-01" \
  -H "Authorization: Bearer $KEY"
```

`GET /v1/users/{id}/archive` streams everything the instance holds about one user as one zip
file, to answer a data portability request: the user and its connections (never a token),
every record of every list as NDJSON, a FHIR R4 `collection` Bundle, the Open mHealth data
points and every raw payload byte for byte, with a `manifest.json` that counts them. It takes a
tenant key, or a user token with scope `read` for its own user (another user is a 404). See
[exports.md](exports.md#complete-user-archive-data-portability).

```sh
curl -sOJ "$OHS/v1/users/$USER_ID/archive" -H "Authorization: Bearer $KEY"
```

## Webhooks

Rather than polling coverage, register an endpoint with `POST /v1/webhooks` and be told when a
sync writes or deletes records, a user is erased, a connection needs the user to sign in
again or a time tag changes. Requests are signed per Standard Webhooks and retried for about 28 hours. Events,
signature verification and the routes: [webhooks.md](webhooks.md).

## Audit log

Every request authenticated with a tenant key or a user token, every key command of
`ohs admin` and every completed erasure is recorded in an append-only audit log: the credential,
the action, the user it concerns, the route pattern and the status, never a value read or
written. Read it with `GET /v1/audit` (tenant key; filters `user_id`, `action`, `since`,
`until`). Details: [audit-log.md](audit-log.md).

## Consent

Per user, per source and per data category: `PUT /v1/users/{id}/consents/{provider}` with
`{"granted":["heart","sleep"],"evidence":"..."}` sets the categories the user allows from that
source (the others are revoked), `DELETE` revokes them all, `GET /v1/users/{id}/consents` and
`GET /v1/users/{id}/consents/history` read the state and every change. Records outside the
grant are not stored, and a revoked category's stored records are deleted. Details:
[consent.md](consent.md).

## MCP

`POST /mcp` speaks the Model Context Protocol for AI agents, read only. See [mcp.md](mcp.md).

## Provider-facing and operational routes

| Route | Purpose |
|---|---|
| `GET /connect`, `GET /connect/{provider}`, `POST /connect/{provider}` | The [hosted connection page](connect-page.md), for the user's browser |
| `GET /oauth/{provider}/callback` | OAuth redirect target you register with the provider |
| `POST /inbound/{provider}` | Provider push notifications, verified with the provider's own scheme |
| `GET /inbound/{provider}` | Notification URL check and subscription challenge |
| `GET /healthz` | Liveness |
| `GET /readyz` | Readiness: database ping and raw store check, 503 when either fails |
| `GET /metrics` | Prometheus text format; absent when `OHS_METRICS=false` |
| `GET /console` | Read-only admin console (HTML), only when `OHS_CONSOLE=true`; see [console.md](console.md) |

The OAuth callback and the notification routes take no credential and are rate limited per
client address (and the notification routes per provider): over a limit they answer 429 with a
problem body and `Retry-After` in seconds, before any work. Limits and the reverse proxy
setting are in [self-hosting.md](self-hosting.md#rate-limits).

`/metrics` is global to the instance (not per tenant). It asks for the `OHS_METRICS_TOKEN` bearer
token, refuses every scrape when no token is set, and can move to a private address with
`OHS_METRICS_LISTEN`; see [self-hosting.md](self-hosting.md#metrics).
