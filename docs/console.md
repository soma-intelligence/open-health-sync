# Admin console

A web console inside the `ohs` binary for people who run an instance. It shows the whole
pipeline (sources, sync, processing, storage and delivery) and runs it: turn providers on and
save their keys, create users and send them a connection link, start a sync, retry what is
stuck, manage webhook endpoints and API keys, and read the audit log. Everything it does is a
call to a documented API route with your tenant key, exactly as `curl` would.

> **Next release.** The actions and this design are on `main` and ship in the next release; the
> 0.1.0 image serves a read-only console.

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
| `read-only` | The same pages without any action: the page refuses every request but `GET`. For a wall screen. It is not a security boundary, since the key it holds can call every route with `curl` |

Then open `https://your-host/console` and paste a tenant API key (`ohs_sk_...`, from
`ohs admin tenant create`, `ohs admin key create`, the bootstrap tenant or the
[first-run setup](setup.md)).

No key yet and no terminal? With `OHS_SETUP_TOKEN` set, the sign-in screen of an instance that
has no tenant links to `/console/setup`, a separate page that creates the first tenant and shows
its key once; see [setup.md](setup.md). It writes nothing once a tenant exists.

The console sees what that key sees: one tenant. To look at another tenant, sign out and use its
key.

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
signed in; see [admin-api.md](admin-api.md) for those routes.

## Pages

**Overview.** The pipeline as five stages, each with its headline count and a state:

| Stage | Counts |
|---|---|
| Sources | Connections, cloud providers on, users, connections that need the user to reconnect |
| Sync | Runs that succeeded in the last 24 hours, runs queued and running, runs that failed in the last 24 hours |
| Processing | Jobs waiting, running and parked, rejected records |
| Storage | Raw data and raw objects stored in the last 24 hours |
| Delivery | Webhook endpoints, failing ones, deliveries pending and parked |

Below it, a "needs attention" list links each problem to the page that fixes it, and the sources
table lists every provider with its connections.

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
on/off choice saved in the console.

**Users.** The tenant's users, 50 per page, and "New user". A user's `external_ref` is your own
identifier for them; never put a name or an email in it.

**User.** Connections with their health, freshness per source, recent sync runs and coverage.
Actions: create a [connection link](connect-page.md) to send the user, sync a connection now,
disconnect it (optionally erasing its data), and delete the user. The data card shows a preview
of recent records and downloads FHIR, Open mHealth, CSV per dataset and the user's archive.

**Jobs.** Sync runs that failed, across the tenant's users, newest first, and processing jobs that
spent their attempts (parked). Retry one parked job, or all of them.

**Webhooks.** Endpoints with their state and recent deliveries. Create one, edit it, turn it off
and on, send a test event, rotate its signing secret, delete it; retry or replay a delivery. See
[webhooks.md](webhooks.md).

**API keys.** The tenant's keys: prefix, created, last used, revoked. Create one (the full key is
shown once) or revoke one. The key signed in cannot revoke itself, so the console cannot lock you
out.

**Audit log.** The tenant's [audit events](audit-log.md), newest first, with a filter by action.

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

## Security model

- **No new credential and no server-side session.** The page is static. It calls the API with
  `Authorization: Bearer <key>`, so the API's own key checks apply. A user token (`ohs_ut_...`)
  is refused.
- **The key stays in the browser tab.** It is kept in `sessionStorage`, which the browser clears
  when the tab closes; "Sign out" clears it at once. It is never put in a cookie, a URL or
  `localStorage`, and never sent to any other origin.
- **One way to the server.** Every request goes through one wrapper that refuses a URL that is not
  a path on the same server, sends no cookie and follows no redirect. In `read-only` mode it
  refuses every method but `GET`.
- **Writes ask first.** Every action asks for a confirmation. Deleting a user or a webhook
  endpoint, revoking a key and forgetting a provider's keys ask you to type the name of what
  goes. A secret the API returns once (a new API key, a webhook signing secret) is shown once
  with a copy button and dropped from the page when the dialog closes.
- **Health values only on demand.** The console counts records; it shows values only when you
  press "Preview" or a download button on a user, and each read is in the audit log as
  `data.read` or `data.export`. Downloads are built in the page from the API answer, so the key
  never goes into a URL.
- **Same origin, strict headers.** The page, its scripts and its stylesheet are embedded in the
  binary; nothing loads from a CDN. Every console response carries
  `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; connect-src
  'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and
  `X-Frame-Options: DENY`; the HTML is sent with `Cache-Control: no-store`. The page has no inline
  script or style, and API data is inserted as text, never as markup.
- **Exposure.** The page itself holds nothing secret, and without a key it shows only the sign-in
  form. Still, if you prefer that operators reach it only from inside, block `/console` at your
  reverse proxy for the public address, or put it behind the proxy's own login, or leave
  `OHS_CONSOLE` off and call the same routes with `curl`.

## Look and feel

Light and dark follow the system setting. The console shares its type, frame and glass panels
with the [hosted connection page](connect-page.md): the sidebar, the sign-in, the setup page, the
overview's pipeline and the top of each provider sit in the same frame, drawn from gradients and
SVG noise with nothing loaded from elsewhere, and the figures that matter are large display
numerals. Below 860 px wide the sidebar folds into a bar with a menu button; the menu opens over
the page and closes on a link, a tap outside it or Escape.
