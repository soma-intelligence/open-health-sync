# Architecture

Open Health Sync is one Go binary, `ohs`, and one Postgres database with the TimescaleDB
extension. The binary runs up to three roles in one process or split across several.

```
 providers (OAuth pull, push notifications)      iOS apps (HealthKit)
              |                                         |
              v                                         v
   +------------------------- ohs binary --------------------------+
   | collect: OAuth, token refresh, pull scheduler, inbound hooks   |
   | api:     tenants, users, tokens, connect flow, uploads, reads, |
   |          MCP, health and metrics                               |
   |              |  raw file first, then a job row in Postgres     |
   | etl:     read raw -> connector Map -> canonical records ->     |
   |          units, validation, dedup, attribution -> Postgres     |
   +---------------------------------------------------------------+
```

## Roles

`OHS_ROLES` is a comma separated list, `api,collect,etl` by default.

| Role | Runs |
|---|---|
| `api` | The HTTP server: public REST API, upload endpoint, OAuth callback, inbound hooks, MCP, health and metrics. |
| `collect` | Background sync: pulls on push notifications within seconds, plus a backstop pull every 6 hours while notifications arrive and every hour when they do not; renews provider subscriptions that went silent; OAuth token refresh. See [background sync](background-sync.md). |
| `etl` | The ETL workers (two tiers, `standard` and `high_freq`, four jobs at a time each), the deletion worker (users, and one provider's data of a user), the outgoing [webhook](webhooks.md) sender, and the upkeep jobs described under Reliability. |

One process with all roles is the simplest deployment. To scale, run several `api` instances
behind a proxy and a separate `etl` process. The deletion worker lives in the `etl` role: a
deployment that runs `api` without `etl` anywhere never completes user deletions.

## Pipeline

1. **Capture.** A device upload (`POST /v1/ingest/apple-health`), a provider pull, or a
   provider notice a pull cannot fetch back (a deleted record) writes the payload to the raw
   store, verbatim, as gzip NDJSON, one data type per object. The key is
   `raw/<tenant>/<user>/<provider>/<yyyy-mm-dd>/<batch>_<part>.ndjson.gz`. A raw object is
   never rewritten.
2. **Queue.** In the same transaction as the `raw_objects` row, an `etl_jobs` row is inserted.
   Jobs are claimed with `FOR UPDATE SKIP LOCKED`.
3. **Map.** The ETL reads the object, parses each line and hands it to the owning connector's
   `Map`, a pure function that returns canonical records (see [data-model.md](data-model.md)).
4. **Process.** Units are converted to the storage unit of each quantity name, values are
   validated against plausible ranges (hard bounds reject a record, soft bounds flag it),
   duplicates are dropped by content hash, and sources are attributed.
5. **Write.** A deletion the provider reported (`MapResult.Deletions`) is applied first: the
   stored rows of that provider record go (its sleep or workout with their children, and the
   samples stored under its id or `<id>_<suffix>`, found through an index on provider and
   external id), the daily summary figures it filled are cleared when the deletion names them,
   and a tombstone in `ohs_data.deleted_records` keeps any later copy of it from being written.
   A Replaceable sleep, workout or (opted in with `SupersedeMoved`) quantity sample whose start
   moved replaces the stored copy of the same provider record instead of adding a second one. Records go to the `ohs_data` hypertables. Daily totals for step count, energy,
   distance and flights are derived per user, provider and local day when the provider sent no
   daily summary of its own. The local day comes from the sample's `timezone` metadata (UTC when
   absent). Where several sources report the same day, the largest per-source sum wins, so a
   phone and a watch are not added together.
6. **Read.** The REST API and the MCP server read through one query layer that always filters on
   `tenant_id` and `user_id`.

