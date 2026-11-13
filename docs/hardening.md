# Hardening checklist

What to set before an instance holds real people's health data. Each item is one setting or
command; the reasons are in [security-model.md](security-model.md). Commands assume the bundled
compose stack, run from the repository root with:

```sh
DC="docker compose --env-file .env -f deploy/docker-compose.yml"
```

## Network and TLS

- **Terminate TLS at a reverse proxy** in front of port 8080. `ohs` serves plain HTTP and
  bearer secrets travel in headers.
- **Set `OHS_PUBLIC_URL` to the https address** clients and providers use, for example
  `OHS_PUBLIC_URL=https://health.example.org`. OAuth callbacks and notification URLs are built
  from it.
- **Route the health checks to `ohs`.** `/healthz` and `/readyz` must reach the process, not a
  redirect of the proxy (see [self-hosting.md](self-hosting.md#reverse-proxy) and
  `deploy/Caddyfile`), or monitors read a healthy instance as down.
- **Publish the API on loopback only** when the proxy runs on the same host. The compose file
  already does (`127.0.0.1:8080:8080`): leave `OHS_HTTP_BIND` unset, and set `OHS_HTTP_PORT` to
  a port number only.
- **Never expose Postgres.** The bundled compose file publishes no database port; keep it
  that way. It connects with `sslmode=disable`, which is only acceptable there: the two
  containers share one host and one private network. A database elsewhere belongs on a private
  network, reached with `sslmode=verify-full` in `OHS_DATABASE_URL`.
- **Send the security headers at the proxy.** `deploy/Caddyfile` sets
  `Strict-Transport-Security`, `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`
  and `Permissions-Policy`; do the same with another proxy. Add `includeSubDomains` to HSTS only
  when every subdomain serves https.
- **Keep the rate limits on, and rate limit at the proxy too.** Since 0.2.0, the
  application limits only
  the provider-facing routes, per instance (`OHS_RATE_LIMIT_*`, see
  [self-hosting.md](self-hosting.md#rate-limits)). Behind a reverse proxy, list it in
  `OHS_TRUSTED_PROXIES` so limits apply to client addresses, not to the proxy's.
- **Keep `/metrics` private.** It is closed by default (403). To scrape it, set
  `OHS_METRICS_TOKEN` and give that bearer token to Prometheus, or serve it on a private address
  with `OHS_METRICS_LISTEN` and never publish that port. `OHS_METRICS=false` removes it.
- **Leave `OHS_WEBHOOK_ALLOW_PRIVATE` unset (false)** unless every tenant is trusted. Turned on,
  a tenant can point a webhook at your internal network.

## Secrets

- **Generate `OHS_ENCRYPTION_KEY`** with `openssl rand -base64 32`. Store it in a secret
  manager, apart from the database backups, not in the repository. Losing it makes stored OAuth
  tokens and webhook secrets unreadable, and, while the vault's root key is derived from it, every
  sealed health record too. To replace it, follow [key-rotation.md](key-rotation.md): the old
  key goes to `OHS_PREVIOUS_ENCRYPTION_KEYS` until `ohs admin rekey` has run.
- **Give the vault a root key of its own.** Unset, it is derived from `OHS_ENCRYPTION_KEY`, so
  one secret guards both (`ohs doctor` warns). Set `OHS_VAULT_ROOT_KEY_FILE` (or
  `OHS_VAULT_ROOT_KEY`) to 32 random bytes, or a KMS provider with `OHS_VAULT_KEY_PROVIDER`, run
  `ohs vault rotate root` once, then restart every process. Back the root key up apart from the
  database: losing it loses the health data ([self-hosting.md](self-hosting.md)).
- **Seal what an earlier version stored.** After an upgrade to 0.3.0, run `ohs vault seal` once
  and check with `ohs vault status --check`.
- **Set a strong `POSTGRES_PASSWORD`** before the first `up`: `openssl rand -hex 24`, as the
  quickstart does. Since 0.3.0 the compose file has no default and refuses to run without one.
  It only applies when the volume is created; afterwards, and to leave the old default `ohs` of
  an earlier install, use `ALTER USER ohs PASSWORD '...'`, see
  [self-hosting.md](self-hosting.md#rotating-secrets).
- **Restrict who can read `.env`**: `chmod 600 .env`. Anyone who can run `docker inspect` on
  the container can read the key too.
- **Prefer `ohs admin tenant create`** over `OHS_BOOTSTRAP_TENANT`, which logs the new API key
  once. If you used it, treat that log line as a secret.
- **Revoke keys you no longer use**: `$DC exec ohs /ohs admin key list --tenant <id>`, then
  `$DC exec ohs /ohs admin key revoke --tenant <id> --key <prefix>`. A leaked user token is
  revoked the same way with `--key ohs_ut_...`.
- **Mint least-privilege user tokens.** Ask only for the scopes the app needs
  (`"scopes": ["upload"]` for an uploader) and a short `ttl_seconds`: the default is one hour,
  the maximum 86400, or 2592000 when `read` is the only scope.
- **Rotate webhook secrets** after a receiver is compromised:
  `POST /v1/webhooks/{id}/rotate-secret`.
- **Set the optional inbound secrets** of the providers you enable, such as
  `OHS_POLAR_WEBHOOK_SECRET` and `OHS_STRAVA_SUBSCRIPTION_ID`; without them those notifications
  are accepted unverified (see each guide under [connectors](connectors/README.md)).

## Storage

- **Encrypt the disks** that hold the Postgres volume, the raw store and backups. The vault
  seals health values and raw payloads, but ids, record types, times, units and providers stay
  in clear, and rows stored before 0.3.0 stay in clear until `ohs vault seal`.
- **Keep the raw folder private** to the container user:
  `chown -R 65532:65532 <folder> && chmod 750 <folder>`. Files are created 0600.
- **On S3, turn on default server-side encryption** and block public access on the bucket,
  for example
  `aws s3api put-bucket-encryption --bucket <bucket> --server-side-encryption-configuration '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"aws:kms"}}]}'`.
  Give `ohs` credentials limited to that bucket. `ohs` can also ask for it on every write,
  whatever the bucket's default: add `sse=AES256`, or `sse=aws:kms` with `sse_kms_key_id=`, to
  `OHS_RAW_STORE` ([self-hosting.md](self-hosting.md#s3-compatible-raw-store)).
- **If bucket versioning is on, expire noncurrent versions** with a lifecycle rule, or a
  deleted user's raw files survive as old versions.
- **Set a retention period** if you do not need everything forever:
  `OHS_RETENTION_DAYS=<days>`.

## Backups

- **Back up the database, the raw store and `.env` together**, as in
  [self-hosting.md](self-hosting.md#backup-and-restore), and encrypt them on the way out:

  ```sh
  $DC exec -T postgres pg_dump -U ohs -Fc ohs | gpg --encrypt -r <recipient> > ohs-$(date +%F).dump.gpg
  docker run --rm -v ohs_raw:/raw alpine tar czf - -C /raw . | gpg --encrypt -r <recipient> > raw-$(date +%F).tgz.gpg
  ```

- **Keep the encryption key apart from the database backup**, so one stolen copy does not
  hold both, and **test a restore**.
- **Plan for erasure in backups.** A backup taken before an erasure still holds that user's
  data for whoever has the root key: expire backups, or rotate the root key
  (`ohs vault rotate root`) and destroy the previous version. Keep backup retention as short as
  you can.

## Container

- **Already true:** the image is distroless (no shell), runs as user 65532 (non-root), and
  writes only to the raw store folder `/var/lib/ohs/raw`.
- **Add a read-only root filesystem and drop capabilities** with an overlay, then check with
  `$DC run --rm ohs doctor`:

  ```yaml
  # deploy/docker-compose.hardened.yml (yours, not shipped)
  services:
    ohs:
      read_only: true
      cap_drop: [ALL]
      security_opt: ["no-new-privileges:true"]
  ```

## Logs

- **Keep `OHS_LOG_LEVEL=info`** (the default) in production. Logs are JSON on stderr.
- **Ship logs to a store with access control and a retention limit.** They never hold tokens or
  health values, and hold one key only: the `OHS_BOOTSTRAP_TENANT` key, logged once when that
  tenant is created. They hold tenant and user ids.
- **Use the request id** to investigate: every response carries `X-Request-Id`, and the
  matching log lines carry `request_id`.

## Updates and image signatures

- **Pin a version** (`OHS_VERSION=<version>` in `.env`) and read the
  [changelog](../CHANGELOG.md) and [SECURITY.md](../SECURITY.md) before upgrading. Only the latest
  0.x release gets fixes.
- **Watch the repository's releases and security advisories** on GitHub.
- **Verify the image signature before deploying.** Images released after 0.1.0 are signed
  keylessly by the release workflow. Find the digest of a tag, then verify it:

  ```sh
  docker buildx imagetools inspect ghcr.io/soma-intelligence/open-health-sync:<version> | awk '/^Digest:/ {print $2}'
  cosign verify ghcr.io/soma-intelligence/open-health-sync@<digest> \
    --certificate-identity-regexp '^https://github.com/soma-intelligence/open-health-sync/.github/workflows/release.yml@refs/tags/v' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
  ```

  Then deploy that digest: `OHS_VERSION=<version>@<digest>` in `.env` pins the tag and the digest.
  The compose file pulls `ohs` by version tag (Postgres is pinned by digest in the file itself),
  so without this step you run whatever the tag points to at pull time. With the Helm chart, set
  `image.digest` to the verified digest.
- **Verify release binaries** with the signed checksums (download `checksums.txt` and
  `checksums.txt.sigstore.json` from the release):

  ```sh
  cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
    --certificate-identity-regexp '^https://github.com/soma-intelligence/open-health-sync/.github/workflows/release.yml@refs/tags/v' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
  sha256sum --check --ignore-missing checksums.txt
  ```

## Operations

- **Run the `etl` role somewhere.** Without it, user deletions are accepted but never carried
  out.
- **Give provider keys to every process running `collect` or `etl`**, so deletions can revoke
  grants at the provider.
- **Run `$DC run --rm ohs doctor`** after every configuration change.
