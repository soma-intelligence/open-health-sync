# Self-hosting Open Health Sync

## Quick start

Needs Docker Compose v2.24 or later, and `openssl`. Run every command from the repository
root. This is the README sequence:

```sh
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
docker compose --env-file .env -f deploy/docker-compose.yml run --rm ohs doctor
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs admin tenant create --name dev
```

Compose reads `${...}` variables (`OHS_VERSION`, `OHS_HTTP_PORT`, `POSTGRES_PASSWORD`, `S3_*`)
from the shell or `--env-file`, so keep `--env-file .env` on every `docker compose` command.
The containers read `.env` themselves either way.

`OHS_ENCRYPTION_KEY` must hold 32 random bytes in base64; the `sed` line above fills it. Keep
it: it encrypts the OAuth tokens stored in the database. If it is empty, `ohs` exits at start
and its container restarts in a loop, never healthy. `ohs doctor` then prints a freshly
generated key: paste it into `.env` and run the same `up -d` command again, so the container is
recreated with the new environment.

The `ohs` container runs `ohs up`, which is `ohs migrate up` followed by
`ohs serve` (the runtime image has no shell, so the binary chains them). Its
healthcheck is `ohs doctor --quick` (database and raw store).

### Port, and a second install

The API is published on host port 8080. Set `OHS_HTTP_PORT` in `.env` to use another one, and
set `OHS_PUBLIC_URL` to match: `ohs doctor` cannot tell when they disagree, and OAuth callbacks
use `OHS_PUBLIC_URL`.

```sh
OHS_HTTP_PORT=8081
OHS_PUBLIC_URL=http://localhost:8081
```

The compose project is named `ohs`, which also names its containers and volumes. A second
install on the same host must use another project name and another port, or it takes over the
first one's containers and data. Run it from its own checkout (the `ohs` container reads the
`.env` at the root of the checkout its compose file sits in), give that `.env` its own
`OHS_HTTP_PORT` and `OHS_PUBLIC_URL`, and pass the project name on every command:

```sh
docker compose -p ohs-test --env-file .env -f deploy/docker-compose.yml up -d --wait
```

`COMPOSE_PROJECT_NAME=ohs-test` in the shell does the same as `-p ohs-test`. Both installs can
run the same published tag. With the build overlay, both build the same `ohs:local` tag, so the
last build is what each runs after its next restart.

## ohs doctor

`ohs doctor` prints one `ok` or `fail` line per check and exits non-zero on any
failure: configuration, database, raw store, public URL, and the required keys of
every enabled connector. For each enabled OAuth connector it prints the callback
URL to register with the provider: `<OHS_PUBLIC_URL>/oauth/<slug>/callback`.
A connector that needs keys and has none set gets a `skip` line,
`skip  connector withings: disabled (no keys)`; one with only some of its keys is a
`fail`. `--quick` checks the database and raw store only. The full run also writes and deletes
a probe object in the raw store; a host folder bind mounted at `/var/lib/ohs/raw` must be
writable by the container user (`chown -R 65532:65532 <folder>`). `ohs serve` runs the same
write check at start and stops if it fails.

## Images and architectures

