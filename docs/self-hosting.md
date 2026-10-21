# Self-hosting Open Health Sync

This page covers Docker Compose. To run it on Kubernetes, use the Helm chart in
`deploy/helm/ohs`: see [Kubernetes with Helm](helm.md). The configuration, backup and operating
notes below apply to both; the commands are written for compose.

No server of your own? [Render, Fly.io and Railway](deploy-platforms.md) runs it on Render (one
button), Fly.io or Railway, with TimescaleDB and a persistent raw store.

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

No wearable account yet? Fill the instance with 90 days of synthetic data from six sources, and
get a tenant key, a user and example calls to try ([demo.md](demo.md)):

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs seed --demo
```

Compose reads `${...}` variables (`OHS_VERSION`, `OHS_HTTP_PORT`, `OHS_HTTP_BIND`, `POSTGRES_PASSWORD`, `S3_*`)
from the shell or `--env-file`, so keep `--env-file .env` on every `docker compose` command.
The containers read `.env` themselves either way.

`OHS_ENCRYPTION_KEY` must hold 32 random bytes in base64; the `sed` line above fills it. Keep
it: it encrypts the OAuth tokens stored in the database. To change it later, follow
[rotating the encryption key](key-rotation.md) (next release; on 0.1.0 a new key leaves the stored tokens unreadable). If it is empty, `ohs` exits at start
and its container restarts in a loop, never healthy. `ohs doctor` then prints a freshly
generated key: paste it into `.env` and run the same `up -d` command again, so the container is
recreated with the new environment.

The `ohs` container runs `ohs up`, which is `ohs migrate up` followed by
`ohs serve` (the runtime image has no shell, so the binary chains them). Its
healthcheck is `ohs doctor --quick` (database and raw store).

### Port, and a second install

The API is published on host port 8080 of `127.0.0.1`, so only this machine reaches it: other
machines go through a reverse proxy with TLS (see below), or set `OHS_HTTP_BIND=0.0.0.0` in
`.env` to publish plain http on every interface. Set `OHS_HTTP_PORT` in `.env` to use another port, and
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
failure: configuration, database, raw store, public URL, the required keys of
every enabled connector, and the keys that seal the stored secrets. For each enabled OAuth connector it prints the callback
URL to register with the provider: `<OHS_PUBLIC_URL>/oauth/<slug>/callback`.
A connector that needs keys and has none set gets a `skip` line,
`skip  connector withings: disabled (no keys)`; one with only some of its keys is a
`fail`. The stored OAuth tokens and webhook secrets are counted by the key that seals them: a
`warn` line (which does not fail the run) names how many still use a previous key or open with
no configured key, and says when `OHS_PREVIOUS_ENCRYPTION_KEYS` is no longer needed (see
[rotating the encryption key](key-rotation.md)). `--quick` checks the database and raw store only. The full run also writes and deletes
a probe object in the raw store; a host folder bind mounted at `/var/lib/ohs/raw` must be
writable by the container user (`chown -R 65532:65532 <folder>`). `ohs serve` runs the same
write check at start and stops if it fails.

## Images and architectures

Each release publishes `ghcr.io/soma-intelligence/open-health-sync` for `linux/amd64` and
`linux/arm64`, tagged with the version (`0.1.0`) and `edge` (the newest final release, never a pre-release such as `0.1.0-rc.1`).
There is no `latest` tag while the version is 0.x: pin a version. The compose file
uses `${OHS_VERSION:-0.1.0}`; set `OHS_VERSION` to move to another release.
Release binaries for Linux and macOS (amd64, arm64) and their `checksums.txt` are
attached to each [GitHub release](https://github.com/soma-intelligence/open-health-sync/releases).
Releases after 0.1.0 sign the image digest and `checksums.txt` with Sigstore cosign (keyless);
[hardening.md](hardening.md#updates-and-image-signatures) has the verification commands.

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
| `OHS_ENCRYPTION_KEY` | none, required | 32 random bytes, base64. Seals stored OAuth tokens and webhook secrets. `openssl rand -base64 32` makes one, or `ohs doctor` prints one |
| `OHS_PREVIOUS_ENCRYPTION_KEYS` | none | Comma separated keys `OHS_ENCRYPTION_KEY` replaced: they still open what they sealed, never seal. Set only while [rotating the key](key-rotation.md). Next release |
| `OHS_PUBLIC_URL` | `http://localhost:8080` | Public base URL, used for OAuth callbacks, notification URLs and [share links](share-links.md). Use your https address |
| `OHS_RAW_STORE` | `file:///var/lib/ohs/raw` | `file:///path` or `s3://bucket?endpoint=&region=&path_style=true` |
| `OHS_ROLES` | `api,collect,etl` | Roles this process runs |
| `OHS_BOOTSTRAP_TENANT` | none | Creates this tenant at first start and logs its API key once. It is an [instance admin](console.md#the-instance-admin) |
| `OHS_LISTEN` | `:8080` | Listen address |
| `OHS_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`; logs are JSON on stderr |
| `OHS_CONSOLE` | `false` | `true` serves the [admin console](console.md) on `/console`, where you run the pipeline and turn providers on; `read-only` serves it without any action. It asks for a tenant API key and calls the API with it; off, the route is not mounted (404). The actions and `read-only` are next release: 0.1.0 serves the read-only console |
| `OHS_SETUP_TOKEN` | none | 16 characters or more (`openssl rand -hex 32`). Turns on the browser [first-run setup](setup.md): with `OHS_CONSOLE` on, `/console/setup` creates the first tenant and shows its API key once, for whoever types this token, while the instance has no tenant. Unset, `/v1/setup` is not mounted. Next release |
| `OHS_METRICS` | `true` | Serve Prometheus metrics on `/metrics` (see [Metrics](self-hosting.md#metrics)) |
| `OHS_METRICS_TOKEN` | none | Bearer token a scrape of `/metrics` presents, 16 characters or more (`openssl rand -hex 32`). Without it the API listener refuses every scrape |
| `OHS_METRICS_LISTEN` | none | Serve `/metrics` on this address instead of the API listener, for example `127.0.0.1:9464`; the token is then asked for only when set |
| `OHS_CONSENT_REQUIRED` | `false` | Store nothing from a source until the user's [consent](consent.md) for it is recorded; by default a source without consent stores everything |
| `OHS_AUDIT_RETENTION_DAYS` | `365` | Delete [audit events](audit-log.md) older than this many days, once a day; 0 keeps every event |
| `OHS_RETENTION_DAYS` | `0` | Delete raw files and normalized data older than this many days; 0 keeps everything |
| `OHS_PULLS_PER_PROVIDER` | `4` | Pulls of one provider running at once, across every `collect` instance. See [provider rate limits](background-sync.md#provider-rate-limits) |
| `OHS_BACKFILLS_PER_PROVIDER` | `2` | Of those, backfills of new connections |
| `OHS_INGEST_CONCURRENCY` | `8` | Device uploads one `api` process handles at once; more answer 503 with `Retry-After`. The Pi overlay sets 2 |
| `OHS_WEBHOOK_ALLOW_PRIVATE` | `false` | `true` lets [webhook](webhooks.md) endpoints use plain http and private, loopback or link-local addresses (a receiver on the same host or network). Leave it off when tenants are not trusted |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | none | Turns on OpenTelemetry traces, sent over OTLP to this address. The other standard `OTEL_` variables apply; see [observability](observability.md) |
| `OHS_RATE_LIMIT_INBOUND` | `30/120` | Notification requests (`/inbound/{provider}`) per client address: `rate/burst` in requests per second, `0` disables. See [rate limits](#rate-limits). Next release |
| `OHS_RATE_LIMIT_INBOUND_PROVIDER` | `300/600` | Notification requests per provider, whatever the address; `0` disables. Next release |
| `OHS_RATE_LIMIT_OAUTH` | `2/20` | OAuth callback requests per client address, and in buckets of their own `POST /v1/setup` requests and share link manifest and file requests; `0` disables. Next release |
| `OHS_TRUSTED_PROXIES` | none | Comma-separated CIDRs (or addresses) of your reverse proxies. Only a request from one of them has its client address read from `X-Forwarded-For`. Next release |
| `OHS_<PROVIDER>_CLIENT_ID`, `_CLIENT_SECRET` | none | Keys of a provider; its connector is on when they are set. Every process that runs `collect` or `etl` needs them (see below). Or save them from the [console](console.md#where-provider-keys-come-from) instead (next release); set here, they win |

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

Keys saved from the [console](console.md) (next release) need none of this: they are stored in the database,
sealed with `OHS_ENCRYPTION_KEY`, and every process reads them within 30 seconds. Each process
still needs the same `OHS_ENCRYPTION_KEY`.

Put a reverse proxy with TLS in front of port 8080; the bearer secrets travel in headers.
[hardening.md](hardening.md) is the full production checklist, and
[security-model.md](security-model.md) the threat model behind it.

### Rate limits

> **Next release.** This is on `main` and ships in the next release; the 0.1.0 image does not have it.

The provider-facing routes take no credential: `POST` and `GET /inbound/{provider}`
(notifications and the URL check) and `GET /oauth/{provider}/callback`. Neither does
`POST /v1/setup` (the [first-run setup](setup.md)), which checks a token, nor the
[share link](share-links.md) routes `POST /shl/{manifest_id}` and `GET /shl/file/{ticket}`. Each notification may
verify a signature, write rows and queue pulls, and each callback reads the database, so an
in-memory token bucket bounds them, per `api` process. A request over a limit is answered 429
with `Retry-After` (seconds) before any work, and counted in `/metrics` as
`ohs_rate_limited_total{route_group="inbound"}`, `{route_group="oauth_callback"}`,
`{route_group="setup"}` or `{route_group="share"}`. The
authenticated API is not limited.

| Bucket | Default | Variable |
|---|---|---|
| Notifications, per client address | 30 requests/s, bursts of 120 | `OHS_RATE_LIMIT_INBOUND` |
| Notifications, per provider (all addresses) | 300 requests/s, bursts of 600 | `OHS_RATE_LIMIT_INBOUND_PROVIDER` |
| OAuth callbacks, per client address | 2 requests/s, bursts of 20 | `OHS_RATE_LIMIT_OAUTH` |
| First-run setup (`POST /v1/setup`), per client address | same as OAuth callbacks, its own bucket | `OHS_RATE_LIMIT_OAUTH` |
| Share link manifest and file requests, per client address | same as OAuth callbacks, their own bucket | `OHS_RATE_LIMIT_OAUTH` |

The values are `rate/burst` (`30/120`; the rate may be a decimal, `0.5/10`), and `0` turns a
bucket off. The defaults sit well above what a provider sends, batched notifications and a
large backfill included; providers retry a 429. The per provider bucket keeps a flood from
many spoofed addresses from queueing unlimited work. An IPv6 client is counted by its /64.
Buckets idle for 10 minutes are dropped, and at most 100,000 addresses are tracked per route
group: beyond that, new addresses share one bucket until idle ones are dropped.

The client address is the TCP peer. Behind a reverse proxy every request comes from the proxy,
so list the proxy in `OHS_TRUSTED_PROXIES`: for a request from one of those addresses, the
client is the rightmost `X-Forwarded-For` entry that is not itself a trusted proxy. The header
is ignored from any other peer, so a client cannot pick its own address. With the bundled
compose file and a proxy on the host in front of the published port 8080, requests reach the
container from the Docker bridge gateway. Make sure the proxy appends the address it received
the request from to `X-Forwarded-For` (most do by default), then trust the bridge networks:

```
# .env
OHS_TRUSTED_PROXIES=172.16.0.0/12
```

Name only addresses your proxies use: a trusted range that also holds clients lets them forge
the header. Several `api` processes each keep their own buckets, so the limits add up across
them.

## Upgrading

Releases are 0.x: any of them may change the API, the configuration or the schema, so read the
[changelog](../CHANGELOG.md) and the release notes first, and take a backup
([`scripts/backup.sh`](backup-and-restore.md)). Then pin
the new version in `.env` and recreate the container:

```sh
# .env: OHS_VERSION=0.1.0
docker compose --env-file .env -f deploy/docker-compose.yml pull ohs
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs version
```

The container runs `ohs up`, so pending migrations apply on start; `ohs migrate version` prints
the schema version. There is no step-by-step downgrade (`ohs migrate down --all` drops
everything): to go back, run the old version on a restored backup.

Without compose, the order is the same: back up, stop `ohs`, replace the binary, then start it
with `ohs up` (or run `ohs migrate up` before `ohs serve`). Several processes on one database
(split roles) must all run the same version: stop them all, migrate once, then start the new
version everywhere. If a migration fails, `ohs migrate version` shows `(dirty)` and `ohs up`
refuses to serve: restore the backup and report the error rather than editing the schema by
hand.

With the Helm chart, see [Upgrading](helm.md#upgrading) there: a Job applies the migrations
before the new pods start.

### What the upgrade test checks

Every change to `main` runs [`test/upgrade/upgrade.sh`](../test/upgrade/upgrade.sh)
(`make upgrade-test`). It builds the last release from its tag, installs it on Postgres with
TimescaleDB, creates a tenant, a user and an upload token, and uploads the Apple Health test
export. Then it migrates that database to the new code and checks that:

- every migration applies on top of that data, and no table loses or gains rows (code catalogs
  may gain new codes);
- the server starts with `ohs up` and `/readyz` answers 200;
- the API key and the upload token issued by the old release still work;
- samples, sleep, workouts, daily summaries, coverage and the user record read back exactly as
  before (new fields may be added, none change);
- endpoints added since that release (freshness, webhooks, FHIR, Open mHealth, exports) answer
  for the old user, and a new upload is processed;
- the down migrations bring the schema back to the old release's version, the old release serves
  the same data again, and the new code migrates up once more.

It covers one data source (Apple Health uploads) and the bundled database, not your provider
connections or a database tuned differently, so the backup above is still the safety net.
`OHS_UPGRADE_FROM=v0.0.1 make upgrade-test` tests from an older release.

## Backup and restore

`scripts/backup.sh` writes one archive with the database (`pg_dump`, run inside the database
container) and the raw files; `scripts/restore.sh` puts it back into a fresh or existing stack
and refuses an archive newer than the running release. Keep `.env` (it holds
`OHS_ENCRYPTION_KEY`) separately: without the key the stored OAuth tokens cannot be read. CI
tests the full cycle. See [Backup and restore](backup-and-restore.md) for what is in an
archive, a cron example, the S3 raw store, restore steps and how to verify a backup.

## Rotating secrets

- **Tenant API key.** Add a key, move your clients to it, then revoke the old one:

  ```sh
  DC="docker compose --env-file .env -f deploy/docker-compose.yml"
  $DC exec ohs /ohs admin tenant list
  $DC exec ohs /ohs admin key create --tenant <tenant id>
  $DC exec ohs /ohs admin key list --tenant <tenant id>
  $DC exec ohs /ohs admin key revoke --tenant <tenant id> --key <old prefix>
  ```

  A user token minted with the old key keeps working until it expires. `key revoke` with a
  whole user token (`ohs_ut_...`) revokes that token.
- **`OHS_ENCRYPTION_KEY`.** From the next release, rotate it online: put the new key in `OHS_ENCRYPTION_KEY` and the
  old one in `OHS_PREVIOUS_ENCRYPTION_KEYS`, restart every process, run `ohs admin rekey`, then
  remove the old key once `ohs doctor` says nothing uses it. The whole procedure, and what
  happens when a step is skipped, is in [rotating the encryption key](key-rotation.md). On 0.1.0,
  and whenever the old key is not kept, replacing it leaves the stored OAuth tokens, webhook secrets
  and provider keys saved in the console unreadable: each user must connect the provider again. Uploads and stored data are not
  affected.
- **Provider secrets.** See the Withings note under operating notes below.
- **`POSTGRES_PASSWORD`** only applies when the database volume is first created. To change it
  later, run `ALTER USER ohs PASSWORD '...'` in `psql`, then update `.env` and recreate both
  containers.

## Operating notes

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
- **Hourly statistics.** A TimescaleDB job refreshes the `quantity_hourly` continuous
  aggregate every 5 minutes, recomputing only the hours that changed, so late data and
  deletions (an erased user included) leave the statistics within about 5 minutes. On an
  existing install its first run builds the whole history once (about 11 s for 10 million
  samples in our test). `SELECT * FROM timescaledb_information.job_stats` shows its runs.
- **Large uploads.** An instance processes a bounded number of uploads at once
  (`OHS_INGEST_CONCURRENCY`, default 8, 2 on the Pi) and answers 503 with `Retry-After` above it;
  clients retry.
- **Rate limits on the provider-facing routes.** Notifications and OAuth callbacks over the
  limits answer 429 with `Retry-After`; see [rate limits](self-hosting.md#rate-limits). Behind a
  reverse proxy, set `OHS_TRUSTED_PROXIES`, or every request counts against the proxy's address.

### Metrics

`/metrics` serves Prometheus counters for the whole instance (requests by route, ingest load, ETL
queues, sync state by provider), never per-tenant data. It is still information about your
instance, so it is closed unless you open it one of two ways:

- **A token** (`OHS_METRICS_TOKEN`): the API listener serves `/metrics` to a scrape that sends
  `Authorization: Bearer <token>`, and answers 401 otherwise. In Prometheus:

  ```yaml
  scrape_configs:
    - job_name: ohs
      authorization:
        credentials_file: /etc/prometheus/ohs-metrics-token
      static_configs:
        - targets: ["ohs:8080"]
  ```

- **A private address** (`OHS_METRICS_LISTEN`, for example `127.0.0.1:9464` or an address on
  your monitoring network): `/metrics` moves off the API listener to that address. Whoever can
  reach the address can scrape it, unless `OHS_METRICS_TOKEN` is also set. Never publish that
  port.

With neither, `/metrics` answers 403 and names the two settings. `OHS_METRICS=false` removes the
route altogether.
[observability.md](observability.md) lists the metrics, and how to turn on OpenTelemetry
traces.
