# Changelog

All notable changes to Open Health Sync are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the version is 0.x, any
release may change the API, the configuration or the database schema.

## [Unreleased]

### Added

- Oura connector (`oura`, Oura Cloud API v2, OAuth 2.0, scheduled pulls): daily activity,
  sleep periods with stages, daily readiness, daily SpO2, heart rate and workouts. Not yet
  verified with a real account; see [docs/connectors/oura.md](docs/connectors/oura.md).
- WHOOP connector (#6): bring your own WHOOP developer app, OAuth connection, scheduled pulls
  of cycles, recoveries, sleeps, workouts and body measurements from the API v2, and signed
  webhooks for recovery, sleep and workout updates. Not yet verified with a real account.
- Strava connector (`strava`, API v3, OAuth 2.0, scheduled pulls and webhook events): activities
  as workouts. Not yet verified with a real account; see
  [docs/connectors/strava.md](docs/connectors/strava.md).
- `GET /inbound/{provider}` lets a connector answer a provider's subscription challenge
  (Strava's `hub.challenge`), through the optional `connector.ChallengeResponder` interface.
- `Workout.Replaceable`: a connector can mark a workout so an edited copy from the provider
  replaces the stored one instead of being skipped.
- `SleepSession.Replaceable`: a re-scored or edited sleep session replaces the stored figures
  and its sleep phases instead of keeping the first copy; a Replaceable session or workout whose
  start moved supersedes the stored copy of the same provider record. Migration `0009` indexes
  that lookup. Oura, WHOOP and Polar now mark their sleep sessions and workouts Replaceable.
- Quantity name `hrv_rmssd` (milliseconds) for HRV reported as RMSSD, kept apart from
  `hrv_sdnn`. Migration `0008` registers it in `quantity_codes`.
- Polar connector (AccessLink v3): OAuth connect with user registration, scheduled pulls of
  exercises, sleep with hypnogram, Nightly Recharge, daily activity, continuous heart rate and
  physical info, and AccessLink webhooks with optional signature verification
  (`OHS_POLAR_WEBHOOK_SECRET`). Not yet verified with a real account.
- Provider deletions: `Map` can return `model.Deletion` records (`MapResult.Deletions`). The
  ETL removes the stored sleep sessions, workouts, category samples and nutrition logs of that
  provider record, with their phases, route points, track summaries and derived quantity
  samples, scoped to the job's tenant and user, and records a tombstone in the new
  `ohs_data.deleted_records` table (migration `0010`) so the record is never written again,
  whichever arrives first.
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
  category samples) and VO2 max (`vo2_max`). Migration `0011` registers the new quantity
  names. The consent now asks for the `session` scope.

### Changed

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
