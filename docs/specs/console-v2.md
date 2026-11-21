# Spec: console v2 (settings, sessions, React console)

Status: draft for review (2026-11-14). Supersedes the page design of
[admin-console.md](admin-console.md) and its "Editing settings other than provider keys" out of
scope line. Guide to update: [console.md](../console.md).

## Problem

The console at `/console` runs the pipeline, but it falls short for the developer who integrates
Open Health Sync:

- Every setting except provider keys is an environment variable. Changing a retention period, a
  rate limit or the webhook pause threshold means editing `.env`, redeploying and restarting
  every process. Nothing in the browser says what a setting is, what it is set to, or where the
  value comes from.
- The page holds the pasted tenant key in the browser for the whole visit. A script injected
  into the page could read it, and looking at a second tenant means pasting a second key.
- The page is hand-written JavaScript (about 3,400 lines in `app.mjs` and `lib.mjs`) with no
  types against the API. It is hard to extend, and its layout wastes the screen.
- It never shows the API behind what it displays, while the people who use it are writing code
  against that API.

## Goals

1. Every server setting is visible in the console with its effective value, its source and its
   default, and every setting that can safely change is editable there.
2. A changed setting applies without a redeploy: at once for a chosen set, at the next restart
   for the rest, and the console says which processes still run an old configuration.
3. The console signs in with a tenant key once and then holds a server session, never the key.
4. A new console in React, TypeScript and Vite, typed against `api/openapi.yaml`, with a layout
   built for a developer: navigation, content, and an inspector that shows the API call, the
   JSON and the related settings of whatever is selected.
5. `go build` and `go install` stay free of Node.

## Non-goals

- Login with a password or SSO, roles inside a tenant, named operators. A tenant key stays the
  credential; the session only stands in for it.
- A restart button. The console never kills its own process; the operator restarts with their
  own tooling (Docker, systemd, Kubernetes).
- Editing the bootstrap settings listed in 1.3 from the browser.
- Creating or deleting tenants from the browser. `ohs admin tenant` stays the way.
- Charts of health data.

## 1. Settings

### 1.1 The registry

`internal/config/settings.go` declares every server setting once. It is the single source of
truth for `config.Load`, the settings API, the console and `.env.example`. Each entry has:

| Field | Meaning |
|---|---|
| `Name` | The variable, for example `OHS_WEBHOOK_PAUSE_AFTER` |
| `Label`, `Section`, `Help` | What the console shows. Sections: Server, Storage, Security, Console, Metrics, Retention, Consent, Ingest, Sync, Processing, Webhooks, Event stream, Rate limits, Vault |
| `Type` | `bool`, `int` (with bounds), `duration`, `enum` (with values), `url`, `list`, `rate` (`rate/burst`), `string` |
| `Default` | The built-in default, as the string `config.Load` uses today |
| `Secret` | Write only: the API says whether it is set, never its value |
| `Apply` | `live`, `restart` or `bootstrap` (1.3) |
| `Validate` | The same check `config.Load` runs today, reused by the API before anything is stored |

Out of the registry, and out of this spec:

- The per-provider variables `OHS_<PROVIDER>_*`. They stay in `ohs_core.provider_settings` with
  the rules of admin-console.md section 3.
- Variables the server never reads: those of the CLI and the MCP server (`OHS_URL`, `OHS_TOKEN`,
  `OHS_MCP_URL`, `OHS_MCP_KEY`), of Docker Compose (`OHS_VERSION`, `OHS_HTTP_BIND`,
  `OHS_HTTP_PORT`) and the standard `OTEL_*` variables.

`.env.example` stays hand-written prose, but a unit test checks that it names every registry
entry and nothing else of the server, and that each default in its comments matches `Default`.
A second test parses `internal/config` and fails when `config.Load` reads an `OHS_*` name that
has no registry entry.

### 1.2 Precedence

For each setting, the value a process runs with is the first of:

1. its environment variable (or its `_FILE` twin), when set and not empty;
2. the value stored from the console;
3. `Default`.

A setting set by the environment is shown as locked in the console, with its value (or `set`
for a secret). The console never mixes the two sources for one setting.

### 1.3 Apply modes

**`bootstrap`**: needed before the database can be read, or dangerous to change from a page
served by the process it changes. Never stored, never editable; the console shows the value
(secrets as `set`) and a note naming the variable:

`OHS_DATABASE_URL`, `OHS_ENCRYPTION_KEY`, `OHS_ENCRYPTION_KEY_FILE`,
`OHS_PREVIOUS_ENCRYPTION_KEYS`, every `OHS_VAULT_*` variable, `OHS_RAW_STORE`, `OHS_LISTEN`,
`OHS_OPS_LISTEN`, `OHS_METRICS_LISTEN`, `OHS_ROLES`, `OHS_CONSOLE`, `OHS_SETUP_TOKEN`,
`OHS_SETUP_TOKEN_FILE`, `OHS_BOOTSTRAP_TENANT`.