Because the raw payload is the source of truth, a user can be re-processed from the raw store
after a mapper fix ([self-hosting](self-hosting.md#operating-notes) has the requeue recipe).

## Storage

Three schemas:

- `ohs_core`: tenants, API keys, end users, user tokens, idempotency keys, deletion jobs.
- `ohs_sync`: connections with encrypted OAuth tokens, OAuth states, pull jobs and coverage,
  the sync log, inbound subscriptions, raw object registry, ETL jobs.
- `ohs_data`: the canonical tables (hypertables for time series, plain tables for sleep phases,
  workout tracks and the reference tables `quantity_codes` and `device_aliases`), plus
  `failed_records`.

Every row in `ohs_sync` and `ohs_data` carries `tenant_id` and `user_id`, except the two
reference tables. `quantity_samples`, the largest table, is compressed once a chunk is 30 days
old (about 7 times smaller on disk). Compression is a Timescale License feature, included in
the default `timescale/timescaledb` image; on an Apache-only build the migration leaves the
table uncompressed and everything else works the same. No continuous aggregates are used.

Duplicates are dropped on write. Most tables do it with a unique index on `(tenant_id,
user_id, content_hash, <time>)`. `quantity_samples` has no such index, because checking a unique
index on a compressed chunk decompresses whole batches: the writer reads the stored hashes of
the batch's time window for that user and inserts only the missing rows, under a per-user lock.

The raw store is a local folder (`OHS_RAW_STORE=file:///var/lib/ohs/raw`) or any S3 compatible
store (`s3://bucket?endpoint=...&region=...&path_style=true`). Writes are conditional (never
overwrite), which S3 stores must support through `If-None-Match`.

Secrets: API keys and user tokens are stored as SHA-256 hashes only. OAuth access and refresh
tokens are sealed with AES-256-GCM under `OHS_ENCRYPTION_KEY`, bound to
`tenant_id|user_id|provider` so a stolen ciphertext cannot be moved to another row. Each sealed
value names the key that sealed it, so the key can be rotated online
([key rotation](key-rotation.md)). Logs never contain tokens, keys or health values.

## Tenancy and auth

A tenant is a customer of the instance with one or more API keys. An end user belongs to one
tenant. Your server holds a tenant key; your apps get short-lived user tokens scoped to one user
and to `upload`, `connect` or `read`. A user or object of another tenant reads as 404. See
[api.md](api.md).

## Connectors

A connector implements the contract in `internal/connector` and registers itself in `init()`.
Three kinds, and one connector may be several:

- `OAuthConnector`: the server pulls (`AuthorizeURL`, `Exchange`, `Refresh`, `Fetch`).
- `PushConnector`: the provider notifies (`HandleInbound`).
- `DeviceConnector`: an SDK uploads (`ParseUpload`).

A connector is enabled when its keys (`OHS_<PROVIDER>_CLIENT_ID` and so on) are set; a device
connector needs none. See [writing-a-connector.md](writing-a-connector.md).

## Reliability

- **Crash safety.** Ingest writes the raw file first, then the `raw_objects` and `etl_jobs`
  rows in one transaction. A crash between the two leaves an orphan raw object, which the
  orphan scanner queues. The scanner looks at the last 7 days only.
- **Retries.** A failed ETL job is retried with exponential backoff (30 s, doubling) and parked
  after 5 attempts. A claimed job that is not settled within 10 minutes is claimed again.
  Parked jobs show in `parked_jobs` of the coverage report and need an operator.
- **Drainer.** Records that failed for a recoverable reason (mapping, unit, or a whole object
  that failed) are replayed once per process start, so a fixed binary picks them up after a restart. Records
  that failed deterministically (parse, validation) are closed and kept for inspection.
- **Reconciler.** Reports what the pipeline lost or stalled on.
- **Rate limits.** A provider 429 defers that connection's job until `Retry-After`, without
  counting a failed attempt.
- **Re-authorization.** A refused refresh token marks the connection `needs_reauth`, visible in
  the API.
- **Retention.** `OHS_RETENTION_DAYS` prunes old rows and raw files; 0 keeps everything.
- **Deletion.** `DELETE /v1/users/{id}` is a hard delete of raw files and rows, run by a
  background worker that survives restarts.
