# Self-host a health data API on a Raspberry Pi 5

A Raspberry Pi 5 with 4 GB of memory runs the whole Open Health Sync stack (the API, the sync
workers and Postgres with TimescaleDB) with Docker Compose and one overlay file,
`deploy/docker-compose.pi.yml`, that trims memory use. The published image is built for
`linux/arm64`, so nothing is emulated. Open Health Sync is open source and self-hosted.

## 1. What you need

| | |
|---|---|
| Board | Raspberry Pi 5 with 4 GB, on a 64-bit OS (the images are `linux/amd64` and `linux/arm64` only) |
| Software | Docker with Compose v2.24 or later, `git`, `openssl` |
| Disk | a USB 3 SSD or an NVMe HAT: Postgres writes a lot during a large first upload, and an SD card wears out sooner |

Postgres is pinned by its multi-arch index digest and Go cross-compiles the image, so the same
compose file runs on a Pi and on an x86 server.

## 2. Configure

Clone the repository and create `.env` with a fresh encryption key, as in the
[quickstart](../../README.md#quickstart):

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git && cd open-health-sync
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
chmod 600 .env
```

Keep `OHS_ENCRYPTION_KEY` somewhere safe, apart from the Pi: it seals the stored OAuth tokens
and webhook secrets, and a backup restored without it cannot read them.

## 3. Start with the Pi overlay

Keep the same `-f` files on every later command, so set them once:

```sh
DC="docker compose --env-file .env -f deploy/docker-compose.yml -f deploy/docker-compose.pi.yml"
$DC up -d --wait
$DC run --rm ohs doctor
```

`ohs doctor` prints one `ok` or `fail` line per check (configuration, database, raw store,
public URL, connector keys) and exits non-zero on a failure. The container runs `ohs up`, which
applies migrations and then serves.

What the overlay changes:

| Setting | Default stack | Pi overlay |
|---|---|---|
| Postgres `shared_buffers` | Postgres default | `128MB` |
| Postgres `effective_cache_size` | Postgres default | `512MB` |
| Postgres `work_mem` | Postgres default | `4MB` |
| Postgres `max_connections` | 100 | `30` |
| `timescaledb.max_background_workers` | image default | `4` |
| `ohs` memory limit (`mem_limit`) | `1g` | `512m` |
| `GOMEMLIMIT` | derived: three quarters of the limit | `256MiB` |
| `OHS_INGEST_CONCURRENCY` | `8` | `2` |
| `OHS_INGEST_TENANT_CONCURRENCY` | half of the above | `2` (the household's one tenant may use both) |

An upload over the 2 slots answers 503 with `Retry-After`; the SDKs and `ohs import` retry on
their own. Above 512 MB the kernel restarts `ohs` alone, never Postgres.

## 4. Create your user

```sh
$DC exec -T ohs /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
set -a; . ~/.ohs.env; set +a
curl -s "$OHS_URL/readyz"
```

`ohs admin personal setup` creates a `personal` tenant, a user (`me` by default, `--person` for
another member of the household) and a new API key, printed as `OHS_URL`, `OHS_KEY` and
`OHS_USER`. No device yet? `$DC exec ohs /ohs seed --demo` writes 90 days of synthetic data
([demo data](../demo.md)).

## 5. Reach it from other devices

The API is published on `127.0.0.1:8080`, so only the Pi itself reaches it. Two ways out:

- **HTTPS, recommended.** Put a reverse proxy with TLS in front of port 8080 and set
  `OHS_PUBLIC_URL` to its https address: see
  [Run Open Health Sync with Docker Compose and HTTPS](docker-compose-https.md).
- **Plain http on your home network.** Set `OHS_HTTP_BIND=0.0.0.0` and
  `OHS_PUBLIC_URL=http://<pi address>:8080` in `.env`, then run `$DC up -d --wait` again. Bearer
  secrets then travel unencrypted on the network.

OAuth callbacks are built from `OHS_PUBLIC_URL`, so set it before you connect a cloud provider.
Devices with standard Bluetooth health profiles can upload from the Pi itself through
[`ohs-bridge`](../bridge.md), built on the 64-bit Raspberry Pi OS.

## 6. Back up

The backup script reads the same overlays from `OHS_COMPOSE_FILES` (colon separated):

```sh
OHS_COMPOSE_FILES=deploy/docker-compose.yml:deploy/docker-compose.pi.yml scripts/backup.sh
```

A daily cron entry that keeps two weeks:

```sh
15 3 * * * cd /srv/open-health-sync && OHS_COMPOSE_FILES=deploy/docker-compose.yml:deploy/docker-compose.pi.yml OHS_BACKUP_KEEP=14 scripts/backup.sh >>/var/log/ohs-backup.log 2>&1
```

Copy each archive off the Pi, or a dead disk takes the backups with the data. Restore with
`scripts/restore.sh <archive>`, and check one with `scripts/restore.sh --verify <archive>`
([Backup and restore](../backup-and-restore.md)).

## 7. Upgrade

Releases are 0.x and there is no `latest` tag. Take a backup, read the
[changelog](../../CHANGELOG.md), pin the new version in `.env` (`OHS_VERSION=...`), then:

```sh
$DC pull ohs && $DC up -d --wait && $DC exec ohs /ohs version
```

## Related

- [Run Open Health Sync with Docker Compose and HTTPS](docker-compose-https.md)
- [Import Apple Health data into PostgreSQL](apple-health-to-postgres.md)
- [Give an AI agent read-only access to wearable data over MCP](mcp-remote-agent.md)
- [Get a webhook when new wearable data arrives](webhooks-new-data.md)
- [Record a Bluetooth heart rate strap, blood pressure cuff or scale without a phone](bluetooth-devices.md)
- [Self-hosting reference](../self-hosting.md)
- [README](../../README.md)
