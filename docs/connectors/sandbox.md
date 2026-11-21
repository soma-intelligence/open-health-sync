# Sandbox provider

Slug `sandbox`. A provider that does not exist, for evaluating Open Health Sync: it runs the
complete OAuth connect flow (consent page, authorization code, token exchange, refresh) against
an authorization server and a data API built into the instance, and serves synthetic sleep,
heart rate, steps and workouts. You can connect a user, watch the backfill, the scheduled pulls
and the token refresh, and read the data back through every API, in the first minutes and
without a developer account at any vendor.

Status: available, **off by default**. Never turn it on where real users connect.

## Turn it on

In `.env`:

```sh
OHS_SANDBOX_ENABLED=true
# Optional: the key that signs the sandbox's codes and tokens.
# OHS_SANDBOX_SECRET=a-long-random-string
```

Restart. `GET /v1/providers` lists `sandbox`, and the hosted connection page offers it.

It is never on unless `OHS_SANDBOX_ENABLED` says so. Turning it on automatically when no real
provider is configured was considered and refused: an instance with no vendor keys yet, or one
that only takes Apple Health and Health Connect uploads, can already hold real users, and
invented records next to theirs would be a mistake nobody chose. An explicit setting is a
decision someone made, visible in the configuration. The sandbox takes no tenant keys: only the
operator switches it.

Without `OHS_SANDBOX_SECRET` a built-in key signs the codes and tokens. That is enough: a sandbox
token only ever unlocks invented data, and the core's single-use OAuth state still protects
every connection. Set the secret if you want tokens from one sandbox instance refused by
another.

## Connect a user

Exactly as for a real provider (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/sandbox \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
# {"authorize_url":"https://ohs.example.org/inbound/sandbox?client_id=ohs-sandbox&redirect_uri=...&response_type=code&scope=...&state=..."}
```

Open the `authorize_url` in a browser. The consent page says in plain words that this is not a
real provider, and offers one **Allow** link per invented person (the
[demo personas](../demo.md): `mix`, `runner`, `sleep`, `weight`) and a **Deny** link. Allow sends
the browser to `<OHS_PUBLIC_URL>/oauth/sandbox/callback` with a code and the state, the core
trades the code at the sandbox token endpoint, stores the sealed tokens, and lands the user on
your `redirect_uri` with `status=connected`. Deny comes back with `status=error`.

Then everything is the ordinary path: a 30 day backfill, a pull every hour, and the read API.

| Step | What the sandbox does |
|---|---|
| Consent page | `GET /inbound/sandbox?response_type=code&...`, served by the instance (connectors cannot mount routes of their own; this provider-facing route hands a GET to the connector). The `redirect_uri` must be this instance's sandbox callback, on the host the page is served from |
| Token endpoint | `authorization_code` and `refresh_token` grants, with client id `ohs-sandbox`. Codes last 10 minutes and are bound to their `redirect_uri`; access tokens last 1 hour, so the core refreshes them within the first day; refresh tokens last 30 days |
| Data API | `GET /v1/sleep`, `/v1/heart_rate`, `/v1/steps`, `/v1/workouts` with `start` and `end` (RFC 3339, at most 31 days apart), bearer access token |
| Notifications | none: a POST to `/inbound/sandbox` is answered 401 |

The token endpoint and the data API are called in process, through an HTTP client whose
transport serves the sandbox handler directly: requests, answers, status codes and refusals are
real HTTP, and nothing listens on a port. Codes and tokens are signed, not stored, so any
replica accepts what another issued, before or after a restart.

## Data produced

The data comes from the demo generator (`internal/demo`, the one behind `ohs seed --demo`). It
is deterministic: the same persona and dates give the same records on every pull and every
instance, so pulls duplicate nothing. Only complete days exist, up to yesterday in the persona's
zone (Europe/Paris), so last night's sleep appears once the day is over.

| Raw data type | Records |
|---|---|
| `sleep` | a sleep session with stages (awake, light, deep, rem), total and per-stage durations, sleep score, HRV, resting heart rate, respiratory rate |
| `heart_rate` | `heart_rate` samples (bpm) |
| `steps` | `step_count` samples over intervals |
| `workouts` | workouts with activity type, duration, distance, energy, average and maximum heart rate |

For each kind, the sandbox uses the first of the persona's demo sources that has it, so nothing
is served twice.

## Never confusable with real data

- The provider of every record is `sandbox`; samples and workouts also have the source slug
  `sandbox`, and samples the source name `Sandbox (synthetic)`.
- Every record's metadata holds `"synthetic": true`, the same flag `ohs seed --demo` sets, and
  `"sandbox_persona"`.
- The provider user id of a connection reads `sbx-<persona>-<hex>`, and every token starts with
  `sbx_`.

To remove it all, disconnect with `DELETE /v1/users/{id}/connections/sandbox?delete_data=true`,
then unset `OHS_SANDBOX_ENABLED`.
