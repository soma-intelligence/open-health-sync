# API guide

[`api/openapi.yaml`](../api/openapi.yaml) is the source of truth: every route, parameter and
response shape. This guide explains how the pieces fit. Examples assume
`OHS=http://localhost:8080` and a tenant key in `KEY`.

A running server serves the description of its own version at `GET /openapi.yaml`, without
credentials and readable from any origin. Import `$OHS/openapi.yaml` by URL into Postman
(Import, Link), Insomnia or [editor.swagger.io](https://editor.swagger.io) (File, Import URL),
or render a local page with `npx @redocly/cli build-docs $OHS/openapi.yaml`. The server has no
documentation page of its own, so it never loads third party scripts next to health data.

## Credentials

Every call carries `Authorization: Bearer <secret>`.

| Secret | Looks like | Used by | Calls |
|---|---|---|---|
| Tenant API key | `ohs_sk_` + 43 characters | your server | tenant routes: users, tokens, connections, reads |
| User token | `ohs_ut_` + 43 characters | one end user's app | user routes: uploads (`upload`), reads over MCP (`read`) |

The first key comes from `ohs admin tenant create --name <name>` (or `OHS_BOOTSTRAP_TENANT`,
which logs a key once on first start). Both secrets are stored as SHA-256 hashes: a lost one
cannot be shown again. The connection routes (`/v1/users/{id}/connections...`) take the tenant key. The `connect` scope
exists but no route uses it yet.
A tenant key on a user route, or a user token on a tenant route, gets 403.

A user token is minted for one active user with `POST /v1/users/{id}/tokens`:

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/tokens -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"scopes":["upload","connect"],"ttl_seconds":3600}'
# {"token":"ohs_ut_...","expires_at":"...","scopes":["upload","connect"]}
```

The lifetime is one hour by default and at most 24 hours. A token whose only scope is `read`
may live up to 30 days (2592000 seconds) so a person can put it in their own agent
configuration. The response is `Cache-Control: no-store`; the token is shown once.

## Conventions

- **JSON and time.** Bodies are JSON. Times are RFC 3339 in UTC. Values are in the storage
  unit of their quantity name, see [data-model.md](data-model.md).
- **Lists** answer `{"data": [...], "next_cursor": "..." | null}`, oldest first. Pass
  `next_cursor` back as `cursor` until it is null. `limit` defaults to 100 and is at most 1000;
  a larger value is a 400.
- **Time windows** (`start`, `end`) are half open: `start <= time < end`. Both take an RFC 3339
  time or a `YYYY-MM-DD` date (midnight UTC). `end` must not be before `start`.
- **Errors** are RFC 9457 `application/problem+json`:
  `{"type":"about:blank","title":"Bad Request","status":400,"detail":"..."}`.
  A user, workout or sleep session of another tenant is a 404, never a 403. A 401 carries
  `WWW-Authenticate: Bearer`.
- **Request ids.** Every response carries `X-Request-Id`, and a problem body repeats it as
  `request_id`. Send your own `X-Request-Id` (1 to 128 letters, digits, `-`, `_`, `.`) to have
  it kept, for example your proxy's. Every log line about the call carries it as `request_id`,
  so quote it when you ask the operator about a failed call.
- **Idempotency.** Send `Idempotency-Key` (1 to 255 characters) on `POST /v1/users`. A retry with the same key and request within
  24 hours gets the first response back with `Idempotent-Replayed: true`. The same key with a
  different request is a 422; a retry while the first call still runs is a 409. Other routes
  ignore the header (token minting would have to store the token, and each connect call mints a
  fresh state); uploads are idempotent by their own headers.

## Users

```sh
curl -s -X POST $OHS/v1/users -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"external_ref":"customer-42"}'
```

`external_ref` is your own identifier, unique per tenant (a second user with the same one is a
409); it may be omitted. `GET /v1/users` pages through the tenant's users, `GET /v1/users/{id}`
returns one.

`DELETE /v1/users/{id}` answers 202 with a `job_id`. The user's status becomes `deleting` at
once: reads return 404 and the user's tokens stop working, so uploads get 401 (410 only for an
upload whose token was accepted just before the deletion started). A stored `Idempotency-Key`
response that created the user is deleted with them. A background job then revokes provider connections (best
effort), deletes the user's raw files and every row, then the user.

`GET /v1/deletions/{job_id}` reads that job with the same tenant key (another tenant's job is a
404). Poll it until `status` is `done`:

```json
{
  "job_id": "5f0c2b8e-8d1a-4c3e-9a57-0b6f1d2e3a4b",
  "user_id": "0d9e7c1a-2b3f-4e5d-8a6b-7c8d9e0f1a2b",
  "status": "done",
  "created_at": "2026-03-01T14:31:02Z",
  "finished_at": "2026-03-01T14:31:06Z"
}
```

`status` is `pending`, `running`, `done` or `failed`; `finished_at` is null until the job ends.

## Connecting a provider

`GET /v1/providers` lists the connectors enabled on this instance and how they work:
`oauth` (the server pulls), `push` (the provider notifies), `device` (an SDK uploads).

For an OAuth provider, your server asks for a consent URL and sends the user there:

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/withings -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"redirect_uri":"https://app.example.org/connected"}'
# {"authorize_url":"https://account.withings.com/oauth2_user/authorize2?..."}
```