**`live`**: applied by every process within 30 seconds of the change, without a restart:

| Setting | Consumer today, to read a snapshot instead |
|---|---|
| `OHS_LOG_LEVEL` | the `slog` handler level, moved to a `slog.LevelVar` |
| `OHS_RETENTION_DAYS`, `OHS_AUDIT_RETENTION_DAYS`, `OHS_TRACKING_RETENTION_DAYS` | the daily cleanup in the `etl` role |
| `OHS_CONSENT_REQUIRED` | `etl.WithConsentRequired` |
| `OHS_BACKFILL_DAYS` | the first pull of a new connection |
| `OHS_WEBHOOK_PAUSE_AFTER`, `OHS_WEBHOOK_ROTATION_GRACE_HOURS` | the webhook dispatcher |
| `OHS_RATE_LIMIT_API`, `OHS_RATE_LIMIT_AUTH_FAILURES`, `OHS_RATE_LIMIT_INBOUND`, `OHS_RATE_LIMIT_INBOUND_PROVIDER`, `OHS_RATE_LIMIT_OAUTH` | the API limiters, rebuilt when their rate changes (counters restart from full) |

**`restart`**: every other registry entry. Stored at once, applied when each process next
starts.

Moving a setting from `restart` to `live` later is a code change in its consumer plus one field
in the registry; nothing else changes.

### 1.4 Storage

Migration 0056 creates:

```sql
CREATE TABLE ohs_core.instance_settings (
  name        text PRIMARY KEY,           -- OHS_* name, checked against the registry in Go
  value       text,                       -- clear value; NULL when sealed
  sealed      bytea,                      -- secret value sealed with OHS_ENCRYPTION_KEY, bound to name
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid NOT NULL,              -- tenant id of the admin that saved it
  CHECK ((value IS NULL) <> (sealed IS NULL))
);
CREATE TABLE ohs_core.instance_settings_revision (
  id        boolean PRIMARY KEY DEFAULT true CHECK (id),
  revision  bigint NOT NULL DEFAULT 0
);
```

Every write to `instance_settings` happens in one transaction that also increments `revision`.
`ohs admin rekey` re-seals `sealed` like it re-seals `provider_settings`. A row whose name is no
longer in the registry (a setting removed in a later release) is ignored and listed by
`ohs doctor`.

### 1.5 Start

`serve` reads the bootstrap settings from the environment, opens the database, loads
`instance_settings` and calls `config.Load` with a stacked lookup (environment, then stored,
then nothing so `Load` applies its default). `config.Load` keeps its signature.

A stored value that fails validation does not stop the process: it is skipped (the environment
value or the default applies), logged at `warn` with the setting name, and reported in the
heartbeat (1.7) so the console shows it.

A process that cannot read `instance_settings` at start (a database older than migration 0056
is already refused by the schema check) fails to start, as it does today when the database is
unreachable.

### 1.6 Live reload

`internal/settings.Live` follows the pattern of `internal/providerset`: it reads the revision
every 30 seconds, reloads the rows when it changed, validates them, and swaps an atomic snapshot
of the `live` settings. A failed reload is logged and the last good snapshot stays. The process
that saved a change reloads at once. Consumers of a `live` setting take a getter
(`func() int`, `func() api.RateLimit`) instead of a value.

### 1.7 Process heartbeats

Migration 0057 creates `ohs_core.process_heartbeats`: process id (random at start), host name,
roles, binary version, start time, the settings revision loaded at start, the revision of the
live snapshot, the names of stored settings it skipped as invalid, the last reload error, and
`seen_at`. Each process upserts its row every 30 seconds. Rows not seen for 5 minutes are
deleted by the daily cleanup and hidden by the API after 2 minutes.

A process is **behind** when a `restart` setting changed after the revision it loaded at start.
It is **stale** when its live revision is older than the current revision for more than
60 seconds.

### 1.8 Routes

All under `/v1/admin`, so they need a key (or session) of an instance admin tenant, and all
written to the audit log. Values of secret settings never appear in an answer, a log line or an
audit event.

| Route | Purpose |
|---|---|
| `GET /v1/admin/settings` | Every registry entry: name, label, section, help, type and its constraints, default, secret, apply mode, source (`env`, `console`, `default`), effective value (or `set` for a secret), stored value when the environment overrides it, `invalid` with the message when the stored value was skipped, and the current `revision`. Answer carries `ETag: "<revision>"` |
| `PATCH /v1/admin/settings` | Body `{"OHS_X": "value", "OHS_Y": null}`; `null` removes the stored value. Needs `If-Match: "<revision>"`. All or nothing. Answers 200 with the new revision and the names that need a restart; 412 when the revision moved; 409 naming each setting that is `bootstrap` or set by the environment; 422 with one message per invalid setting |
| `GET /v1/admin/settings/export` | The effective configuration as a `.env` file, every secret replaced by `# set` or left empty |
| `GET /v1/admin/processes` | The live heartbeats with `behind` and `stale` |

