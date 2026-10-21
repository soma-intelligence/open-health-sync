# Spec: admin console

Status: draft for review (2026-10-17). Guide: [console.md](../console.md). Plan:
[plans/admin-console.md](../plans/admin-console.md).

## Problem

The binary ships a read-only console at `/console`: it shows users, connections, freshness and
webhook deliveries. Everything else an operator does to run the pipeline still takes a terminal:

- Turning a provider on means editing `OHS_<PROVIDER>_*` environment variables and restarting
  every process. There is no screen that says which providers exist, which are on, what keys
  each one needs, or which redirect URL to register at the provider.
- Creating a user, sending a user the connection link, starting a sync, disconnecting a
  provider, deleting a user: one `curl` call each.
- A processing job that spent its attempts stays parked until someone writes SQL.
- Webhook endpoints are created, tested, rotated and replayed with `curl`.
- API keys are created and revoked with `ohs admin key`, which needs a shell on the server.
- Nothing shows the pipeline as a whole: sources, sync, processing, storage and delivery, with
  what is stuck at each stage.

A self-hoster who runs Open Health Sync for a study, a clinic or their own family should be able
to run all of it from a browser, without learning the API first.

## Scope

### 1. One console for the whole pipeline

`/console` becomes a management console, still one static page embedded in the binary (no Node
at build or run time, no CDN, no new Go dependency). Pages:

| Page | Shows | Does |
|---|---|---|
| Overview | The pipeline as five stages (sources, sync, processing, storage, delivery), each with its counts and a state; a "needs attention" list | Links to the page that fixes each item |
| Providers | Every connector the binary knows: status, kinds, where its keys come from, the URLs to register at the provider, the keys it needs | Turn a provider on or off, save its keys (instance admin only, see 3) |
| Users | Users, 50 per page | Create a user |
| User | Connections, freshness, recent sync runs, coverage, data outputs | Create a connection link, sync now, disconnect, preview recent data, download FHIR, Open mHealth, CSV and the archive, delete the user |
| Jobs | Recent sync runs across users (failed first), parked processing jobs | Retry one parked job, retry every parked job |
| Webhooks | Endpoints and deliveries | Create, edit, turn off and on, send a test, rotate the secret, delete; retry and replay a delivery |
| API keys | The tenant's keys: prefix, created, last used, revoked | Create a key (shown once), revoke a key |
| Audit log | The tenant's audit events, newest first, filtered by action | Nothing |

Every action calls a documented API route with the pasted tenant key, exactly as `curl` would.
The console adds no server-side session, cookie or credential.

### 2. Modes

