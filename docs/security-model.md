# Security model

This page states what Open Health Sync protects, from whom, how, and where it stops. It
describes the code as it is on `main`; file paths point at the mechanism. For the operator
checklist see [hardening.md](hardening.md); to report a problem see
[SECURITY.md](../SECURITY.md).

## What is protected

| Asset | Where it lives | Protection in the application |
|---|---|---|
| Health records (samples, sleep, workouts, daily summaries) | Postgres, schema `ohs_data` | Every query filters on `tenant_id` and `user_id`. Stored in clear. |
| Raw provider and device payloads | Raw store: a folder or an S3 bucket | Keyed by `raw/<tenant>/<user>/...`, written once. Gzip, not encrypted. |
| Provider OAuth access and refresh tokens | `ohs_sync` connections | AES-256-GCM under `OHS_ENCRYPTION_KEY`, bound to the row |
| Tenant API keys (`ohs_sk_`) | `ohs_core.api_keys` | SHA-256 hash only |
| User tokens (`ohs_ut_`) | `ohs_core.user_tokens` | SHA-256 hash only, scoped, expiring |
| Webhook signing secrets (`whsec_`) | `ohs_core.webhook_endpoints` | AES-256-GCM under `OHS_ENCRYPTION_KEY`, bound to the endpoint |
| OAuth `state` values | `ohs_sync.oauth_states` | SHA-256 hash only, single use, 10 minute lifetime |
| `OHS_ENCRYPTION_KEY` and provider client secrets | The process environment | Not stored by the application; the operator's job |

## Actors and trust boundaries

```
 tenant operator ──(CLI on the host)──┐
 tenant backend ──(ohs_sk_ key)──────┐│        ┌──> webhook receivers (tenant URLs)
 end-user device ─(ohs_ut_ token)───┐││        │
 providers ──(OAuth callback,       │││  ┌─────┴─────┐      ┌──────────┐
              inbound notifications)┴┴┴─>│    ohs    │─────>│ Postgres │
                                         │           │─────>│ raw store│
                                         └─────┬─────┘      └──────────┘
                                               └──> provider APIs (pulls, revocation)
 instance operator: runs the host, holds the environment, the database and the raw store
```

- **Instance operator.** Runs the containers and holds `OHS_ENCRYPTION_KEY`, the database and
  the raw store. Fully trusted: nothing in the application protects data from the operator.
- **Tenant operator.** A customer of the instance, created with `ohs admin tenant create`. Holds
  one or more tenant API keys. Trusted with its own users' data only.
- **Tenant backend.** A server holding an `ohs_sk_` key. Can create users, mint user tokens,
  start provider connections, read and export data, manage webhooks and delete users, all within
  its tenant.
- **End-user device.** An app holding an `ohs_ut_` token for one user, with some of the scopes
  `upload`, `connect` and `read`.
- **Providers.** Call the unauthenticated routes `GET /oauth/{provider}/callback` and
  `POST /inbound/{provider}`. Anyone on the internet can call them too.
- **Webhook receivers.** URLs a tenant registers; `ohs` posts to them from the `etl` role.
- **Database and raw store.** Trusted storage (see limitations).

## Threats and mitigations by boundary

### Callers of the API (tenant backends, devices)

- **Stolen or guessed credentials.** Keys and tokens are 32 random bytes; only their SHA-256
  hash is stored, so a database read does not yield a usable credential
  (`internal/auth/keys.go`). A fast hash is adequate for secrets of that entropy. Revoked keys,
  expired tokens and tokens of a user being deleted are refused
  (`internal/tenant/store.go`, `LookupKey`).
- **A credential used beyond its purpose.** Each route declares what it takes: a tenant key, or
  a user token holding a given scope; anything else is 401 or 403 (`internal/auth/middleware.go`,
  routes in `internal/api/`). User tokens live one hour by default, at most 24 hours, or 30 days
  when `read` is the only scope (`internal/api/handlers_tokens.go`). The MCP server is read-only.
- **Reading another tenant's data.** Every query on user data filters on `tenant_id` and
  `user_id` taken from the authenticated principal, never from the request body. A user or
  object of another tenant answers 404, not 403, so ids cannot be probed
  (`internal/tenant/store.go`, `internal/api/read/`).
- **Oversized requests.** JSON bodies and inbound notifications are size capped, and device
  uploads are bounded by `OHS_INGEST_CONCURRENCY` (503 with `Retry-After` above it).
