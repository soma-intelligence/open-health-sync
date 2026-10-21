# Sync history and connection health

When a user says "my data isn't arriving", three questions need an answer: is the connection still
allowed to sync, what happened on the last pulls, and can a pull run right now. Open Health Sync
answers each with one call, and tells your backend by webhook when a connection breaks or
recovers.

All routes take a tenant key. `{id}` is the user's id.

## Connection health

`GET /v1/users/{id}/connections` and `GET /v1/users/{id}/freshness` carry a `health` object for
every connection:

```json
"health": {
  "healthy": false,
  "reason": "needs_reauth",
  "action": "reconnect",
  "since": "2026-10-12T08:30:12Z",
  "retry_at": null
}
```

| `reason` | What happened | `action` |
|---|---|---|
| `needs_reauth` | The provider refused the grant: the token expired for good or the user revoked access | `reconnect` |
| `missing_scope` | The grant lacks a permission the data needs: the user unticked it at consent or withdrew it | `reconnect` |
| `failing` | The last 3 pulls failed (a provider outage, an answer the connector did not expect) | `wait` |
| `rate_limited` | The provider asked to wait an hour or more (a spent daily quota); pulls resume at `retry_at` | `wait` |

`reconnect` means: send the user through the connect flow again
(`POST /v1/users/{id}/connections/{provider}`). Their data is kept and the connection picks up
where it stopped. `wait` means nothing is needed from the user: pulls go on with backoff and the
connection heals on the next successful one. A healthy connection has `healthy: true` and every
other field null.

A short rate limit (a provider asking to wait a minute) is routine and does not break the
connection: the pull is simply retried later.

## Webhooks: `connection.broken` and `connection.restored`

Subscribe an endpoint to these events (see [outgoing webhooks](webhooks.md)) to hear about a
broken connection when it happens, rather than when the user complains:

```json
{
  "type": "connection.broken",
  "data": {
    "user_id": "4b7e1c9a-2f3d-4e5a-8b6c-1d2e3f4a5b6c",
    "external_ref": "app-user-42",
    "provider": "oura",
    "reason": "needs_reauth",
    "action": "reconnect"
  }
}
```

`connection.broken` is sent once when a connection breaks, not on every failed pull. It is sent
again only when the reason gets worse (a `failing` connection whose grant is then refused).
`failing` adds `last_error` and `consecutive_failures`; `rate_limited` adds `retry_at`.

`connection.restored` is sent when a broken connection syncs again, or when the user connects
again. It carries `previous_reason`, `broken_since` and `broken_seconds`.

`connection.needs_reauth` is still sent as before, alongside `connection.broken`, so existing
integrations keep working.

## Sync history

`GET /v1/users/{id}/sync-runs` lists every pull of the user's connections, newest first.
`?provider=oura` keeps one provider; `limit` (1 to 100, default 50) and `cursor` page through it.

```json
{
  "data": [
    {
      "id": 1842,
      "provider": "oura",
      "trigger": "notification",
      "status": "retrying",
      "data_type": "sleep",
      "window_start": "2026-10-12T06:00:00Z",
      "window_end": "2026-10-12T08:00:00Z",
      "attempts": 1,
      "objects_stored": 0,
      "error": "data endpoint: 502",
      "created_at": "2026-10-12T08:00:03Z",
      "started_at": "2026-10-12T08:00:04Z",
      "finished_at": null,
      "next_attempt_at": "2026-10-12T08:00:34Z"
    }
  ],
  "next_cursor": null
}
```

- `trigger`: `backfill` (the first 30 days of a new connection), `scheduled` (the regular backstop
  pull), `notification` (the provider said something changed) or `manual` (sync now, below).
- `status`: `queued`, `running`, `retrying` (the last attempt failed, the next is at
  `next_attempt_at`), `rate_limited` (the provider asked to wait until `next_attempt_at`),
  `succeeded`, `failed` (five attempts spent, or the grant was refused) or `cancelled` (the
  connection was disconnected, or an instance admin turned the provider off for the tenant; see
  [admin-api.md](admin-api.md#providers-per-tenant)).
- `error` is the provider's error, made safe to show: no tokens, no query strings, no health
  values, at most 200 bytes.
- `objects_stored` counts the raw provider payloads the pull stored. The records written from them
  are announced by `data.written` webhooks, and the processing of each payload is visible in the
  ETL metrics.

Finished runs are kept 30 days. `GET /v1/users/{id}/sync-runs/{run_id}` returns one run.

## Sync now

`POST /v1/users/{id}/connections/{provider}/sync` queues a pull of that connection ahead of
routine work, and answers `202` with the run and a `Location` header to follow it:

```sh
curl -s -X POST "$OHS/v1/users/$USER_ID/connections/oura/sync" -H "Authorization: Bearer $TENANT_KEY"
# {"id":1843,"provider":"oura","trigger":"manual","status":"queued",...}
curl -s "$OHS/v1/users/$USER_ID/sync-runs/1843" -H "Authorization: Bearer $TENANT_KEY"
```

The window ends now and starts where the connection's data ends (an hour earlier, to catch late
data), or a day back for a connection that has not synced yet. Pass `{"from": "2026-10-01T00:00:00Z"}`
to reach further back, up to 30 days.

- A "sync" button can be pressed many times: while a manual run of the connection is still queued
  or running, the call answers `200` with that run and queues nothing.
- `409` when the connection cannot sync: it needs the user to connect again (`needs_reauth`), it is
  disconnected, its provider is not enabled on this instance, or an instance admin turned the
  provider off for this tenant.
- Apple Health and Health Connect have no sync route: their data arrives when the phone uploads.

Providers that send notifications already pull within seconds of a change (see
[background sync](background-sync.md)), so sync now is mostly for support and for a user who just
fixed something on the provider's side.