The audit event of a `PATCH` lists the changed names in `detail`, never their values.

## 2. Console sessions

### 2.1 Sign in and out

- `POST /console/session` with `{"key": "ohs_sk_..."}`. The server checks the key like any API
  call, then inserts a row in `ohs_core.console_sessions` (migration 0058): SHA-256 of a random
  32-byte token, tenant id, API key id, acting tenant id, read only flag, CSRF token hash,
  created, last seen. It sets the cookie `ohs_console=<token>`: `HttpOnly`, `SameSite=Strict`,
  `Path=/`, `Secure` unless the public URL is `http://localhost` or `127.0.0.1`. The answer
  body carries the CSRF token and the tenant summary. The key is not stored and never returned.
- `DELETE /console/session` deletes the row and clears the cookie.
- `GET /console/session` returns the tenant summary and a fresh CSRF token for a page reload.
- Sign in failures count against `OHS_RATE_LIMIT_AUTH_FAILURES` like any bad key.

### 2.2 Lifetime

A session ends after 12 hours without a request, 7 days after its creation, when its API key is
revoked, or when its tenant is deleted. `last_seen` is written at most once a minute. Ended
rows are deleted by the daily cleanup.

### 2.3 Using a session

The API auth middleware accepts either `Authorization: Bearer <key>` (unchanged) or the
`ohs_console` cookie. With the cookie:

- the request must not carry an `Origin` header naming another origin;
- every method but `GET`, `HEAD` and `OPTIONS` needs `X-OHS-CSRF` equal to the session's CSRF
  token, else 403;
- a read only session gets 403 on every method but `GET` and `HEAD`;
- the request runs as the acting tenant, and its audit event records both the session's tenant
  (`actor_tenant_id`) and the acting tenant.

`OHS_CONSOLE=read-only` makes every new session read only. This is now enforced by the server
for sessions; an API key used with `curl` keeps full rights, as before.

### 2.4 Tenant switch

A session of an instance admin tenant can call `PUT /console/session/tenant` with a tenant id
(any tenant listed by `GET /v1/admin/tenants`). The acting tenant changes; the session tenant
does not. A session of any other tenant gets 403. Health values of the acting tenant stay
behind the explicit Preview and download actions, recorded as `data.read` and `data.export`.

## 3. The console

### 3.1 Stack and build

- Source in `web/console/`: React 19, TypeScript in strict mode, Vite, React Router, TanStack
  Query. No component library. API types generated from `api/openapi.yaml` with
  `openapi-typescript` into `web/console/src/api/schema.ts` (generated, committed).
- `vite build` writes to `internal/console/dist/`, committed and embedded with `go:embed`.
  File names carry a content hash; `index.html` is served with `Cache-Control: no-cache`, the
  hashed assets with a one year immutable cache.
- Inter and JetBrains Mono are bundled. Nothing loads from another origin.
- The content security policy is unchanged: no inline script or style, `form-action 'none'`, no
  framing. Vite is configured to emit no inline script.
- Routes are paths under `/console/*`. The Go handler serves the asset when one matches, else
  `index.html`.
- `make console` builds the page; `make console-dev` runs Vite with a proxy to a local
  `ohs serve`.

### 3.2 Look

The Codex-in-green tokens of the project site, light and dark, switched by a Light, Dark, Auto
control:

| Token | Light | Dark |
|---|---|---|
| page | `#f6faf7` (mint canvas) | `#070a09` |
| panel | `#ffffff` | `#0e1311`, raised `#121916` |
| ink | `#0b1510` | `#eef4f0` |
| muted | `#4b5a53` | `#8fa39a` |
| line | `rgb(11 21 16 / 8%)` | `rgb(238 244 240 / 8%)` |
| accent | `#17a865` | `#3ddc8e` |
| on accent | `#04140c` | `#04140c` |

Code, keys, ids and setting names use the mono font. The overview opens on a black
"Pipeline, live" band in both themes.

### 3.3 Layout

Three panes:

- **Navigation** (left): the tenant name, the tenant switcher for admin sessions, the pages, the
  theme control, sign out.
- **Content** (centre): a list, or the page of one item.
- **Inspector** (right, can be closed, remembered per browser): for the item or list selected,
  the API call that produced it in cURL, TypeScript and Python (with the key shown as
  `$OHS_API_KEY`), the JSON answer, and the `OHS_*` settings that shape it, each linking to its
  field in Settings.

