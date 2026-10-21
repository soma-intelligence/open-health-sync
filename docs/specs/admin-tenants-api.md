# Spec: tenants and their keys from the admin API

Status: draft for review (2026-10-17). Guide: [admin-api.md](../admin-api.md). Builds on
[admin-console.md](admin-console.md) (the instance admin tenant and `/v1/admin/providers`).

## Problem

One instance can serve several teams, studies or applications, each in its own tenant: its
users, data, webhooks, audit log and API keys are its own. Today a tenant is created in two
ways only, both from the server itself:

- `ohs admin tenant create`, which needs a shell and the database URL;
- the one-shot [first-run setup](../setup.md), which closes once the first tenant exists.

An operator who runs an instance for several teams wants to do it from their own tooling: a
script, an internal portal where a team signs up and gets its key, an infrastructure pipeline.
Each of them needs the database credentials today, or a person at a terminal. The keys of an
existing tenant are managed over HTTP already (`/v1/keys`), but only by that tenant's own key,
so a lost last key also takes a shell.

## Scope

Routes under `/v1/admin/tenants`, beside `/v1/admin/providers`: they need a key of an
[instance admin](../console.md#the-instance-admin) tenant (`auth.TenantAdmin`). Any other tenant
key gets 403 and a user token 403, exactly as the provider settings answer.

| Route | Answer |
|---|---|
| `GET /v1/admin/tenants` | Every tenant, oldest first, paged with `limit` (1 to 1000, default 100) and `cursor` like `GET /v1/users` |
| `POST /v1/admin/tenants` | Create a tenant and its first API key; 201 with the tenant and the key, its secret this once |
| `GET /v1/admin/tenants/{tenant_id}` | One tenant |
| `GET /v1/admin/tenants/{tenant_id}/pipeline` | That tenant's pipeline, the same answer as its own `GET /v1/pipeline` |
| `GET /v1/admin/tenants/{tenant_id}/keys` | That tenant's keys, revoked ones included, oldest first |
| `POST /v1/admin/tenants/{tenant_id}/keys` | A new key for that tenant; 201 with its secret this once |
| `DELETE /v1/admin/tenants/{tenant_id}/keys/{key_id}` | Revoke one of its keys; 204 |
| `POST /v1/admin/tenants/{tenant_id}/demo-user` | Fill one user of that tenant with synthetic data, as `ohs seed --demo --tenant` does |

### Shapes

A tenant (`AdminTenant`):

```json
{ "id": "uuid", "name": "Team Blue", "admin": false, "created_at": "RFC 3339", "active_keys": 1 }
```

`active_keys` counts the keys not revoked. A list is `{"data": [...], "next_cursor": null}`.

`POST /v1/admin/tenants` takes `{"name": "Team Blue"}`. The name is trimmed and must then be 1 to
100 characters with no control character (400 otherwise). Any other member is refused (400), so
the route never creates an admin tenant: making one stays `ohs admin tenant create --admin` and
`ohs admin tenant admin`, run by the operator on the server. The answer is the tenant with its
first key under `key`, the same object `POST /v1/keys` answers (`id`, `prefix`, `created_at`,
`last_used_at`, `revoked_at`, `current` false, and the whole `key`), with
`Cache-Control: no-store` and `Location: /v1/admin/tenants/{id}`.

The key routes answer the objects of `/v1/keys`: a list `{"data": [APIKey]}`, a new key
`CreatedAPIKey` (201, `Cache-Control: no-store`), a revocation 204. `current` is true only for
the key making the call, so only in a listing of the caller's own tenant. Revoking a revoked
key succeeds again. The key making the call cannot be revoked here either (409), so the
operator's tooling cannot lock itself out. A `key_id` that is not a key of `{tenant_id}` is a 404.

`POST /v1/admin/tenants/{tenant_id}/demo-user` takes `{"external_ref": "demo-user", "days": 30}`,
both optional: `external_ref` is 1 to 255 bytes (as `POST /v1/users`, default `demo-user`),
`days` 1 to 90 (default 30). It reuses the tenant's user with that `external_ref`, or creates
it, then writes `days` days of synthetic data ending yesterday, through the code path of
`ohs seed --demo` (one shared function). It answers 201 when it created the user, 200 when the
user existed:

```json
{ "user": { "id": "uuid", "external_ref": "demo-user", "status": "active", "created_at": "..." },
  "days": 30, "rows_written": 12345 }
```

`user` is the object `POST /v1/users` answers. `rows_written` counts the rows stored by this
call; running it again adds nothing for the days already there (0), and on a later day adds the
new days only. A user with that `external_ref` being deleted is a 409. The data is synthetic,
marked `{"synthetic": true}` like every demo record ([demo.md](../demo.md)).

Every route answers 404 for a `tenant_id` that is not a tenant (a malformed one included), with
the usual problem body.

### Idempotency

`POST /v1/admin/tenants` and `POST .../keys` do not honour `Idempotency-Key`, like `POST /v1/keys`
and token minting: a replay would mean storing the plaintext secret to send it again. A caller
that lost the answer lists the tenants or the keys and revokes what it cannot use. The demo user
route is safe to repeat as it is.

### Audit

Every request is recorded on the caller's (admin) tenant, as any route. The action follows the
route: `tenant.create` (`POST /v1/admin/tenants`), `key.create` and `key.revoke` (the key
routes), `data.write` (the demo user), `config.read` for the reads. The event's `detail` holds
the `tenant_id` of the path, and `key_id` for a revocation.

The target tenant gets one event of its own for each change made to it, written by the code that
makes the change, so its log tells the whole story without the admin's:

| Change | Action | `detail` |
|---|---|---|
| Tenant created | `tenant.create` | `admin_tenant_id`, `key_id`, `key_prefix` of the first key |
| Key created | `key.create` | `admin_tenant_id`, `key_id`, `key_prefix` |
| Key revoked | `key.revoke` | `admin_tenant_id`, `key_id`, `key_prefix` |
| Demo user | `data.write`, with the user's `user_id` | `admin_tenant_id`, `synthetic` true, `days`, `rows_written`, `user_created` |

Actor type `admin` (as `ohs admin`), actor id `api_key:` and the admin key's id; `method`,
`route`, `status` and `request_id` of the request. A key appears by its prefix (the first 12
characters, then `...`), never in full. The tenant and key events are written in the same
transaction as the change: if the event cannot be written, nothing changes and the answer is a
500.

## Out of scope

- Renaming or deleting a tenant. Tenants are never deleted today (the first-run setup relies on
  it); deleting one means erasing every user, and is its own change.
- Making a tenant an instance admin, or clearing the mark, over HTTP. It stays a command run on
  the server.
- The admin console. It still shows the tenant of the key signed in.
- The server SDKs: like `/v1/admin/providers`, these routes are for the operator's tooling, not
  a tenant's backend, and are left out of the generated clients.
- Per-tenant quotas or limits.

## Decisions to confirm

- The list is paged like `GET /v1/users` (same cursor, oldest first). An instance has few
  tenants, but tooling that creates one per project can reach thousands.
- 90 days is the most the demo route writes in one call (`ohs seed --demo` keeps 365): one
  request writes about 44,000 rows for 90 days in about 3 seconds (about 15,000 rows in a
  little over a second for the default 30), and a longer request would hold an HTTP connection
  open for too long.
- The demo user is audited as `data.write` on both sides, since it writes data; the creation of
  the user, when it happens, is `user_created` in the event's detail rather than a second event.
