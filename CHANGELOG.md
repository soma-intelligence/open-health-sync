# Changelog

All notable changes to Open Health Sync are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the version is 0.x, any
release may change the API, the configuration or the database schema.

## [Unreleased]

### Added

- RR intervals and HRV: `GET /v1/users/{id}/rr-intervals` serves the stored beat-to-beat
  intervals of heartbeat series, and `GET /v1/users/{id}/hrv` the RMSSD, SDNN, pNN50, mean RR
  and mean heart rate derived from each series, after implausible and ectopic intervals are set
  aside. MCP tools `get_rr_intervals` and `get_hrv`. See [docs/hrv.md](docs/hrv.md).
- Personal use and recipes: `ohs admin personal setup` readies a one person install (a
  `personal` tenant, a user, a new API key) and prints it as an env file. `recipes/` holds eight
  single file Python tools with no dependency that run against it: sleep report, heart rate and
  HRV trend, source comparison, weekly digest, HTML dashboard, full export, read-only AI token,
  account connection. Each has a page under [docs/recipes](docs/recipes/README.md), and the
  quickstart end to end suite runs them all.
- Erasing one provider's data of one user, for providers whose terms ask for it on disconnect:
  `DELETE /v1/users/{id}/connections/{provider}?delete_data=true` disconnects and queues the
  erasure (202 with a `job_id`), and `DELETE /v1/users/{id}/data/{provider}` does it without a
  connection, for device providers such as `apple_health` and `health_connect` (409 while the
  user is still connected). A background job of the `etl` role deletes every record of that
  user and provider in every `ohs_data` table (children through their parent), the raw files
  and their rows, failed records, ETL and pull jobs, pull coverage, the sync log, push
  subscriptions, consent states and the disconnected connection, lifting the TimescaleDB
  decompression limit for compressed chunks; the user and their other providers stay. It waits
  for a pull of the provider still running; while it is pending the provider cannot be
  connected again and its uploads are refused (409). An ETL job that read an erased raw object
  writes nothing. `GET /v1/deletions/{job_id}` gains `provider` (null for a user deletion), and
  the new `data.erased` webhook announces the end. Without `delete_data` a disconnect keeps the
  data, as before. Migration `0020`.

- Recorded connector contracts: `make record PROVIDER=<slug>` records what a real account's API
  answers (or an Apple Health export), anonymized on the contributor's machine, and CI replays
  every committed recording through its connector. See
  [docs/connectors/recording.md](docs/connectors/recording.md).
- Dexcom connector (`dexcom`, API v3, OAuth 2.0, scheduled pulls): CGM estimated glucose values
  as `blood_glucose`, with trend and out-of-range status. `OHS_DEXCOM_REGION` picks the US, EU,
  Japan or sandbox host. See
  [docs/connectors/dexcom.md](docs/connectors/dexcom.md).
- OMRON connector (`omron`, OMRON Wellness partner API, OAuth 2.0, scheduled pulls): blood
  pressure (paired into the FHIR panel), pulse, possible AFib flags, weight and body
  composition. See
  [docs/connectors/omron.md](docs/connectors/omron.md).
- Health Connect connector (`health_connect`) and `POST /v1/ingest/health-connect`: an Android
  app uploads Health Connect records (steps, heart rate, HRV, SpO2, temperature, blood pressure,
  glucose, body measurements, sleep, exercise, deletions). See
  [docs/connectors/health-connect.md](docs/connectors/health-connect.md).
- FHIR: a glucose sample measured in interstitial fluid (a CGM reading) is coded with LOINC
  `99504-3` instead of `2339-0`.
- FreeStyle Libre research notes: Abbott has no public API; readings arrive through Health
  Connect or Apple Health.
- Open mHealth output: `GET /v1/users/{id}/omh/data-points` serves heart rate, steps, blood
  pressure pairs, body weight, height, BMI, body fat, body temperature, SpO2, respiratory rate,
  blood glucose, active energy, sleep sessions (`sleep-episode`) and workouts
  (`physical-activity`) as Open mHealth data points, in the order and with the cursor of the FHIR
  search, filtered by `schema`. Every mapping is checked against the official Open mHealth
  schemas, vendored with their own test data, by the unit tests. See
  [docs/open-mhealth.md](docs/open-mhealth.md).
