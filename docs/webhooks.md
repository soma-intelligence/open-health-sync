# Outgoing webhooks

Instead of polling `coverage`, your server can be told when something changes. Register an
endpoint, and Open Health Sync posts a signed JSON event to it when a sync writes new or changed
records, when a provider deletes records, when a user is erased, or when a connection needs the
user to sign in again.

Events are thin: they say what changed, for which user, from which provider and over which days,
never a health value. Your server reads the data with the API it already uses. A leaked or
misrouted event therefore carries no health data.

Requests follow the [Standard Webhooks](https://www.standardwebhooks.com) specification, so the
verification libraries it lists (Go, Python, JavaScript, Java, Kotlin, Ruby, PHP, Rust, C#,
Elixir...) work as they are.

## Register an endpoint

```sh
curl -s -X POST $OHS/v1/webhooks -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://app.example.com/hooks/health","event_types":["data.written","connection.needs_reauth"]}'
# {"id":"...","url":"...","event_types":[...],"status":"active",...,"secret":"whsec_..."}
```

Store `secret` now: it is shown only in this response. An empty or absent `event_types` subscribes
to every type, including types added in later versions. A tenant has at most 20 endpoints.

The URL must be `https` and must not point at a private, loopback or link-local address; the check
runs again on every connection, after DNS resolution, and redirects are not followed. To deliver
to a receiver on the same machine or network (`http://localhost:3000/hooks`,
`http://app:8000/hooks` in docker compose), set `OHS_WEBHOOK_ALLOW_PRIVATE=true` on the instance.

| Route | Does |
|---|---|
| `POST /v1/webhooks` | Register an endpoint; returns its secret once |
| `GET /v1/webhooks` | List the tenant's endpoints |
| `GET /v1/webhooks/{webhook_id}` | One endpoint, with `failing_since` and `last_success_at` |
| `PATCH /v1/webhooks/{webhook_id}` | Change `url`, `description`, `event_types`; `enabled` false or true |
| `DELETE /v1/webhooks/{webhook_id}` | Delete it and its deliveries |
| `POST /v1/webhooks/{webhook_id}/rotate-secret` | New secret, used from the next request on |
| `POST /v1/webhooks/{webhook_id}/test` | Send a `webhook.test` event to this endpoint |
| `GET /v1/webhooks/{webhook_id}/deliveries` | Deliveries, newest first; `status`, `cursor`, `limit` |
| `POST /v1/webhooks/{webhook_id}/deliveries/{delivery_id}/retry` | Send a done or parked delivery again |

Every route takes a tenant API key. The MCP server's `list_webhooks` tool shows the same endpoints
and their recent deliveries to an agent; the MCP server stays read-only, so changes go through
the REST routes.

## Events

| Type | Sent when | `data` |
|---|---|---|
| `data.written` | A sync or an upload wrote new or changed records | `user_id`, `external_ref`, `provider`, `records` (rows per table), `types`, `first_day`, `last_day` |
| `data.deleted` | Records the provider deleted were removed | `user_id`, `external_ref`, `provider`, `records_deleted` |
| `user.deleted` | A user's erasure finished | `user_id`, `external_ref` |
| `connection.needs_reauth` | The provider refused the user's grant; nothing syncs until they connect again | `user_id`, `external_ref`, `provider` |
| `webhook.test` | You called the test route | `endpoint_id` |

```json
{
  "id": "0199d6c4-8a5e-7d3c-9f1e-6b2a4c8d0e17",
  "type": "data.written",
  "timestamp": "2026-10-12T08:30:12.345Z",
  "data": {
    "user_id": "4b7e1c9a-2f3d-4e5a-8b6c-1d2e3f4a5b6c",
    "external_ref": "app-user-42",
    "provider": "oura",
    "records": { "sleep_sessions": 1, "sleep_phases": 24, "daily_summaries": 1 },
    "types": ["daily_summary", "sleep_analysis"],
    "first_day": "2026-10-11",
    "last_day": "2026-10-12"
  }
}
```

`first_day` and `last_day` are calendar days (the user's local day where the source gives a time
zone), so the natural follow-up is a read of that window:
`GET /v1/users/{user_id}/sleep?start=2026-10-11&end=2026-10-13`. One sync can produce several
`data.written` events (one per raw object: a provider pull is one object per data type). An
upload that only repeats stored records sends nothing.

Events are written in the same database transaction as the change they announce: a change that
rolls back sends nothing, and a committed one is never lost by a restart.

## Verify a request

Each request carries three headers:

| Header | Value |
|---|---|
| `webhook-id` | The event id, the same on every retry: deduplicate on it |
| `webhook-timestamp` | Unix seconds when this attempt was signed |
| `webhook-signature` | `v1,` + base64 HMAC-SHA256 of `{webhook-id}.{webhook-timestamp}.{body}` |

The HMAC key is the secret after `whsec_`, base64-decoded. Compute it over the raw body bytes, as
received, compare in constant time, and refuse a timestamp more than 5 minutes from your clock.
In Python, without a library:

```python
import base64, hashlib, hmac, time

def verify(secret: str, headers, body: bytes) -> bool:
    key = base64.b64decode(secret.removeprefix("whsec_"))
    msg_id, ts = headers["webhook-id"], headers["webhook-timestamp"]
    if abs(time.time() - int(ts)) > 300:
        return False
    expected = base64.b64encode(hmac.new(key, f"{msg_id}.{ts}.".encode() + body, hashlib.sha256).digest()).decode()
    return any(hmac.compare_digest(expected, sig.removeprefix("v1,"))
               for sig in headers["webhook-signature"].split() if sig.startswith("v1,"))
```

With the `standardwebhooks` package: `Webhook(secret).verify(body, headers)`.

## Delivery and retries

Answer with any 2xx within 15 seconds; do the work after answering. Anything else (another
status, a timeout, a refused connection, a redirect) is a failed attempt, retried after 5 seconds,
5 minutes, 30 minutes, 2 hours, 5 hours, 10 hours and 10 hours: 8 attempts over about 28 hours.
Delivery is at least once and not ordered, so a receiver must accept an event twice and must not
rely on order; re-reading the window an event names is always safe.

A delivery that fails all 8 attempts is `parked`. `GET /v1/webhooks/{id}/deliveries?status=parked`
lists them with the last status code and error, and the retry route sends one again with its
original id. An endpoint that has accepted nothing for 5 days is disabled when one more delivery
runs out of attempts; its pending deliveries are parked and `disabled_reason` says why. Fix the
receiver, then `PATCH` it with `{"enabled": true}` and retry what you need.

Finished deliveries are kept 30 days. When a user is deleted, the events not yet sent about them
are dropped and `user.deleted` is queued.

Deliveries are posted by the `etl` role, so an instance split into roles needs outbound network
access from its `etl` processes. If `OHS_ENCRYPTION_KEY` changes, the stored secrets no longer
open: deliveries fail with a message saying so, and each endpoint needs a `rotate-secret`.
