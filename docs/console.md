# Admin console

A web console inside the `ohs` binary for people who run an instance. It shows the whole
pipeline (sources, sync, processing, storage and delivery) and runs it: turn providers on and
save their keys, create users and send them a connection link, start a sync, retry what is
stuck, manage webhook endpoints and API keys, and read the audit log. Everything it does is a
call to a documented API route with your tenant key, exactly as `curl` would.

> **0.2.0 and later.** The actions ship in 0.2.0; the 0.1.0 image serves a read-only console. The
> React console described here replaces the hand-written page of earlier builds.

![The overview, light](assets/console/overview-light.png#gh-light-mode-only)
![The overview, dark](assets/console/overview-dark.png#gh-dark-mode-only)

## Turn it on

The console is off by default. Set `OHS_CONSOLE` on the process that runs the `api` role and
restart it:

```sh
# .env
OHS_CONSOLE=true
```

| Value | Console |
|---|---|
| `false` (default) | Not mounted: `/console` answers 404 |
| `true` | The management console, with its actions |
| `read-only` | The same pages without any action: every session the console opens is read only, and the server refuses every request of such a session but `GET` and `HEAD` (see [Read only](#read-only)). For a wall screen. A bearer key keeps all its rights, so it is not a way to take a key's rights away |

Then open `https://your-host/console` and sign in once with a tenant API key (`ohs_sk_...`, from
`ohs admin tenant create`, `ohs admin key create`, the bootstrap tenant or the
[first-run setup](setup.md)). The server answers with a session, and the page uses the session from
then on: see [Sign in](#sign-in). Everything else in this guide happens inside that session.

No key yet and no terminal? With `OHS_SETUP_TOKEN` set, the sign-in screen of an instance that
has no tenant links to `/console/setup`, a page that creates the first tenant and shows its key
once; see [setup.md](setup.md). It writes nothing once a tenant exists.

The console sees what that key sees: one tenant. An instance admin can switch to another tenant
without another key, from the tenant picker (see [Switching tenant](#switching-tenant)); any other key sees its own tenant, and
to look at another one you sign out and use its key.

## Sign in

Pasting a key opens a **console session**: the page sends the key once, in the body of
`POST /console/session`, and the server answers with a cookie and a CSRF token. The key is not kept
anywhere after that, not in the page, `sessionStorage`, `localStorage` or a cookie.

- **The cookie** is named `ohs_console`: `HttpOnly`, `SameSite=Strict`, `Path=/`, and `Secure`
  unless the public URL is `http://localhost`, `http://127.0.0.1` or `http://[::1]`, or there is no
  public URL and the request is plain http to this machine. It has no `Max-Age`, so the
  browser drops it when it closes; the server enforces the lifetime. It holds a random token of 32
  bytes (43 characters, base64url); the database stores only its SHA-256 hash.
- **The CSRF token** is another random token. The page keeps it in memory only, and sends it in
  the `X-OHS-CSRF` header on every request but `GET`, `HEAD` and `OPTIONS`. Reloading the page
  reads `GET /console/session`, which answers a new token and ends the old one; a second open tab
  refreshes its token once and retries.
- **Lifetime.** A session ends after 12 hours without a request, or 7 days after it was created,
  or when its API key is revoked or expires, or its tenant is deleted. Sign out
  (`DELETE /console/session`) ends it at once. Ended sessions are deleted by a daily `etl` job.
- **A failed sign in** counts against `OHS_RATE_LIMIT_AUTH_FAILURES` like any bad key, and is an
  `auth.failed` event. The key needs the `tenant:read` scope, and a user token (`ohs_ut_...`) is
  refused.
- **Plain http** is allowed only on `localhost`, `127.0.0.1` and `[::1]`. Elsewhere, sign in is
  refused (403) until `OHS_PUBLIC_URL` is the `https` address of the instance, so the cookie never
  travels in the clear. An `OHS_PUBLIC_URL` that is plain `http` on a remote host is refused the
  same way: browsers drop the `Secure` cookie it would get.
- **Bearer wins.** A request with an `Authorization` header is authenticated by it, unchanged, and
  its cookie is ignored. `curl` with a key works as before. The session routes exist only when
  `OHS_CONSOLE` is `true` or `read-only`; with the console off the cookie is never read.

## Switching tenant

A session of an instance admin tenant can act on another tenant: `PUT /console/session/tenant` with
`{"tenant_id": "..."}` (the key needs `admin:read`; 404 for an unknown tenant). The session then
answers every tenant route for that tenant, with the admin key's own rights, while the tenant the
admin signed in with stays the session's tenant. `GET /console/session` returns both as `tenant`
and `acting_tenant`. An admin session shows an "Acting tenant" picker at the top of the navigation;
choosing a tenant calls this route, and the navigation then reads "Acting as" with its name. A
session of any other tenant cannot switch (403). Deleting the tenant a session acts on ends that
session, and so does `ohs admin tenant admin --off` on the tenant it signed in with, once it
views another tenant. A token a session mints for a user of another tenant is tied to no key. The
audit log records the switch and what you do after it; see [audit-log.md](audit-log.md).

## Read only

With `OHS_CONSOLE=read-only`, every new session is read only. The server enforces it, not the page:
a request of such a session with any method but `GET` or `HEAD` answers 403 with "this
console session is read-only (OHS_CONSOLE=read-only)". It can still sign out and switch tenant.
A bearer key is not affected. Sessions opened before the setting changed keep the mode they were
created with.

## The instance admin

Most pages work with any tenant key. Provider keys are different: they are instance wide, since
every tenant's users connect through the same provider apps. Changing them takes a key of an
**instance admin** tenant. The sidebar says which kind of key is signed in.

- The first tenant of an instance is its admin, however it was created (`ohs admin tenant
  create`, the first-run setup, `ohs admin personal setup`). So is the tenant named by
  `OHS_BOOTSTRAP_TENANT`, at every start.
- `ohs admin tenant create --name <name> --admin` creates another admin tenant.
  `ohs admin tenant admin --tenant <id>` makes an existing tenant one, and `--off` stops it.
  `ohs admin tenant list` shows which tenants are admins.
- On upgrade, migration 0038 makes the only tenant of a single-tenant instance its admin. An
  instance with several tenants gets no admin until you name one with `ohs admin tenant admin`.

A key of a tenant that is not an admin sees the providers enabled on the instance, read only.

Admin keys also manage the instance's tenants over HTTP: create one per team, create and revoke
its keys, read its pipeline, fill a demo user. The console itself stays on the tenant of the key
signed in, or the tenant an admin session switched to; see [admin-api.md](admin-api.md) for those routes.

## Pages

The console has three panes. The navigation on the left lists the pages (Workspace: Overview,
Users, Providers, Jobs. Developers: Webhooks, API keys, Audit log. Instance: Settings, for admins
only), the tenant picker of an admin session, the theme switch and Sign out. The page sits in the
middle. The **inspector** on the right shows the API call behind the page: the request as cURL,
TypeScript or Python (copy it, with `$OHS_API_KEY` where the key goes), the answer as JSON, and the
settings that shape what the page shows. The language you pick and whether the inspector is open
are kept in the browser; nothing else is.

Press Ctrl+K (Cmd+K on a Mac) for the **command palette**: type to jump to a page, a setting, or
run an action such as creating a user, a webhook endpoint or an API key. Arrow keys move, Enter
runs, Escape closes. Tables are keyboard driven too: Tab into one, arrows move the selection, and
Enter (or a double click) opens the row.

![A user, dark](assets/console/user-dark.png#gh-dark-mode-only)
![A user, light](assets/console/user-light.png#gh-light-mode-only)

**Overview.** The pipeline as five stages, each with its headline count and a state:

| Stage | Counts |
|---|---|
| Sources | Connections, cloud providers on, users, connections that need the user to reconnect |
| Sync | Runs that succeeded in the last 24 hours, runs queued and running, runs that failed in the last 24 hours |
| Processing | Jobs waiting, running and parked, rejected records |
| Storage | Raw data and raw objects stored in the last 24 hours |
| Delivery | Webhook endpoints, failing ones, deliveries pending and parked |

Below it, a "Needs attention" list links each problem to the page that fixes it, and "Get started"
ticks off the first steps (turn on a provider, add a user, connect an account, receive data, add a
webhook endpoint). "First data" counts the last 24 hours, so an idle tenant with older data shows
that step as not done.

**Providers.** Every connector the binary knows, cloud providers first, then app uploads (Apple
Health, Health Connect, Xiaomi, activity files, self reports), each with its state: on, off, not
set up, or refused. Opening a cloud provider walks through turning it on:

1. **Register an app** at the provider. The page shows the redirect URL (and, for a provider that
   sends notifications, the notification URL) to paste in the provider's developer portal, each
   with a copy button. They are built from `OHS_PUBLIC_URL`; the page warns when it is not set,
   since a provider cannot reach `localhost`.
2. **Save its keys.** One row per key, required ones first. A secret is write only: the page
   shows whether it is set, never its value. Keys that are not secrets (`CLIENT_ID`, `REGION`,
   `ENVIRONMENT`, `WEBHOOK_SIGNATURE`, `SUBSCRIPTION_ID`) show their value. The connector checks
   the keys before they are stored, so a typo in a region or a signature mode is refused with the
   connector's own message and nothing changes.
3. **Turn it on.** The provider is live at once on this process, and on every other process (a
   second replica, a separate `collect` or `etl` role) within 30 seconds. No restart.

Turning a provider off keeps its connections and data. Pulls, new connections, the OAuth callback
and notifications stop until it is on again. "Forget saved settings" deletes the keys and the
on/off choice saved in the console, after you type the provider's slug. A key that is not an
instance admin sees the providers enabled on the instance, read only.

**Users.** The tenant's users, 50 per page, with a search by exact `external_ref` and "New user".
A user's `external_ref` is your own identifier for them; never put a name or an email in it.

**User.** A header with the user's state, "Connection link" and "Delete user" (type the name), and
tabs: **Data** (coverage per provider, a preview of recent records, and downloads), **Connections**
(health, freshness, sync now, disconnect with an optional erase), **Consents**, **Sync runs** and
**Shares** (the last two and Consents are read only). "Connection link" creates a
[connection link](connect-page.md) to send the user. The Data tab previews the first rows of daily
summaries, sleep and workouts only when you press a button, and downloads FHIR, Open mHealth, CSV
per dataset and the user's archive. The console draws no charts of health data.

**Jobs.** Sync runs that failed, across the tenant's users, newest first, and processing jobs that
spent their attempts (parked). Retry one parked job, or all of them.

**Webhooks.** Endpoints with their state and recent deliveries. Create one, edit it, turn it off
and on, send a test event, rotate its signing secret, delete it (type its URL); retry or replay a
delivery. See [webhooks.md](webhooks.md).

**API keys.** The tenant's keys: prefix, created, last used, [scopes](api.md#key-scopes),
revoked. Create one (the full key is shown once) or revoke one (type its prefix). A key created
here can hold any subset of the scopes of the key signed in. The key signed in cannot revoke
itself, so the console cannot lock you out. Sign in with a key that holds `*`: with fewer scopes,
the pages whose routes the key cannot call show the 403 instead.

**Settings.** Every server setting in one list, grouped by section (Server, Storage, Security, Console,
Metrics, Retention, Consent, Ingest, Sync, Processing, Webhooks, Event stream, Rate limits, Vault). Each setting has
its own address, `/console/settings/<section>/<name>`. Each row shows its value, where the value comes from (`env`,
`console` or `default`) and how a change applies (see [Settings saved from the console](#settings-saved-from-the-console)).
Edits are collected, then "Review" shows a diff and "Apply" saves them in one call, with the revision you read. A
secret is write only: the page says whether it is set, never what it is. A setting the environment sets is locked,
with the value the environment gives, and a stored value it overrides stays visible. A stored value the server
skipped as invalid is shown with its message. "Export" downloads the effective configuration as a `.env` file, with
every secret left as `# set`. The page also lists the running processes and says which are behind or stale. Only
instance admins see Settings.

Raising `OHS_RETENTION_DAYS` from `0` (keep everything) to a number deletes raw files and health data older than that
many days at the next daily cleanup. It is a live setting, so a save from this page takes effect without a restart:
check the number before you save it.

**Audit log.** The tenant's [audit events](audit-log.md), newest first, filtered by action and by
actor (one of the tenant's API keys). Selecting an event shows it in the inspector.

A session that is read only (see [Read only](#read-only)) sees every page without a write button.

## Where provider keys come from

A provider runs with its `OHS_<PROVIDER>_*` environment variables when any of them is set, else
with the keys saved in the console. The two are never mixed: configuration as code wins, provider
by provider, and the console shows those keys as set by the environment and does not edit them.
The on/off switch applies to both, so a provider configured by environment can be turned off from
the console without a redeploy.

Keys saved in the console are stored in `ohs_core.provider_settings`, sealed with
`OHS_ENCRYPTION_KEY` (AES-256-GCM, bound to the provider). `ohs admin rekey` re-seals them after a
[key rotation](key-rotation.md). A process that cannot open them (a wrong or lost encryption key)
leaves that provider off and the console says why; save its keys again from the console.

The routes behind the page are `GET /v1/admin/providers`, `GET`, `PATCH` and `DELETE`
`/v1/admin/providers/{provider}`, for admin keys only (403 otherwise). See [api.md](api.md).

## Settings saved from the console

For each setting, a process runs with the first of these:

1. its environment variable (or its `_FILE` twin), when it is set and not empty;
2. the value saved from the console;
3. the default.

The environment always wins. The console never mixes the two sources for one setting, and a setting the
environment sets answers 409 when you try to change it.

A setting applies in one of three ways:

- **live**: every process applies it within 30 seconds, without a restart. These are `OHS_LOG_LEVEL`,
  `OHS_RETENTION_DAYS`, `OHS_AUDIT_RETENTION_DAYS`, `OHS_TRACKING_RETENTION_DAYS`, `OHS_CONSENT_REQUIRED`,
  `OHS_BACKFILL_DAYS`, `OHS_WEBHOOK_PAUSE_AFTER`, `OHS_WEBHOOK_ROTATION_GRACE_HOURS`, `OHS_RATE_LIMIT_API`,
  `OHS_RATE_LIMIT_AUTH_FAILURES`, `OHS_RATE_LIMIT_INBOUND`, `OHS_RATE_LIMIT_INBOUND_PROVIDER` and
  `OHS_RATE_LIMIT_OAUTH`. A rate limit that changes starts again with full counters.
- **restart**: every other setting. It is stored at once and applies when each process next starts.
- **bootstrap**: read from the environment before the database opens, or too risky to change from a page the
  process serves. Never stored, never editable here; the console shows the value (a secret as `set`). These are
  `OHS_DATABASE_URL`, `OHS_ENCRYPTION_KEY`, `OHS_ENCRYPTION_KEY_FILE`, `OHS_PREVIOUS_ENCRYPTION_KEYS`, every
  `OHS_VAULT_*` variable, `OHS_RAW_STORE`, `OHS_LISTEN`, `OHS_OPS_LISTEN`, `OHS_METRICS_LISTEN`, `OHS_ROLES`,
  `OHS_CONSOLE`, `OHS_SETUP_TOKEN`, `OHS_SETUP_TOKEN_FILE` and `OHS_BOOTSTRAP_TENANT`.

Stored values live in `ohs_core.instance_settings`; a secret is sealed with `OHS_ENCRYPTION_KEY`, bound to its name,
and `ohs admin rekey` re-seals it. A stored value that does not hold (it fails its check, or no configured key opens
it) does not stop a process: it is skipped, logged by name and never by value, and the environment value or the
default applies. A stored name no release setting reads any more is ignored. `ohs doctor` lists both kinds, and
`{"OHS_OLD": null}` in a `PATCH` removes a stored setting a later release no longer has.

Each process writes a heartbeat every 30 seconds. A process is **behind** when a `restart` setting changed after the
settings it loaded at start, so it still runs the old value. It is **stale** when its live settings have been older
than the stored ones for more than 60 seconds, which points at a process that cannot reach the database. The
console hides a heartbeat not seen for 2 minutes.

The routes behind the page, for admin keys only (403 otherwise):

| Route | Purpose |
|---|---|
| `GET /v1/admin/settings` | Every setting, its source, effective value and the current revision (`ETag`) |
| `PATCH /v1/admin/settings` | `{"OHS_X": "value", "OHS_Y": null}`; `null` removes the stored value. Needs `If-Match` with the revision you read. All or nothing |
| `GET /v1/admin/settings/export` | The effective configuration as a `.env` file |
| `GET /v1/admin/processes` | The running processes with `behind` and `stale` |

A `PATCH` answers 200 with the new revision and the names that need a restart; 428 without `If-Match`; 412 when the
revision moved (read again, then retry); 409 naming each setting that is bootstrap or set by the environment; 422 with
one message per invalid value. See [api.md](api.md#admin-settings). Each `PATCH` is written to the
[audit log](audit-log.md) with the names it changed, never the values.

## Security model

- **A server session, not the key.** The key is sent once to sign in ([Sign in](#sign-in)) and the
  browser holds only an `HttpOnly` cookie from then on. The page never keeps a key: not in
  `localStorage`, `sessionStorage`, a cookie it writes, or a URL. The key is never logged,
  returned or written to an audit event. The API's own key checks still apply to every request,
  through the key behind the session. A user token (`ohs_ut_...`) is refused.
- **Cookie and CSRF.** The cookie is `SameSite=Strict` and a request with it needs the
  `X-OHS-CSRF` header, kept in the page's memory only. A request with a cookie and a foreign
  `Origin` is refused, and so is a sign in with one. A stale token is a 403; the page refreshes it
  once and retries once.
- **One way to the server.** Every request goes through one wrapper that refuses a URL that is not
  a path on the same server and follows no redirect. It sends the cookie and the CSRF header to
  this server only. In `read-only` mode the server, not the wrapper, refuses every method but
  `GET` and `HEAD` of a session.
- **Writes ask first.** Every action asks for a confirmation. Deleting a user or a webhook
  endpoint, revoking a key and forgetting a provider's keys ask you to type the name of what
  goes. A secret the API returns once (a new API key, a webhook signing secret) is shown once
  with a copy button and dropped from the page when the dialog closes. Secrets are never
  revealed afterwards, and there is no restart button.
- **Health values only on demand.** The console counts records; it shows values only when you
  press a preview or a download button on a user, and each read is in the audit log as
  `data.read` or `data.export`. Downloads are built in the page from the API answer, so the key
  never goes into a URL.
- **Same origin, strict headers.** The page, its scripts, its stylesheet and its fonts are embedded
  in the binary; nothing loads from a CDN. Every console response carries
  `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; connect-src
  'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and
  `X-Frame-Options: DENY`. The HTML and every file outside `assets/` are sent with
  `Cache-Control: no-cache`; the content hashed files under `assets/` with `public,
  max-age=31536000, immutable`. The page has no inline script or style, and API data is inserted
  as text, never as markup.
- **Exposure.** The page itself holds nothing secret, and without a key it shows only the sign-in
  form. Still, if you prefer that operators reach it only from inside, block `/console` at your
  reverse proxy for the public address, or put it behind the proxy's own login, or leave
  `OHS_CONSOLE` off and call the same routes with `curl`.

## Look and feel

The console is quiet and dense: a navigation, a page and an inspector, with Inter for text and
JetBrains Mono for code, keys, ids and setting names. The colours are tokens, in two sets.

| Token | Light | Dark |
|---|---|---|
| Page | `#f6faf7` | `#070a09` |
| Panel | `#ffffff` | `#0e1311` |
| Raised | `#f1f6f3` | `#121916` |
| Ink | `#0b1510` | `#eef4f0` |
| Muted | `#4b5a53` | `#8fa39a` |
| Accent | `#17a865` | `#3ddc8e` |

Pick Light, Dark or Auto (follow the system) at the foot of the navigation; the choice stays in
the browser. Below 1024 px wide the navigation folds into a menu, and below 768 px the inspector
is a bottom sheet.

## Working on the console

The console is a React app in `web/console` (Node.js 22), built into `internal/console/dist`, which
the binary embeds. Both `dist` and `web/console/src/api/schema.ts` (the API types, generated from
`api/openapi.yaml`) are committed, and CI fails when either differs from what a build writes.

| Command | What it does |
|---|---|
| `make console` | Installs the pinned packages, writes `schema.ts` and builds `dist`. Run it after a change to the app or to `api/openapi.yaml`, and commit the result |
| `make console-dev` | Vite dev server on port 5173, proxying the API to `OHS_DEV_TARGET` (default `http://localhost:8080`). Set the local server's `OHS_PUBLIC_URL` to the same address, since a sign in is checked against it |
| `make console-e2e` | The browser tests (Playwright) against two real `ohs serve` processes. Needs `OHS_E2E_DATABASE_URL`, the URL of an empty Postgres database with TimescaleDB |

Unit tests, types and lint run with `npm test`, `npm run typecheck` and `npm run lint` in
`web/console`. To refresh the screenshots above, run the end to end tests with `OHS_E2E_SHOTS=1`
(`e2e/screenshots.spec.ts` writes them under `docs/assets/console`).