- CSV and Parquet exports: `GET /v1/users/{id}/exports/{dataset}` streams every sample, category
  sample, sleep session, workout, nutrition log or daily summary of a user in a time window as
  one CSV or Parquet file, with the columns of the REST records (typed, UTC timestamps, JSON
  metadata). See [docs/exports.md](docs/exports.md).
- Outgoing webhooks ([docs/webhooks.md](docs/webhooks.md)): `POST /v1/webhooks` registers an
  endpoint for `data.written`, `data.deleted`, `user.deleted` and `connection.needs_reauth`
  events. Requests are signed per Standard Webhooks (HMAC-SHA256, `whsec_` secrets sealed with
  `OHS_ENCRYPTION_KEY`), written in the transaction of the change they announce, posted by the
  `etl` role and retried 8 times over about 28 hours; an endpoint failing for 5 days is
  disabled. Payloads name the user, provider and days, never a health value. Routes to list,
  change, delete, test, rotate the secret, list deliveries and retry one; MCP tool
  `list_webhooks`. Endpoint URLs must be https and public unless
  `OHS_WEBHOOK_ALLOW_PRIVATE=true`. Migration `0018`.
- Fitbit connector (`fitbit`) on the Google Health API v4, not the legacy Fitbit Web API: Google
  OAuth with granted-scope handling, paged pulls of 21 data types, webhooks checked with the
  subscriber secret and Google's ECDSA signature, and revoke on disconnect. Google is not
  onboarding new projects yet.
- Suunto connector (`suunto`, Suunto Cloud API, partner program): OAuth 2.0 with the API Zone
  subscription key, workouts from `/v3/workouts`, HMAC-signed notifications; 24/7 sleep, recovery
  and daily activity are stored raw only until their schema is confirmed.
- Ultrahuman connector (`ultrahuman`, Partnership API): OAuth 2.0 and one metrics call per day.
  Ring heart rate, HRV (RMSSD), skin temperature, steps, resting heart rate, VO2 max, sleep with
  stages, and M1 CGM glucose (mmol/L converted to mg/dL).
- intervals.icu connector (`intervals_icu`): OAuth 2.0, scheduled pulls of activities and daily
  wellness, and webhooks checked with the app's webhook secret. Activities keep their upstream
  source (Garmin, Wahoo, Zwift, Coros, Suunto, ...).
- `connector.StateExchanger`: the OAuth callback hands the state to connectors that need PKCE
  (for a future Garmin connector).
- Oura connector (`oura`, Oura Cloud API v2, OAuth 2.0, scheduled pulls): daily activity,
  sleep periods with stages, daily readiness, daily SpO2, heart rate and workouts. See [docs/connectors/oura.md](docs/connectors/oura.md).
