# Outgoing webhooks

Instead of polling `coverage`, your server can be told when something changes. Register an
endpoint, and Open Health Sync posts a signed JSON event to it when a sync writes new or changed
records, when a provider deletes records, when a user is erased, when a connection needs the
user to sign in again, or when a time tag is created, changed or deleted.

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
| `POST /v1/webhooks/{webhook_id}/test` | Send a `webhook.test` event, or a sample of any event type, to this endpoint |
| `GET /v1/webhooks/{webhook_id}/deliveries` | Deliveries, newest first; `status`, `cursor`, `limit` |
| `GET /v1/webhooks/{webhook_id}/deliveries/{delivery_id}` | One delivery with its payload and attempt history |
| `POST /v1/webhooks/{webhook_id}/deliveries/{delivery_id}/retry` | Send a done or parked delivery again |
| `POST /v1/webhooks/{webhook_id}/deliveries/{delivery_id}/replay` | Send a done or parked delivery's event again, as a new delivery |

Every route takes a tenant API key. The MCP server's `list_webhooks` tool shows the same endpoints
and their recent deliveries to an agent; the MCP server stays read-only, so changes go through
the REST routes.

## Events

| Type | Sent when | `data` |
|---|---|---|
| `data.written` | A sync or an upload wrote new or changed records | `user_id`, `external_ref`, `provider`, `records` (rows per table), `types`, `first_day`, `last_day` |
| `data.deleted` | Records the provider deleted were removed | `user_id`, `external_ref`, `provider`, `records_deleted` |
| `data.erased` | The erasure of a user's data from one provider finished (a disconnect with `delete_data=true`, or `DELETE /v1/users/{id}/data/{provider}`) | `user_id`, `external_ref`, `provider`, `job_id` |
| `user.deleted` | A user's erasure finished | `user_id`, `external_ref` |
| `consent.granted` | Categories of a source were granted ([consent](consent.md)) | `user_id`, `external_ref`, `provider`, `categories` |
| `consent.revoked` | Categories of a source were revoked; their stored records are being deleted | `user_id`, `external_ref`, `provider`, `categories` |
| `connection.needs_reauth` | The provider refused the user's grant; nothing syncs until they connect again | `user_id`, `external_ref`, `provider` |
| `connection.broken` | A connection stopped syncing: grant refused, a permission missing, pulls failing, or a provider quota spent | `user_id`, `external_ref`, `provider`, `reason`, `action`, and `retry_at` or `last_error` ([details](sync-health.md)) |
| `connection.restored` | A broken connection synced again, or the user connected again | `user_id`, `external_ref`, `provider`, `previous_reason`, `broken_since`, `broken_seconds` |
| `tag.created`, `tag.updated`, `tag.deleted` | A [time tag](time-tags.md) of the user was created, changed or deleted through the API | `user_id`, `external_ref`, `tag_id` |
| `webhook.test` | You called the test route | `endpoint_id` |

