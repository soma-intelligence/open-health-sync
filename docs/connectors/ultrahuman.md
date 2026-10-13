# Ultrahuman connector

Slug `ultrahuman`. Users connect their own Ultrahuman account over OAuth 2.0 through the
Ultrahuman **Partnership API**; Open Health Sync then pulls their Ring AIR metrics (and M1 CGM
glucose, when they wear one) once per day of the pull window. You bring your own Partnership API
client: nothing is shared between instances.

Status: **available.** Access is not self-serve: Ultrahuman issues the client ID and secret
through partner onboarding, after reviewing an application. The endpoints and OAuth parameters
below come from Ultrahuman's developer documentation; the shape of the metrics answer comes from
published third-party integrations **(to confirm)**. Points marked **(to confirm)** are the ones
to check first.

## 1. Get your keys

1. Sign in to the developer dashboard at
   [vision.ultrahuman.com/developer](https://vision.ultrahuman.com/developer) with an Ultrahuman
   account.
2. Ask for **Partnership API** access. Ultrahuman describes the API in
   [Accessing the Ultrahuman Partnership API](https://www.ultrahuman.com/blog/accessing-the-ultrahuman-partnership-api/):
   approved apps receive a client ID and a client secret through "partner onboarding". How to
   request it is not documented publicly **(to confirm)**: the dashboard, the Developers section
   of [ultrahuman.com/partners](https://www.ultrahuman.com/partners/), or `support@ultrahuman.com`.
   Third parties report a review of one to two weeks, asking for the use case, the user base and
   how the data is used **(to confirm)**. Read the API agreement linked from the developer docs
   before applying.
3. Register the **redirect URI** printed by `ohs doctor`, which is
   `<OHS_PUBLIC_URL>/oauth/ultrahuman/callback`. Ultrahuman requires an exact match, including
   the scheme.
4. Copy the **client ID** and the **client secret**.

Do not confuse the Partnership API with **UltraSignal**, Ultrahuman's raw-sensor developer kit
(PPG, accelerometer and temperature streams for building algorithms): this connector does not use
it. Some third-party pages call the Partnership API "UltraSignal" too; ask for the Partnership
API by that name.

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_ULTRAHUMAN_CLIENT_ID=...
OHS_ULTRAHUMAN_CLIENT_SECRET=...
```

Restart, then run `ohs doctor`. It reports the connector as enabled and prints the callback URL
to register:

```
ok    connector ultrahuman
      ultrahuman callback: https://ohs.example.org/oauth/ultrahuman/callback
```

Both keys are required: with only one set, `ohs doctor` fails and the connector stays off.
`GET /v1/providers` lists `ultrahuman` when it is on.

## 3. Scopes

The consent page asks for all three scopes the Partnership API documents:

| Scope | Gives |
|---|---|
| `ring_data` | Ring AIR metrics in the metrics endpoint: sleep, heart rate, HRV, temperature, steps, VO2 max, indexes |
| `cgm_data` | M1 CGM metrics in the metrics endpoint: glucose and its daily summaries |
| `profile` | the `user_info` endpoint (user id and time zone). Only the id is kept, as the connection's provider user id |

A user without an M1 patch simply has no glucose entries.

## 4. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/ultrahuman \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url` (`https://auth.ultrahuman.com/authorise`). After
consent, Open Health Sync trades the code for tokens at
`https://partner.ultrahuman.com/api/partners/oauth/token` (client ID and secret in the form, as
documented), then calls `GET /api/partners/v1/user_data/user_info` for the user id. The
`user_info` field names are not published: the connector reads `id` or `user_id`, at the top
level or under `data` **(to confirm)**. A failed lookup does not fail the connect; nothing in a
pull needs the id.

Tokens: the documentation's examples show `expires_in` of about one day (86399 s), while its
prose and the blog say about a week **(to confirm which)**; the connector uses `expires_in`
either way. A refresh (`grant_type=refresh_token`) returns a new refresh token, which replaces
the old one. A refused grant (`invalid_grant`, or HTTP 401 at the token endpoint) marks the
connection `needs_reauth`; refused client credentials (`invalid_client`, `unauthorized_client`)
are reported as an operator error instead, so a mistyped secret does not disconnect every user.

Disconnecting the provider, or deleting the user, revokes the refresh token and then the access
token at `POST /api/partners/oauth/revoke` (best effort; Ultrahuman answers 200 for a token
already invalid). A disconnect keeps the data already collected; add `?delete_data=true` to
erase everything held from Ultrahuman for that user (see
[erasing one provider's data](../api.md#erasing-one-providers-data)).

## 5. What is pulled

The metrics endpoint answers **one date per call**:
`GET https://partner.ultrahuman.com/api/partners/v1/user_data/metrics?date=YYYY-MM-DD` with
`Authorization: Bearer <token>`. A pull walks every date of its window (UTC dates, both ends
included) with a short pause between calls, and stores each date's answer verbatim as one raw
line of data type `daily_metrics`. A date with no metric is not stored. A new connection is
backfilled over the last 30 days in 7 day windows (about 30 calls in all). Since Ultrahuman
sends no notifications, the connection is then pulled every hour; each regular pull covers the
dates since the previous one, usually one or two calls (today's date is asked again until it is
over, and its figures are replaced). How far back Ultrahuman serves data is not documented
**(to confirm)**.

No webhooks are documented, so the connector is **pull only** **(to confirm that none exist)**.

### Mapping

The answer is read as `{"data":{"metric_data":[{"type":..., "object":{...}}, ...]}}`, each
object carrying `day_start_timestamp` (the user's local midnight, Unix seconds) and either a
`values` series of `{value, timestamp}` or a single `value` **(to confirm: the shape published by
third-party integrations, not by Ultrahuman)**.

| Ultrahuman type | Canonical record | Unit | Notes |
|---|---|---|---|
| `hr` | `heart_rate` samples | bpm | one per reading; zero readings skipped |
| `hrv` | `hrv_rmssd` samples | ms | Ultrahuman states its sleep HRV is RMSSD ([HRV study](https://www.ultrahuman.com/science/studies/ultrahuman-ring-hrv-accuracy/)); that the daytime `hrv` series is RMSSD too comes from third parties **(to confirm)** |
| `temp` | `skin_temperature` samples | celsius | absolute skin temperature (`°C`); a Fahrenheit unit is converted; values outside 20 to 45 °C dropped |
| `steps` `values` | `step_count_intraday` samples | count | per-reading counts, zeros skipped; whether they are increments per interval is **(to confirm)** |
| `steps` `total` | `step_count`, one per local day | count | replaced while the day is still running |
| `sleep_rhr` `value`, else `night_rhr` `avg` | `resting_heart_rate`, one per local day | bpm | |
| `temperature_deviation` `value` | `body_temperature_deviation`, one per local day | celsius | deviation from the user's baseline |
| `vo2_max` `value` | `vo2_max`, one per local day | mL/kg/min | |
| `glucose` `values` | `blood_glucose` samples | mg/dL | M1 CGM, about one reading every 5 minutes. mmol/L readings are converted (× 18.0182); without a `unit` the value is read as mg/dL, the unit Ultrahuman's field list gives, and flagged `unit_assumed`. The glucose entry's exact shape is **(to confirm)** |
| `Sleep` | one `SleepSession` per date, with phases | | `bedtime_start`/`bedtime_end`; deep, light, REM and awake seconds from `sleep_stages`; total sleep from `quick_metrics` `total_sleep` (else deep + light + REM); average HRV from `avg_sleep_hrv`; resting HR; average SpO2 from `spo2`; phases from `sleep_graph` intervals (`deep_sleep`, `light_sleep`, `rem_sleep`, `awake`; other kinds dropped) |

**Raw only** (stored in the raw line, no canonical record, because no canonical name exists):
`recovery_index`, `movement_index`, `metabolic_score`, `active_minutes`, `inactive_time`,
`weekly_active_minutes`, `movements`, `morning_alertness`, the CGM daily summaries
(`average_glucose`, `glucose_variability`, `time_in_target`, `hba1c`), the sleep summary
scores, toss-and-turn count and sleep cycles. A re-map after a canonical name is added picks them
up from the stored lines.

**Time zones.** The user's UTC offset for each date is derived from `day_start_timestamp` (local
midnight of that date). Every record carries `utc_offset_minutes`, plus `timezone` as an
`Etc/GMT` zone when the offset is a whole hour. A day without `day_start_timestamp` is read as a
UTC day and flagged `local_date_as_utc`. Timestamps are Unix seconds (a value too large for
seconds is read as milliseconds).

Series samples are hashed on their time, daily figures on their date and the night on its date,
all replaceable: pulling a day again replaces its figures instead of duplicating them.

## 6. Rate limits and errors

Ultrahuman publishes no rate limits **(to confirm)**. On HTTP 429 the pull stops and the
scheduler waits for `Retry-After` (or its default back-off) without counting a failure; the days
already stored stay stored. HTTP 401 or 403 on a data call makes the scheduler refresh the token
once and retry; a second refusal marks the connection `needs_reauth`. 404 or 204 for a date means
no data. Transport errors are retried a few times with a growing, jittered wait.

## 7. Personal API tokens (not supported yet)

Ultrahuman also documents a **personal** API at
[vision.ultrahuman.com/developer-docs](https://vision.ultrahuman.com/developer-docs): an owner
creates an API token in the developer portal and reads
`GET https://partner.ultrahuman.com/api/v1/partner/daily_metrics?date=YYYY-MM-DD` with
`Authorization: <token>` (no `Bearer`). Other users can share their data with a token holder by
entering the holder's "data sharing code" in the Ultrahuman app (Profile, Settings, Partner ID);
the holder then adds `email=<user's email>`. It also accepts `start_epoch`/`end_epoch` ranges of
up to 7 days.

This is the only path open to an individual self-hoster without a partnership, but it does not
fit the connector contract today: connections are created by the OAuth callback, and there is no
way yet to create a connection from an operator-held API key plus a per-user email. It is left as
a follow-up (it needs a core change: a keyed connection type). The mapper already accepts the
answer shape third parties report for this API (`{"data":{"metrics":{"<date>":[...]}}}`, same
entries), so only the fetch side and the connection setup are missing.

## Testing

The tests run against `connectors/ultrahuman/ultrahumanfake`, an `httptest` fake of the token,
revoke, user_info and metrics endpoints answering invented data for a user at UTC+01:00. No test
calls Ultrahuman.

```sh
go test ./connectors/ultrahuman/...
```

To check it against your own account (needs an approved Partnership API client):

1. Connect a Ring AIR user and run a pull; check that each date stored under `daily_metrics`
   holds `data.metric_data`, and compare the entries with the table above.
2. Check what `user_info` returns, and the real `expires_in`.
3. Check the `glucose` entry with an M1 user, in both a mg/dL and a mmol/L region.
4. Check whether the `steps` values are per-interval counts, and whether `hrv` outside sleep is
   RMSSD.
5. Check how a 401 (expired token) and a missing scope are answered, and whether rate limit
   headers are sent.

## Sources (checked 2026-10-12)

- Ultrahuman, [Accessing the Ultrahuman Partnership API](https://www.ultrahuman.com/blog/accessing-the-ultrahuman-partnership-api/)
  (scopes, base URL, bearer tokens, refresh on 401).
- Ultrahuman, [developer docs, OAuth](https://vision.ultrahuman.com/developer-docs?type=oauth)
  (authorize, token, revoke, metrics and user_info endpoints, token answer) and
  [personal API](https://vision.ultrahuman.com/developer-docs) (daily_metrics, field list and units).
- Ultrahuman, [Ring HRV accuracy study](https://www.ultrahuman.com/science/studies/ultrahuman-ring-hrv-accuracy/)
  (sleep HRV is RMSSD).
- Third party: the published documentation of another open-source project's Ultrahuman
  integration (answer shape, glucose units by region, HRV as RMSSD, rotating refresh tokens).