- **Tracing an incident.** Every response carries `X-Request-Id`, and every log line written for
  the request carries the same `request_id` (`internal/reqid/reqid.go`, `internal/api/router.go`).
  Request logs record the route pattern and status, never the query string or a header.

### Providers and the public routes

- **Forged OAuth callback / login CSRF.** `StartConnection` mints a 32 byte random state, stores
  only its hash with the tenant, user, provider and an expiry of 10 minutes. The callback
  consumes it with a single `DELETE ... RETURNING`, so a state works once, for one provider,
  and only before it expires. The tenant's `redirect_uri` must be https (http only for
  localhost). Providers that require PKCE get it (`internal/collect/oauth.go`).
- **Forged notifications.** Each connector verifies what its provider offers before anything is
  queued: HMAC signatures over the body (and a timestamp where the provider sends one, refused
  outside a tolerance window), a shared secret compared in constant time, or a signed callback
  URL for providers that sign nothing (`connectors/*/inbound.go`). A rejected request is 401 and
  enqueues nothing. An accepted event only schedules a pull with the user's own token for a user
  who already has an active connection. Only deletion notices are stored from the event itself:
  from Oura and WHOOP, which sign them, and from Strava only when `OHS_STRAVA_SUBSCRIPTION_ID`
  is set.
- **Slow or large bodies.** Inbound bodies are capped at 1 MiB and must arrive within 30 seconds
  (`internal/collect/inbound.go`).

### Stored provider tokens

- **Database theft.** Access and refresh tokens are sealed with AES-256-GCM under
  `OHS_ENCRYPTION_KEY` with a fresh random nonce. The additional authenticated data is
  `tenant_id|user_id|provider`, so a ciphertext copied to another row does not open
  (`internal/crypto/box.go`, `internal/collect/connections.go`). Webhook secrets are sealed the
  same way, bound to their endpoint id (`internal/webhook/endpoints.go`).

### Webhook receivers

- **Server-side request forgery.** Without `OHS_WEBHOOK_ALLOW_PRIVATE`, endpoint URLs must be
  https, carry no credentials, and not name localhost or a private, loopback, link-local
  (cloud metadata), CGNAT, multicast or unspecified address. The same check runs again in the
  dialer on the address actually connected to, so a name that resolves or rebinds to a private
  address is refused. Redirects are not followed (`internal/webhook/guard.go`,
  `internal/webhook/worker.go`).
- **Forged or replayed deliveries.** Requests follow Standard Webhooks: `webhook-id`,
  `webhook-timestamp` and an HMAC-SHA256 signature over `id.timestamp.body`, with a 5 minute
  tolerance on the receiver side (`internal/webhook/signature.go`). Secrets can be rotated with
  `POST /v1/webhooks/{id}/rotate-secret`.
- **Data leaking through webhooks.** Payloads say what changed, for whom and over which days,
  never a health value; the receiver reads data through the API with its own key.

### Logs

- Logs never contain keys, tokens, OAuth codes or health values. Provider error text has URL
  query strings stripped before it is logged (`internal/collect/connections.go`, `safeErr`).
  Logs do contain tenant, user and connection ids, provider names and route patterns.

### Metrics

- **Instance information on `/metrics`.** The Prometheus counters are instance wide (requests by
  route, ingest load, queues, sync state by provider), never per-tenant data, but they are
  closed by default. On the API listener a scrape must send the `OHS_METRICS_TOKEN` bearer token
  (16 characters or more, compared in constant time; 401 otherwise), and with no token set every
  scrape is refused with 403. `OHS_METRICS_LISTEN` moves `/metrics` to an address of its own,
  for example `127.0.0.1:9464`, where whoever reaches the address can scrape it unless the token
  is also set. `OHS_METRICS=false` removes the route (`internal/api/handlers_ops.go`,
  `internal/app/app.go`).

### Erasure

- `DELETE /v1/users/{id}` marks the user deleting in one transaction (their tokens stop working
  at once), then a worker in the `etl` role revokes the provider grants, deletes every raw file
  under `raw/<tenant>/<user>/`, and deletes the user's rows from every table of `ohs_sync` and
  `ohs_data` that has a `user_id` column (found from the catalog, so a new table is covered),
  their tokens, idempotency records, unsent webhook deliveries and the user row
  (`internal/deletion/`). It survives restarts and retries with backoff.

### Accountability, consent and portability