Each release publishes `ghcr.io/soma-intelligence/open-health-sync` for `linux/amd64` and
`linux/arm64`, tagged with the version (`0.0.2`) and `edge` (the newest final release, never a pre-release such as `0.1.0-rc.1`).
There is no `latest` tag while the version is 0.x: pin a version. The compose file
uses `${OHS_VERSION:-0.0.2}`; set `OHS_VERSION` to move to another release.
Release binaries for Linux and macOS (amd64, arm64) and their `checksums.txt` are
attached to each [GitHub release](https://github.com/soma-intelligence/open-health-sync/releases).

The image is distroless (static, nonroot). Go cross-compiles, so no emulation is
involved: `make image` runs `docker buildx build --platform linux/amd64,linux/arm64`.
Postgres is pinned by its multi-arch index digest, so the same compose file works
on a Raspberry Pi.

### Build from source

The overlay `deploy/docker-compose.build.yml` builds the image from your checkout
(tagged `ohs:local`) instead of pulling it. It needs the `docker buildx` plugin
(`docker buildx version` answers).

```sh
docker compose --env-file .env -f deploy/docker-compose.yml -f deploy/docker-compose.build.yml up -d --build
```

Without buildx, compose falls back to the classic builder and fails with
`failed to parse platform : "" is an invalid OS component of ""`: the Dockerfile cross-compiles
with `FROM --platform=$BUILDPLATFORM`, which only BuildKit understands. Install buildx, or build
the image with the script the end to end suites use (it needs Go 1.26: it cross-compiles the
binary on the host with the Dockerfile's flags, then builds the Dockerfile's final stage around
it), and start without `--build`:

```sh
scripts/image-without-buildx.sh ohs:local
docker compose --env-file .env -f deploy/docker-compose.yml -f deploy/docker-compose.build.yml up -d
```

Add it before any other overlay (Raspberry Pi, S3). The end to end suites
(`make e2e`, `make reliability`) always build from source this way.

## Raspberry Pi 5 (4 GB)

```sh
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.pi.yml up -d
```

The overlay trims Postgres (`shared_buffers=128MB`, `effective_cache_size=512MB`,
`work_mem=4MB`, `max_connections=30`, `timescaledb.max_background_workers=4`) and
caps `ohs` at 512 MB with `GOMEMLIMIT=256MiB`.

## S3 compatible raw store

```sh
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.s3.yml up -d
```

The overlay runs VersityGW (`versity/versitygw:v1.8.0`, pinned by digest, POSIX
backend) and creates the `ohs-raw` bucket. It was chosen by pulling the
candidates anonymously (VersityGW, SeaweedFS, RustFS) and keeping the first one
that passed the `rawstore` conformance suite. Never start it with `--nometa`:
conditional writes (`If-None-Match`) depend on the stored metadata. Set
`S3_ACCESS_KEY` and `S3_SECRET_KEY` in the environment to change the demo
credentials. To use AWS S3 or another service instead, skip the overlay and set
`OHS_RAW_STORE=s3://bucket?region=...` plus the usual `AWS_*` credentials.

## Configuration

Environment variables only, documented line by line in `.env.example`.

| Variable | Default | Meaning |
|---|---|---|
| `OHS_DATABASE_URL` | set by compose | Postgres connection URL; size its pool with `pool_max_conns` (see below) |
| `OHS_ENCRYPTION_KEY` | none, required | 32 random bytes, base64. Seals stored OAuth tokens. `openssl rand -base64 32` makes one, or `ohs doctor` prints one |
| `OHS_PUBLIC_URL` | `http://localhost:8080` | Public base URL, used for OAuth callbacks and notification URLs. Use your https address |
| `OHS_RAW_STORE` | `file:///var/lib/ohs/raw` | `file:///path` or `s3://bucket?endpoint=&region=&path_style=true` |
| `OHS_ROLES` | `api,collect,etl` | Roles this process runs |
| `OHS_BOOTSTRAP_TENANT` | none | Creates this tenant at first start and logs its API key once |
| `OHS_LISTEN` | `:8080` | Listen address |
| `OHS_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`; logs are JSON on stderr |
| `OHS_METRICS` | `true` | Serve Prometheus metrics on `/metrics` |
| `OHS_RETENTION_DAYS` | `0` | Delete raw files and normalized data older than this many days; 0 keeps everything |
| `OHS_INGEST_CONCURRENCY` | `8` | Device uploads one `api` process handles at once; more answer 503 with `Retry-After`. The Pi overlay sets 2 |
| `OHS_<PROVIDER>_CLIENT_ID`, `_CLIENT_SECRET` | none | Keys of a provider; its connector is on when they are set. Every process that runs `collect` or `etl` needs them (see below) |

### Database pool size

Each process opens one Postgres pool. The compose file sets `pool_max_conns=20` (all roles in
one process). Set it in the URL form of `OHS_DATABASE_URL` (`postgres://...?pool_max_conns=20`):
the keyword/value form (`host=db pool_max_conns=20`) is not supported for it, because the
migrations and `ohs doctor` drop the `pool_` parameters from the URL form only. Elsewhere, without a setting pgx sizes it to the number of CPUs (at
least 4), which is too small once several roles share it: the ETL alone runs 4 jobs per tier on
two tiers, and the uploads, pulls, deletion worker and background jobs each hold a connection
while they work. Set the size with `pool_max_conns` in the URL:

```
OHS_DATABASE_URL=postgres://ohs:secret@db:5432/ohs?sslmode=disable&pool_max_conns=20
```

| Roles in the process | Recommended `pool_max_conns` |
|---|---|
| `api,collect,etl` (the default, and the Pi) | 20 |
| `api` only | 12 |
| `collect` only | 8 |
| `etl` only | 12 |

Keep the sum over all processes under Postgres `max_connections` (100 by default, 30 in the Pi
overlay), with a few to spare for `psql` and migrations.

### Provider keys in every role

The `collect` role pulls and subscribes; the `etl` role carries out user deletions, which revoke
grants and unsubscribe notifications at the provider. Give the provider keys to every process
that runs either role. A process without them cannot call that provider: it logs a warning, and
a deletion it carries out still deletes the data but leaves the grant valid at the provider.

Put a reverse proxy with TLS in front of port 8080; the bearer secrets travel in headers.

## Upgrading

Releases are 0.x: any of them may change the API, the configuration or the schema, so read the
[changelog](../CHANGELOG.md) and the release notes first, and take a backup (below). Then pin
the new version in `.env` and recreate the container:

```sh
# .env: OHS_VERSION=0.0.2
docker compose --env-file .env -f deploy/docker-compose.yml pull ohs
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs version
```

The container runs `ohs up`, so pending migrations apply on start; `ohs migrate version` prints
the schema version. There is no step-by-step downgrade (`ohs migrate down --all` drops
everything): to go back, run the old version on a restored backup.

## Backup and restore

A backup is three things taken together: the database, the raw store, and `.env` (it holds
`OHS_ENCRYPTION_KEY`; without the key, stored OAuth tokens cannot be read and users must
connect again). Raw files can rebuild normalized data, not users, keys or connections, so the
database matters most. With the bundled compose stack (project `ohs`, so the volume is
`ohs_raw`; replace `ohs` with your project name):

```sh
DC="docker compose --env-file .env -f deploy/docker-compose.yml"
$DC exec -T postgres pg_dump -U ohs -Fc ohs > ohs-$(date +%F).dump
docker run --rm -v ohs_raw:/raw alpine tar czf - -C /raw . > raw-$(date +%F).tgz
cp .env env-$(date +%F).backup
```

With the S3 raw store, back the bucket up with your provider's tools instead. Restore into the
same version that took the backup, with `ohs` stopped (TimescaleDB needs the pre and post
restore calls):

```sh
$DC stop ohs
$DC exec -T postgres psql -v ON_ERROR_STOP=1 -U ohs -d postgres \
  -c 'DROP DATABASE ohs' -c 'CREATE DATABASE ohs'
$DC exec -T postgres psql -v ON_ERROR_STOP=1 -U ohs -d ohs \
  -c 'CREATE EXTENSION IF NOT EXISTS timescaledb' -c 'SELECT timescaledb_pre_restore()'
$DC exec -T postgres pg_restore -U ohs -d ohs --no-owner < ohs-2026-10-11.dump
$DC exec -T postgres psql -U ohs -d ohs -c 'SELECT timescaledb_post_restore()'
# empty the raw volume first, so files written after the backup do not linger
docker run --rm -v ohs_raw:/raw alpine find /raw -mindepth 1 -delete
docker run --rm -i -v ohs_raw:/raw alpine tar xzf - -C /raw < raw-2026-10-11.tgz
$DC start ohs
```

`pg_dump` warns about circular foreign keys in TimescaleDB's own catalog; the full dump above
restores despite it. Restore the `.env` of the same date, so the key matches.

## Rotating secrets

- **Tenant API key.** Add a key, move your clients to it, then revoke the old one:

  ```sh
  $DC exec ohs /ohs admin tenant list
  $DC exec ohs /ohs admin key create --tenant <tenant id>
  $DC exec ohs /ohs admin key list --tenant <tenant id>
  $DC exec ohs /ohs admin key revoke --tenant <tenant id> --key <old prefix>
  ```

  A user token minted with the old key keeps working until it expires. `key revoke` with a
  whole user token (`ohs_ut_...`) revokes that token.
- **`OHS_ENCRYPTION_KEY`.** 0.x has one key and no re-encryption. A new key leaves the stored
  OAuth tokens unreadable: pulls for those connections fail until each user connects the
  provider again. Uploads and stored data are not affected. Change it only when the old key is
  compromised.
- **Provider secrets.** See the Withings note under operating notes below.
- **`POSTGRES_PASSWORD`** only applies when the database volume is first created. To change it
  later, run `ALTER USER ohs PASSWORD '...'` in `psql`, then update `.env` and recreate both
  containers.

## Operating notes

- **`/metrics` is unauthenticated and global.** It exposes instance wide counters, not
  per-tenant data, but anyone who can reach it can read it. Block it at your reverse proxy, or
  set `OHS_METRICS=false`.
- **Run `etl` somewhere.** User deletion is carried out by a worker that runs with the `etl`
  role. An `api` only deployment accepts `DELETE /v1/users/{id}` but never finishes it, and
  uploads are never processed, until a process with the `etl` role is running against the same
  database.
- **Deletion and providers.** Deleting a user revokes their provider grants on a best effort
  basis. If a provider is down, the user's data is still deleted and the revocation is logged as
  failed; the grant may then stay valid at the provider until the user removes it there.
- **After a crash.** A job interrupted mid-way is claimed again after 10 minutes. A failed job
  is retried with backoff and parked after 5 attempts (`parked_jobs` in the coverage report).
  A raw file whose row was never written (the process died between the two) is not requeued:
  the daily file scanner reports it, and the data comes back when the client retries the
  upload, or when the reclaimed pull job fetches that window again.
  Records that failed for a recoverable reason (mapping, unit, or a whole object that failed)
  are replayed by the drainer once per process start, so deploy the fix and restart. Lines of a
  type the connector does not know are skipped, not failed, so the drainer never replays them:
  re-process the user (below) once a release maps that type.
- **Long outages.** The orphan scanner only looks at raw objects from the last 7 days. After an
  outage longer than that, raw objects that were stored but never queued stay unprocessed. Requeue
  them with this SQL, run once in `psql` (it queues every raw object that has no job; the
  tier matches what the scanner uses):

  ```sql
  INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, tier)
  SELECT o.tenant_id, o.user_id, o.provider, o.key,
         CASE WHEN o.provider = 'apple_health' THEN 'high_freq' ELSE 'standard' END
  FROM ohs_sync.raw_objects o
  WHERE o.created_at < now() - interval '1 minute'
    AND NOT EXISTS (SELECT 1 FROM ohs_sync.etl_jobs j WHERE j.key = o.key);
  ```
- **Re-processing a user from the raw files.** Raw payloads are never rewritten, so after a
  mapper fix (a new type mapped, a unit corrected) a user's data can be built again from them.
  Queue one ETL job per raw object of the user, run once in `psql` with the two ids filled in
  (objects that already have a job waiting are left alone):

  ```sql
  INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, tier)
  SELECT o.tenant_id, o.user_id, o.provider, o.key,
         CASE WHEN o.provider = 'apple_health' THEN 'high_freq' ELSE 'standard' END
  FROM ohs_sync.raw_objects o
  WHERE o.tenant_id = '<tenant id>' AND o.user_id = '<user id>'
    AND NOT EXISTS (SELECT 1 FROM ohs_sync.etl_jobs j
                    WHERE j.key = o.key AND j.status IN ('pending', 'running'));
  ```

  Records are de-duplicated by content hash, so what was already stored is not stored twice,
  and the coverage report's `pending_jobs` shows the progress. Rows the old mapper wrote are not
  removed: if the fix changes what identifies a record (its type name, its value after
  conversion), the old row stays beside the new one until you delete it. Drop the `user_id`
  condition to re-process a whole tenant.
- **Rotating a provider secret.** The Withings client secret also signs the notification URLs
  registered with Withings. After a rotation every connected user has to be subscribed again, or
  their notifications are refused (pulls keep working every 6 hours). See
  [connectors/withings.md](connectors/withings.md#6-rotating-the-client-secret).
- **MCP over a redirecting URL.** `ohs mcp` does not follow redirects. Set `OHS_MCP_URL` to the
  final https URL of the instance, see [mcp.md](mcp.md).
- **OAuth state is not bound to a browser session.** A consent link is single use and expires
  after 10 minutes, but whoever completes it connects the account they sign in with to the
  user it was minted for. Tenants must hand an authorize URL only to the user it belongs to,
  and never post it somewhere others can open it.
- **Compression.** Chunks of `quantity_samples` older than 30 days are compressed by a
  TimescaleDB background job. Late data for an old period (a re-upload, a history import) is
  written to the uncompressed part of the chunk and folded in when the job runs again.
  `SELECT * FROM chunk_compression_stats('ohs_data.quantity_samples')` shows the sizes before
  and after.
- **Large uploads.** An instance processes a bounded number of uploads at once
  (`OHS_INGEST_CONCURRENCY`, default 8, 2 on the Pi) and answers 503 with `Retry-After` above it;
  clients retry.