- WHOOP connector (#6): bring your own WHOOP developer app, OAuth connection, scheduled pulls
  of cycles, recoveries, sleeps, workouts and body measurements from the API v2, and signed
  webhooks for recovery, sleep and workout updates.
- Strava connector (`strava`, API v3, OAuth 2.0, scheduled pulls and webhook events): activities
  as workouts. See
  [docs/connectors/strava.md](docs/connectors/strava.md).
- `GET /inbound/{provider}` lets a connector answer a provider's subscription challenge
  (Strava's `hub.challenge`), through the optional `connector.ChallengeResponder` interface.
- `Workout.Replaceable`: a connector can mark a workout so an edited copy from the provider
  replaces the stored one instead of being skipped.
- `SleepSession.Replaceable`: a re-scored or edited sleep session replaces the stored figures
  and its sleep phases instead of keeping the first copy; a Replaceable session or workout whose
  start moved supersedes the stored copy of the same provider record. Migration `0011` indexes
  that lookup. Oura, WHOOP and Polar now mark their sleep sessions and workouts Replaceable.
- Quantity name `hrv_rmssd` (milliseconds) for HRV reported as RMSSD, kept apart from
  `hrv_sdnn`. Migration `0010` registers it in `quantity_codes`.
- Polar connector (AccessLink v3): OAuth connect with user registration, scheduled pulls of
  exercises, sleep with hypnogram, Nightly Recharge, daily activity, continuous heart rate and
  physical info, and AccessLink webhooks with optional signature verification
  (`OHS_POLAR_WEBHOOK_SECRET`).
- Provider deletions: `Map` can return `model.Deletion` records (`MapResult.Deletions`). The
  ETL removes the stored sleep sessions, workouts, category samples and nutrition logs of that
  provider record, with their phases, route points, track summaries and derived quantity
  samples, scoped to the job's tenant and user, and records a tombstone in the new
  `ohs_data.deleted_records` table (migration `0012`) so the record is never written again,
  whichever arrives first.
- `QuantitySample.SupersedeMoved`: a Replaceable sample whose `ExternalID` names one figure of
  one record replaces the stored copy of the same provider, external id and quantity type under
  another start (an edited sleep or workout, a shifted cycle) instead of adding a second row.
  WHOOP sets it on all its samples, Oura on the samples of a night.
- `Deletion.ClearSummaryFields`, `DailySummary.RecordID` and the `summary_date` sample metadata
  (`model.MetadataSummaryDate`): a deletion clears the daily summary figures its record filled
  (allow-listed column names only), and a deleted record's summary is not written again. WHOOP
  clears a recovery's `recovery_score` and `resting_hr` on `recovery.deleted` and
  `sleep.deleted`.
- `connector.InboundEvent.Lines`: a notification that a pull cannot fetch back (a delete
  notice) is stored verbatim as a raw object for every connection of the provider user and
  queued for the ETL, instead of queueing a pull.
- WHOOP `sleep.deleted` and `workout.deleted` webhooks and Strava activity `delete` events (with
  `OHS_STRAVA_SUBSCRIPTION_ID` set) now remove the record.
- Oura webhooks (optional, `OHS_OURA_VERIFICATION_TOKEN`): the subscription challenge is
  answered, notifications are verified with `x-oura-signature`, create and update events queue
  a pull, and deleted sleep periods, workouts and sessions are removed. Subscriptions are
  created once by the operator; see [docs/connectors/oura.md](docs/connectors/oura.md).
- Oura daily sleep score (`sleep_score`), daily stress (`stress_high_time`,
  `recovery_high_time`, in minutes), meditation and breathing sessions (`mindful_session`
  category samples) and VO2 max (`vo2_max`). Migration `0013` registers the new quantity
  names. The consent now asks for the `session` scope.

- `ohs import apple-health <export.zip|export.xml>`: loads the history of an Apple Health
  export (the Health app's *Export All Health Data*) without an iOS app. Records, workouts with
  their routes, blood pressure and food correlations, heartbeat series, audiograms and ECGs are
  converted to the upload contract's lines and sent through `POST /v1/ingest/apple-health`, in
  constant memory, with `--since`, `--tz`, `--dry-run` and a batch id derived from the export so
  a re-run uploads nothing twice. See
  [importing a Health app export](docs/connectors/apple-health.md#importing-a-health-app-export).
- FHIR R4 output: `GET /v1/users/{id}/fhir/Observation` serves a user's records as FHIR R4
  Observations in one paged `searchset` Bundle (`application/fhir+json`), in time order:
  quantity samples; blood pressure, a systolic and a diastolic sample of the same reading paired
  into the R4 blood pressure panel (85354-9, never split across pages); sleep sessions (93832-4,
  stages and night averages as components); workouts (55411-3, activity type and measurements as
  components); and daily summaries over the user's local day. Filtered by `type`, `code` (LOINC,
  quantity type or record type), `category`, `start` and `end`;
  `GET /v1/users/{id}/fhir/Observation/{observation_id}` reads one back. Vital signs meet the R4
  vital signs and blood pressure profiles; LOINC and UCUM codes come from the quantity type
  catalog. The golden files pass the official HL7 validator (`make fhir-validate`, not in CI).
  Mapping and limits: [docs/fhir.md](docs/fhir.md).
- `GET /v1/users/{id}/freshness`: how up to date each source of a user is (`synced_through`,
  `lag_seconds`, last notification, next pull, last error, and a `fresh`/`stale`/`failing`
  state), Apple Health uploads included. `/metrics` gains `ohs_sync_connections` by provider
  and state, `ohs_sync_max_lag_seconds` and `ohs_sync_push_silent_connections`.
- Docs: [background sync](docs/background-sync.md), including what the iOS and Android SDKs
  need to upload in the background.

### Fixed

- `OHS_<SLUG>_<FIELD>` keys of a provider slug that contains an underscore (`intervals_icu`) are
  now read under the whole slug.

### Changed

- The backstop pull adapts to notifications: every 6 hours while a connection's provider
  notifications arrive, every hour when none came for 24 hours or the provider sends none
  (before: every 6 hours for all).
- A provider subscription that has heard nothing for 24 hours and is more than 7 days old is
  renewed at the provider, which repairs a subscription the provider dropped silently.
- Notification pulls go first: they are claimed before backfill and backstop pulls, so a
  burst of slot jobs or a rate limit never delays data a provider just announced.
- A Withings notification pulls only what it concerns (a measurement: measures and ECG;
  activity: activity and intraday; sleep; workouts), 1 to 4 calls instead of 9, which
  leaves the app's rate limit for fresh data. Connectors opt in with
  `connector.NotifiedFetcher`.
- Migration 0008 adds freshness columns to `ohs_sync.connections`; migration 0009 adds
  `data_type` and `priority` to `ohs_sync.pull_jobs`.

### Changed

- `quantity_samples` chunks older than 30 days are compressed (TimescaleDB compression,
  segmented by tenant and user): about 7 times less disk in our benchmark of 10.5 million
  samples. Builds of TimescaleDB without compression keep the table as it is.
- Duplicate quantity samples are now found by the writer, in the stored rows of the batch's
  time window, instead of a unique index (migration 0015 drops `quantity_samples_dedup_key`).
  A history import into a compressed period is about 34 times faster than with the index,
  and no longer fails on TimescaleDB's decompression limit.
- User deletion and retention lift TimescaleDB's per-transaction decompression limit, so they
  can never stop half way on compressed data.
- The ETL retries an object's write transaction up to 3 times on a deadlock or serialization
  failure, which late data can hit while the compression job compresses the same chunk.
- Oura sleep periods of type `deleted` or `rest` (rejected by the user) remove the stored
  session of that id instead of being skipped. An Oura data endpoint answering 404 is skipped
  with a warning instead of failing the pull.
- Oura connector checked against Oura's OpenAPI document (1.37). The consent asks for
  `spo2Daily`; the old `spo2` is not an Oura scope. Heart rate is filed under its UTC day (Oura
  sends it in UTC). Records at a half or quarter hour offset (+05:30, +05:45, -03:30...) land on
  the user's local day instead of the UTC day. A null contributor is no longer stored as 0, and
  a zero-length sleep period or a timestamp outside 2000 to 2199 is not stored.
- Oura pulls retry a 5xx answer twice with backoff. A data type that fails no longer stops the
  others: the pull stores what it could and fails with the errors at the end. Paging survives
  an empty page and stops on a repeated `next_token`, and date windows overlap by a day so no
  day is missed. An `invalid_client` answer of the token endpoint is reported as an
  application error instead of marking every user `needs_reauth`.
- Oura webhooks accept data and event types in any case and a signature over the compacted
  body, and queue no pull for a data type the connector does not store.
- WHOOP checked against the published API v2: `recovery.deleted` webhooks are applied
  (recovery samples are now identified by their sleep), the cycle's `step_count` fills the
  daily summary's steps, offsets that are not whole hours (`+05:30`, `+05:45`) give the right
  local day, and an ongoing cycle (no `end`) maps.
- WHOOP pulls are sturdier: one request schedule per process across concurrent pulls, a wait
  for `X-RateLimit-Reset` when the window is spent, retries on HTTP 500/502/503/504, a data
  type whose scope was not granted skipped instead of failing the pull, a failing data type no
  longer stopping the others, and a guard against a repeated `next_token`. A grant without a
  refresh token (`offline` unticked) is refused, and deleting a user revokes the WHOOP grant.
- A provider deletion now removes the quantity and category samples stored under the record id
  or `<id>_<suffix>` whenever they start, also when no sleep or workout of that id is stored (a
  WHOOP `recovery.deleted` while the sleep stays, a record that only gave samples). Before, only
  quantity samples within a day of a deleted sleep or workout went, and category samples only
  matched the exact id. Migration `0014` adds partial indexes on (`tenant_id`, `user_id`,
  `provider`, `external_id` in the "C" collation) of `quantity_samples` and `category_samples`
  so the lookup is an index range, not a scan.
- A token refresh that returns no new refresh token keeps the stored one (RFC 6749 section 6);
  this was already the behaviour and is now covered by a test.

## [0.0.2] - 2026-10-11

Usability release. Nothing changes in the data model or the database schema. Read the
[release notes](docs/release-notes/v0.0.2.md) for the two settings that are now checked at
start.

### Added

- `ohs admin tenant list`, `ohs admin key create`, `key list` and `key revoke`: rotate a
  tenant API key without touching the database by hand.
- `GET /openapi.yaml` serves the OpenAPI description of the running version, readable from
  any origin, to import into Postman, Swagger UI or Redoc.
- `X-Request-Id` on every API response (the caller's own is kept when well formed), repeated
  as `request_id` in problem bodies and on every log line about the request.
- `ohs help`, `ohs help <command>`, `-h`/`--help` on every command and `ohs --version`.
- Docs: a providers page (`docs/connectors/README.md`) listing the supported and planned
  sources with status and integration path; upgrade, backup and restore, and secret rotation
  in the self-hosting guide; Claude Desktop and Claude Code setup in the MCP guide.
- README rewritten around real screenshots (light and dark) and a social preview image.
- The container image carries the `io.modelcontextprotocol.server.name` label and the
  standard OCI source, description and licenses labels, and `server.json` at the repository
  root describes the stdio MCP package for the official MCP registry.

### Changed

- Logs write durations as text (`24h0m0s`) instead of nanoseconds, and `ohs started` names
  the version.
- `ohs mcp` says what a refused handshake means (401: the key; 403: the scope; 404 or 405:
  the URL) instead of `cannot reach`.
- MCP server instructions and the `get_samples` description steer agents to `get_daily` for
  trends and to narrow windows for raw samples.
- A known API path called with the wrong method answers 405 with an `Allow` header instead of
  404 `route not found`.
- A 403 names the credential the endpoint takes (a tenant API key, or a user token with a
  given scope).
- A command line error (unknown command, wrong arguments) exits 2; other failures still exit 1.
- `ohs serve` and `ohs doctor` refuse an `OHS_PUBLIC_URL` that is not an absolute http(s) URL
  (or has a query or fragment) and an unknown `OHS_LOG_LEVEL` at load, and an
  `OHS_ENCRYPTION_KEY` error says how to generate a key.
- `ohs serve` checks at start that the raw store accepts writes (a probe object outside `raw/`,
  deleted at once) and stops with a hint instead of failing every upload; `ohs doctor` runs
  the same check (not `--quick`). A database it cannot reach, or a listen address already in
  use, now names the variable to check.

## [0.0.1] - 2026-10-11

First public release, an early preview. See the
[release notes](docs/release-notes/v0.0.1.md) for known limitations.

### Added

- `ohs`, one Go binary with three roles (`api`, `collect`, `etl`) that run in one process or
  apart, on Postgres with TimescaleDB.
- Apple Health ingest: an iOS app (yours; no SDK ships yet) uploads HealthKit data as gzip
  NDJSON parts, covering the quantity, category, sleep, workout, route, ECG, heartbeat series
  and nutrition types.
- Withings connector: bring your own developer app, OAuth connection, scheduled pulls and push
  notifications for measurements, activity, sleep, workouts and ECG.
- One canonical model with one unit per quantity, deduplication, source and device
  attribution, validation, and daily summaries per local day.
- Raw payloads kept as the source of truth, in a local folder or any S3 compatible store, so a
  user can be re-processed after a mapper fix.
- Multi-tenancy: tenant API keys (`ohs_sk_`), short-lived scoped user tokens (`ohs_ut_`),
  OAuth tokens encrypted with AES-256-GCM.
- REST API described by `api/openapi.yaml`, and a read-only MCP server (`POST /mcp` and
  `ohs mcp` over stdio).
- Hard deletion of a user's raw files and rows, run by a worker that survives restarts, with
  `GET /v1/deletions/{job_id}` to follow it.
- Coverage per user and day, with the jobs still pending and the lines the ETL rejected
  (`rejected_records`).
- Reliability: crash-safe ingest, retries with backoff, parking after 5 attempts, an orphan
  scanner, a drainer for recoverable failures, provider rate limit handling, retention.
- `ohs doctor` configuration checks, `/healthz`, `/readyz` and Prometheus `/metrics`.
- ECG recordings and heartbeat series are stored; their read routes are planned.
- Docker Compose deployment with overlays for a Raspberry Pi 5 and a local S3 store, and an
  opt-in overlay to build from source.
- Release artefacts: a multi-arch image (`linux/amd64`, `linux/arm64`) at
  `ghcr.io/soma-intelligence/open-health-sync`, and static binaries for Linux and macOS on
  amd64 and arm64 with SHA-256 checksums.

[Unreleased]: https://github.com/soma-intelligence/open-health-sync/compare/v0.0.2...HEAD
[0.0.2]: https://github.com/soma-intelligence/open-health-sync/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/soma-intelligence/open-health-sync/releases/tag/v0.0.1