After consent the provider calls `GET /oauth/{provider}/callback` on Open Health Sync, which
sends the user to `redirect_uri?status=connected&provider=withings` (`status=error` on failure).
Without a `redirect_uri` the user sees a small built-in result page. `redirect_uri` must be an
absolute https URL (http is allowed for localhost). The `state` of a consent link is single use
and expires after 10 minutes.

`GET /v1/users/{id}/connections` shows each connection's `status` (`active`, `needs_reauth`,
`disconnected`), `last_sync_at` and `initial_backfill_at`. A connection in `needs_reauth` needs
the user to go through consent again. `DELETE /v1/users/{id}/connections/{provider}` revokes the
grant (best effort) and stops pulling; data already collected is kept.

`GET /v1/users/{id}/freshness` says how up to date each source of the user is, Apple Health
uploads included: `synced_through`, `lag_seconds`, the last provider notification, the next
pull, the last error, and a `state` (`fresh`, `stale`, `failing`, `syncing`, `needs_reauth`,
`disconnected`). Show it in your app, or alert on it. See [background sync](background-sync.md).

## Uploading device data

`POST /v1/ingest/apple-health` takes one gzip NDJSON part with a user token that has the `upload`
scope. The contract (headers, size, idempotency) is in
[connectors/apple-health.md](connectors/apple-health.md). `POST /v1/ingest/health-connect` takes
Android Health Connect records the same way; its line format is in
[connectors/health-connect.md](connectors/health-connect.md). Both routes share the limit of
uploads in flight.

## Reading data

All reads are `GET /v1/users/{id}/<resource>` with a tenant key, paged as above.

| Resource | Filters | Ordered by |
|---|---|---|
| `samples` | `type` (required), `start`, `end` | `start_date` |
| `category-samples` | `type` (optional), `start`, `end` | `start_date` |
| `sleep` | `start`, `end` | `bedtime_start` |
| `sleep/{session_id}/phases` | `start`, `end` | `start_date` |
| `workouts` | `type` (activity, optional), `start`, `end` | `start_date` |
| `workouts/{workout_id}/route` | `start`, `end` | `timestamp` |
| `nutrition` | `start`, `end` | `start_date` |
| `daily` | `start`, `end` (compared as dates) | `date` |
| `coverage` | `provider`, `start`, `end` | not paged |

`samples` answers 400 when `type` is not a quantity type of the catalog (a typo such as
`heartrate`), so a wrong name is not mistaken for missing data. The names are listed in
[data-model.md](data-model.md) and by the MCP tool `list_quantity_types`.

```sh
curl -s "$OHS/v1/users/$USER_ID/samples?type=heart_rate&start=2026-03-01&end=2026-03-02&limit=500" \
  -H "Authorization: Bearer $KEY"
```

```json
{
  "data": [
    {
      "id": 1,
      "type": "heart_rate",
      "value": 72.5,
      "unit": "bpm",
      "start_date": "2026-03-01T14:30:00Z",
      "end_date": "2026-03-01T14:30:00Z",
      "source_name": "Apple Watch",
      "source_bundle_id": "com.apple.health.ABC",
      "device_model": "Watch",
      "product_type": "Watch7,1",
      "external_id": "",
      "is_primary_source": false,
      "was_user_entered": false,
      "metadata": {
        "device": "Apple Watch",
        "timezone": "Europe/Paris",
        "HKTimeZone": "Europe/Paris",
        "sourceName": "Apple Watch",
        "original_unit": "count/min",
        "original_value": 72.5,
        "sourceBundleId": "com.apple.health.ABC"
      },
      "provider": "apple_health",
      "source": "apple_health",
      "device": "Apple Watch",
      "ingested_at": "2026-03-01T14:31:02.418Z"
    }
  ],
  "next_cursor": null
}
```

