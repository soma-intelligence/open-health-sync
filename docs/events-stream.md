# Event stream

[Webhooks](webhooks.md) need a public HTTPS receiver; polling needs a loop. The event stream is
the third way: open one long-lived request and read the events as they happen, from a backend
behind a firewall, a worker, a dashboard or a script. It is a
[Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html) stream
(`text/event-stream`), so a browser's `EventSource`, any SSE library or plain `curl -N` reads it.

It carries exactly the events webhooks carry, with the same names and the same JSON body: thin,
never a health value. Read the data with the API, as after a webhook.

## Routes

| Route | Credential | Key scope | Streams |
|---|---|---|---|
| `GET /v1/events/stream` | tenant API key | `webhooks:read` | every event of the tenant |
| `GET /v1/users/{id}/events/stream` | tenant API key, or that user's token with scope `read` | `data:read` | the events about one user |

`webhooks:read` is the scope that already reads delivery payloads, which are the same events.
The per-user route takes a user token, so an app can stream its own user's events without a
backend in between; a user token reads only its own user (another user is a 404). Its key scope
is `data:read`, the scope a key needs to mint a `read` token, so a token never streams more than
its key reads. With a tenant key, `GET /v1/events/stream?user_id=...` streams the same events
under `webhooks:read`.

| Parameter | Does |
|---|---|
| `types` | Only these event types, separated by commas (or the parameter repeated), from the [webhook event table](webhooks.md#events). Absent: every type, including types added later. An unknown type is a 400 |
| `user_id` | Tenant route only: the events about this user |
| `Last-Event-ID` header, or `last_event_id` | Resume after this event, see [resume](#resume) |

```sh
curl -N "$OHS/v1/events/stream?types=data.written,connection.broken" -H "Authorization: Bearer $KEY"
```

## What arrives

```text
retry: 3000

: connected

id: 0199d6c4-8a5e-7d3c-9f1e-6b2a4c8d0e17
event: data.written
data: {"id":"0199d6c4-8a5e-7d3c-9f1e-6b2a4c8d0e17","type":"data.written","timestamp":"2026-10-12T08:30:12.345Z","data":{"user_id":"4b7e1c9a-2f3d-4e5a-8b6c-1d2e3f4a5b6c","external_ref":"app-user-42","provider":"oura","records":{"sleep_sessions":1},"types":["sleep_analysis"],"first_day":"2026-10-11","last_day":"2026-10-12"}}

: ping
```

- `id` is the event id: the `webhook-id` its deliveries carry. Deduplicate on it.
- `event` is the event type, `data` the body a webhook delivery posts, on one line.
- `retry: 3000` asks the client to wait 3 seconds before it reconnects.
- A comment (`: ping`) comes every 15 seconds while nothing else does, so proxies and clients
  keep the connection open. An `EventSource` never shows comments.

The one event that is not a webhook event is `stream.reset`, without an id:
`{"reason": "last_event_id_unknown", "window_hours": 24}` or `"reason": "too_many_missed"`. The
stream could not resume where the client left off: re-read what you need (the coverage of your
users, for example), then go on with the events that follow.

Events come in the order their transactions committed, a few milliseconds after the commit.

## Resume

A client that reconnects sends the id of the last event it received in `Last-Event-ID`; an
`EventSource` does it on its own. The stream first sends the events written after that one, then
goes on live. A client that cannot set headers passes `last_event_id` instead (the header wins).

Events are kept **24 hours**. An id older than that, unknown, or of another tenant gets a
`stream.reset` and the stream goes on live. More than 10,000 events to replay get a
`stream.reset` too.

The stream is best effort, at most once per connection: an event can be missed in the moment a
connection drops, when another transaction committed out of order with the last event the client
saw, and events older than 24 hours are gone. Use webhooks where every event must arrive (they
are retried for about 28 hours), or re-read the window an event names, which is always safe.

## Limits

| | |
|---|---|
| Streams per tenant | `OHS_STREAM_MAX_PER_TENANT` (default 10) per `api` process. One more answers 429 with `Retry-After` |
| Streams per process | `OHS_STREAM_MAX` (default 1000). One more answers 429. `0` turns the stream off: both routes answer 404 and no event is kept |
| Slow client | Up to 256 events wait for one stream, and one write may take 10 seconds. A client that does not keep up is disconnected, and resumes when it reconnects |
| Lifetime | A stream ends after an hour, and when the server shuts down. The client reconnects with `Last-Event-ID`, and its credential is checked again, so a revoked key or token stops streaming within the hour |
| Rate limit | Opening a stream is one request of the credential's [rate limit](self-hosting.md#rate-limits); the events it then carries are not requests |

An `EventSource` gives up on a 429 or another error status: retry it yourself after
`Retry-After`, or use the SDK helpers below, which retry a 503 and stop on anything else. The
caps are per `api` process, so behind a load balancer a tenant's real cap is the sum over the
replicas.

Responses are never compressed and say `Cache-Control: no-store` and `X-Accel-Buffering: no`.

## From code

The server SDKs carry a typed helper that parses the stream, reconnects with `Last-Event-ID`
and turns `stream.reset` into a message of its own.

TypeScript (`@open-health-sync/server`):

```ts
import { streamEvents } from "@open-health-sync/server";

const controller = new AbortController();
for await (const m of streamEvents({ baseUrl: OHS, apiKey: KEY, types: ["data.written"], signal: controller.signal })) {
  if (m.kind === "reset") await resyncEveryone();
  else await refresh(m.event.data.user_id, m.event.data.first_day, m.event.data.last_day);
}
```

`user` streams one user's events (with that user's token, or a key); `userId` filters the
tenant stream; `lastEventId` resumes after an event you stored.

Python (`open_health_sync`, standard library only):

```python
from open_health_sync import StreamEvent, StreamReset, stream_events

for m in stream_events(OHS, KEY, types=["data.written"]):
    if isinstance(m, StreamReset):
        resync_everyone()
    else:
        data = m.event["data"]
        refresh(data["user_id"], data["first_day"], data["last_day"])
```

In a browser, with a user token minted by your backend for its own user (an `EventSource`
cannot set the `Authorization` header, so use a fetch based SSE client that can, or proxy the
stream through your backend):

```js
import { streamEvents } from "@open-health-sync/server";
for await (const m of streamEvents({ baseUrl: OHS, apiKey: userToken, user: userId })) { /* ... */ }
```

With `httpx` in Python:

```python
import httpx

with httpx.stream("GET", f"{OHS}/v1/events/stream", headers={"Authorization": f"Bearer {KEY}"}, timeout=60) as r:
    for line in r.iter_lines():
        print(line)
```

## Self-hosting

The events are written by whichever process produces them (the `etl` and `api` roles), in the
same transaction as the change, to a table of their own (`ohs_core.stream_events`, migration
`0054`) together with a Postgres `NOTIFY` that carries only the row's id. Every `api` process
listens on one connection of its own and loads each row once for the streams it serves, so an
instance split into roles and replicas needs nothing more. The `etl` role deletes the rows after
24 hours. Give every process the same `OHS_STREAM_MAX`: with `0` on one process, the events it
produces are not kept.

A user's erasure removes the events about them, except the `user.deleted` that announces it.

Behind a reverse proxy, the stream must not be buffered and the read timeout must exceed the
15 second heartbeat. The bundled Caddyfile passes it as it is. nginx honours
`X-Accel-Buffering: no`; the Helm chart's ingress values set
`nginx.ingress.kubernetes.io/proxy-buffering: "off"` and a 300 second
`nginx.ingress.kubernetes.io/proxy-read-timeout`. For another proxy, turn response buffering off
for `/v1/events/stream` and `/v1/users/*/events/stream`.
