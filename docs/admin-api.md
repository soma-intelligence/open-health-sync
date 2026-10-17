# Admin API: tenants and their keys

One instance can serve several teams, studies or applications, each in its own tenant: users,
data, connections, webhooks, the audit log and API keys belong to one tenant and no other key
sees them. `ohs admin tenant create` makes a tenant from a shell on the server. The routes below
do the same over HTTP, so your own tooling (a script, an internal portal where a team signs up,
an infrastructure pipeline) can create a tenant per team, hand out and revoke its keys, watch its
pipeline and give it a demo user, without the database credentials.

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
  "rows_written": 14923
}
```

What `ohs seed --demo --tenant <id>` does, through the same code: the tenant's user with this
`external_ref` is reused, or created, then filled with `days` days of made-up data from six
sources, ending yesterday. Every record carries `{"synthetic": true}`; [demo.md](demo.md) says
what is written. Both members are optional: `external_ref` is 1 to 255 bytes (default
`demo-user`), `days` 1 to 90 (default 30). `user` is the object `POST /v1/users` answers.

The answer is 201 when the user was created, 200 when it existed. `rows_written` counts the rows
this call stored: running it again adds nothing for the days already there (0), and on a later
day adds the new days. 30 days take about a second, 90 days a few seconds. A user with that
`external_ref` being deleted is a 409.

The team reads the data with its own key, like any user's: `GET /v1/users/{id}/daily`,
`/sleep`, FHIR, exports. The admin key does not see it (404), since the user is not in the
admin's tenant.

## Retries

`POST /v1/admin/tenants` and `POST .../keys` do not honour `Idempotency-Key`, like `POST /v1/keys`
and token minting: replaying the answer would mean storing the secret. If a call fails without
an answer, list the tenants (or the keys), and revoke a key you never received. The demo user
route can be repeated as it is.

## Audit

Each request is recorded in the admin tenant's [audit log](audit-log.md), like any request:
`tenant.create`, `key.create`, `key.revoke` and `data.write` (the demo user) for the changes,
`config.read` for the reads, with the `tenant_id` of the path in `detail`.

The tenant that was changed gets an event of its own, so its log tells the whole story: actor
type `admin`, actor id `api_key:` followed by the admin key's id, the action above, and in
`detail` the admin's tenant (`admin_tenant_id`) with the key's `key_id` and `key_prefix` (never
the key), or for the demo user its `user_id`, `days`, `rows_written` and `user_created`. A
tenant or key change and its event are written together: if the event cannot be written,
nothing changes.

## Not here

- Renaming or deleting a tenant.
- Making a tenant an instance admin, or clearing the mark: `ohs admin tenant admin`.
- The [admin console](console.md) still shows the tenant of the key signed in.
- The server SDKs leave these routes out, as they do the provider settings; their types
  (`AdminTenant`, `CreatedAdminTenant`, `DemoUser`) are generated.
