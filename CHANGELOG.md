# Changelog

All notable changes to Open Health Sync are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the version is 0.x, any
release may change the API, the configuration or the database schema.

## [Unreleased]

## [0.3.0] - 2026-10-29

A reliability release. Each provider is pulled on its own schedule, deleted and edited HealthKit
samples leave the server, a stress and fuzz pass fixed how `ohs` behaves under load, outages and
bad input, and many figures are now validated. Migration `0043` applies on start. The compose file
caps `ohs` at 1 GB, Garmin notifications need `OHS_GARMIN_INBOUND_TOKEN`, and an uploaded part may
expand to 64 MiB: read the [release notes](docs/release-notes/v0.3.0.md) before you upgrade.

### Added

- SMART on FHIR connector (`smart_fhir`, preview): a patient connects their health system's
  patient portal (Epic MyChart and any certified US EHR) with a standalone SMART launch and PKCE,
  and the server pulls their record from its FHIR R4 API. Every resource is kept raw; vital signs
  become quantity samples and the other Observations (labs) generic observations of type
  `ehr`. See [docs/connectors/smart-fhir.md](docs/connectors/smart-fhir.md).
- The console's provider page shows the provider's sync schedule: how often it is pulled, with and
  without notifications, how far each pull reads again, the catch-up after an outage, the backfill
  of a new connection and the daily reconciliation. `GET /v1/admin/providers` and
  `GET /v1/admin/providers/{provider}` serve it as `sync` (seconds, null for an upload from an
  app). See [docs/background-sync.md](docs/background-sync.md#sync-policy-per-provider).
- `deploy/Caddyfile`: Caddy with automatic TLS in front of the compose stack, and the routes to
  keep on `ohs` when the host serves other pages too, so `/healthz` and `/readyz` are never
  redirected. See [docs/self-hosting.md](docs/self-hosting.md#reverse-proxy).
- Each provider is pulled on its own sync policy (`connector.SyncPolicy`): interval with and
  without notifications, lookback for late data, catch-up bound, backfill length and an optional
  daily reconciliation that widens the 00:00 UTC pull. Dexcom re-reads 6 hours (its readings
  reach the API up to 3 hours late outside the US, so they were missed after the backfill),
  OMRON 72 hours, Ultrahuman 48, Eight Sleep 36 and the others 24. Oura, WHOOP, Withings and
  Dexcom are backfilled over 90 days, Polar and Suunto over the 28 days their APIs serve, and
  Strava and Suunto are pulled less often to stay inside their rate limits. See
  [docs/background-sync.md](docs/background-sync.md#sync-policy-per-provider).
- A data quality score for every figure (`internal/quality`, no API change yet): the probability
  that a figure is within its metric's tolerance of the truth, a grade, the source's bias and
  random error in the figure's context, and calibrated 68 and 95% intervals. It combines published
  error figures with citations and evidence tags, copy detection, agreement between a user's
  sources (Bland-Altman, triple collocation), a gradient boosted residual model and split conformal
  calibration. A leave one subject out backtest on GalaxyPPG, PPG-DaLiA and synthetic data
  (`go test -tags bench ./internal/quality/bench`) publishes its numbers in
  [docs/quality/report.md](docs/quality/report.md), and Walch et al. 2019, held out, scores the
  frozen model in [docs/quality/walch-eval.md](docs/quality/walch-eval.md). See
  [docs/quality.md](docs/quality.md). The score stays a probability with no NaN on any input:
  the agreement estimators refuse a series with a NaN or an infinity, and the anchor's bias
  update no longer overflows to NaN on a tiny spread or effective size.
- A synthetic data generator for every documented provider (`internal/synth`): an invented
  person's timeline rendered in each vendor's wire format, written from the vendor's published
  documentation, with scenarios for DST and travel, several devices, gaps, duplicates, missing,
  out of range and malformed values, unit variants and huge exports. `go test ./...` runs a light
  version; the nightly `synthetic-data` workflow runs every scenario, and every provider
  through a running server and database (`test/synthflow`). See
  [docs/specs/synthetic-data.md](docs/specs/synthetic-data.md).

### Changed

- The default compose file caps `ohs` at 1 GB (`mem_limit`, `OHS_MEM_LIMIT`), and `ohs` sets
  `GOMEMLIMIT` itself to three quarters of the container's memory limit when it is unset or
  empty, so the soft limit follows `OHS_MEM_LIMIT` (`OHS_GOMEMLIMIT` overrides it). Eight concurrent large uploads took the
  process to 623 MB and had it killed by the host's out-of-memory killer, along with a process
  of another container. [Memory](docs/self-hosting.md#memory) lists the profiles, the Raspberry
  Pi one included.
- Device uploads: past its share of the ingest slots (half by default,
  `OHS_INGEST_TENANT_CONCURRENCY`) a tenant takes a slot only while one slot in eight (at least
  one) stays free for the other tenants, so a lone tenant still uses the free slots and no
  tenant can refuse the others' uploads; and a part may expand to at most 64 MiB once decompressed
  instead of 512 MiB (`OHS_INGEST_MAX_DECOMPRESSED_MIB`, up to 512). Eight gzip bombs of 611 KB
  held every slot of the instance for 14 s, and every other tenant's uploads got 503 meanwhile.
  The SDKs and `ohs import` write parts of at most 8 MiB of NDJSON, so they are not affected.
- A call that fails because the database cannot be reached (Postgres restarting, out of
  connections) answers 503 with `Retry-After: 5` instead of 500, so clients that retry 503 and
  429 keep the upload or the read. A pooled connection the server dropped mid-session (a
  backend killed or restarted: pgx's "conn closed" at the next begin or commit) counts as an
  outage too. Other failures stay 500, a refused database login, a protocol error and a
  canceled request included.
- Swift SDK: the anchored sync of one type is `AnchoredSync`, over `AnchoredChangeSource` and
  `AppleHealthPartUploader`, so it is tested with fakes on every platform; `HealthKitSync` runs
  it over `HKAnchoredObjectQuery`. `SyncReport` (same module, `OpenHealthSyncHealthKit`) now
  compiles on every platform and gains `deletionsByType`, `deletionsUploaded` and
  `deletionsPendingByType`. `UploadResult` gains `features`, `acceptsDeletionLines` and a
  public initializer.
- An upload to a full or read-only raw store (no space left, a quota, a read-only or
  unwritable directory) answers 503 with `Retry-After: 60` instead of a 500 a client may give up
  on; nothing is recorded, so the same part can be sent again once there is room.
- A database that does not answer at all (a frozen host, a network partition) no longer holds
  every request until it comes back (300 s and more in a stress run with Postgres paused): the
  credential lookup gives up after 10 s with the same 503, and a new pool connection gives up
  after 10 s (`connect_timeout` in `OHS_DATABASE_URL` overrides it).

### Fixed

- A NaN or infinite value no longer passes validation. NaN failed no range check and +Inf passed
  every rule with no ceiling, so a connector that read `"NaN"` or `"Infinity"` stored it.
- Connector fuzzing (new fuzz tests for Dexcom, Fitbit, Health Connect, intervals.icu, OMRON,
  Polar, Strava, Suunto, Ultrahuman, Withings, Xiaomi and Bluetooth) found and fixed:
  - `"NaN"`, `"Inf"` and overflowing unit conversions read as figures (OMRON, Ultrahuman,
    Fitbit, Health Connect, Xiaomi); they now read as missing.
  - Durations that overflowed into wrong ends or negative totals (Strava, intervals.icu,
    Withings intraday, Xiaomi and Polar sleep stages).
  - A Withings measure with a huge power of ten hung the mapper or scaled to infinity.
  - Zero times (Dexcom, OMRON, Ultrahuman) and UTC offsets no zone uses (Dexcom, OMRON, Polar,
    Bluetooth) were accepted.
  - `null` list entries were stored as raw lines, and failed every Withings pull.
  - A Polar hypnogram with a clock time written twice mapped differently from run to run.
  - The Xiaomi export converter wrote a line the server could not decode (`formatVersion` 2).
- One Dexcom or OMRON record without a readable time failed the whole window on every retry;
  it is now skipped and counted in a warning.
- An `Idempotency-Key` that is not UTF-8 answered 500; it now answers 400.
- A read bound outside the years 1 to 9999 (`start=0000-01-01`, or year 1 with a `+23:59`
  offset) answered 500; the REST API and the MCP tools now refuse it.
- `/mcp` transport errors (400, 403, 405, 413, 415) were plain text; they are now
  `application/problem+json`, and the OpenAPI document lists them.
- MCP argument decode errors named internal Go types; they now name the argument only.
- A time zone written as `Local` was read in the server's own zone in FHIR daily summaries,
  daily rollups and cycles; it now reads as UTC.
- Consent: a refused category (heart, respiratory, sleep and the others in
  [docs/consent.md](docs/consent.md)) is now also cleared from the figures of sleep sessions,
  workouts and daily summaries that stay, at ingest and when a revocation is purged.
- A NUL character or invalid UTF-8 in a path, a query parameter or a JSON string no longer ends
  in a 500 (31 operations did) or an MCP "internal error": the path and the query are checked
  before routing and answer 400, a JSON body with a `\u0000` answers 400 naming the field, and an
  MCP tool refuses the argument by name. An audit event whose path value held a NUL was dropped;
  its text is now cleaned and the event written.
- A failed pull window is fetched again: the regular pull now reaches back to the end of the
  last complete pull (`synced_through`) instead of the last successful job, which a pull of one
  notified data type moved forward.
- Apple Health: deleted and edited HealthKit samples are removed from the server. The Swift SDK
  used to drop the deleted objects of its anchored queries and move the anchor past them, and
  since HealthKit edits a sample by deleting it and saving a new one, every edited weight or
  blood pressure reading stayed stored twice. The upload contract has a deletion line,
  `{"deletedUuid":"<uuid>"}` ([docs](docs/connectors/apple-health.md#deletions)). Records keep
  their HealthKit `uuid` as `external_id`, and every sample UUID leaves an alias on the row it was
  stored as or de-duplicated into (`ohs_data.record_aliases`, migration 0043, pruned with its rows
  by `OHS_RETENTION_DAYS`): a deletion removes a row only once no live sample stands for it, so two apps writing the same reading, or a new
  copy saved a sync before the old one is deleted, keep it. Uploading the history again
  (`resetAnchor`) makes rows stored by 0.2.0 deletable. Upload answers list
  `features: ["deletion_lines"]`; `HealthKitSync` sends a round's deletions once, after its
  samples, only to a server that lists it, keeps them beside the anchor otherwise, and saves the
  anchor only once both are uploaded. Sleep stages, ECG and heartbeat series are not covered.
- A provider deletion recomputes the derived daily totals of the days of the samples it removed,
  and a derived day left without samples is dropped.
- A new provider subscription is due at once even when the database clock runs ahead of the
  server's. Its row took the database's `now()` while the subscribe pass compares with the
  server clock, so a few milliseconds of skew left it waiting for the next pass.
- An upload whose every line fails validation or parsing (dates in the future, for example) is no
  longer replayed at every process start. It got a recoverable job-level failure row, so the
  drainer queued it again after each restart and its line failures were written again: one
  restart of a loaded stack replayed 11086 such objects and added 391865 rows to
  `failed_records`.
- A client sending its headers and then the body a byte at a time held its connection and a
  goroutine for as long as it kept sending, on any route and without a key: 4000 such
  connections took 140 MB, and enough would push `ohs` past its memory limit. A request must
  now be read within 2 minutes (uploads keep 5), and `OHS_MAX_CONNECTIONS` (default 4096)
  caps the open connections; one over it is closed at once.
- After a Postgres restart, the ETL and pull jobs that were in flight no longer wait out the 10
  minute visibility timeout: recording a job's outcome is retried while the database cannot be
  reached (up to 5 minutes), so the job is retried after its usual backoff. A stack whose
  database was killed for 30 s used to finish its last jobs about 600 s later.
- Sleep phases, ECG recordings, daily summaries and observations are now validated, and an end
  before the start or far in the future is refused on every record kind.
- Apple Health glucose in mmol/L and the `yd`, `in`, `s`, `oz` and `lb` unit spellings are
  accepted instead of refused.
- Workout heart rate, energy, distance, speed, climb and VO2 max are range-checked.
- Nutrition logs with negative energy, macros or nutrients are refused.
- Health Connect: `recordingMethod` `manual_entry` marks the record as user entered.
- A NUL character in a source name or a metadata value no longer fails the whole raw object:
  the job used to retry until parked and every record beside it was lost. NUL and invalid
  UTF-8 are removed from mapped records and from failed record payloads.
- The sample export and the archive decide the merged view in one pass over the stored samples
  instead of one lookup per record: on second by second data the default export streamed about
  110 rows a second; an Apple Health account of 57 575 merged samples now exports in under a
  second instead of 10 minutes, record for record the same.
- Fitbit / Google Health: Apple Health data relayed by Google (`platform` `HEALTH_KIT`) stores
  its HRV as `hrv_sdnn` instead of `hrv_rmssd`, since HealthKit only measures SDNN (#205), and a
  deep sleep RMSSD of 0 is left out of metadata. Points relayed from Apple Health or Health
  Connect are attributed to that store (source `apple_health` or `health_connect`), so they no
  longer add up with the tracker's own minutes in one stream. Rows stored earlier keep their
  type and source until you run `docs/how-to/fitbit-relayed-points.sql` (see
  [the guide](docs/how-to/fitbit-relayed-points.md)): a script rather than a migration, because
  it has to decompress the compressed chunks of `quantity_samples` it scans.
- FHIR: the `hrv_avg_ms` component of sleep sessions and daily summaries no longer carries LOINC
  80404-7, the SDNN code: the value is RMSSD, which has no LOINC code, so it keeps the Open
  Health Sync component code only, as `hrv_rmssd` samples do.
- Validation: an oxygen saturation under 50 % or a body fat percentage under 2 % or over 80 % is
  refused instead of stored with a suspect flag (body fat from 70 to 80 % is kept and flagged),
  so a fraction sent with a percent unit (0.97) no longer reaches the exports. The figures of
  sleep sessions (stage durations, score, HRV, resting heart rate, respiratory rate, SpO2) and
  of daily summaries (steps, energy, distance, floors, resting heart rate, HRV, scores) are
  range-checked like the matching samples.
- One bad measurement no longer costs the whole record: a sample whose unit cannot be converted
  is dropped on its own (the record used to be parked with every sample beside it), and a
  figure of a sleep session, workout or daily summary outside its hard band is removed from that
  record, listed under `quality_dropped` in its metadata, while the record and its other figures
  are written. Suspect figures of these records are flagged like suspect samples.
- Every record kind refuses a time before 1990-01-01 UTC: a zero date (0001-01-01), the Unix
  epoch or the FIT epoch was stored as a measurement, and a year 0001 sample lost its end.
- Ultrahuman: a pull asks every date whose local day overlaps its window instead of the UTC
  dates only. West of UTC the end of every day was never pulled (in California its last 7
  hours, the closing step count among them). No date after the one UTC+14 has reached is
  asked, and a refusal (400, 422) or an empty answer for a date after the current UTC day is no
  data, not a failed pull.
- Garmin: notifications are refused (401) while `OHS_GARMIN_INBOUND_TOKEN` is unset. A push
  notification is stored as data and Garmin signs nothing, so without the token anyone who knew
  a connected user's Garmin id could store invented summaries for them. Pings are refused too,
  and in ping mode the notification is what triggers a pull, so without the token only the
  scheduled pulls bring data: set it and add `?token=<value>` to the notification URLs. A
  warning is logged at startup when the keys are set without it, and refusals are logged at
  warning level, at most every 10 minutes.
- Apple Health export.xml from a phone set to US units: a height in feet (every height record
  was refused as a unit error), masses in ounces, lengths in inches, speeds in ft/s and volumes in
  mL or fluid ounces are converted to their storage unit.
- Withings: a sleep session's resting heart rate is the night's lowest heart rate (`hr_min`),
  as for Oura, instead of the mean heart rate asleep (`hr_average`), which now stays in
  metadata. Sessions stored earlier keep the mean.

### Security

- Webhook destinations: the private address guard now also refuses 0.0.0.0/8, 192.0.0.0/24,
  198.18.0.0/15, 240.0.0.0/4 and fec0::/10, and judges NAT64, 6to4 and IPv4-compatible IPv6
  addresses by the IPv4 address they carry.
- `github.com/klauspost/compress` 1.18.7 (GO-2026-5841). `govulncheck` found the vulnerable
  code unreachable from Open Health Sync; the bump keeps it so.

## [0.2.0] - 2026-10-21

An admin console that runs the pipeline, an admin API for instances that serve several teams,
provider keys per tenant, and new sources: Garmin, Eight Sleep, Xiaomi, activity files (FIT, GPX,
TCX) and standard Bluetooth health devices through the new `ohs-bridge`. ECG recordings are
served, and self reports, share links (SMART Health Links), sample statistics, encryption key
rotation and signed releases are new. Migrations `0035` to `0042` apply on start.
`OHS_CONSOLE=true` now serves the console with its actions, and the FHIR Observation search
refuses parameters it does not support: read the [release notes](docs/release-notes/v0.2.0.md)
before you upgrade.

### Added

- Releases carry the Android example app, `ohs-example-android.apk`, so a phone installs it without
  a build; its checksum is in the signed `checksums.txt`.
- `data_types` on each provider of `GET /v1/providers`: the record kinds the connector writes
  (`sample`, `sleep`, `workout`...) and the quantity names it can produce (`heart_rate`...), so an
  app can tell which source gives what without reading the docs. The lists are read from each
  mapper's source by `go run ./internal/connector/cmd/gendatatypes`, which also writes the table in
  [docs/connectors/README.md](docs/connectors/README.md#what-each-connector-delivers).
- `OHS_ENCRYPTION_KEY_FILE` and `OHS_SETUP_TOKEN_FILE`: read the encryption key or the setup token
  from a file instead of the variable. A missing file is created on first start with a random
  value (mode 0600), so app store templates without per-install secrets (CasaOS, Unraid) still get
  a different key on every install. Setting a variable and its `_FILE` together is an error, and
  `ohs doctor` says which file each secret came from without printing it. See
  [docs/setup.md](docs/setup.md).
- Admin console for the whole pipeline. `/console` now runs the instance, not only shows it: an
  overview of the five pipeline stages (sources, sync, processing, storage, delivery) with what
  needs attention, providers turned on and their keys saved from the browser, users created and
  sent a connection link, a sync started, parked processing jobs retried, webhook endpoints and
  deliveries managed, API keys created and revoked, and the audit log. Each action is a call to a
  documented route with the tenant key and asks for a confirmation first. Light and dark, works
  on a phone. See [docs/console.md](docs/console.md).
- Providers turned on without a restart. An instance admin saves a provider's keys and turns it
  on or off with `PATCH /v1/admin/providers/{provider}` (also `GET`, `DELETE`, and
  `GET /v1/admin/providers` with each provider's state, keys and the URLs to register at the
  provider). Keys are checked by the connector before they are stored, sealed with
  `OHS_ENCRYPTION_KEY` in the new `ohs_core.provider_settings`, re-sealed by `ohs admin rekey`,
  and picked up by every process within 30 seconds. A provider's `OHS_<PROVIDER>_*` variables,
  when set, still win; the on/off switch applies to both. Secrets are write only: the API never
  returns their value.
- Instance admin tenants: the first tenant of an instance, the `OHS_BOOTSTRAP_TENANT` tenant and
  the one of `ohs admin personal setup` are admins. `ohs admin tenant create --admin` and
  `ohs admin tenant admin --tenant <id> [--off]` set the mark, `ohs admin tenant list` shows it,
  and `tenant.admin` is audited. Only admin keys reach `/v1/admin/*`.
- `GET /v1/tenant` (the tenant of the key), `GET /v1/keys`, `POST /v1/keys` and
  `DELETE /v1/keys/{key_id}`: a tenant manages its own API keys over the API, as
  `ohs admin key` does from a shell. A key cannot revoke itself (409).
- Tenants managed over HTTP, for an instance that serves several teams: with an instance admin
  key, `GET` and `POST /v1/admin/tenants` list (paged) and create tenants (never an admin; the
  first key's secret is in the 201 answer only), `GET /v1/admin/tenants/{tenant_id}` and
  `.../pipeline` read one, `GET`, `POST .../keys` and `DELETE .../keys/{key_id}` manage its
  keys, and `POST .../demo-user` fills one of its users with up to 90 days of synthetic data, the
  code path of `ohs seed --demo` now shared by both. Each change is also recorded in the audit
  log of the tenant it changed (actor `admin`, the admin key's id, the key prefix, never the
  key), in the same transaction for tenants and keys. See [docs/admin-api.md](docs/admin-api.md).
- Providers per tenant: an instance admin turns a provider off, or back on, for one tenant with
  `PATCH /v1/admin/tenants/{tenant_id}/providers/{provider}` (`{"enabled": false}`), and lists the
  switches with `GET /v1/admin/tenants/{tenant_id}/providers`. On by default. While off, the
  tenant's `GET /v1/providers` and connect page leave the provider out, starting a connection,
  the OAuth callback and uploads answer 403, sync now answers 409, no pull is planned and a
  queued one ends `cancelled` without reaching the provider. Connections and data are kept. The
  switch is audited as `tenant.provider` on both tenants and applies in every process within 30
  seconds; migration 0041 adds `ohs_core.tenant_providers`.
- A tenant brings its own provider keys: with an instance admin key,
  `PUT /v1/admin/tenants/{tenant_id}/providers/{provider}/keys` sets the tenant's own OAuth
  application for a provider (`GET` shows how they stand, never a value; `DELETE` removes them).
  The tenant's new connections then go through its application, whose name its users see on the
  consent screen, while every other tenant keeps the instance's keys; a provider the instance has
  no keys for becomes available to that tenant only. The callback URL stays the instance's. A
  connection keeps the keys it was made with for its refreshes, pulls, notifications (each
  application's signature reaches only its own connections) and revocation. Removing the keys,
  or replacing them with another client id, sets the connections made with them `needs_reauth`
  with a `connection.needs_reauth` webhook event; rotating only a secret keeps them. Keys are
  checked by the connector before they are stored, sealed with `OHS_ENCRYPTION_KEY`, covered by
  `ohs admin rekey` and `ohs doctor`, audited as `tenant.provider_keys` on both tenants and
  applied in every process within 30 seconds. `GET /v1/admin/tenants/{tenant_id}/providers`
  gains `own_keys`. Migration 0042 adds `ohs_core.tenant_provider_keys` and a `tenant_keys_id`
  column to connections and OAuth states. See [docs/admin-api.md](docs/admin-api.md#a-tenants-own-provider-keys).
- An instance admin runs one tenant's API with its own key: under
  `/v1/admin/tenants/{tenant_id}`, the tenant's users (list, create, read, delete, deletion
  jobs), their data (`daily`, `samples`, `samples/stats`, `sleep`, `workouts`, `nutrition`,
  `coverage`), their connections (`connections`, `freshness`, `sync-runs`, connection links,
  sync now, disconnect), its webhooks and deliveries, its sync runs and processing jobs (with
  retries) and its audit log answer what the tenant's own key gets from the route of the same
  name, through the same handlers. A `Location` points at the admin route. Each request is
  recorded on the tenant served (actor `admin`, the user of the path, `admin_tenant_id`) as well
  as on the admin's tenant (without the user); a request on the admin's own tenant is recorded
  once. A portal can run a team's projects behind its own sign in, with no tenant key in a
  browser.
- Demo personas: `ohs seed --demo --persona` and `"persona"` on
  `POST /v1/admin/tenants/{tenant_id}/demo-user` pick `mix` (the default, unchanged), `runner`
  (polar, whoop, apple_health: resting heart rate about 46, HRV about 78, VO2 max about 56, runs
  on most days), `sleep` (oura, apple_health: about 6.2 h a night with late, irregular
  bedtimes) or `weight` (withings, fitbit: about 92 kg with a slow yearly drift). Each is its
  own person with its own seed and, by default, its own user (`demo-runner`, `demo-sleep`,
  `demo-weight`); the answer and the audit event name the persona. See
  [docs/demo.md](docs/demo.md#personas).
- `GET /v1/pipeline` (the tenant's pipeline counts in one answer), `GET /v1/pipeline/sync-runs`
  (sync runs across users, with `status` and `provider` filters), `GET /v1/pipeline/etl-jobs`
  (parked, pending or running processing jobs), and `POST /v1/pipeline/etl-jobs/{job_id}/retry`
  and `POST /v1/pipeline/etl-jobs/retry` to queue parked jobs again with fresh attempts.
- `GET /v1/providers` gives each provider its display `name`.
- Audit events of a route that names a provider, an API key or a processing job in its path keep
  that `provider`, `key_id` or `job_id` in `detail`.
- Migration 0038 adds `tenants.admin`, `provider_settings` and two indexes for the console's
  lists of failed runs and parked jobs.
- Bluetooth devices: heart rate straps, blood pressure cuffs, scales, glucose meters,
  thermometers and pulse oximeters that implement the standard Bluetooth SIG health profiles
  send their readings straight to the server, with no vendor account and no phone. The new
  `POST /v1/ingest/bluetooth` upload route (provider `bluetooth`, user token with scope
  `upload`) stores each characteristic value as the bytes the device sent, with the time it
  arrived, and the ETL decodes it: heart rate and RR intervals, blood pressure with pulse, mean
  arterial pressure and the cuff's status flags, weight, BMI and height, body composition, blood
  glucose (control solution left out), body temperature and SpO2. Device clocks are used when
  they hold a plausible time (metadata `clock`), shared devices are filtered by user index, and
  the source slug comes from the manufacturer (`polar`, `omron`, `and`...). Blood pressure in
  kPa is converted to mmHg. No migration, no new Go dependency. See
  [docs/connectors/bluetooth.md](docs/connectors/bluetooth.md).
- `ohs-bridge`, a desktop bridge written in Rust (`bridge/`), for macOS, Linux (Raspberry Pi
  included) and Windows: it scans for standard Bluetooth health devices, connects and
  reconnects, sets their clock, asks glucose meters for new records, spools every value on
  disk and uploads it in parts that survive a crash or a network loss. `ohs-bridge files`
  uploads new FIT files from a folder, such as a watch mounted over USB, through the activity
  files route. `ohs-bridge watch` shows heart rate, RR and battery live, and `--status-file`
  keeps a one-line status for menu bar tools. See [docs/bridge.md](docs/bridge.md).
- Swift and Kotlin SDKs: `BluetoothRecorder` builds the Bluetooth upload parts and
  `uploadBluetoothPart` sends them; the Swift package gets an `OpenHealthSyncBluetooth`
  product (CoreBluetooth) that does the scanning and subscriptions on iOS and macOS, and the
  Android example app shows the same with `BluetoothGatt`.
- Activity intensity minutes: `moderate_activity_time` and `vigorous_activity_time`, the minutes
  of a day a provider classed as moderate or vigorous activity, one sample per provider day in
  `min`. Oura's `daily_activity` (medium and high activity time) and Withings' `getactivity`
  (`moderate` and `intense`) fill them; the metadata copies stay. Read them with `/samples`,
  `/samples/stats`, the exports and the MCP tools. Migration 0039 adds the two types to the
  catalog with LOINC 77592-4 and 77593-2.
- FHIR follows the HL7 Physical Activity IG (1.0.0) for its measures: the two intensity types
  and workouts also carry the IG's `PhysicalActivity` category, workout mean and maximum heart
  rate carry LOINC 55425-3 and 55426-1, and the daily summary's `total_kcal` carries 41979-6.
  CI validates these golden files against the IG's activity measure profile as well. See
  [docs/fhir.md](docs/fhir.md#physical-activity).

- Self reports: `POST /v1/users/{id}/self-reports` records a person's own ratings next to their
  synced data: how they feel, mood, stress, energy, sleep quality and muscle soreness (1 to 5),
  the effort of a session and pain (0 to 10). It takes a tenant key, or the user's own token
  with scope `upload`, and honours `Idempotency-Key`. Each report becomes a quantity sample of
  type `reported_<kind>` from the new `self_report` source, so it is read with `/samples`,
  `/samples/stats`, the exports, the archive and the MCP tools, consented to and erased like any
  other source. A report sent again with the same `id` replaces its value. In FHIR, self reports
  are Observations with category `survey` and the person as `performer`; pain carries LOINC
  72514-3. Migration 0036 adds the eight types to the catalog. See
  [docs/self-reports.md](docs/self-reports.md).

- Share links (SMART Health Links, HL7 FHIR IG "SMART Health Cards and Links" STU1): a person
  shares a time window of some data categories of their own records with whoever they choose.
  `POST /v1/users/{id}/shares` (tenant key) writes the records as a FHIR R4 Bundle, encrypts it
  as a JWE (`dir`, `A256GCM`, standard library only) with a fresh key, stores the ciphertext
  only and answers the `shlink:/` link once. A link expires (at most 365 days), may need a
  passcode (10 wrong tries disable it), is revoked with `DELETE`, by a consent change that
  refuses a category it holds or by an erasure of a provider's data, and goes with the user.
  The public `POST /shl/{manifest_id}` and `GET /shl/file/{ticket}` serve the reader, rate
  limited per client address (`OHS_RATE_LIMIT_OAUTH`, `route_group="share"`); `/shl/view` is a
  built-in viewer that decrypts in the browser. Every access is in the audit log (`share.access`,
  actor `share_link`); webhooks `share.created`, `share.revoked`, `share.accessed`. Migration
  0037. See [docs/share-links.md](docs/share-links.md).
- `GET /v1/users/{id}/samples/stats` and the MCP tool `get_sample_stats`: count, sum, mean,
  min and max of a quantity type per hour or per local day in any IANA time zone (`tz`), for
  ranges up to 10,000 intervals. By default (`view=merged`) each interval keeps the best
  ranked source that recorded in it; `view=all` returns one series per provider, source and
  unit. Migration 0035
  adds the `quantity_hourly` continuous aggregate it reads (refreshed every 5 minutes); a year
  of daily heart rate takes 31 ms instead of 443 ms from the samples. Builds without
  continuous aggregates, and zones with a half-hour offset, get the same answer from the
  samples.
- First-run setup in the browser, for installs with no terminal (Umbrel, CasaOS and other app
  stores). With `OHS_SETUP_TOKEN` (16 characters or more) and `OHS_CONSOLE=true`, the page
  `/console/setup` creates the first tenant and shows its API key once, for whoever types the
  token. `GET /v1/setup` tells whether the instance still needs it, and `POST /v1/setup` does it:
  one shot, atomic under concurrent calls, closed for good (409) once any tenant exists, token
  compared in constant time and never logged, creation recorded in the audit log. The console's
  sign-in screen links to the page while it is needed; the console itself stays read only.
  Unset, the routes are not mounted. Tries are rate limited per client address like the OAuth
  callback (`OHS_RATE_LIMIT_OAUTH`, its own bucket, `route_group="setup"` in
  `ohs_rate_limited_total`). See [docs/setup.md](docs/setup.md).
- A reproducible Apple Health ingestion benchmark in Go: `bench/ingest/` and
  [the method and results](docs/benchmarks/ingest.md).
- Eight Sleep connector, slug `eight_sleep`. Eight Sleep has no public API: the connector signs
  in the way the Eight Sleep app does (a password grant with the app's client id and secret,
  built in from the values pyEight publishes, overridable with `OHS_EIGHT_SLEEP_CLIENT_ID` and
  `OHS_EIGHT_SLEEP_CLIENT_SECRET`) and pulls the nightly trends, following the open source
  pyEight library. It is on by default; turn it off in the console. Sleep sessions with stages and sleep score,
  `heart_rate`, `hrv_rmssd` and `respiratory_rate` samples, and bed and room temperature in the
  session metadata. See [docs/connectors/eight-sleep.md](docs/connectors/eight-sleep.md).
- Sign-in page for providers without OAuth. A connector can implement
  `connector.PasswordConnector`; starting a connection to it returns this server's own page,
  `/oauth/{provider}/signin`, which asks for the account email and password and ends like the
  OAuth callback. The password goes to the connector only, is never logged, and is kept sealed
  under `OHS_ENCRYPTION_KEY` with the connection so the server can sign in again. The hosted
  connection page says which sources ask for a password.
- Garmin connector (Health API), slug `garmin`. Users connect their Garmin Connect account over
  OAuth 2.0 with PKCE (the code verifier is derived from the OAuth state, so nothing is stored
  per consent). Pulls dailies, 15 minute epochs, sleeps with stages, activities, body
  composition, HRV (as `hrv_rmssd`), pulse ox, respiration, VO2 max, blood pressure and skin
  temperature, in 24 hour upload windows paced at about 100 requests a minute; stress and Body
  Battery are stored raw. Garmin's ping and push notifications arrive at `/inbound/garmin`: a
  ping queues a pull of that type's upload window, a push stores its items as received.
  Optional `OHS_GARMIN_INBOUND_TOKEN`: a secret the notification URLs carry, refused when
  missing. Disconnecting or deleting a user deletes the Garmin registration. Garmin issues keys
  to approved businesses only, and every instance brings its own. See
  [docs/connectors/garmin.md](docs/connectors/garmin.md).
- Xiaomi: `ohs import mi-fitness <folder|copy.zip>` loads the copy of Mi Fitness data that
  Xiaomi sends from its account privacy center (heart rate, resting heart rate, SpO2, steps,
  distance, active energy, weight, body fat, VO2 max, sleep with stages, workouts) through the new
  `POST /v1/ingest/xiaomi` upload route, provider `xiaomi`. Records that Mi Fitness, Mi Health and
  Zepp Life write to Health Connect now get the source slug `xiaomi`; those of the Zepp app
  (Amazfit) get `zepp`, and those of the RingConn app `ringconn`. No private Xiaomi API is
  used. See [docs/connectors/xiaomi.md](docs/connectors/xiaomi.md).
- Activity files: `ohs import files <file|folder|zip>` imports FIT, GPX and TCX files (and their
  `.gz` versions) through the new `POST /v1/ingest/activity-files` upload route, provider
  `activity_files`, one file per part. It reads single files, folders and account exports with
  zips inside zips: the Garmin account export, the Strava archive, the COROS bulk export, and
  the files Wahoo, Zwift, Suunto and Polar let a user download. Each file gives workouts with
  their GPS route, per-second heart rate, and cycling or running power. The source slug comes
  from the file (FIT manufacturer, TCX or GPX creator: `garmin`, `coros`, `wahoo`, `zwift`...)
  and the device from the FIT product. A file is uploaded once, keyed by its SHA-256, so a
  re-run or an overlapping export sends nothing twice; `--dry-run` counts without uploading.
  No vendor API is used. New dependency: `github.com/muktihari/fit` v0.28.4 (BSD-3-Clause), the
  FIT decoder. See [docs/connectors/activity-files.md](docs/connectors/activity-files.md).
- MCP prompts: the prompt library is now served by the MCP server (`prompts/list`,
  `prompts/get`). Twelve ready-made questions about a person's own training, sleep and recovery
  (morning check, taper check, after a break, a new habit, two devices side by side...), each
  with its blanks as arguments and an optional `user_id`, and each ending with the same rules:
  read the data first and show the numbers, compare with the person's own history, one source
  per figure, say when a figure is missing instead of filling it in, wellness only. The same
  library generates [docs/learn/prompts.md](docs/learn/prompts.md) and a new agent skill,
  [skills/open-health-sync](skills/open-health-sync/SKILL.md). See [docs/mcp.md](docs/mcp.md#prompts).
- Rate limits on the provider-facing routes (`POST` and `GET /inbound/{provider}`,
  `GET /oauth/{provider}/callback`): an in-memory token bucket per client address, plus one per
  provider for notifications, answers 429 with `Retry-After` before any work. Defaults 30/s
  (bursts of 120) per address and 300/s per provider for notifications, 2/s (bursts of 20) per
  address for callbacks; `OHS_RATE_LIMIT_INBOUND`, `OHS_RATE_LIMIT_INBOUND_PROVIDER` and
  `OHS_RATE_LIMIT_OAUTH` change them (`0` disables). `OHS_TRUSTED_PROXIES` lists the reverse
  proxies whose `X-Forwarded-For` is read. Refusals are counted in `ohs_rate_limited_total`.
  See [docs/self-hosting.md](docs/self-hosting.md#rate-limits).
- Encryption key rotation: `OHS_PREVIOUS_ENCRYPTION_KEYS` lists, comma separated, the keys
  `OHS_ENCRYPTION_KEY` replaced; they still open what they sealed and never seal. Sealed values
  (OAuth tokens, webhook signing secrets) now start with a format version and the id of their
  key; values written before open as they are. `ohs admin rekey` re-seals every stored secret
  under the active key, in batches of short row-locked transactions, safe while the instance
  runs, and prints how many it rewrote, found current or could not open (left as they are).
  `ohs doctor` counts the secrets still under a previous or unknown key and says when the
  previous keys can go. See [docs/key-rotation.md](docs/key-rotation.md).
- Signed releases: the release workflow signs the image by digest and the binaries'
  `checksums.txt` (bundle `checksums.txt.sigstore.json` on the release) with Sigstore cosign,
  keyless, using the workflow's GitHub OIDC identity. Verification commands are in
  [SECURITY.md](SECURITY.md#verifying-releases).
- Docs: [security model](docs/security-model.md) (assets, trust boundaries, mitigations with
  their code, residual risks) and a [hardening checklist](docs/hardening.md) for production.
- ECG recordings are served. `GET /v1/users/{id}/ecg` lists a user's Apple Watch and Withings
  recordings (metadata, paged, `start` and `end`), `GET /v1/users/{id}/ecg/{ecg_id}` returns one
  with its voltage samples (offsets in seconds, microvolts, at most 100000). The classification
  is the recording device's own, passed through in its maker's vocabulary with the stored value;
  nothing is computed from the signal. FHIR: each recording is an Observation `ecg-{id}`
  (LOINC 11524-6, the waveform as `component.valueSampledData` under MDC 131328), in the
  Observation search (`code=http://loinc.org|11524-6`), read by id, Provenance and
  `$everything`; a search page holds at most 100 of them. MCP: `get_ecg_recordings` lists them
  and summarizes one without its samples. Exports: dataset `ecg` (metadata) and
  `canonical/ecg.ndjson` in the user archive (with samples). No migration. See
  [docs/api.md](docs/api.md#ecg-recordings) and [docs/fhir.md](docs/fhir.md#ecg-recordings).
- Health Connect nutrition and energy: `Nutrition` and `Hydration` records become nutrition logs
  (every mass converted from grams to its nutrient's unit, the meal type, the food name in
  `metadata.food_name`), identified by record id like the samples, so an edit replaces the
  figures, a moved start replaces the older copy and a `Deletion` removes it.
  `TotalCaloriesBurned` becomes the new quantity type `total_energy_burned` (kcal), which is the
  daily summary's `total_kcal` when a source sends it, and `BasalMetabolicRate` the new type
  `basal_metabolic_rate` (kcal/d). Migration 0040 adds the two types to the catalog. See
  [docs/connectors/health-connect.md](docs/connectors/health-connect.md).
- Nutrition in FHIR: each nutrition log is an Observation `nutrition-{id}` (record type
  `nutrition_log`, no category) with the meal type and every figure as components, coded by
  REST field and by LOINC where a point in time code exists (energy 9052-2, protein 9079-5,
  carbohydrates 9059-7, fat 9066-2, water 8999-5). In the Observation search, read by id,
  Provenance, `$everything` and the user archive. See
  [docs/fhir.md](docs/fhir.md#nutrition-logs).
- Web example: a Next.js app (`examples/web`) built on the TypeScript server SDK that goes from
  the first user to webhooks in four pages: create and list users; get data in through the
  hosted connection page or an upload of 14 days of synthetic steps, resting heart rate and
  sleep with an upload-only user token; read it back as daily summaries, samples, sleep, FHIR R4
  and Open mHealth; and a webhook receiver that verifies with `verifyWebhook` and lists the last
  20 events. The tenant key stays on the server. CI type checks, tests and builds it. See
  [examples/web](examples/web/README.md).

### Changed

- The recipes are Go programs: `recipes/<name>/main.go`, standard library only, run with
  `go run ./recipes/<name>` (they were Python scripts). Their flags, output and files are the
  same. `recipes/starter` holds the block every recipe starts with, to copy for a tool of your
  own, and the seven tutorials in `docs/learn/`, the webhook examples and the export examples are
  in Go or DuckDB SQL too. The quickstart end to end test needs Go in place of Python 3.
- `OHS_CONSOLE=true` now serves the console with its actions. An instance that turned it on for
  the read-only view gets them on upgrade; set `OHS_CONSOLE=read-only` to keep the console
  without any action.
- Migration 0038 makes the only tenant of a single-tenant instance its instance admin, so the
  console can turn providers on after the upgrade. An instance with several tenants gets no admin
  until one is named with `ohs admin tenant admin`.
- A connector can list the keys it runs without (`connector.OptionalKeyed`), and `Configure` may
  be called again while the connector runs, when the console saves new keys.
- The hosted connect page and its result page are redesigned: frosted glass panels on a
  background the page draws itself, with no image or web font. Each source is one row and one
  button, with a line on what it shares (from the data its connector reads) and a "Connected" or
  "Needs reconnecting" status. Two large figures give the connected sources and the minutes left
  on the link. The page says that the access is read only and that the user signs in on each
  source's own site. The result page says the data now syncs in the background and offers a
  button back to the sources. Still no script, and fits a phone screen.
- A canonical `Workout` can name its source and device (`SourceSlug`, `DeviceID`), written to
  `workouts.source_slug` and `workouts.device_id` (columns that already existed). A connector
  that leaves them empty gets the previous behavior: the provider as the source, no device.
- The default workouts order of the merged view puts `garmin`, `coros`, `wahoo`, `suunto` and
  `zwift` after `polar` and before `strava`: the original recording of a workout, which carries
  its route and streams, wins over a summary copy of it. A user's own `source-priorities` still
  take precedence.
- Merged view reads are faster on a long history: a merged page's lookups of a better ranked
  record are bounded by the page's dates, a page with no end is read through growing windows
  (2, 16, 128 days), and these statements are planned for their parameters at every run (a
  generic plan scanned whole chunks for each record). On a user with 400 days of data, a
  FHIR page of 1,000 over a recent week went from 5.5 s to 0.35 s, a one-day page from
  0.74 s to 0.06 to 0.15 s, and `/samples` of 1,000 heart rates from 1.2 s to 0.17 to 0.5 s.
  Pages over compressed history still cost about 2 ms per record.
- FHIR Observation pages read back their records within the page's time range, and the blood
  pressure pairing subqueries carry that range too, so TimescaleDB plans only the chunks the
  page lies in instead of every chunk of the user's history. On a user with 400 days of
  samples (58 chunks, compressed), a page of 1,000 went from 260 ms to 75 ms (median), and
  the full export of 125,200 Observations from 47 s to 24 s.
- FHIR: an ECG's atrial fibrillation classification (`ecg_afib_classification`, Withings) is
  served as `valueCodeableConcept` (`negative`, `positive`, `inconclusive`) instead of a
  quantity in the unit "classification".
- WHOOP energy (`total_kcal` of a cycle, a workout's `active_energy_kcal`) is rounded to a
  tenth of a kcal instead of carrying the conversion's dozen digits.
- `pending_jobs` in `GET /v1/users/{id}/coverage` (and the MCP `get_coverage`) also counts the
  provider pulls not done yet. Before, it read 0 right after a provider connection while the
  backfill had not run, so a client waiting on it concluded too early.
- The FHIR Observation search answers 400 for a parameter it does not support instead of
  ignoring it: `date` and `_count` used to return the whole list unfiltered. The message names
  `start`/`end` and `limit`. `_format` asking for JSON is accepted.
- `ohs import apple-health` prints its first progress line two seconds in (a short import
  prints only the summary), and the summary splits parts into `stored now` and
  `already stored` instead of `uploaded`, which counted both.

### Fixed

- Apple Health basal and active energy recorded in `Cal` (HealthKit's large calorie, a
  kilocalorie) is stored as is. An export written with Calories as the energy unit spells those
  records `Cal`, and their values were stored 1,000 times too low (read as the small calorie),
  so a derived day's `total_kcal` was close to its active energy alone. Values already stored
  stay wrong: erase the Apple Health data (`DELETE /v1/users/{id}/data/apple_health`) and
  import the export again.
- `ohs import` given a folder says to pass export.zip or export.xml, instead of the system's
  "is a directory".
- Suunto: a workout whose `startTime` or `workoutId` has an unexpected JSON type (a string
  where a number was expected) no longer drops its whole page of workouts; that workout is
  kept and reported by the mapper like any unreadable start.
- Disconnecting or deleting a user now revokes the Oura access token (`GET /oauth/revoke`),
  like WHOOP, Strava and Polar.
- A provider deletion notice (Oura, WHOOP, Strava) now updates the connection's
  `last_event_at`, so `push_healthy` no longer reads false for a connection whose only recent
  notifications were deletions.
- MCP `get_coverage` without `start` reports the last 30 days. On years of Apple Health history
  it answered with over a megabyte of JSON, more than an agent can read.
- Nutrient units: every nutrient now has one documented unit (kcal, g, mg, mcg or mL; see
  [docs/api.md](docs/api.md#nutrition-logs)) and every mapper converts to it. The extended
  nutrients of Apple Health (`nutrients.caffeine`, `nutrients.vitamin_d`...) were stored in the
  unit of the upload, and a food entry (`HKCorrelationTypeIdentifierFood`) stored its core masses
  without the conversion single dietary samples had, so a mass sent in kilograms landed 1,000
  times too low in `protein_g`. A unit that does not convert now rejects the record instead.
  Rows already stored keep their values: erase the provider's data
  (`DELETE /v1/users/{id}/data/apple_health`) and import again to rewrite them.
- `GET /v1/users/{id}/connections` and the MCP tool `list_connections` return `created_at`,
  `initial_backfill_at`, `scopes` and `updated_at`, as `api/openapi.yaml` documents. Only
  `connected_at` was sent, so the Swift and Kotlin SDKs could not decode the list and the
  console showed `-` for when a source was connected and when its backfill started.
  `connected_at` stays, with the same value as `created_at`.
- Apple Health nights are whole again. A night recorded "in bed" had no figures; a night that
  crossed midnight, which uploads file in two parts, was split in two, or its stages went to the
  next night's session; a nap uploaded with a night went to that night or was dropped. Each
  night is now one session with all its stages, whatever order its parts are processed in, and
  its figures are the sums of its stages (`total_sleep_seconds` is the Health app's time
  asleep). A night with no in bed sample is marked `"synthesized": true`. To rebuild nights
  stored before, see [docs/connectors/apple-health.md](docs/connectors/apple-health.md#sleep).

### Security

- Binaries and the image are built with Go 1.26.10, which fixes GO-2026-6809 in `crypto/x509`
  (govulncheck found it reachable from the server on 1.26.9).
- The release workflow builds only a version tag on a commit of `main`, and never rebuilds a
  published release: re-pushing its tag stops before anything is built, signed or uploaded.
  The image build context is an allowlist of what `go build` needs, so a local `.env`, a dump
  in `backups/` or a health export never reaches a builder.

## [0.1.0] - 2026-10-13

Eleven new sources, FHIR R4 and Open mHealth output, CSV and Parquet exports, outgoing
webhooks, background sync, sample compression, a personal mode with recipes, a read-only admin
console, a hosted connection page, erasing one provider's data on disconnect, a Helm chart for Kubernetes, an audit log, consent per source and category, a complete
user archive, and client SDKs
for Kotlin, Swift, React Native and Flutter. Reads now default to a merged view across
sources (`view=all` keeps every source's rows). Migrations `0008` to `0034` apply on start.
`/metrics` is now closed by default: read the
[release notes](docs/release-notes/v0.1.0.md) before you upgrade.

### Security

- Consent per user, per source and per data category (activity, location, heart, ECG, blood
  pressure, glucose, respiratory, body, sleep, nutrition, reproductive, mental, other).
  `PUT /v1/users/{id}/consents/{provider}` sets the granted categories, `DELETE` revokes them,
  `GET /v1/users/{id}/consents` and `.../consents/history` read the state and every change
  (credential and evidence). Records of a category not granted are dropped before they are
  stored; a revoked category's stored records are deleted in the background. New webhooks
  `consent.granted` and `consent.revoked`. `OHS_CONSENT_REQUIRED=true` stores nothing from a
  source until consent is recorded. See [docs/consent.md](docs/consent.md).
- Audit log: every request authenticated with a tenant key or a user token, every
  `ohs admin` key command and every completed erasure is recorded in
  `ohs_core.audit_events` (credential, action, user, route pattern, status, request id; never a
  value, a query string or a secret). Append only, enforced by a database trigger; kept after a
  user's erasure; pruned after `OHS_AUDIT_RETENTION_DAYS` (default 365). Read it with
  `GET /v1/audit`. Failed writes count in `ohs_audit_write_failures_total`. See
  [docs/audit-log.md](docs/audit-log.md).
- `/metrics` is protected by default. On the API listener a scrape now presents the
  `OHS_METRICS_TOKEN` bearer token; with no token set, every scrape is refused (403).
  `OHS_METRICS_LISTEN` (for example `127.0.0.1:9464`) serves it on an address of its own
  instead. **Breaking for scrapers:** set `OHS_METRICS_TOKEN` and give it to Prometheus
  (`authorization: { credentials: ... }`), or set `OHS_METRICS_LISTEN`. See
  [docs/self-hosting.md](docs/self-hosting.md#metrics).

### Added

- `ohs seed --demo` fills an instance with 90 days of synthetic data for one invented person
  seen through six overlapping sources (ring, strap, scale and cuff, band, sports watch, phone),
  about 35,000 samples written through the normal processing path and marked
  `{"synthetic": true}`. It creates a `demo` tenant and user and prints an API key and calls to
  try. See [docs/demo.md](docs/demo.md).
- Client SDKs 0.1.0 under [sdks/](sdks), AGPL-3.0-only like the server, not on package registries yet:
  Kotlin (Android and JVM), Swift (iOS, macOS, Linux; with a HealthKit sync that uploads what
  changed since the last run), React Native (TypeScript, also Node and browsers) and Flutter
  (Dart). Each covers every API route with typed models, paging, problem errors, an upload
  part builder and retried uploads. CI runs their tests and each quickstart example against a
  live server. Guides: [docs/sdks](docs/sdks/README.md).
- Admin console (`OHS_CONSOLE=true`, off by default): a read-only page at `/console`, embedded in
  the binary, that reads the API with a tenant API key and shows health, users, connections,
  freshness, coverage and webhook deliveries, with stale, failing and `needs_reauth` sources in
  red. It adds no write route. See [docs/console.md](docs/console.md).
- RR intervals and HRV: `GET /v1/users/{id}/rr-intervals` serves the stored beat-to-beat
  intervals of heartbeat series, and `GET /v1/users/{id}/hrv` the RMSSD, SDNN, pNN50, mean RR
  and mean heart rate derived from each series, after implausible and ectopic intervals are set
  aside. MCP tools `get_rr_intervals` and `get_hrv`. See [docs/hrv.md](docs/hrv.md).
- Menstrual cycles: `GET /v1/users/{id}/menstrual-cycles` and the MCP tool
  `get_menstrual_cycles` derive cycles at read time from the stored `menstrual_flow` samples of
  every provider, so they follow every new sample and deletion: each cycle's start day, its
  period's flow days (the heaviest flow of each local day, with the providers that recorded it),
  and its length once the next period is recorded. A gap of up to 2 days without flow stays in
  one period; intermenstrual bleeding never starts one. Nothing is predicted. Health Connect
  `MenstruationFlow` and `IntermenstrualBleeding` records are now stored, in the same category
  types as Apple Health's. See [docs/menstrual-cycles.md](docs/menstrual-cycles.md).
- Time tags: intervals that give context to a user's data (`illness`, `injury`, `travel`,
  `jet_lag`, `vaccination`, `medication`, `alcohol`, `other`), with an optional label, note and
  IANA time zone, and an open end while ongoing. `POST`, `GET`, `PATCH` and `DELETE` under
  `/v1/users/{id}/tags`; the list keeps the tags that overlap a window. `tag.created`,
  `tag.updated` and `tag.deleted` webhook events, and the read-only MCP tool `get_tags`. Tags are
  deleted with their user. See [docs/time-tags.md](docs/time-tags.md).
- FHIR beyond Observations: `GET /v1/users/{id}/fhir/Patient/{id}` serves the user as a
  Patient (the tenant's `external_ref` as its identifier), `/fhir/Device` and
  `/fhir/Device/{id}` the devices the records came from, with stable ids derived from their
  provenance, and `/fhir/Provenance?target=Observation/{id}` and `/fhir/Provenance/{id}` who
  produced each Observation (its device as author, the provider as assembler, the provider's
  record as entity). `/fhir/Patient/{id}/$everything` pages through the Patient, the
  Observations, their Devices and Provenance in one searchset Bundle, with the Observation
  search's cursor, and `/fhir/metadata` serves a CapabilityStatement. `Observation.device` is
  now a reference to the Device, and keeps its `display`. The HL7 validator runs in CI on every
  change to the mapping (`.github/workflows/fhir-validate.yml`). See [docs/fhir.md](docs/fhir.md).
- Helm chart in `deploy/helm/ohs` for Kubernetes, against a Postgres with TimescaleDB that you
  provide: one Deployment running every role (or one per role), a `pre-install`/`pre-upgrade`
  Job for the migrations, a volume or S3 raw store, a generated encryption key kept across
  upgrades, and optional Ingress, ServiceMonitor, NetworkPolicy, autoscaler and disruption
  budget. CI lints and renders it and installs it on kind. See [docs/helm.md](docs/helm.md).
- Deploy templates for Render (`render.yaml`, a Blueprint), Railway (`railway.json`) and Fly.io
  (`deploy/fly/`): the API built from `deploy/Dockerfile` with a TimescaleDB database next to it,
  health checks on `/readyz` and a volume for the raw store. CI checks the templates against the
  configuration. See [docs/deploy-platforms.md](docs/deploy-platforms.md).
- OpenTelemetry tracing, off by default: set `OTEL_EXPORTER_OTLP_ENDPOINT` to export traces
  over OTLP (HTTP/protobuf, or gRPC with `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`). Spans cover API
  requests (named by route pattern, joining an incoming W3C `traceparent`), pull jobs, ETL
  jobs, webhook delivery attempts and the database queries made within them; log lines inside a
  span carry `trace_id` and `span_id`. Spans never record a raw path, a query string, a
  header, an SQL argument, a secret or a health value. See
  [docs/observability.md](docs/observability.md).
- Complete user archive (data portability): `GET /v1/users/{id}/archive` streams everything
  the instance holds about one user as one zip file: a manifest with record counts, the user
  and its connections (never a token), every record of every list as NDJSON, a FHIR R4
  `collection` Bundle, the Open mHealth data points and every raw payload byte for byte. It
  takes a tenant key, or a user token with scope `read` for its own user. See
  [docs/exports.md](docs/exports.md#complete-user-archive-data-portability).
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
- Learn by building: seven short beginner projects on your own data (first sync, sleep report,
  HRV trend, an AI assistant over MCP, training against sleep, a dashboard, a webhook that writes
  a morning line) and a prompt library for people who train. See
  [docs/learn](docs/learn/README.md).
- Recorded connector contracts: `make record PROVIDER=<slug>` records what a real account's API
  answers (or an Apple Health export), anonymized on the contributor's machine, and CI replays
  every committed recording through its connector. The first one is an anonymized three day
  Apple Watch export covering 23 data types. See
  [docs/connectors/recording.md](docs/connectors/recording.md).
- Sync history and connection health: every pull is a sync run with its trigger, window, status,
  attempts, stored raw objects and provider error (`GET /v1/users/{id}/sync-runs`, kept 30
  days). `POST /v1/users/{id}/connections/{provider}/sync` queues a priority pull. Connections
  carry a `health` object (`needs_reauth`, `missing_scope`, `failing`, `rate_limited`, with the
  action to take), and the `connection.broken` and `connection.restored` webhook events announce
  each change. Migration `0026`. See [docs/sync-health.md](docs/sync-health.md).
- Webhook delivery sandbox: each delivery keeps its attempts (status, duration, error, the first
  1 KiB of the answer). `GET /v1/webhooks/{id}/deliveries/{delivery_id}` shows the exact payload
  sent and the attempt history, `POST .../replay` sends a finished delivery again with the same
  event id, and `POST /v1/webhooks/{id}/test` sends a realistic sample of any event type, marked
  `"test": true`. Migration `0031`. See [docs/webhooks.md](docs/webhooks.md).
- Hosted connection page: `POST /v1/users/{id}/connect-sessions` returns a short-lived link to
  a page served by the instance that lists the allowed providers with the user's connection
  status and starts the provider's consent, so a backend needs no OAuth UI of its own. Tokens
  are stored hashed. Migration `0021`. See [docs/connect-page.md](docs/connect-page.md).
- `scripts/backup.sh` and `scripts/restore.sh` for the compose stack: one archive with a
  `pg_dump` of the database (run inside the database container), the raw files and a manifest
  with versions and checksums; restore handles TimescaleDB's pre and post restore steps and
  refuses an archive newer than the running release. `ohs migrate latest` prints the schema
  version a binary migrates to. The full cycle runs in CI (`make backup-test`). See
  [docs/backup-and-restore.md](docs/backup-and-restore.md).
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
- Source priorities and a merged view. When several sources recorded the same thing (a ring
  and a watch the same night, a phone and a watch the same steps, one sample directly and
  through an aggregator), the reads now list only the best ranked source's record by default,
  so totals and summaries count nothing twice. Every raw record stays stored, and `view=all`
  returns all of them as before. The order of sources is resolved per metric (metric, then
  group, then `*`, then built-in defaults) and set per user with
  `GET`/`PUT /v1/users/{id}/source-priorities`, stored in `ohs_sync.source_priorities`
  (migration `0032`). Overlap is judged per kind of metric: any shared time for counts such as
  steps, 5 minutes for readings such as heart rate, 12 hours for nightly figures, an hour for
  body measurements, half the shorter session for sleep and workouts. `GET .../daily` merges
  one summary per day with each figure from its best ranked source and `sources` naming it;
  the FHIR search leaves out covered records and shows a daily summary's own figures only. The
  MCP read tools take the same `view`. An optional `fusion.Scorer` (`api.Deps.Scorer`) can rank
  the sources of metrics a user has not ordered, from a quality measure.

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
- Documentation website (`docs-site/`): the guides and an API reference generated from
  `api/openapi.yaml`, served under `/documentation`, with a sitemap, schema.org data, an
  `llms.txt` with full addresses and an alternatives page.
- README rewritten around a first screen with every source and output, a roadmap at a glance and
  use cases.
- `mcp-registry.yml` publishes `server.json` to the official MCP registry with GitHub OIDC, when a
  release is published or by hand.
- A fuzz test of the Apple Health upload parser (`FuzzParseUpload`), run for 30 seconds in CI.
  Besides crashes, it checks that no parse error quotes the upload body and that no mapped sample
  is NaN or infinite. Thanks to @yashthakur2008, the first outside contributor (#87).

### Fixed

- The merged view tells apart the apps behind Apple Health for sleep and workouts. A night the
  watch and the phone (or a sleep app) both recorded was returned twice; the watch's session now
  covers the others. On a real export of six years, 1,310 of 14,662 sessions were such doubles.
- Apple Health walking and running speed are stored. HealthKit spells them in `km/hr` (or
  `mi/hr`), which had no conversion to `m/s`, so every such sample was parked as a unit error. An
  instance restarted on this version replays the parked records. The recorded-contract replay now
  applies the same unit conversion as the ETL, so CI catches the next unit with no conversion.
- `OHS_<SLUG>_<FIELD>` keys of a provider slug that contains an underscore (`intervals_icu`) are
  now read under the whole slug.

### Changed

- The sample, category sample, sleep, workout, daily, FHIR, Open mHealth and export reads
  default to the merged view (see Added). A client that needs every source's rows passes
  `view=all`; `GET .../daily` returns one summary per day instead of one per provider and day.
  The recipes read with `view=all`, since they pick or compare the apps themselves.
- Pulls are claimed in order: notifications and manual syncs first, then regular pulls, then
  backfills. At most `OHS_PULLS_PER_PROVIDER` (default 4) pulls of one provider run at once
  across every instance, `OHS_BACKFILLS_PER_PROVIDER` (default 2) of them backfills. A rate
  limit pauses every pull of its provider (Retry-After, at least 30 seconds doubling per limit
  in a row, at most 15 minutes) until the next success; `/metrics` adds
  `ohs_sync_provider_paused_seconds`. Migration `0027`. See
  [docs/background-sync.md](docs/background-sync.md#provider-rate-limits).
- The release workflow stops at a draft GitHub release with its binaries, checksums and image;
  a maintainer reads it and publishes it.
- Docker Compose publishes the API on `127.0.0.1` by default, so plain http is reachable from
  this machine only. Set `OHS_HTTP_BIND=0.0.0.0` to publish it on every interface, or put a
  reverse proxy with TLS in front (before: every interface).
- The forbidden-terms guard names only the file and line of a match in CI logs, never the term.
- Every GitHub Action in the workflows is pinned to a commit SHA.
- CI upgrades a v0.0.2 database to the current code and checks that its data is still served
  (`make upgrade-test`), then migrates it back down and up again.
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

### Security

- CI checks the gitleaks release tarball against a pinned sha256 (taken from the release's
  checksums file) before running it.

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

[Unreleased]: https://github.com/soma-intelligence/open-health-sync/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/soma-intelligence/open-health-sync/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/soma-intelligence/open-health-sync/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/soma-intelligence/open-health-sync/compare/v0.0.2...v0.1.0
[0.0.2]: https://github.com/soma-intelligence/open-health-sync/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/soma-intelligence/open-health-sync/releases/tag/v0.0.1
