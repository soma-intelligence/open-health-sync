# Audit log

Open Health Sync keeps an append-only audit log per tenant: who read, exported, changed or
deleted which user's data, when, with which credential, and with what outcome. It is the record
a data protection officer, a security questionnaire or an incident review asks for (GDPR
articles 30 and 32 and comparable health data rules expect one).

## What is recorded

| Source | Events |
|---|---|
| Every API request authenticated with a tenant key or a user token | One event, whatever the outcome (a 404 on another tenant's user is recorded too) |
| `ohs admin tenant create`, `tenant admin`, `key create`, `key revoke` | `tenant.create`, `tenant.admin` (with `admin` true or false in `detail`), `key.create`, `key.revoke`, actor `admin` |
| A change an instance admin key makes to a tenant through the [admin API](admin-api.md) | On the tenant it changed, besides the request's own event on the admin's tenant: `tenant.create`, `key.create` and `key.revoke` with `admin_tenant_id`, `key_id` and `key_prefix` in `detail`; `data.write` for the demo user, with its `user_id` and `admin_tenant_id`, `synthetic`, `days`, `persona`, `rows_written` and `user_created` in `detail`; `tenant.provider` for a provider turned off or on, with `admin_tenant_id`, `provider` and `enabled` in `detail`; `tenant.provider_keys` for the tenant's own provider keys set, replaced or removed, with `admin_tenant_id`, `provider`, `set` and `reauth_required` in `detail`, never a key; for a request through the tenant routes (`/v1/admin/tenants/{tenant_id}/users...`, `/webhooks...`, `/pipeline/...`, `/deletions/...`, `/audit`), the action of the tenant route with the user of the path and `admin_tenant_id` (and `provider` or `job_id` when the path names one). Actor `admin` |
| The [first-run setup](setup.md) (`POST /v1/setup`) | `tenant.create`, actor `system` with id `setup`, the key prefix in `detail` |
| The deletion worker | `user.erased` when a user's data is gone, with the job id, actor `system` |
| A [console session](console.md#sign-in) | `session.create` (sign in), `session.read`, `session.delete` (sign out) and `session.tenant` (a tenant switch), actor `api_key` with the id of the session's key; see [Console sessions](#console-sessions) |
| A [share link](share-links.md) opened by its reader | `share.access` for each manifest request on a known link and each file download, actor `share_link`, the outcome in `detail` |

A request with no credential, or one that is not shaped like a key or a token, is not an audit
event: `/metrics` counts those by route. A well formed key or token that is refused (unknown,
revoked or expired, or a token of a user being deleted) is recorded as `auth.failed`, see below. A request refused with 429 by a
rate limit is not recorded. Provider-facing routes (`/oauth/...`, `/inbound/...`) take no tenant credential and are
not recorded either. The share link routes (`/shl/...`) take none either, but a request on a
known link is recorded under the link's own actor (above); one on an unknown link names no
tenant and is not.

Each event holds:

| Field | Content |
|---|---|
| `at` | Server time of the event |
| `actor.type`, `actor.id` | `api_key` and the key's id (as `ohs admin key list` shows); `user_token` and the first 16 hex characters of the token's SHA-256 hash (the token cannot be recovered from it); `admin` and `ohs admin` for the command line, or `api_key:` and the id of the admin key that changed this tenant through the admin API; `system` and the worker; `share_link` and the share's id |
| `action` | What was done, see below |
| `user_id` | The end user the action concerns: the user of a user token, the `{id}` of the route, or the demo user an admin key filled |
| `method`, `route`, `status` | The HTTP method, the route **pattern** (`GET /v1/users/{id}/samples`) and the status code |
| `actor_tenant_id` | Set when the request was made through a [console session](console.md#sign-in): the id of the session's own tenant, which differs from the tenant of the event when an admin acted on another tenant. `null` for a bearer key, a user token and every other source |
| `request_id` | The request's `X-Request-Id`, to match the event with your own logs |
| `detail` | Identifiers only, such as a deletion job id or a key prefix. For a route that names a tenant, a provider, an API key or a processing job in its path, its `tenant_id`, `provider`, `key_id` or `job_id`. For a request refused with 403 because the key lacks the [scope](api.md#key-scopes) of the route, `missing_scope`; for `key.create`, the `scopes` of the new key. A NUL character is removed and invalid UTF-8 replaced by U+FFFD, so such a value never stops the event from being written |

An event never holds a health value, a request or response body, a query string (which may hold
dates or provider codes), a token or a key. Only `auth.failed` events hold an IP address, the
one the refused credential came from.

### Actions

| Action | Requests |
|---|---|
| `data.read` | Lists, FHIR, Open mHealth, coverage, freshness, MCP |
| `data.export` | CSV and Parquet exports, the user archive |
| `data.write` | Device uploads (`/v1/ingest/...`), and a demo user filled with synthetic data through the admin API (`POST /v1/admin/tenants/{tenant_id}/demo-user`) |
| `user.create`, `user.read`, `user.update`, `user.delete` | The user routes; `user.update` sets or clears the user's time zone, `user.delete` is the request, `user.erased` the completed erasure |
| `token.create` | Minting a user token |
| `token.revoke` | Revoking one token of a user (`token_id` in `detail`) or all of them |
| `auth.failed` | A well formed API key or user token refused with 401. `detail` holds `client_address`, `reason` (`unknown`, `revoked`, `expired`, or `user_inactive` for a token whose user is being deleted), for a key its `key_prefix` (and `key_id` when the key is known), and `suppressed`, the failures of that address (for a revoked, expired or `user_inactive` credential: of that credential) that were not written since its last event. Actor `api_key` with the key's id or prefix, or `user_token` with the token id. A revoked or expired credential is recorded on its tenant, so `GET /v1/audit?action=auth.failed` shows a retired key that is still used; an unknown credential names no tenant and is stored under the nil tenant id (`00000000-0000-0000-0000-000000000000`), which only the instance operator reads, in the database. At most one event per client address and minute for unknown credentials, one per credential and minute for the others, and 600 a minute per server process, 150 of which are kept for credentials that are not unknown. A failure answered 429 (the address used up its failures) is counted in `suppressed`, not written. With `OHS_RATE_LIMIT_AUTH_FAILURES=0` nothing is answered 429 and the events are written within the same bounds |
| `connection.read`, `connection.create`, `connection.delete` | The provider connection routes and sync history (`sync-runs`), and creating a hosted connection page link (`connect-sessions`) |
| `connection.sync` | Asking for an immediate pull (`POST .../connections/{provider}/sync`) |
| `webhook.read`, `webhook.write` | The webhook endpoint routes |
| `share.create`, `share.read`, `share.revoke` | Making, listing or reading, and revoking [share links](share-links.md) |
| `share.access` | A manifest request or a file download on a share link; `detail` holds `share_id` and `outcome` (`served`, `wrong_passcode`, `inactive`, `file`). Never the reader's name |
| `tenant.create` | Creating a tenant through the admin API (`POST /v1/admin/tenants`) |
| `key.create`, `key.revoke` | Creating and revoking an API key (`POST /v1/keys`, `DELETE /v1/keys/{key_id}`, and for another tenant `POST /v1/admin/tenants/{tenant_id}/keys`, `DELETE /v1/admin/tenants/{tenant_id}/keys/{key_id}`); listing keys is `config.read` |
| `tenant.provider` | Turning a provider off or back on for one tenant (`PATCH /v1/admin/tenants/{tenant_id}/providers/{provider}`); `detail` holds `provider` and, on the tenant changed, `enabled` |
| `tenant.provider_keys` | Setting, replacing or removing a tenant's own keys for a provider (`PUT` or `DELETE /v1/admin/tenants/{tenant_id}/providers/{provider}/keys`); `detail` holds `provider` and, on the tenant changed, `set` and `reauth_required` (the connections set `needs_reauth`), never a key |
| `session.create`, `session.read`, `session.delete`, `session.tenant` | The console session routes: sign in (`POST /console/session`, `detail` holds `key_prefix`), reading the session (`GET /console/session`), sign out (`DELETE /console/session`) and switching tenant (`PUT /console/session/tenant`, `detail` holds `from_tenant_id` and `to_tenant_id`). Never a token |
| `audit.read` | Reading this log |
| `config.read`, `config.write` | Anything else: the provider list, the tenant, the pipeline and retrying processing jobs (`/v1/pipeline`), the provider settings (`/v1/admin/providers`), the server settings (`/v1/admin/settings`; a `PATCH` writes `config.write` with `detail` holding `settings`, the names it changed, and the new `revision`, never a value), reading tenants, their keys, pipelines and providers (`/v1/admin/tenants`) |

### Console sessions

The key behind a [console session](console.md#sign-in) is the actor of every event it makes
(`actor.type` `api_key`, `actor.id` the key's id), and `actor_tenant_id` is the session's tenant.
Where the event is stored depends on the route:

- A request to a tenant route made while acting on a tenant is recorded on that acting tenant, with
  `actor_tenant_id` set to the session's tenant. A tenant sees what an admin did in its data, and who.
- A request to an instance admin route (`/v1/admin/...`) is recorded on the session's own tenant,
  never on the tenant the console is viewing.
- All four `session.*` events (`session.create`, `session.read`, `session.delete` and
  `session.tenant`) are recorded on the session's own tenant, even while it views another one.
  `session.read` is a buffered event.

Filter the log by `actor_tenant_id` in the result to see what sessions of another tenant did.

New actions may be added; treat an unknown one as `config.read` or `config.write` by its method.

## Reading it

`GET /v1/audit` with a tenant key, newest first, paged with `cursor` and `limit` (1 to 1000,
default 100). Filters:

- `user_id`: one end user;
- `action`: an exact action (`data.export`) or a prefix ending in a dot (`data.`);
- `since`, `until`: RFC 3339 times or `YYYY-MM-DD` dates (`until` is exclusive).

```sh
# Everything done with one user's data in October.
curl -s "$OHS/v1/audit?user_id=$USER_ID&action=data.&since=2026-10-01&until=2026-11-01" \
  -H "Authorization: Bearer $KEY"
```

```json
{
  "data": [
    {
      "id": 1042,
      "at": "2026-10-13T09:12:44.120511Z",
      "actor": { "type": "api_key", "id": "7d0c9f1e-5b2a-4c36-9e0d-2b8f4a1c6e53" },
      "action": "data.export",
      "user_id": "1b9e2c4a-0f3d-4e8b-a7c6-5d2e1f0a9b8c",
      "method": "GET",
      "route": "GET /v1/users/{id}/exports/{dataset}",
      "status": 200,
      "request_id": "4c1f0e9a2b7d4e6f8a3c5b1d9e0f2a7c",
      "detail": {}
    }
  ],
  "next_cursor": null
}
```

A user token cannot read the log. Reading it is itself recorded (`audit.read`).

## Guarantees and limits

- **Append only.** A database trigger refuses `UPDATE`, `DELETE` and `TRUNCATE` on
  `ohs_core.audit_events`, for the application and for anyone connecting with its database
  role. The only exception is the retention job, which sets a flag for its own transaction.
  A database superuser or the table owner can still drop the trigger: if you need evidence that
  holds against your own database administrators, ship the events to write-once storage (poll
  `GET /v1/audit` and store the pages elsewhere).
- **Kept after erasure.** Deleting a user removes their data, tokens and connections, but not
  the audit events that name them: those hold the user's id only, no personal data, and are the
  proof the erasure happened (`user.erased`).
- **Retention.** Events older than `OHS_AUDIT_RETENTION_DAYS` (default 365) are deleted once a
  day by the `etl` role; `0` keeps every event. Set it to what your legal basis requires.
- **Buffered for reads and uploads.** When the instance runs the buffered writer, the events
  of the high volume actions (`data.read`, `data.write`, `user.read`, `connection.read`,
  `connection.sync`, `consent.read`, `webhook.read`, `audit.read`, `config.read`, `share.read`
  and `auth.failed`) are queued in memory and written in batches, at most a second later, so a
  request does not wait for its audit row; `at` is still the time of the request. The queue
  holds 4,096 events: with it full, an event is written before its request ends instead, like a
  synchronous one, so a flood slows requests down and loses nothing. A batch the database does
  not take is tried again, each of its events alone, then twice more after 250 and 500 ms; the
  events still not written are dropped, logged and counted in `ohs_audit_dropped_total`. When
  the process stops, the queue is written out, within 10 seconds. What stays best effort: the
  events in memory when the process is killed (`kill -9`, an out of memory kill, a power loss)
  rather than stopped, at most about one second of reads and uploads while the database
  answers; the events of a batch the database refused three times; and the events still queued
  if the database does not take them within the 10 seconds of a stop. Every
  other action (key and token changes, user creation and deletion, exports, consent changes,
  share links made, revoked or opened, connections made or removed, webhook and settings
  changes, tenant changes) is written before the request ends, as described next.
- **Best effort on a database failure.** A synchronous event is written after the response, on
  the same request. If the write fails (the database went away mid-request), the response stands, the
  failure is logged and counted in `ohs_audit_write_failures_total` on `/metrics`. Alert on that
  counter. Erasures, admin commands and the tenant and key changes of the admin API are recorded
  in the same transaction as the change, so they are never lost; the admin API's demo user is
  answered only once its event is written.
- **Volume.** One row per authenticated request. A client polling every user every minute
  writes that many rows; size retention accordingly.
