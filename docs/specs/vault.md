# Spec: health data vault

Status: design approved 2026-10-27, not built. Plan: to be written.

## Problem

Open Health Sync encrypts the secrets it holds (provider OAuth tokens, webhook secrets, provider
keys) under `OHS_ENCRYPTION_KEY`, but the health data itself is stored in clear
([security-model.md](../security-model.md#what-is-protected)): every sample, sleep phase,
workout, daily summary and self-report sits readable in `ohs_data`, and the raw store keeps
every provider and device payload as plain gzip. A copied database, a leaked backup, a
misconfigured bucket or a stolen disk gives away every person's health history.

The vault closes that. After it, a copy of Postgres, of the raw store or of a backup holds no
readable health value, and every feature that works today keeps working: ingestion, ETL,
rollups, REST reads, stats, FHIR, Open mHealth, MCP, exports, share links and webhooks.

## Goals

1. Every health value at rest is sealed under a key that belongs to one user.
2. A database dump, a raw store copy or a backup, without the root key, reveals no health value
   and no value-derived hash that can be guessed back.
3. Ingestion can be run by processes that can seal but never open what is stored.
4. Erasing a user makes every row and object left behind unreadable, before the rows are gone.
5. The root key can live in a file, an environment variable or an external KMS/HSM, chosen by
   the operator; the default works on a Raspberry Pi with no external service.
6. Keys rotate without re-encrypting the data.
7. One code path: the vault is always on, no instance runs with health data in clear once
   migrated.

## Non-goals

- **Zero knowledge.** A running `ohs` process that holds the root key can read every user's data,
  and so can the instance operator who runs it. Server-side features (ETL, rollups, FHIR, MCP,
  webhooks) need the values; keeping all of them was the point of this design. A
  client-held-key mode is a separate, later question.
- **Hiding the shape of the data.** Who has data, of which type, from which provider, at which
  times, stays visible to whoever reads the database (see [What stays in clear](#what-stays-in-clear)).
  Encrypting timestamps and types would make every range query a full scan of a user's history.
- **Data in transit to the tenant.** Webhooks, API answers and exports still reach the tenant in
  clear over TLS, as today. Share link files are already end to end encrypted and stay so.

## Threat model

| Attacker holds | Before | After |
|---|---|---|
| A database dump or replica | Every health value | Index columns only; values sealed |
| A raw store copy (folder, bucket, snapshot) | Every payload | Sealed objects only |
| A backup of both | Everything | Nothing readable without the root key |
| A backup and the root key | Everything | Everything still in that backup, deleted users included (see [Erasure](#erasure-and-old-backups)) |
| A process running only the `collect` role | Every value it can query | No stored value: it holds no private key. It still holds provider OAuth tokens, so it can pull fresh data from the providers. |
| A process running `api` or `etl` | Everything | Everything (non-goal) |
| A tenant API key | Its tenant's data through the API | Same; the vault changes nothing above the store |

## Key hierarchy

```
root key            (KeyProvider: local file/env, Azure Key Vault, AWS KMS, HashiCorp Vault)
  └─ tenant key     32 random bytes per tenant, stored wrapped by the root key
       └─ user keys per user, stored wrapped by the tenant key:
            data key     32 bytes, XChaCha20-Poly1305 for rows
            index key    32 bytes, HMAC-SHA-256 for dedup hashes and group keys
            KEM secret   ML-KEM-1024 seed (64 bytes) + X25519 private key (32 bytes)
          + KEM public   ML-KEM-1024 encapsulation key + X25519 public key, in clear
```

Every key carries a version. New writes use the newest version; reads accept every version
still stored.

Algorithms, all from the Go standard library except XChaCha20-Poly1305
(`golang.org/x/crypto/chacha20poly1305`, already a dependency of the module's tree):

| Use | Algorithm | Why |
|---|---|---|
| Row and object payloads | XChaCha20-Poly1305, 24 byte random nonce | Random nonces are safe at any volume (192 bits); no GCM nonce budget per key. AES-GCM-SIV is not available in Go. |
| Wrapping keys (local provider) | XChaCha20-Poly1305 | Same primitive, one implementation to review |
| Dedup and group digests | HMAC-SHA-256 under the user's index key | A plain SHA-256 of a heart rate can be guessed back from 200 candidates |
| Sealing raw objects | ML-KEM-1024 + X25519 hybrid KEM, combined with SHA3-256 | Post-quantum and classical: secure while either holds |
| Key derivation | HKDF-SHA-256 | |

Symmetric 256-bit keys are already safe against a quantum attacker; the hybrid KEM is where the
post-quantum choice matters, because it is the only public-key step.

### Hybrid KEM

Sealing an object for user U (no private key needed):

1. `ss_m, ct_m = ML-KEM-1024.Encapsulate(U.mlkem_pub)`
2. `e = X25519 ephemeral key`, `ss_x = X25519(e, U.x25519_pub)`, `ct_x = e.pub`
3. `k = SHA3-256("ohs.vault.kem.v1" || ss_m || ss_x || ct_x || U.x25519_pub)`, the X-Wing
   combiner shape with ML-KEM-1024 in place of ML-KEM-768 (ML-KEM's ciphertext binding
   makes hashing `ct_m` unnecessary; `ct_x` and the X25519 public key are hashed because X25519
   has no such binding).
4. The object is streamed under `k` (see [Sealed object](#sealed-object)).

Opening needs the user's KEM secret, so only processes that unwrap user keys can do it.

## Formats

### Sealed row

Each sealed table gets one `sealed bytea` column holding every sensitive column of the row,
in one AEAD. The plaintext is a small versioned binary encoding written in the vault package
(a format byte, then tag, length and value per field; float64 values as their IEEE 754 bits, so
a value comes back bit for bit), which adds no dependency:

```
0x02 || user key version (uint32 BE) || nonce (24) || ciphertext || tag (16)
```

AAD: `"ohs.vault.row.v1" || tenant_id || user_id || table name || row identity`, where the row
identity is the table's dedup key (for samples: `content_hash || start_date`). A sealed value
moved to another row, user, tenant or table fails to open. One AEAD per row keeps the overhead at
45 bytes per row instead of 45 per column.

### Sealed object

Raw store objects, and any export file kept at rest:

```
"OHSV" || 0x01 || user key version (uint32) || ct_m (1568) || ct_x (32) || chunks...
chunk: nonce (24) || ciphertext of up to 64 KiB || tag (16)
```

STREAM construction: each chunk's AAD is the header, the chunk index (uint64) and a final-chunk
flag, so chunks cannot be reordered, dropped or truncated. Objects are gzipped before sealing,
as today; the object key path stays `raw/<tenant>/<user>/...`.

### Wrapped keys

`ohs_core.vault_keys`:

| Column | |
|---|---|
| `tenant_id`, `user_id` (null for the tenant key), `version` | Primary key |
| `wrapped` | The key material sealed by the parent key (root for a tenant key, tenant for user keys), AAD = tenant, user, version |
| `parent_version` | Which root or tenant key version wraps it |
| `kem_public` | User keys only: the ML-KEM-1024 and X25519 public keys, in clear |
| `created_at`, `retired_at` | |

A user's keys are created with the user (`end_users` insert, same transaction).

## What is sealed

Every table of `ohs_data` that holds a value about a person:

`quantity_samples`, `category_samples`, `heartbeat_samples`, `ecg_recordings`, `ecg_samples`,
`sleep_sessions`, `sleep_phases`, `workouts`, `workout_tracks`, `route_points`,
`nutrition_logs`, `observations`, `daily_summaries`, `time_tags`, `failed_records`, the derived
rollup and profile tables when they land, self-reports, and the new
`quantity_hourly_sealed`. Plus every raw store object and every export file at rest.

Not sealed, because they hold no health value: `quantity_codes` (a static code list),
`deleted_records` (provider ids of deleted records; the plan confirms each column),
`record_aliases` and `device_aliases` (ids only; the plan confirms each column).

### What stays in clear

The columns queries filter, sort or join on, and nothing else:

- `id`, `tenant_id`, `user_id`, `provider`, record type (`quantity_type`, `category_type`, ...)
- `start_date`, `end_date`, `summary_date`, `ingested_at`
- `unit`, `source_slug`, `is_primary_source`, and a new boolean `worn` (computed by the ETL from
  the device fields, which are sealed, so the merged view can still rank sources in SQL)
- `content_hash`, now an HMAC under the user's index key (see below)
- `external_id`: the provider's own record id, needed to apply provider deletions; it carries no
  value

Everything else is sealed: values, `metadata`, `source_name`, `source_bundle_id`, `device_id`,
`device_model`, `product_type`, `was_user_entered`, notes, routes, ECG voltages.

### Keyed hashes

`content_hash` today is a SHA-256 over the record's content. Over a small value space (a heart
rate, a step count in an hour) that hash can be reversed by trying every candidate. It becomes
`HMAC-SHA-256(index key, same canonical content)`. Dedup keeps working because the mapping is
one to one per user. The same key makes the group key of a sealed source:
`source_key = HMAC(index key, source)` replaces the clear source string in the hourly table.

`daily_summaries` and the other upserts compare rows with `IS DISTINCT FROM` on value columns.
Sealed bytes differ on every write (fresh nonce), so the comparison moves to a `content_mac`
column: the HMAC of the row's plaintext. An unchanged row is still not rewritten.

## Aggregates move to Go

Today SQL computes on values in three places: the TimescaleDB continuous aggregate
`ohs_data.quantity_hourly` (migration 0035), the raw-sample fallback in
`internal/api/read/stats.go`, and the daily sums in `internal/etl/daily.go` and the sleep totals
in `internal/etl/sleep_sessionize.go`. None of them can sum sealed bytes.

- **Hourly table.** `ohs_data.quantity_hourly_sealed` (tenant, user, quantity_type, provider,
  source_key, slug, worn, unit, bucket, sealed `{n, sum, min, max}`), written by the ETL inside
  the job's transaction for every hour the job touched: it opens that hour's samples, aggregates
  in Go and upserts the bucket. Provider deletions and imports recompute the hours they touch the
  same way. The continuous aggregate and its policy are dropped.
- **Stats reads.** SQL selects hourly rows by index (user, type, bucket range); Go opens them and
  folds them into the requested interval and time zone. Hours stay the unit, so zones with a
  whole-hour offset read only hourly rows, as today; other zones read samples and fold them in
  Go.
- **Daily sums and sleep totals.** Same pattern: SQL selects, Go opens and sums, Go writes the
  sealed result.
- **Value filters.** Any API filter on a value (if the plan finds one) runs in Go after opening,
  with the same page size limits.

Reads stream rows and keep memory bounded per request, so a year of samples does not sit in
memory at once.

## Data flow

1. **Ingestion** (device upload on `api`, provider pull and inbound notification on `collect`):
   the payload is gzipped and sealed to the user's KEM public key, then written to the raw store
   (`internal/collect/rawobject.go`, the single write point). This step reads only public keys.
2. **ETL**: unwraps the user keys (cache), opens the raw object, normalizes, seals each row,
   writes rows, hourly buckets and daily summaries in the job's transaction.
3. **Reads** (REST, stats, FHIR, OMH, MCP, exports, share links, webhooks): SQL filters on clear
   columns, the store layer opens rows; everything above the store layer is unchanged.

### Roles and keys

A process unwraps tenant and user keys only when it runs `api` or `etl`. A process started with
`OHS_ROLES=collect` alone never calls the KeyProvider and holds no private key: it seals raw
objects and cannot open them or any row. In the default deployment one process runs all three
roles and so holds everything; the write-only boundary exists only when the operator splits
roles. The docs say so in those words.

### Key cache

Unwrapped user keys live in an in-memory LRU with a time to live (default 10 minutes, 1024
users), sized down by the low-memory profile. Entries are zeroed on eviction (best effort in
Go: the byte slices are overwritten; copies made by the runtime are out of reach). Tenant keys
follow the same cache. A KMS call happens once per tenant per TTL, not per request.

## KeyProvider

```go
type KeyProvider interface {
    Name() string
    Wrap(ctx context.Context, plain, aad []byte) (wrapped []byte, version string, err error)
    Unwrap(ctx context.Context, wrapped, aad []byte, version string) ([]byte, error)
}
```

| Provider | `OHS_VAULT_KEY_PROVIDER` | Settings |
|---|---|---|
| Local (default) | `local` | `OHS_VAULT_ROOT_KEY` or `OHS_VAULT_ROOT_KEY_FILE` (32 bytes, base64), previous keys in `OHS_VAULT_ROOT_KEY_PREVIOUS`. Unset: derived from `OHS_ENCRYPTION_KEY` with HKDF (`"ohs.vault.root.v1"`), so an upgrade needs no new secret, and `ohs doctor` warns that one secret then guards both. |
| Azure Key Vault / Managed HSM | `azurekv` | `OHS_VAULT_AZURE_KEY_ID` (key URI); auth by managed identity or the standard Azure environment. Wrap = RSA-OAEP-256 or AES-KW on Managed HSM. |
| AWS KMS | `awskms` | `OHS_VAULT_AWS_KEY_ARN`; standard AWS credentials chain |
| HashiCorp Vault transit | `hashicorp` | `OHS_VAULT_HASHICORP_ADDR`, `_MOUNT`, `_KEY`, token from `VAULT_TOKEN` or a file |

The hosted instance uses `azurekv`. Cloud SDKs are imported only by their provider package, so
the binary for a Pi does not grow unless they are built in (build tags decided in the plan).

## Erasure and old backups

Deleting a user (`deletion_jobs`) first deletes the user's rows in `ohs_core.vault_keys` and
evicts them from every cache (a `NOTIFY` on a vault channel), in its own committed transaction,
then removes rows and raw objects as today. From that commit on, whatever the deletion has not
yet removed (rows, objects, replicas, a crash halfway) cannot be opened.

Old backups are different: a backup taken before the deletion holds the user's wrapped keys,
and opens with the root key. Erasure there needs one of:

- the backup's normal expiry (the operator's retention), or
- a root key rotation (`ohs vault rotate root`) after which the previous root key version is
  destroyed in the KMS: every backup older than the rotation becomes unreadable as a whole.

The docs state this plainly; the vault does not claim to erase a deleted user from existing
backups on its own.

## Rotation

- `ohs vault rotate root`: new root version, every tenant key rewrapped. Seconds, not hours.
- `ohs vault rotate tenant <id>`: new tenant key, that tenant's user keys rewrapped.
- `ohs vault rotate user <tenant> <user>`: new user key version; new writes use it;
  `ohs vault reseal` rewrites old rows in the background (optional, resumable). Needed after a
  suspected user key leak, not for routine rotation.
- `OHS_ENCRYPTION_KEY` rotation (`ohs rekey`) is unchanged and independent.

## Migration of existing instances

The vault is always on, so an upgrade must convert existing data without downtime.

1. **Release N** adds the vault tables, the `sealed`, `content_mac` and `worn` columns
   (nullable) and the hourly sealed table. New writes are sealed only. Reads take `sealed` when
   present and the clear columns otherwise. Startup creates missing tenant and user keys.
2. `ohs vault seal` converts old rows user by user, in batches, resumable after a stop: seal the
   row, recompute `content_hash` as an HMAC, fill `worn` and `content_mac`, null the clear
   columns, in one transaction per batch. Raw objects are rewritten sealed. The hourly table is
   rebuilt for each converted user. `ohs vault status` reports what is left.
3. **Release N+1** drops the clear columns and the continuous aggregate. Its migration refuses
   to run while `ohs vault status` would report a clear row, with a message naming the command.

Migration numbers are assigned when the branch merges (the derived metrics branch already
claims 0044 and 0045).

## Errors

- **Root key unavailable** (KMS down, wrong key, missing file): an `api` or `etl` process refuses
  to start; a running one answers 503 on `/readyz` and on reads that need an uncached key. It never
  writes in clear and never serves an empty answer for data it could not open.
- **A row or object that fails to open** (tag mismatch, AAD mismatch, unknown key version):
  the request fails with 500 and the request id, an audit event `vault.open_failed` (tenant,
  user, table, no value) and the metric `ohs_vault_open_failures_total{kind}`. The ETL marks the
  job failed rather than skipping the object.
- **A user with no keys** (erased, or a race with a deletion): reads answer as for an absent user
  (404); writes are refused.
- Logs never contain key material, wrapped or not, nor plaintext of a sealed field.

## Observability and audit

Audit events: `vault.key_created`, `vault.key_rotated`, `vault.user_shredded`,
`vault.open_failed`, `vault.seal_progress` (from `ohs vault seal`). Metrics: opens and seals by
result, cache hits and misses, KMS latency, rows left in clear (until release N+1).

## Performance targets

Measured with `bench/` on the demo data set (30 days, 6 providers) before and after:

- stats over 30 days: at most 2x today's latency at p50 and p95
- ETL throughput: at most 20% lower
- raw object write: at most one KEM operation per object, never per row
- storage: at most 25% more for `ohs_data`

Missing a target is a finding for the plan's review, not a reason to drop a sealed column.

## Tests

- **Unit**: row and object round trips; one flipped bit anywhere fails; a sealed value moved to
  another row, user, tenant, table or key version fails; truncated and reordered chunks fail;
  hybrid KEM against the Go ML-KEM test vectors and a fixed X25519 vector; key wrap per provider
  with fakes; cache eviction zeroes keys.
- **Leak test**: ingest the demo data set, then `pg_dump` the database and walk the raw store;
  neither may contain any of a list of planted canary values (a unique heart rate, a note, a
  device name) nor the plain SHA-256 of any sample.
- **Equivalence**: stats, daily summaries, sleep totals, FHIR bundles, OMH and exports from the
  demo data set are byte for byte the same with and without the vault (the fixture built from
  `main` before the change).
- **Roles**: a `collect`-only process seals and fails to open; it makes no KeyProvider call.
- **Erasure**: after the key deletion commits, a crash before rows are removed leaves rows that
  fail to open; a later pass removes them.
- **Migration**: `ohs vault seal` stopped at random points and restarted converges; release N+1
  refuses to migrate while clear rows remain; the contract tests stay green.
- **Fuzz**: opening rows, objects and wrapped keys from arbitrary bytes never panics.
- **Providers**: `local` in every test run; `azurekv`, `awskms`, `hashicorp` against their
  emulators or fakes in CI, with a manual check of the hosted instance's Azure Key Vault.

## Documentation

Updated with the code: [security-model.md](../security-model.md) (the asset table, a vault
section with the threat model above, the operator and backup limits),
[hardening.md](../hardening.md), [key-rotation.md](../key-rotation.md),
[backup-and-restore.md](../backup-and-restore.md) (backups need the root key; losing it loses
the data), [self-hosting.md](../self-hosting.md) (the new variables), and the CHANGELOG.

## Decisions

| # | Decision | Chosen over |
|---|---|---|
| V1 | Protect against stolen database, raw store and backups; server keeps reading | Zero knowledge (breaks server features) |
| V2 | Pluggable root key, local default | Env only; KMS required |
| V3 | Seal all health content, keep index columns in clear | Seal timestamps too; raw store only |
| V4 | Aggregates computed in Go from sealed hourly buckets | Vault as an option with two code paths; numbers left in clear |
| V5 | Symmetric envelope plus hybrid ML-KEM-1024 + X25519 sealing at ingestion | Symmetric only; a KEM per record |
| V6 | Vault always on, `ohs vault seal` for existing data | A per-instance switch |
| V7 | XChaCha20-Poly1305 | AES-256-GCM-SIV (not in Go) |
| V8 | One sealed blob per row | One AEAD per column |
| V9 | Keyed HMAC for dedup and group keys | Plain SHA-256 |
| V10 | Erasure deletes keys first; old backups need expiry or root rotation | Claiming backup erasure |