- **Who did what.** Every request authenticated with a tenant key or a user token writes one
  event to `ohs_core.audit_events`, whatever its outcome, with the credential, the user it
  touched, the action and the status; `ohs admin` commands and erasures are recorded too. A
  database trigger refuses `UPDATE`, `DELETE` and `TRUNCATE` except for the retention job
  (`OHS_AUDIT_RETENTION_DAYS`). Tenants read it with `GET /v1/audit` (`internal/audit/`,
  [audit-log.md](audit-log.md)).
- **Data collected beyond consent.** A user's consent is kept per source and per data category.
  The ETL drops the records of a category not granted before anything is written, and a
  revocation queues a purge of what was already stored. `OHS_CONSENT_REQUIRED=true` stores
  nothing from a source with no consent recorded. Every change is kept with the key that made it
  and announced by the `consent.granted` and `consent.revoked` webhooks (`internal/consent/`,
  [consent.md](consent.md)).
- **Portability.** `GET /v1/users/{id}/archive` streams everything held about one user as one
  zip file, never a token or a sealed value; a failure after the first byte aborts the
  connection so a truncated archive never looks complete (`internal/api/handlers_archive.go`,
  [exports.md](exports.md#complete-user-archive-data-portability)).

## Known limitations and residual risks

- **Health data is not encrypted by the application.** Records in Postgres and payloads in the
  raw store are in clear (raw files are gzip). Encryption at rest is the operator's job: disk or
  volume encryption, and server-side encryption on the bucket. Raw files are created with mode
  0600 and folders 0750 by the filesystem driver; the S3 driver sets no encryption header.
- **The database and the raw store are trusted.** Anyone who can read them reads every tenant's
  health data. Anyone who can also read the environment can open the OAuth tokens. Tenant
  isolation is enforced in application queries, not by Postgres row-level security.
- **One encryption key, no rotation.** Changing `OHS_ENCRYPTION_KEY` makes the stored OAuth
  tokens and webhook secrets unreadable; users must reconnect and webhook secrets must be
  rotated. Losing it has the same effect; leaking it exposes the tokens of a stolen database.
- **No rate limiting** in the application beyond the upload concurrency bound. Brute force of
  credentials is not practical at 256 bits, but request floods must be handled at the proxy.
- **OAuth state is not bound to a browser session.** Whoever completes a consent link connects
  the account they sign in with to the user it was minted for. Tenants must hand an authorize
  URL only to its user.
- **Some providers sign nothing.** Without `OHS_STRAVA_SUBSCRIPTION_ID` (Strava) or
  `OHS_POLAR_WEBHOOK_SECRET` (Polar), anyone can post an event that triggers a pull of a
  connected user's recent window. The pull uses that user's own token, so no data is injected.
  Strava deletion events are acted on only with the subscription id set, and that id is a
  shared value, not a signature: someone who learns it can forge the deletion of a connected
  athlete's activity.
- **User tokens cannot be revoked through the API.** They expire, stop working when the user is
  deleted, or the instance operator revokes one with `ohs admin key revoke --key ohs_ut_...`.
  A user token minted with a key stays valid after that key is revoked, until it expires.
- **Provider revocation on deletion is best effort.** If a provider is unreachable, or the
  process carrying out the deletion lacks that provider's keys, the data is still deleted but the
  grant may stay valid at the provider.
- **Deletion does not reach copies.** Backups, bucket object versions, webhook receivers and
  anything a tenant already read keep what they hold.
- **The bootstrap key is logged.** `OHS_BOOTSTRAP_TENANT` logs the new tenant's API key once, at
  first start. Treat that log line as a secret, or create tenants with `ohs admin tenant create`.
- **Transport security is external.** `ohs` serves plain HTTP; TLS terminates at a reverse
  proxy. The bundled compose file connects to Postgres with `sslmode=disable` on the compose
  network.

## Out of scope

- The security of provider APIs and of the accounts users sign in with there.
- The host, container runtime, reverse proxy, database server, S3 service and backups.
- Clients built by tenants: how they store keys and tokens, and what they do with the data.
- Medical correctness of the data; the application validates plausibility only.
- An instance operator acting against tenants or users.

## Planned hardening

These are in progress and not in a release yet; check the [changelog](../CHANGELOG.md) for what
has shipped:

- encryption key rotation (`OHS_PREVIOUS_ENCRYPTION_KEYS`, `ohs admin rekey`);
- rate limits on the provider-facing routes (`OHS_RATE_LIMIT_*`, `OHS_TRUSTED_PROXIES`).
