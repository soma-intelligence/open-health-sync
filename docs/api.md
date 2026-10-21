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
| Tenant API key | `ohs_sk_` + 43 characters | your server | tenant routes: users, tokens, connections, reads, time tags, self reports, share links |
| User token | `ohs_ut_` + 43 characters | one end user's app | user routes: uploads and self reports (`upload`), reads over MCP and the user archive (`read`) |

The first key comes from `ohs admin tenant create --name <name>` (or `OHS_BOOTSTRAP_TENANT`,
which logs a key once on first start, or the browser [first-run setup](setup.md)). Both secrets are stored as SHA-256 hashes: a lost one
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
- **Idempotency.** Send `Idempotency-Key` (1 to 255 characters) on `POST /v1/users`,
  `POST /v1/users/{id}/tags` or `POST /v1/users/{id}/self-reports`. A retry with the same key and request within
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

`GET /v1/providers` lists the connectors enabled on this instance, with their display `name`
and how they work: `oauth` (the server pulls), `push` (the provider notifies), `device` (an SDK
uploads). `data_types` says what each one delivers: the record kinds it writes (`sample`,
`sleep`, `workout`...) and then the quantity names it can produce (`heart_rate`...), as listed in
[connectors/README.md](connectors/README.md#what-each-connector-delivers). An instance admin turns providers on and saves their keys from the
[console](console.md), or with the `OHS_<PROVIDER>_*` environment variables.

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
[connectors/health-connect.md](connectors/health-connect.md). `POST /v1/ingest/xiaomi` takes the
rows of a Mi Fitness data copy, as `ohs import mi-fitness` writes them
([connectors/xiaomi.md](connectors/xiaomi.md)). `POST /v1/ingest/activity-files` takes one FIT, GPX
or TCX activity file per part, base64 on one line, as `ohs import files` sends them
([connectors/activity-files.md](connectors/activity-files.md)). `POST /v1/ingest/bluetooth` takes
the values standard Bluetooth health devices send, one per line in hex with the time they arrived,
as `ohs-bridge` and the Swift and Kotlin SDKs send them; the server decodes them
([connectors/bluetooth.md](connectors/bluetooth.md)). The routes share the limit of
uploads in flight.

## Reading data

All reads are `GET /v1/users/{id}/<resource>` with a tenant key, paged as above.

| Resource | Filters | Ordered by |
|---|---|---|
| `samples` | `type` (required), `start`, `end`, `view` | `start_date` |
| `samples/stats` (0.2.0 and later) | `type`, `start`, `end` (required), `interval`, `tz`, `provider`, `view` | interval, not paged |
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
| `ecg` | `start`, `end` | `start_date`, metadata only ([ECG recordings](#ecg-recordings)) |
| `ecg/{ecg_id}` | none | one recording with its samples, not paged |
| `coverage` | `provider`, `start`, `end` | not paged |

`view` is `merged` (the default) or `all`; see
[the merged view](#several-sources-of-one-metric-the-merged-view).

`samples` answers 400 when `type` is not a quantity type of the catalog (a typo such as
`heartrate`), so a wrong name is not mistaken for missing data. The names are listed in
[data-model.md](data-model.md) and by the MCP tool `list_quantity_types`.

`samples/stats` gives count, sum, mean, min and max per `hour` or per local `day` (the default)
in the IANA zone `tz` (default UTC), one series per provider, source and unit. A phone and a
watch that both count steps stay two series: pick one, or the largest, rather than adding
them up. A year of daily heart rate is one call:

```sh
curl -s "$OHS/v1/users/$USER_ID/samples/stats?type=heart_rate&interval=day&tz=Europe/Paris&start=2026-01-01&end=2027-01-01" \
  -H "Authorization: Bearer $KEY"
```

```json
{
  "type": "heart_rate", "interval": "day", "tz": "Europe/Paris",
  "start": "2026-01-01T00:00:00+01:00", "end": "2027-01-01T00:00:00+01:00", "truncated": false,
  "buckets": [
    { "start": "2026-01-01T00:00:00+01:00", "provider": "apple_health", "source": "com.apple.health",
      "unit": "count/min", "count": 1412, "sum": 98840, "avg": 70, "min": 48, "max": 151 }
  ]
}
```

With TimescaleDB's continuous aggregates (the default image) it reads an hourly aggregate, so
long ranges stay fast; without them it computes the same answer from the samples. Data written
for a past hour, such as an imported history, shows in the statistics within about 5 minutes.

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

### ECG recordings

`GET /v1/users/{id}/ecg` lists the user's ECG recordings (Apple Watch through Apple Health,
Withings) by start time, metadata only. `GET /v1/users/{id}/ecg/{ecg_id}` returns one recording
with its voltage samples. Both take a tenant key, like every read.

```sh
curl -s "$OHS/v1/users/$USER_ID/ecg/12" -H "Authorization: Bearer $KEY"
```

```json
{
  "id": 12,
  "start_date": "2026-03-01T08:00:00Z",
  "end_date": "2026-03-01T08:00:30Z",
  "classification": "sinus_rhythm",
  "classification_value": 1,
  "symptoms_status": "none",
  "average_heart_rate": 64,
  "sampling_frequency_hz": 512,
  "number_of_measurements": 15360,
  "metadata": { "timezone": "Europe/Paris" },
  "provider": "apple_health",
  "source": "apple_health",
  "device": null,
  "ingested_at": "2026-03-01T09:15:30.25Z",
  "samples": {
    "count": 15360,
    "truncated": false,
    "offsets_s": [0, 0.001953125, 0.00390625],
    "voltages_uv": [12.5, 14.25, 20]
  }
}
```

The arrays are cut to three values here. `offsets_s` is the time of each sample from
`start_date`, in seconds; `voltages_uv` the voltage of the same sample, in microvolts, single
lead. Both are as stored, in offset order. One read returns at most 100000 samples (an Apple
Watch recording holds about 15360, a Withings one about 9000); `truncated` is true when more are
stored.

`classification` is the label the recording device or its app gave the recording, in its
maker's vocabulary, passed through as recorded. Open Health Sync does not compute, check or
interpret it. `classification_value` keeps the stored integer.

| Provider | `classification_value` | `classification` |
|---|---|---|
| `apple_health` | HealthKit's `HKElectrocardiogram.Classification`: 0, 1, 2, 3, 4, 5, 6, 100 | `not_set`, `sinus_rhythm`, `atrial_fibrillation`, `inconclusive_low_heart_rate`, `inconclusive_high_heart_rate`, `inconclusive_poor_reading`, `inconclusive_other`, `unrecognized` |
| `withings` | the recording's AFib result: 0, 1, 2 | `negative`, `positive`, `inconclusive` |

Another value is served with `classification` null. `symptoms_status` is HealthKit's: `not_set`,
`none` or `present` (Withings recordings are `not_set`).

Recordings are the `ecg` consent category: revoking it deletes the provider's recordings and
their samples. The same recordings are FHIR Observations with the waveform as `SampledData`
([fhir.md](fhir.md#ecg-recordings)), the MCP tool `get_ecg_recordings` lists them and
summarizes one without its samples, and `exports/ecg` and the user archive hold them
([exports.md](exports.md)).

### Nutrition logs

`GET /v1/users/{id}/nutrition` lists food and drink entries, from Apple Health (dietary samples
and food entries) and Health Connect (`Nutrition` and `Hydration` records). Seven nutrients have
their own field; every other one is in `nutrients`, by name. Each nutrient has one unit, whatever
the source: the mappers convert to it and refuse a value whose unit does not convert.

| Nutrients | Unit |
|---|---|
| `energy_kcal` (energy consumed), `nutrients.energy_from_fat` | kcal |
| `protein_g`, `carbs_g`, `fat_g`, `fiber_g`, `sugar_g`; `nutrients.fat_saturated`, `fat_monounsaturated`, `fat_polyunsaturated`, `fat_unsaturated`, `fat_trans` | g |
| `sodium_mg`; `nutrients.cholesterol`, `caffeine`, `calcium`, `chloride`, `copper`, `iron`, `magnesium`, `manganese`, `phosphorus`, `potassium`, `zinc`, `niacin`, `pantothenic_acid`, `riboflavin`, `thiamin`, `vitamin_b6`, `vitamin_c`, `vitamin_e` | mg |
| `nutrients.biotin`, `chromium`, `folate`, `folic_acid`, `iodine`, `molybdenum`, `selenium`, `vitamin_a`, `vitamin_b12`, `vitamin_d`, `vitamin_k` | mcg |
| `nutrients.water` | mL |

`meal_type` is `breakfast`, `lunch`, `dinner`, `snack` or null. Rows written before 0.2.0 may
hold `nutrients` in the unit the upload gave (an Apple Health food entry also kept its core
masses unconverted); erasing the provider's data and importing again rewrites them. A Health
Connect entry is identified by its record id: a re-sent record replaces its figures, a record
whose start moved replaces its older copy, and a `Deletion` line removes it. The same logs are
FHIR Observations ([fhir.md](fhir.md#nutrition-logs)).

### Knowing when an upload is processed

Processing is asynchronous. `GET /v1/users/{id}/coverage` returns record counts per provider and
local day, `pending_jobs` (work not finished yet: ETL jobs, and provider pulls queued or
running) and `parked_jobs` (ETL work that exhausted its retries and needs an operator). After an
upload, or after a user connects a provider (the backfill), poll until `pending_jobs` is 0. A
pull that keeps failing stays pending while it retries: `GET /v1/users/{id}/freshness` gives
its `last_error`. A server that would rather not poll can subscribe to the `data.written`
[webhook](webhooks.md). Coverage is not paged: at most 10000 days, the earliest, with
`truncated: true` when more matched.

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

## Self reports

A self report is the person's own rating, a whole number on a fixed scale: `feeling`, `mood`,
`stress`, `energy`, `sleep_quality` and `muscle_soreness` from 1 to 5, `exertion` and `pain` from
0 to 10. `POST /v1/users/{id}/self-reports` records 1 to 100 of them (tenant key, or the user's
own token with scope `upload`) and answers 202; each becomes a quantity sample of type
`reported_<kind>` from the source `self_report`, read back with `/samples`, `/samples/stats`,
FHIR (category `survey`) and the exports. A report sent again with the same `id` replaces its
value. Scales, retries and corrections: [self-reports.md](self-reports.md).

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/self-reports -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"reports":[{"kind":"feeling","value":4,"start_at":"2026-03-01T07:30:00Z","timezone":"Europe/Paris"}]}'
curl -s "$OHS/v1/users/$USER_ID/samples?type=reported_feeling" -H "Authorization: Bearer $KEY"
```

## Share links

`POST /v1/users/{id}/shares` makes a SMART Health Link: a snapshot of the user's records of some
categories (`heart`, `sleep`, `activity`...) in a time window, written as a FHIR R4 Bundle,
encrypted with a fresh key and stored as ciphertext only. The answer holds the link (`uri`,
`shlink:/...`) and the built-in viewer's link (`viewer_url`), once: the server keeps no key.
`expires_at` is required, a `passcode` optional. `GET /v1/users/{id}/shares` lists the user's
links (never a key or a link), `GET .../shares/{share_id}` adds who opened one, and `DELETE`
revokes it. The reader's viewer calls the public `POST /shl/{manifest_id}` with no credential.
Categories, the reader's side, revocation and limits: [share-links.md](share-links.md).

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/shares -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"categories":["heart","sleep"],"start":"2026-09-01","end":"2026-10-01","expires_at":"2026-10-21T00:00:00Z"}'
```

## FHIR R4

`GET /v1/users/{id}/fhir/Observation` serves the same quantity samples, blood pressure readings
(a systolic and a diastolic sample paired into one panel), sleep sessions, workouts, daily
summaries, ECG recordings and nutrition logs as FHIR R4 Observations, in one `searchset` Bundle (`application/fhir+json`) in time
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
`category-samples`, `sleep`, `workouts`, `nutrition`, `daily` or `ecg`) in `[start, end)` as one CSV
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
again, a time tag changes or a share link is made, stopped or opened. Requests are signed per Standard Webhooks and retried for about 28 hours. Events,
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

## Running the instance

These routes back the [admin console](console.md); a script can call them too. All take a tenant
API key and are recorded in the audit log.

| Route | Purpose |
|---|---|
| `GET /v1/tenant` | The tenant of the key: `id`, `name`, `admin`, and the `id` and `prefix` of the key in use |
| `GET /v1/keys`, `POST /v1/keys` | The tenant's API keys (revoked ones included, `current` marks the one in use); create one, its full `key` is in the 201 answer only (`Cache-Control: no-store`) |
| `DELETE /v1/keys/{key_id}` | Revoke a key; 409 for the key making the call |
| `GET /v1/pipeline` | The tenant's pipeline in one answer: users, sources (connections per provider and status, rate limit pauses), sync runs, processing jobs, rejected records, raw data stored in the last 24 hours, webhook endpoints and deliveries |
| `GET /v1/pipeline/sync-runs` | Sync runs across the tenant's users, newest first; filters `status` and `provider`, paged with `cursor` and `limit` |
| `GET /v1/pipeline/etl-jobs` | Processing jobs; `status` is `parked` (default), `pending` or `running`; filter `provider`, paged with `cursor` and `limit` |
| `POST /v1/pipeline/etl-jobs/{job_id}/retry` | Queue a parked job again with fresh attempts; 409 when it is not parked |
| `POST /v1/pipeline/etl-jobs/retry` | Queue again every parked job of the tenant, or of one `provider` in the body; answers `{"retried": n}` |

Provider settings are instance wide, so their routes need a key of an
[instance admin](console.md#the-instance-admin) tenant (403 otherwise):

| Route | Purpose |
|---|---|
| `GET /v1/admin/providers` | Every connector of the binary: `status` (`active`, `off`, `not_configured`, `error`, `built_in`), where its keys come from (`env`, `console`), each key (required, secret, set, and its value only when it is not a secret), and the redirect and notification URLs to register at the provider |
| `GET /v1/admin/providers/{provider}` | One of them |
| `PATCH /v1/admin/providers/{provider}` | `{"keys": {"CLIENT_ID": "...", "CLIENT_SECRET": "..."}, "enabled": true}`; a `null` key removes it. The connector checks the keys before they are stored (422 with its message). 409 when the provider's keys come from the environment and the body changes keys |
| `DELETE /v1/admin/providers/{provider}` | Forget the keys and the on/off choice saved in the console; 404 when nothing was saved |

```sh
curl -s -X PATCH $OHS/v1/admin/providers/oura -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"keys":{"CLIENT_ID":"...","CLIENT_SECRET":"..."},"enabled":true}'
```

An instance that serves several teams manages their tenants with the same admin keys, as
`ohs admin tenant` and `ohs admin key` do from a shell. Shapes, examples and the audit trail:
[admin-api.md](admin-api.md).

| Route | Purpose |
|---|---|
| `GET /v1/admin/tenants` | Every tenant (`id`, `name`, `admin`, `created_at`, `active_keys`), oldest first, paged with `cursor` and `limit` |
| `POST /v1/admin/tenants` | `{"name": "..."}`: a tenant (never an admin) and its first key, whose full `key` is in the 201 answer only (`Cache-Control: no-store`) |
| `GET /v1/admin/tenants/{tenant_id}` | One tenant |
| `GET /v1/admin/tenants/{tenant_id}/pipeline` | The tenant's `GET /v1/pipeline` answer |
| `GET`, `POST /v1/admin/tenants/{tenant_id}/keys` | The tenant's keys; create one, its full `key` in the 201 answer only |
| `DELETE /v1/admin/tenants/{tenant_id}/keys/{key_id}` | Revoke one of its keys; 409 for the key making the call |
| `POST /v1/admin/tenants/{tenant_id}/demo-user` | `{"external_ref": "demo-user", "days": 30}`, both optional: a user of the tenant filled with [synthetic data](demo.md), 201 when created, 200 when it existed |

## MCP

`POST /mcp` speaks the Model Context Protocol for AI agents, read only. See [mcp.md](mcp.md).

## Provider-facing and operational routes

| Route | Purpose |
|---|---|
| `GET /connect`, `GET /connect/{provider}`, `POST /connect/{provider}` | The [hosted connection page](connect-page.md), for the user's browser |
| `GET /oauth/{provider}/callback` | OAuth redirect target you register with the provider |
| `POST /shl/{manifest_id}`, `GET /shl/file/{ticket}` | A [share link](share-links.md)'s manifest and file, for the reader's viewer |
| `GET /shl/view` | The built-in share link viewer |
| `POST /inbound/{provider}` | Provider push notifications, verified with the provider's own scheme |
| `GET /inbound/{provider}` | Notification URL check and subscription challenge |
| `GET /healthz` | Liveness |
| `GET /readyz` | Readiness: database ping and raw store check, 503 when either fails |
| `GET /metrics` | Prometheus text format; absent when `OHS_METRICS=false` |
| `GET /v1/setup`, `POST /v1/setup` | The browser [first-run setup](setup.md): creates the first tenant and its key with `OHS_SETUP_TOKEN`, once; only when that is set |
| `GET /console` | The [admin console](console.md) (HTML), only when `OHS_CONSOLE` is `true` or `read-only` |

The OAuth callback, the notification routes and the share link manifest and file routes take
no credential and are rate limited per client address (and the notification routes per provider): over a limit they answer 429 with a
problem body and `Retry-After` in seconds, before any work. Limits and the reverse proxy
setting are in [self-hosting.md](self-hosting.md#rate-limits).

`/metrics` is global to the instance (not per tenant). It asks for the `OHS_METRICS_TOKEN` bearer
token, refuses every scrape when no token is set, and can move to a private address with
`OHS_METRICS_LISTEN`; see [self-hosting.md](self-hosting.md#metrics).
