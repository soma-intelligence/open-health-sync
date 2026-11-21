# Run Open Health Sync with Docker Compose and HTTPS

To serve Open Health Sync over HTTPS, start it with `deploy/docker-compose.yml`, which publishes
the API on `127.0.0.1:8080` only, and put Caddy in front with the bundled `deploy/Caddyfile`,
which gets a TLS certificate on its own. Then set `OHS_PUBLIC_URL` to the https address and
check the result with `ohs doctor`. Open Health Sync is open source and self-hosted.

`ohs` serves plain HTTP, and every API key and user token travels in an `Authorization` header,
so anything beyond your own machine goes through TLS.

## 1. Configure `.env`

From a clone of the repository:

```sh
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
chmod 600 .env
```

Then edit `.env`:

```sh
OHS_PUBLIC_URL=https://health.example.org
OHS_TRUSTED_PROXIES=172.16.0.0/12
POSTGRES_PASSWORD=<a strong password>
OHS_VERSION=0.3.0
```

| Variable | Why |
|---|---|
| `OHS_PUBLIC_URL` | OAuth callbacks, provider notification URLs and share links are built from it |
| `OHS_TRUSTED_PROXIES` | the proxy on the host reaches the container from the Docker bridge gateway; listing that range makes the rate limits count the client address from `X-Forwarded-For` instead of the proxy's |
| `POSTGRES_PASSWORD` | applies only when the database volume is first created; later, change it with `ALTER USER ohs PASSWORD '...'` |
| `OHS_VERSION` | pin a release: there is no `latest` tag while the version is 0.x |

Keep `OHS_ENCRYPTION_KEY` in a secret manager and with your backups: it seals stored OAuth
tokens and webhook secrets. Leave `OHS_HTTP_BIND` unset, so the port stays on loopback.

## 2. Start the stack

Docker Compose v2.24 or later. Keep `--env-file .env` on every command, since compose reads
`OHS_VERSION`, `POSTGRES_PASSWORD` and the port settings from it:

```sh
DC="docker compose --env-file .env -f deploy/docker-compose.yml"
$DC up -d --wait
$DC exec ohs /ohs admin tenant create --name prod
```

The last command prints the tenant's API key (`ohs_sk_...`) once: store it.

## 3. Put Caddy in front

`deploy/Caddyfile` proxies every path of one host name to `127.0.0.1:8080`, with automatic TLS.
Replace `health.example.org` with your host name, then run Caddy on the same host:

```sh
caddy run --config deploy/Caddyfile
```

When the same host name also serves other pages (a landing page, a sign-in portal), use the
file's second, commented example instead: it names the routes of `ohs` first and sends only the
rest elsewhere. Those routes are `/healthz`, `/readyz`, `/openapi.yaml`, `/metrics`, `/v1/*`,
`/mcp`, `/oauth/*`, `/inbound/*`, `/connect`, `/connect/*`, `/shl/*` and `/console`. A
catch-all redirect placed before them turns `/healthz` into a 302, which monitors read as down,
and breaks provider callbacks the same way.

## 4. Check it

```sh
$DC run --rm ohs doctor
curl -i https://health.example.org/healthz
```

`ohs doctor` prints one `ok`, `warn`, `skip` or `fail` line per check (configuration, database,
raw store, public URL, connector keys, the keys that seal stored secrets) and exits non-zero on a
failure. For each enabled OAuth connector it prints the callback URL to register with the
provider, `<OHS_PUBLIC_URL>/oauth/<slug>/callback`. The `curl` answer must be `200` with `ok` and
no `Location` header.

## 5. Harden before real data

| Item | Setting or command |
|---|---|
| Keep Postgres private | the compose file publishes no database port; keep it that way |
| Keep `/metrics` closed | closed by default (403); to scrape it, set `OHS_METRICS_TOKEN`, or `OHS_METRICS_LISTEN` on a private address |
| Webhooks to private addresses | leave `OHS_WEBHOOK_ALLOW_PRIVATE` unset unless every tenant is trusted |
| Encrypt disks | the Postgres volume, the raw store and backups hold health data in clear |
| Read-only container | an overlay of your own with `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges:true`, then `$DC run --rm ohs doctor` |
| Verify the image | `cosign verify` on the image digest, then pin `OHS_VERSION=<version>@<digest>` |
| Revoke unused keys | `$DC exec ohs /ohs admin key revoke --tenant <id> --key <prefix>` |
| Back up | `scripts/backup.sh`, plus `.env` kept apart from the archives |

The full list, with the cosign command, is the [hardening checklist](../hardening.md). Run
`$DC run --rm ohs doctor` after every configuration change.

## Upgrading

Back up, read the [changelog](../../CHANGELOG.md), set the new `OHS_VERSION` in `.env`, then:

```sh
$DC pull ohs
$DC up -d --wait
$DC exec ohs /ohs version
```

Pending migrations apply on start. There is no step-by-step downgrade: to go back, run the old
version on a restored backup ([Backup and restore](../backup-and-restore.md)).

## Related

- [Self-host a health data API on a Raspberry Pi 5](raspberry-pi.md)
- [Get a webhook when new wearable data arrives](webhooks-new-data.md)
- [Give an AI agent read-only access to wearable data over MCP](mcp-remote-agent.md)
- [Self-hosting reference](../self-hosting.md#reverse-proxy)
- [README](../../README.md)