Under 1024 px the navigation folds into a menu; under 768 px the inspector opens as a bottom
sheet.

A command palette (⌘K, Ctrl+K) reaches every page, every setting by name or label, and the
main actions. Every setting has its own address, for example
`/console/settings/webhooks/OHS_WEBHOOK_PAUSE_AFTER`.

### 3.4 Pages

| Page | Content | Actions |
|---|---|---|
| Overview | A getting started checklist (provider on, user created, first connection, first data, webhook endpoint); the pipeline in five stages with counts and state; needs attention | Each item links to the page that fixes it |
| Users | Users, 50 per page, search by external id | Create a user |
| User | Tabs: Data, Connections, Consents, Sync runs, Shares | Connection link, sync now, disconnect, preview recent data, downloads (FHIR, Open mHealth, CSV, archive), delete |
| Providers | Every connector with status, key source, URLs to register | On and off, save keys (admin), per-tenant on and off |
| Jobs | Sync runs (failed first), parked processing jobs | Retry one, retry all |
| Webhooks | Endpoints and deliveries | Create, edit, pause, test, rotate secret, delete, retry, replay |
| API keys | Keys with scopes, created, last used | Create with a scope picker, revoke |
| Audit | Events, newest first, filters by action and actor | None |
| Settings | Sections with a sub-navigation that follows the scroll; each field shows label, `OHS_*` name, source, apply mode, default; locked fields show why; a banner lists processes that are behind or stale | Edit fields; a bar counts pending changes; Review opens a diff (old, new, apply mode) before Apply; revert one field to default; Copy as .env; Export |
| Setup | The first-run page of `OHS_SETUP_TOKEN`, moved into the same app at `/console/setup` | Create the first tenant |

The actions are those of admin-console.md section 1, and its safety rules (section 6) stay:
confirm every write, type the name for destructive ones, show one-time secrets once.

### 3.5 Errors in the page

- A failed call shows the API status and message; the inspector shows the raw answer.
- 401 from a session sends the page to sign in and back to the same address after.
- 412 on Apply reloads the settings and reopens the diff against the new values.
- 422 on Apply marks each invalid field with its message; nothing was saved.
- Each page sits in a React error boundary: an exception shows an error panel for that page
  only.

## 4. Testing

- **Go unit**: registry against `config.Load` (source scan) and against `.env.example`;
  precedence; each type's validation; secret values absent from answers and audit details.
- **Go integration** (Postgres): atomic `PATCH`, 412 on a moved revision, 409 on bootstrap and
  environment settings, sealing and `rekey`, live reload seen by a second process within one
  interval, `behind` and `stale`, an invalid stored value skipped at start; session create,
  idle and absolute expiry, key revocation, CSRF, foreign `Origin`, read only, tenant switch
  and its audit fields.
- **Front unit** (Vitest, Testing Library): settings diff, field state from source and apply
  mode, request snippets in the three languages, palette search.
- **End to end** (Playwright, headless, in CI): a real `ohs serve` on a seeded database; sign in,
  open every page, change a `live` and a `restart` setting through Review and Apply, see the
  behind banner, read only session refused on a write, sign out.
- **Contract**: `tsc` over the generated schema; the new routes are in `api/openapi.yaml` and
  covered by the existing OpenAPI route test.

## 5. CI

A `console` job in `ci.yml`: Node 22, `npm ci`, ESLint, `tsc --noEmit`, Vitest, `vite build`,
then `git diff --exit-code internal/console/dist web/console/src/api/schema.ts`, then
Playwright. `.gitattributes` marks `internal/console/dist/**` and the generated schema
`linguist-generated`. The Go jobs need no Node.

## 6. Delivery

Three plans, in order, each shippable alone:

1. **Settings**: registry, migrations 0056 and 0057, stacked load, `Live`, heartbeats, the four
   routes, OpenAPI, docs. The current console keeps working.
2. **Sessions**: migration 0058, the session routes, cookie auth in the middleware, read only
   enforcement, tenant switch, audit fields. The current console switches to sessions.
3. **React console**: `web/console`, the build and CI job, every page of 3.4, the setup page;
   then the old `app.mjs`, `lib.mjs`, `console.css`, `setup.mjs` and their tests are deleted
   once each page of admin-console.md section 1 is checked in the new console.

Docs updated with each plan: `docs/console.md` (settings, sessions, the new page),
`docs/self-hosting.md` (precedence and apply modes), `.env.example` (a line
saying settings can also be saved from the console), and admin-console.md (a pointer to this
spec).

`OHS_CONSOLE` keeps its three values. No transition flag: the release after plan 3 ships the new
console.
