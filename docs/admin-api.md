# Admin API: tenants and their keys

One instance can serve several teams, studies or applications, each in its own tenant: users,
data, connections, webhooks, the audit log and API keys belong to one tenant and no other key
sees them. `ohs admin tenant create` makes a tenant from a shell on the server. The routes below
do the same over HTTP, so your own tooling (a script, an internal portal where a team signs up,
an infrastructure pipeline) can create a tenant per team, hand out and revoke its keys, watch its
pipeline, choose its providers, give it a demo user and read its data for support, without the
database credentials.

> **Next release.** These routes are on `main` and ship in the next release.

## The admin key

Every route here takes a key of an [instance admin](console.md#the-instance-admin) tenant, the
same keys that manage the provider settings. Any other tenant key gets 403, and so does a user
token.

An admin key can create a key for any tenant, so whoever holds it can reach every tenant's data.
Treat it like the database password: keep it on a server, never in a browser or a mobile app,
and give your tooling a tenant of its own:

```sh
ohs admin tenant create --name tooling --admin   # prints the tooling tenant's first key, once
```

Rotate that key with the routes below (create a new key for the tooling tenant, deploy it,
revoke the old one). A key cannot revoke itself, so the tooling cannot lock itself out.

## Routes

| Route | Answer |
|---|---|
| `GET /v1/admin/tenants` | `{"data": [tenant, ...], "next_cursor": ...}`, oldest first, paged with `limit` (1 to 1000, default 100) and `cursor` like `GET /v1/users` |
| `POST /v1/admin/tenants` | 201, a new tenant and its first key: body `{"name": "..."}` |
| `GET /v1/admin/tenants/{tenant_id}` | One tenant |
| `GET /v1/admin/tenants/{tenant_id}/pipeline` | The tenant's pipeline, the answer its own key gets from `GET /v1/pipeline` |
| `GET /v1/admin/tenants/{tenant_id}/keys` | `{"data": [key, ...]}`, the tenant's keys, revoked ones included, oldest first |
| `POST /v1/admin/tenants/{tenant_id}/keys` | 201, a new key for the tenant |
| `DELETE /v1/admin/tenants/{tenant_id}/keys/{key_id}` | 204, the key is revoked |
| `POST /v1/admin/tenants/{tenant_id}/demo-user` | 201 or 200, a user of the tenant filled with [synthetic data](demo.md) |
| `GET /v1/admin/tenants/{tenant_id}/providers` | `{"data": [provider, ...]}`, every provider of the instance with the tenant's switch |
| `PATCH /v1/admin/tenants/{tenant_id}/providers/{provider}` | The provider, turned off or back on for the tenant: body `{"enabled": false}` |
| `GET /v1/admin/tenants/{tenant_id}/users` and `.../users/{id}` | The tenant's users, one user: the answers of `GET /v1/users` and `GET /v1/users/{id}` |
| `GET /v1/admin/tenants/{tenant_id}/users/{id}/{data}` | One user's data, `{data}` one of `daily`, `samples`, `samples/stats`, `sleep`, `workouts`, `nutrition`, `coverage`, `connections`, `freshness`: the answer of `GET /v1/users/{id}/{data}` |

A tenant is:

```json
{
  "id": "3f6c1a8e-2b4d-4e7f-9a1c-5d8e0b2f4a61",
  "name": "Team Blue",
  "admin": false,
  "created_at": "2026-10-17T09:12:44.120511Z",
  "active_keys": 1
}
```

`active_keys` counts the keys that are not revoked. A key is the object of `GET /v1/keys`: `id`,
`prefix` (the first 12 characters, kept to tell keys apart), `created_at`, `last_used_at`,
`revoked_at` and `current`, true only for the key making the call (so only when you list your
own tenant's keys). A key just created also has `key`, the whole secret, in that answer only.

An unknown `tenant_id` or `key_id`, a malformed one, or a key of another tenant is a 404. Errors
are [problem details](api.md#conventions):

```json
{ "type": "about:blank", "title": "Not Found", "status": 404, "detail": "tenant not found", "request_id": "..." }
```

## Create a tenant

```sh
curl -s -X POST "$OHS/v1/admin/tenants" -H "Authorization: Bearer $ADMIN_KEY" \
  -H 'Content-Type: application/json' -d '{"name":"Team Blue"}'
```

```json
{
  "id": "3f6c1a8e-2b4d-4e7f-9a1c-5d8e0b2f4a61",
  "name": "Team Blue",
  "admin": false,
  "created_at": "2026-10-17T09:12:44.120511Z",
  "active_keys": 1,
  "key": {
    "id": "8d2e4f60-1a3b-4c5d-8e9f-0a1b2c3d4e5f",
    "prefix": "ohs_sk_2dybB",
    "created_at": "2026-10-17T09:12:44.120511Z",
    "last_used_at": null,
    "revoked_at": null,
    "current": false,
    "key": "ohs_sk_..."
  }
}
```

The name is trimmed, then must be 1 to 100 characters with no control character (400
otherwise). The body takes nothing else: a tenant made here is never an instance admin. Making
one stays `ohs admin tenant create --admin` or `ohs admin tenant admin`, run on the server.

The answer carries `Location: /v1/admin/tenants/{id}` and `Cache-Control: no-store`. The key is
shown this once and never stored in clear: hand it to the team, or store it in your own secret
store, before you do anything else.

## Keys

```sh
# A second key, to rotate: deploy it, then revoke the old one.
curl -s -X POST "$OHS/v1/admin/tenants/$TENANT/keys" -H "Authorization: Bearer $ADMIN_KEY"
curl -s "$OHS/v1/admin/tenants/$TENANT/keys" -H "Authorization: Bearer $ADMIN_KEY"
curl -s -X DELETE "$OHS/v1/admin/tenants/$TENANT/keys/$KEY_ID" -H "Authorization: Bearer $ADMIN_KEY"
```

`POST .../keys` takes no body (or `{}`) and answers the new key with its secret, 201,
`Cache-Control: no-store`. `DELETE` answers 204; revoking a revoked key succeeds again. The key
making the call cannot be revoked (409): revoke it with another key of its tenant. Nothing stops
you from revoking a tenant's last key: `POST .../keys` makes a new one.

## Pipeline

`GET /v1/admin/tenants/{tenant_id}/pipeline` answers what the tenant's own key gets from
`GET /v1/pipeline`: users, sources, sync runs, processing jobs, raw data stored in the last 24
hours, webhook endpoints and deliveries. Counts only, never a health value. See
[api.md](api.md#running-the-instance).

## Demo user

```sh
curl -s -X POST "$OHS/v1/admin/tenants/$TENANT/demo-user" -H "Authorization: Bearer $ADMIN_KEY" \
  -H 'Content-Type: application/json' -d '{"external_ref":"demo-user","days":30}'
```

```json
{
  "user": { "id": "2b9e...", "external_ref": "demo-user", "status": "active", "created_at": "..." },
  "days": 30,
  "rows_written": 14923,
  "persona": "mix"
}
```

What `ohs seed --demo --tenant <id>` does, through the same code: the tenant's user with this
`external_ref` is reused, or created, then filled with `days` days of made-up data, ending
yesterday. Every record carries `{"synthetic": true}`; [demo.md](demo.md) says what is written.
Every member is optional:

- `persona` is the kind of person: `mix` (the default, one person seen through six sources),
  `runner`, `sleep` or `weight`, each with its own sources and figures
  ([demo.md](demo.md#personas)). Any other value is a 400 that lists them.
- `external_ref` is 1 to 255 bytes. By default `demo-user` for `mix` and `demo-<persona>` for
  the others (`demo-runner`), so each persona fills a user of its own.
- `days` is 1 to 90 (default 30).

`user` is the object `POST /v1/users` answers, `persona` the persona written.

The answer is 201 when the user was created, 200 when it existed. `rows_written` counts the rows
this call stored: running it again adds nothing for the days already there (0), and on a later
day adds the new days. 30 days take about a second, 90 days a few seconds. A user with that
`external_ref` being deleted is a 409.

The team reads the data with its own key, like any user's: `GET /v1/users/{id}/daily`,
`/sleep`, FHIR, exports. The admin key does not see it on those routes (404), since the user is
not in the admin's tenant; it reads it through [the admin read routes](#read-a-tenants-data).

## Providers per tenant

The instance runs a provider for every tenant: its keys are the instance's, set in the
[provider settings](console.md). An admin can narrow that, tenant by tenant: a team that has
not signed the provider's terms, a study that collects from one wearable only.

```sh
curl -s "$OHS/v1/admin/tenants/$TENANT/providers" -H "Authorization: Bearer $ADMIN_KEY"
curl -s -X PATCH "$OHS/v1/admin/tenants/$TENANT/providers/oura" -H "Authorization: Bearer $ADMIN_KEY" \
  -H 'Content-Type: application/json' -d '{"enabled":false}'
```

```json
{
  "slug": "oura",
  "name": "Oura",
  "kinds": ["oauth"],
  "instance_status": "active",
  "enabled": false,
  "available": false,
  "updated_at": "2026-10-17T09:30:02.481190Z"
}
```

The list holds every provider the instance knows, in the order of `GET /v1/admin/providers`.
`instance_status` is the provider's status there (`active`, `off`, `not_configured`, `error`,
`built_in`). `enabled` is the tenant's switch: true unless an admin turned it off, the default
for every tenant and every provider, new ones included. `available` is true when the tenant's
users can use the provider now: the instance runs it (`active` or `built_in`) and the switch is
on. `updated_at` is the last change of the switch, null when it was never set.

`PATCH` takes `{"enabled": true}` or `{"enabled": false}`, nothing else (400 otherwise), and
answers the provider as listed. It can be set for a provider the instance does not run yet, so
the tenant does not get it when it is turned on. An unknown tenant or provider is a 404. The
change applies at once on the instance that answered and within 30 seconds on the others.

While a provider is off for a tenant:

- `GET /v1/providers` leaves it out, for the tenant's keys and user tokens, and the
  [hosted connect page](connect-page.md) does not offer it.
- Starting a connection, a connect session that names it, the connect page's button and the
  OAuth callback of a consent already started answer 403, `detail` saying the provider is turned
  off for this tenant. A callback with a `redirect_uri` goes back with `status=error`. The code is
  never exchanged.
- Sync now answers 409, and the scheduler plans no pull. A pull already queued ends `cancelled`
  without reaching the provider; it is not a failure of the connection and is not retried.
  Provider notifications are still answered, and dropped for the tenant.
- Uploads (`/v1/ingest/...`) and self reports answer 403, before anything is stored.

Nothing is deleted: the connections stay, with their tokens, and the data stored stays readable.
Turned back on, the next scheduled pull reaches back to the connection's last successful sync,
up to 7 days, like after an outage.

## Read a tenant's data

Support, or a portal that shows a team what it collected, reads a tenant's users and their data
with the admin key, without asking the team for its own:

```sh
curl -s "$OHS/v1/admin/tenants/$TENANT/users" -H "Authorization: Bearer $ADMIN_KEY"
curl -s "$OHS/v1/admin/tenants/$TENANT/users/$USER_ID/daily?start=2026-10-01" -H "Authorization: Bearer $ADMIN_KEY"
curl -s "$OHS/v1/admin/tenants/$TENANT/users/$USER_ID/coverage" -H "Authorization: Bearer $ADMIN_KEY"
```

Each route is the tenant route after `/v1/admin/tenants/{tenant_id}`, served by the same code:
the same query parameters (`start`, `end`, `view`, `type`, `cursor`, `limit` and the others),
the same answer, the same errors, as the tenant's own key would get. They read only: there is
no admin route that writes a user's data. An unknown tenant is a 404 (`tenant not found`), a user
of another tenant a 404 (`user not found`), the admin's own users included.

| Route | Same as |
|---|---|
| `GET .../users`, `GET .../users/{id}` | [Users](api.md#users) |
| `GET .../users/{id}/daily`, `/samples`, `/samples/stats`, `/sleep`, `/workouts`, `/nutrition`, `/coverage` | [Reading data](api.md#reading-data) |
| `GET .../users/{id}/connections`, `/freshness` | [Connection health](sync-health.md#connection-health) |

Each read is recorded twice, like a change (see [Audit](#audit)). On the admin's tenant, like
any request: the action of the tenant route (`user.read`, `data.read`, `connection.read`), the
`tenant_id` in `detail`, never the user, who is not one of the admin's. On the tenant read:
actor type `admin`, actor id `api_key:` and the admin key's id, the same action, the user of the
path in `user_id` and the admin's tenant in `detail` (`admin_tenant_id`), so the team sees in its
own log who read which user's data, and when. A read of the admin's own tenant through these
routes is recorded once, on that tenant, with the user: a second event there would count every
read twice in one log. A read the tenant route refuses (400, 404 for a user) is recorded with its
status; one for an unknown tenant is recorded on the admin's tenant only.

## Retries

`POST /v1/admin/tenants` and `POST .../keys` do not honour `Idempotency-Key`, like `POST /v1/keys`
and token minting: replaying the answer would mean storing the secret. If a call fails without
an answer, list the tenants (or the keys), and revoke a key you never received. The demo user
route can be repeated as it is.

## Audit

Each request is recorded in the admin tenant's [audit log](audit-log.md), like any request:
`tenant.create`, `key.create`, `key.revoke`, `data.write` (the demo user) and `tenant.provider`
for the changes, `config.read` for the reads of tenants, keys, pipelines and providers, the
action of the tenant route for the [reads of a tenant's data](#read-a-tenants-data), with the
`tenant_id` of the path in `detail`.

The tenant that was changed gets an event of its own, so its log tells the whole story: actor
type `admin`, actor id `api_key:` followed by the admin key's id, the action above, and in
`detail` the admin's tenant (`admin_tenant_id`) with the key's `key_id` and `key_prefix` (never
the key), for the demo user its `user_id`, `days`, `persona`, `rows_written` and
`user_created`, for a provider switch its `provider` and `enabled`. A tenant, key or switch
change and its event are written together: if the event cannot be written, nothing changes. A
read of the tenant's data gets an event too, with the user read.

## Not here

- Renaming or deleting a tenant.
- Making a tenant an instance admin, or clearing the mark: `ohs admin tenant admin`.
- The [admin console](console.md) still shows the tenant of the key signed in.
- The server SDKs leave these routes out, as they do the provider settings; their types
  (`AdminTenant`, `CreatedAdminTenant`, `DemoUser`, `AdminTenantProvider`) are generated. The
  read routes answer the types of the tenant routes.
- Reading through the admin key the routes not listed above (FHIR, Open mHealth, exports, the
  archive, sync runs, consents): use a key of the tenant, made with `POST .../keys`.