Events sent by the test route also carry `"test": true` at the top level, next to `type`. A real
event never has that field. See [Debugging (sandbox)](#debugging-sandbox).

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
access from its `etl` processes. The signing secrets are sealed with `OHS_ENCRYPTION_KEY`;
[rotating the key](key-rotation.md) keeps them. If the key changes without the old one kept in
`OHS_PREVIOUS_ENCRYPTION_KEYS`, the stored secrets no longer open: deliveries fail with a
message saying so, and each endpoint needs a `rotate-secret`.

## Debugging (sandbox)

Every attempt of every delivery is recorded, so you can see why a receiver refused an event, send
it again once the receiver is fixed, and send sample events while you write the receiver.

### List deliveries

`GET /v1/webhooks/{webhook_id}/deliveries` lists the endpoint's deliveries, newest first, with
the last status code and error. Filter with `status` (`pending`, `running`, `done`, `parked`) and
page with `cursor` and `limit`.

### Inspect attempts

`GET /v1/webhooks/{webhook_id}/deliveries/{delivery_id}` returns one delivery with:

- `payload`: the exact JSON body sent on every attempt (verify your signature code against it).
- `attempt_history`: the attempts, oldest first. Each has `attempt` (1, 2, ..., counting on
  across retries), `started_at`, `duration_ms`, `status_code` (null when no HTTP answer came:
  timeout, refused connection, private address), `error` (null on a success) and
  `response_body_excerpt`: the first 1 KiB of your receiver's answer, with anything that is not
  printable UTF-8 replaced by `?`.

The last 20 attempts of a delivery are kept. Attempts are removed with their delivery: after 30
days, when the endpoint is deleted, or when the user the event is about is deleted.

### Retry or replay

Both routes take a `done` or `parked` delivery and answer 409 for one still `pending` or
`running`. Both send the same event id (the `webhook-id` header) and the same payload, so a
receiver that deduplicates on `webhook-id` treats them as the same event; the request is signed
again with the current secret and timestamp.

- `retry` puts the same delivery back in the queue from its first attempt. Its status, last
  error and attempt count start over; its attempt history continues.
- `replay` creates a new delivery, with `replay_of` set to the original's id and its own
  history, and answers 202 with it (and its URL in `Location`). The original stays as it was,
  which keeps the history readable when you replay the same event several times. At most 10
  replays of one endpoint wait to be sent at once; an eleventh is refused with 429 until some
  are sent.

### Send sample events

`POST /v1/webhooks/{webhook_id}/test` with no body sends the generic `webhook.test` event. With
`{"event_type": "<type>"}`, any type from the table above, it sends a realistic sample of that
event instead: the same fields a real one has, about a user that does not exist (`user_id` is
`00000000-0000-0000-0000-000000000000`, `external_ref` is `sample-user`), with `"test": true` in
the envelope. The test route ignores the endpoint's `event_types`, so you can try a type before
subscribing to it.

### Walkthrough with a local receiver

Run an instance with `OHS_WEBHOOK_ALLOW_PRIVATE=true` so it may post to `localhost`, then start a
receiver that refuses the first request and accepts the next ones. Python's standard library is
enough:

```python
# receiver.py: python3 receiver.py
from http.server import BaseHTTPRequestHandler, HTTPServer

seen = 0

class Hook(BaseHTTPRequestHandler):
    def do_POST(self):
        global seen
        seen += 1
        body = self.rfile.read(int(self.headers["Content-Length"]))
        print(self.headers["webhook-id"], body.decode())
        code = 503 if seen == 1 else 200
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"ok": %s}' % (b"false" if code == 503 else b"true"))

HTTPServer(("127.0.0.1", 3000), Hook).serve_forever()
```

Register it and send a sample `data.written`:

```sh
WH=$(curl -s -X POST $OHS/v1/webhooks -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" -d '{"url":"http://localhost:3000/hooks"}' | jq -r .id)
D=$(curl -s -X POST $OHS/v1/webhooks/$WH/test -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" -d '{"event_type":"data.written"}' | jq -r .id)
```

The first attempt gets the 503 and the retry 5 seconds later gets the 200. Inspect both:

```sh
curl -s $OHS/v1/webhooks/$WH/deliveries/$D -H "Authorization: Bearer $KEY" \
  | jq '{status, attempts, history: [.attempt_history[] | {attempt, status_code, error, response_body_excerpt}]}'
# {
#   "status": "done",
#   "attempts": 2,
#   "history": [
#     {"attempt": 1, "status_code": 503, "error": "receiver answered HTTP 503", "response_body_excerpt": "{\"ok\": false}"},
#     {"attempt": 2, "status_code": 200, "error": null, "response_body_excerpt": "{\"ok\": true}"}
#   ]
# }
```

Replay it, and the receiver prints the same `webhook-id` a third time:

```sh
curl -s -X POST $OHS/v1/webhooks/$WH/deliveries/$D/replay -H "Authorization: Bearer $KEY" \
  | jq '{id, event_id, status, replay_of}'
```

To find what failed in production, list the parked deliveries and read one:

```sh
curl -s "$OHS/v1/webhooks/$WH/deliveries?status=parked&limit=5" -H "Authorization: Bearer $KEY" \
  | jq '.data[] | {id, event_type, last_status_code, last_error}'
```