This is the heart rate line of [connectors/apple-health.md](connectors/apple-health.md#line-formats)
read back. `metadata` keeps the provider fields as sent, plus `timezone` and the value and unit
before conversion.

Every record carries its provenance: `provider` (the integration it came through), `source` (the
vendor that produced it, when known), `device` and `ingested_at`. The quantity names and their
units are listed in [data-model.md](data-model.md). `daily` holds one row per provider and local
calendar day; the ETL derives step, energy and distance totals from samples when a provider sends
no daily summary of its own.

### Knowing when an upload is processed

Processing is asynchronous. `GET /v1/users/{id}/coverage` returns record counts per provider and
local day, `pending_jobs` (work not finished yet) and `parked_jobs` (work that exhausted its
retries and needs an operator). After an upload, poll until `pending_jobs` is 0. A server that
would rather not poll can subscribe to the `data.written` [webhook](webhooks.md). Coverage is not
paged: at most 10000 days, the earliest, with `truncated: true` when more matched.

A finished job can still have dropped some lines: a value out of range, a missing `startDate`,
a unit that does not convert. `rejected_records` counts the user's lines in that state (all
providers and days, not narrowed by the filters), each line once even if the object was
processed again; an object that failed as a whole, with no line singled out, counts as one. A
line the server later recovers, after a fix, stops counting. A non-zero value after `pending_jobs` reaches 0 means some uploaded lines were
not stored; an operator finds each line and its reason in `ohs_data.failed_records`.

## FHIR R4

`GET /v1/users/{id}/fhir/Observation` serves the same quantity samples, blood pressure readings
(a systolic and a diastolic sample paired into one panel), sleep sessions, workouts and daily
summaries as FHIR R4 Observations, in one `searchset` Bundle (`application/fhir+json`) in time
order, paged by its `next` link, and `GET /v1/users/{id}/fhir/Observation/{observation_id}` reads
one back. Filters: `type`, `code` (`http://loinc.org|8867-4`), `category` (`vital-signs`,
`laboratory`, `activity`), `start`, `end`. The mapping, the profiles met and the limits are in
[fhir.md](fhir.md).

```sh
curl -s "$OHS/v1/users/$USER_ID/fhir/Observation?type=heart_rate&start=2026-03-01&limit=50" \
  -H "Authorization: Bearer $KEY"
```

## Open mHealth

`GET /v1/users/{id}/omh/data-points` serves heart rate, steps, blood pressure, body
measurements, vital signs, blood glucose, active energy, sleep sessions and workouts as
[Open mHealth](https://www.openmhealth.org/) data points (a `header` and a `body` that follows one
schema of the catalog, checked against the official schemas in CI), in the order of the FHIR
search, paged like every list. Filters: `schema` (`omh:heart-rate`), `start`, `end`. The mapping
and its limits are in [open-mhealth.md](open-mhealth.md).

```sh
curl -s "$OHS/v1/users/$USER_ID/omh/data-points?schema=omh:blood-pressure&start=2026-03-01" \
  -H "Authorization: Bearer $KEY"
```

## Exports

`GET /v1/users/{id}/exports/{dataset}` streams every record of one list (`samples`,
`category-samples`, `sleep`, `workouts`, `nutrition` or `daily`) in `[start, end)` as one CSV
(`format=csv`, the default) or Parquet (`format=parquet`) file, with the columns of the list's
JSON records. Not paged. See [exports.md](exports.md).

```sh
curl -sOJ "$OHS/v1/users/$USER_ID/exports/samples?format=parquet&start=2026-01-01" \
  -H "Authorization: Bearer $KEY"
```

## Webhooks

Rather than polling coverage, register an endpoint with `POST /v1/webhooks` and be told when a
sync writes or deletes records, a user is erased or a connection needs the user to sign in
again. Requests are signed per Standard Webhooks and retried for about 28 hours. Events,
signature verification and the routes: [webhooks.md](webhooks.md).

## MCP

`POST /mcp` speaks the Model Context Protocol for AI agents, read only. See [mcp.md](mcp.md).

## Provider-facing and operational routes

| Route | Purpose |
|---|---|
| `GET /oauth/{provider}/callback` | OAuth redirect target you register with the provider |
| `POST /inbound/{provider}` | Provider push notifications, verified with the provider's own scheme |
| `GET /healthz` | Liveness |
| `GET /readyz` | Readiness: database ping and raw store check, 503 when either fails |
| `GET /metrics` | Prometheus text format; absent when `OHS_METRICS=false` |
| `GET /console` | Read-only admin console (HTML), only when `OHS_CONSOLE=true`; see [console.md](console.md) |

`/metrics` is unauthenticated and global to the instance (not per tenant). Keep it behind your
reverse proxy or turn it off, see [self-hosting.md](self-hosting.md#operating-notes).
