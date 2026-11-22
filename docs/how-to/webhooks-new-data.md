# Get a webhook when new wearable data arrives

To be told when new wearable data arrives, register an endpoint with `POST /v1/webhooks` and
subscribe to `data.written`: Open Health Sync posts a signed JSON event each time a sync or an
upload writes new or changed records, naming the user, the provider and the days. Your server
verifies the Standard Webhooks signature, then reads that window through the API. Open Health
Sync is open source and self-hosted.

Events are thin: they never carry a health value, so a leaked or misrouted event holds no
health data.

## 1. Register an endpoint

With a tenant API key (`OHS_KEY`, from `ohs admin personal setup` or `ohs admin tenant create`):

```sh
curl -s -X POST "$OHS_URL/v1/webhooks" -H "Authorization: Bearer $OHS_KEY" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://app.example.com/hooks/health","event_types":["data.written","connection.needs_reauth"]}'
# {"id":"...","url":"...","event_types":[...],"status":"active",...,"secret":"whsec_..."}
```

Store `secret` now: it is shown only in this response. Without `event_types` the endpoint gets
every type, including types added later. A tenant has at most 20 endpoints. The URL must be
`https` and must not point at a private, loopback or link-local address, unless the instance sets
`OHS_WEBHOOK_ALLOW_PRIVATE=true` (a receiver on the same host or network).

## 2. What arrives

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

`first_day` and `last_day` are calendar days, so the follow-up is a read of that window, for
example `GET /v1/users/{user_id}/sleep?start=2026-10-11&end=2026-10-13`. One sync can send
several `data.written` events (one per raw object), and an upload that only repeats stored
records sends none. Other types worth subscribing to: `data.deleted`, `connection.broken`,
`connection.restored`, `user.deleted` ([full list](../webhooks.md#events)).

## 3. Verify the signature

Each request carries `webhook-id` (the event id, the same on every retry: deduplicate on it),
`webhook-timestamp` (Unix seconds) and `webhook-signature` (`v1,` + base64 HMAC-SHA256 of
`{webhook-id}.{webhook-timestamp}.{body}`). The HMAC key is the secret after `whsec_`,
base64-decoded. Compute it over the raw body bytes as received, compare in constant time, and
refuse a timestamp more than 5 minutes from your clock. In Go, with the standard library:

```go
func verify(secret string, h http.Header, body []byte) bool {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return false
	}
	id, ts := h.Get("webhook-id"), h.Get("webhook-timestamp")
	sent, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(sent, 0)).Abs() > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, sig := range strings.Fields(h.Get("webhook-signature")) {
		if hmac.Equal([]byte(sig), []byte(want)) {
			return true
		}
	}
	return false
}
```

The [server SDKs](../server-sdks.md#verify-a-webhook) (TypeScript and Python, installed from a
clone) do the same check and return the parsed event. Give them the raw body, never a body
parsed and serialized again:

```python
from open_health_sync import WebhookVerificationError, verify_webhook

try:
    event = verify_webhook(os.environ["OHS_WEBHOOK_SECRET"], request.headers, request.get_data())
except WebhookVerificationError:
    return "", 400
```

In other languages, the `standardwebhooks` packages verify the same headers.

## 4. Answer fast

Answer any 2xx within 15 seconds and do the work afterwards. Anything else is retried after 5 s,
5 min, 30 min, 2 h, 5 h, 10 h and 10 h: 8 attempts over about 28 hours. Delivery is at least once
and not ordered, so accept an event twice and re-read the window it names. A delivery that fails
all 8 attempts is `parked`.

## 5. Test and debug

Send a realistic sample of any event type to endpoint `$WH` (the `id` from step 1), about a user
that does not exist, with `"test": true` in the envelope:

```sh
curl -s -X POST "$OHS_URL/v1/webhooks/$WH/test" -H "Authorization: Bearer $OHS_KEY" \
  -H "Content-Type: application/json" -d '{"event_type":"data.written"}'
```

| Route | Does |
|---|---|
| `GET /v1/webhooks/{webhook_id}/deliveries?status=parked` | deliveries that ran out of attempts, with the last status and error |
| `GET /v1/webhooks/{webhook_id}/deliveries/{delivery_id}` | the exact payload and each attempt (status code, error, first 1 KiB of your answer) |
| `POST /v1/webhooks/{webhook_id}/deliveries/{delivery_id}/retry` | send a done or parked delivery again |
| `POST /v1/webhooks/{webhook_id}/rotate-secret` | new secret, used from the next request on |
| `PATCH /v1/webhooks/{webhook_id}` with `{"enabled": true}` | turn an endpoint back on after 5 days of failures disabled it |

Deliveries are posted by the `etl` role: an instance split into roles needs outbound network
access from its `etl` processes.

## Related

- [Run Open Health Sync with Docker Compose and HTTPS](docker-compose-https.md)
- [Combine data from several wearables without counting it twice](merge-multiple-wearables.md)
- [Export WHOOP data to CSV or Parquet](whoop-export.md)
- [Outgoing webhooks reference](../webhooks.md)
- [README](../../README.md)