`OHS_CONSOLE` takes `false` (default: not mounted, 404), `true` (the management console) or
`read-only` (today's console: the page refuses any request but `GET` and shows no action). The
read-only mode is a convenience for a wall screen, not a security boundary: the key it holds can
call every route with `curl`.

### 3. Providers turned on from the console

- New table `ohs_core.provider_settings`, one row per provider: the keys entered in the console
  (one JSON object sealed with `OHS_ENCRYPTION_KEY`, bound to the provider slug), an `enabled`
  flag, who changed it and when. `ohs admin rekey` re-seals it after a key rotation.
- The keys a provider runs with: its `OHS_<PROVIDER>_*` environment variables when any is set
  (configuration as code wins, as a whole, never mixed with stored keys), else the keys stored
  from the console. The `enabled` flag applies to both, so a provider configured by environment
  can be turned off from the console without a redeploy.
- No restart. The process that saves a change applies it at once; every other process (another
  replica, a separate `collect` or `etl` role) reloads the settings within 30 seconds. A saved
  change is validated by the connector's own `Configure` before it is stored, so a typo in a
  region or a signature mode is refused with the connector's message and nothing changes.
- Turning a provider off keeps its connections and data. Pulls, new connections, the OAuth
  callback and notifications stop until it is on again, exactly as when its environment
  variables are removed today.
- Device sources (Apple Health, Health Connect, Xiaomi, activity files, self reports) need no
  key and stay always on.
- Secrets are write only. The API returns a key's value only for keys that are not secrets
  (`CLIENT_ID`, `REGION`, `ENVIRONMENT`, `WEBHOOK_SIGNATURE`, `SUBSCRIPTION_ID`); for every
  other key it says whether it is set. A connector declares its optional keys beside the
  required ones so the form can list them.

### 4. The instance admin tenant

Provider keys are instance wide: every tenant's users connect through the same OAuth apps. So
changing them needs more than any tenant key. A tenant gets an `admin` flag:

- Set on the first tenant of the instance, however it is created (the browser first run,
  `ohs admin tenant create`, `ohs admin personal setup`), on the `OHS_BOOTSTRAP_TENANT` tenant
  (at every start, since the operator named it), and by `ohs admin tenant create --admin`.
- `ohs admin tenant admin --tenant <id>` sets it, `--off` clears it. `ohs admin tenant list`
  shows it.
- Migration 0038 sets it on the only tenant of an instance that has exactly one, so a
  self-hosted instance upgrades with its console working. An instance with several tenants
  gets no admin until the operator names one.
- The `/v1/admin/*` routes need a key of an admin tenant (403 otherwise). Every other route is
  unchanged.

### 5. New routes

All behind a tenant API key, all recorded in the audit log like every route. An event of a route
that names a provider, a key or a job in its path keeps that identifier in `detail`.

| Route | Purpose |
|---|---|
| `GET /v1/tenant` | The tenant of the key: id, name, `admin`, and the prefix of the key in use |
| `GET /v1/keys` | The tenant's API keys, revoked ones included, and which one is in use |
| `POST /v1/keys` | Create a key; the full key is in the 201 answer only, never stored in clear |
| `DELETE /v1/keys/{key_id}` | Revoke a key by its id; 409 for the key making the call, so the console cannot lock itself out |
| `GET /v1/pipeline` | The tenant's pipeline in one answer: users, connections per provider and status, sync runs queued, running and failed in the last 24 hours, providers paused by their rate limit, processing jobs pending, running and parked, rejected records, raw objects stored in the last 24 hours, webhook endpoints and deliveries |
| `GET /v1/pipeline/sync-runs` | Sync runs across the tenant's users, newest first, with `status` (any run status, `failed` for the runs that gave up) and `provider` filters |
| `GET /v1/pipeline/etl-jobs` | Processing jobs, filtered by `status` (`parked` by default, `pending`, `running`) and `provider` |
| `POST /v1/pipeline/etl-jobs/{job_id}/retry` | Queue a parked job again with fresh attempts; 409 when it is not parked |
| `POST /v1/pipeline/etl-jobs/retry` | Queue again every parked job of the tenant (or of one `provider`) |
| `GET /v1/admin/providers` | Every connector: slug, name, kinds, status (`active`, `off`, `not_configured`, `error`, `built_in`), key source (`env`, `console`), keys (name, required, secret, set, value when not secret), the redirect and notification URLs |
| `GET /v1/admin/providers/{provider}` | One of them |
| `PATCH /v1/admin/providers/{provider}` | Set keys (a `null` value removes one) and `enabled`; 409 when its keys come from the environment and the body changes keys |
| `DELETE /v1/admin/providers/{provider}` | Forget what the console stored for a provider (its keys and the `enabled` flag) |

Counts in `GET /v1/pipeline` cover the caller's tenant only. Paused providers and the enabled
providers are instance facts every tenant already sees through `GET /v1/providers` and sync
runs.

### 6. Safety in the page

- Every write asks for a confirmation. Deleting a user, deleting a webhook endpoint, revoking a
  key and forgetting a provider's keys ask the operator to type the name of what goes.
- A secret the API returns once (a new API key, a webhook signing secret) is shown once with a
  copy button and is not kept in the page after it is dismissed.
- Health values appear only when the operator asks for them (the "Preview" and download
  buttons of a user), and each read is in the audit log as `data.read` or `data.export`.
- The content security policy stays: no inline script or style, nothing from another origin,
  no framing. `form-action 'none'` stays: forms are handled in script. Downloads are built in
  the page from the API answer, so the key never goes into a URL.

## Out of scope

- Login with a password or SSO, roles inside a tenant, several operators with their own
  credentials. The console stays a client of the tenant key. A hosted platform keeps its own
  console.
- Managing tenants from the browser. `ohs admin tenant` stays the way to create one.
- Editing settings other than provider keys (retention, rate limits, consent mode). They stay
  environment variables.
- Charts of health data. The console shows the pipeline, not a health dashboard.
- Connecting a user's account from the console on their behalf. The console creates the
  hosted connection link; the user opens it and consents with their own account.

## Decisions to confirm

- `OHS_CONSOLE=true` now shows write actions. An instance that turned the console on for its
  read-only view gets them on upgrade; `read-only` keeps the old behavior. The CHANGELOG says so.
- The first tenant is the instance admin, and migration 0038 promotes the only tenant of a
  single-tenant instance.
- Environment variables win over keys saved in the console, provider by provider.
