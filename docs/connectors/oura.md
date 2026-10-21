# Oura connector

Slug `oura`. Users connect their own Oura account over OAuth; Open Health Sync pulls their data
from the Oura Cloud API v2 on a schedule, and on Oura's webhooks when you subscribe to them. You
register your own Oura API application: nothing is shared between instances.

This connector is available. It is checked against Oura's OpenAPI document (version 1.37, see
[Testing](#testing)) and tested against a fake of the API.

## Before you start

- **Personal access tokens are gone.** Oura retired them at the end of 2025. Every user connects
  over OAuth 2.0 through your application.
- **Oura Membership.** Gen3 and later rings need an active Oura Membership for their data to be
  served by the API. Without one, Oura answers HTTP 403 and the pull fails with a clear error
  (see [Errors](#errors)).
- **Approval past 10 users.** A new Oura application works for up to 10 users. Beyond that, Oura
  has to review and approve it.

## 1. Get your keys

1. Sign in to the Oura developer portal (`cloud.ouraring.com`, under API Applications) and
   create a new application. Portal labels change from time to time; the fields below are what
   matters.
2. Set the **redirect URI** to the address `ohs doctor` prints, which is
   `<OHS_PUBLIC_URL>/oauth/oura/callback`. It must match exactly, including the scheme. Give a
   real instance an https `OHS_PUBLIC_URL`.
3. Copy the **client ID** and the **client secret**.

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_OURA_CLIENT_ID=...
OHS_OURA_CLIENT_SECRET=...
# OHS_OURA_VERIFICATION_TOKEN=...   optional, turns webhooks on (see 5)
```

Restart, then run `ohs doctor`. It reports the connector as enabled and prints the callback URL
to register:

```
ok    connector oura
      oura callback: https://ohs.example.org/oauth/oura/callback
```

Both keys are required: with only one set, `ohs doctor` fails and the connector stays off.
`VERIFICATION_TOKEN` is optional; without it webhooks are off and data arrives every 6 hours.
`GET /v1/providers` lists `oura` when it is on.

## 3. Scopes

The consent page asks for:

| Scope | Gives |
|---|---|
| `personal` | the Oura user id, read once at connect (nothing else of the profile is stored) |
| `daily` | daily activity, sleep periods, daily sleep score, daily readiness, daily stress, VO2 max |
| `heartrate` | the heart rate time series |
| `workout` | workouts |
| `session` | meditation, breathing and other guided sessions |
| `spo2Daily` | daily SpO2 averages |

Oura lets the user untick scopes at consent. A data type whose scope was refused answers 403; it
is skipped for that user with a warning in the log, and the other types are still pulled. A user
who connected before the `session` or `spo2Daily` scope was requested gets no sessions or SpO2
until they connect again (earlier versions asked for `spo2`, which is not an Oura scope). Oura's
OpenAPI document does not say which scope daily stress and VO2 max need; `daily` is assumed. If
your users get 403 on them, check the scopes your application may request.

## 4. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/oura \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url`. After consent they land on your `redirect_uri`
with `status=connected`. A new connection is backfilled over the last 30 days in 7 day
windows, then pulled every 6 hours. Oura refresh tokens are single use: the `collect` role
stores the new pair after each refresh.

## 5. Webhooks (optional)

Without webhooks the 6 hour pull collects everything. With them, a change at Oura queues a pull
of that user's last 3 days within seconds, and a sleep period, workout or session the user
deletes is removed here too.

Oura webhook subscriptions belong to your application, not to a user: you create them once, and
Oura then notifies `<OHS_PUBLIC_URL>/inbound/oura` for every user who connected through the
application. Nothing is subscribed when a user connects.

1. Pick a random verification token and set it, then restart:

   ```sh
   OHS_OURA_VERIFICATION_TOKEN=$(openssl rand -hex 24)
   ```

   Without it the webhook routes refuse everything (401).

2. With the instance running and reachable from the internet, create one subscription per data
   type and event type. Oura checks each new subscription with
   `GET /inbound/oura?verification_token=...&challenge=...`, which Open Health Sync answers with
   `{"challenge": "..."}` when the token matches:

   ```sh
   for dt in sleep daily_sleep daily_activity daily_readiness daily_spo2 daily_stress workout session vo2_max; do
     for ev in create update delete; do
       curl -s -X POST https://api.ouraring.com/v2/webhook/subscription \
         -H "x-client-id: $OHS_OURA_CLIENT_ID" -H "x-client-secret: $OHS_OURA_CLIENT_SECRET" \
         -H 'Content-Type: application/json' \
         -d "{\"callback_url\":\"https://ohs.example.org/inbound/oura\",\"verification_token\":\"$OHS_OURA_VERIFICATION_TOKEN\",\"event_type\":\"$ev\",\"data_type\":\"$dt\"}"
       echo
     done
   done
   ```

   A `delete` subscription only matters for `sleep`, `workout` and `session`; you may skip it
   for the daily types. Heart rate has no webhook.

3. Subscriptions expire. List them, and renew each before its `expiration_time`:

   ```sh
   curl -s https://api.ouraring.com/v2/webhook/subscription \
     -H "x-client-id: $OHS_OURA_CLIENT_ID" -H "x-client-secret: $OHS_OURA_CLIENT_SECRET"
   curl -s -X PUT https://api.ouraring.com/v2/webhook/subscription/renew/<id> \
     -H "x-client-id: $OHS_OURA_CLIENT_ID" -H "x-client-secret: $OHS_OURA_CLIENT_SECRET"
   curl -s -X DELETE https://api.ouraring.com/v2/webhook/subscription/<id> \
     -H "x-client-id: $OHS_OURA_CLIENT_ID" -H "x-client-secret: $OHS_OURA_CLIENT_SECRET"
   ```

   Open Health Sync does not renew them for you; a lapsed subscription only means the data
   waits for the next 6 hour pull, and a deletion made meanwhile is missed.

Each notification is signed: `x-oura-signature` is the hex HMAC-SHA256 of the
`x-oura-timestamp` header (Unix seconds) followed by the body, keyed by your client secret.
Oura's document computes it over the serialized JSON body, so the connector accepts the
signature over the body as received or over its compacted form, in upper or lower case hex. A
notification whose signature is missing or wrong, or whose timestamp is more than an hour old
or 5 minutes ahead of the server clock, is refused (401). A notification replayed within that
hour is accepted: what it does (a pull, or a removal) is idempotent.

| Event | Effect |
|---|---|
| `create`, `update` of a data type the connector stores | a pull of the last 3 days for that Oura user |
| `create`, `update` of another type (`tag`, `meal`, `ring_configuration`...) | acknowledged (200), ignored |
| `delete` of a `sleep`, `workout` or `session` | the record of that Oura id is removed (see [updates and deletes](#updates-and-deletes)) |
| `delete` of a daily document, anything else | acknowledged (200), ignored: the next copy of the day replaces a daily document |

Data and event types are read in any case. A notification names the Oura user by the id read at
connect. A connection made while Oura refused the `personal` scope has no Oura user id and gets
no webhook; the scheduled pull still covers it.

Oura retries a notification that is not answered 2xx up to 10 times over about an hour, and
cancels the subscription on HTTP 410. Open Health Sync never answers 410. A refused notification
(401, or 413 above 1 MiB) is retried by Oura and then dropped; the scheduled pull still covers
the data, but not a deletion.

The signature scheme and the challenge follow Oura's OpenAPI document; they have not yet been
checked against a live Oura subscription. If your notifications are refused with a valid
secret, please report it.

## Data produced

| Oura data | Canonical records |
|---|---|
| Daily activity | quantity samples per local day: `step_count`, `active_energy_burned`, `distance_walking_running` (Oura's equivalent walking distance), `moderate_activity_time` and `vigorous_activity_time` (Oura's medium and high activity time, sent in seconds, stored in minutes); the activity score, total calories and time per intensity also in metadata |
| Sleep | a sleep session per sleep period (nights, naps, rest), with its stages from the 5 minute hypnogram (`deep`, `light`, `rem`, `awake`), average HRV, lowest heart rate (as resting heart rate) and average breathing rate. The main sleep of a day also gives `resting_heart_rate` and `respiratory_rate` samples |
| Daily readiness | `readiness_score` (0 to 100), `readiness_hrv_balance` (the HRV balance contributor, an index, not milliseconds), `body_temperature_deviation` (degrees Celsius); the other contributors in metadata |
| Daily SpO2 | `oxygen_saturation`, the night's average in percent; the breathing disturbance index in metadata |
| Heart rate | `heart_rate` samples, with what the ring was doing (`awake`, `rest`, `sleep`, `workout`...) in metadata. Oura sends readings in UTC, so they carry no time zone and are filed under their UTC day |
| Workouts | workouts, with the activity in the canonical vocabulary (`other` when there is no match; Oura's own name in metadata) |
| Daily sleep | `sleep_score` (Oura's 0 to 100 daily sleep score) per local day; the contributors in metadata. It is a daily figure, not written into a sleep session |
| Daily stress | `stress_high_time` and `recovery_high_time`, the minutes of the day Oura classed as high stress and high recovery (sent in seconds); Oura's day summary (`restored`, `normal`, `stressful`) in metadata. A UTC day, as the document has no time |
| Sessions | a `mindful_session` category sample (the record Apple Health's mindful minutes use) per meditation, breathing or relaxation session, with the session type as its label and the mood in metadata. Naps, rest and body status sessions are not stored (a nap also arrives as a sleep period); the heart rate and HRV series of a session stay in the raw store |
| VO2 max | `vo2_max` in mL/kg/min, Oura's estimate for the day (endpoint `vO2_max`) |

Notes:

- **HRV.** Oura's sleep HRV is an RMSSD. It fills the session's HRV column, marked
  `"hrv_method": "rmssd"` in metadata, and is never stored as `hrv_sdnn`.
- **Time zones.** Oura gives a UTC offset, not a zone name. The connector records the offset
  (`utc_offset`) and a zone with that offset at that instant as `timezone`, so a record lands on
  the user's local day: the fixed-offset zone for a whole number of hours (`Etc/GMT-1` for
  +01:00), and a zone of a place that uses the offset otherwise (`Asia/Kolkata` for +05:30,
  `Asia/Kathmandu` for +05:45, `America/St_Johns` for -03:30). The zone name says nothing about
  where the user is. An offset no zone uses falls back to UTC days.
- **Empty and odd records.** A null or missing figure is left out, never stored as 0. A sleep
  period that ends when it starts, and any timestamp outside the years 2000 to 2199, is not
  stored. An unknown sleep type, workout activity or intensity, heart rate source, mood or
  stress summary is kept as Oura sent it in metadata; a session of an unknown type is not
  stored.
- **Days.** A daily document covers the calendar day it names, from local midnight. Oura's own
  activity day starts at 4 am; the figures are Oura's, only the window is the calendar day.

## Updates and deletes

- **Daily documents** (activity, sleep score, readiness, SpO2, stress, VO2 max) change while the
  day is open and after a late ring sync. Their samples are replaceable: the newest value replaces the stored one.
- **Sleep periods and workouts** are identified by their Oura id and start, and are
  replaceable. A re-sent copy is not stored twice: a corrected copy replaces the stored figures
  (a figure it leaves out keeps its stored value), a corrected hypnogram replaces the stored
  sleep phases, and a copy whose start moved replaces the stored session or workout of the same
  Oura id. The `resting_heart_rate` and `respiratory_rate` samples of a night follow the
  corrected copy too, a copy whose start moved included: the samples under the old start are
  replaced, not kept beside the new ones.
- **Deletes.** A sleep period Oura marks `deleted`, or one the user rejected (type `rest`),
  removes the stored session of that Oura id, with its phases and its `resting_heart_rate` and
  `respiratory_rate` samples, and a tombstone keeps any later copy of it from being stored. A
  pull sees such a period as long as Oura still lists it. With webhooks on, a `delete`
  notification of a sleep period, workout or session does the same: the notification is stored
  verbatim as a raw object (data type `webhook`) for every connection of that Oura user, and
  the ETL removes the record, even when the deletion is processed before the record itself.
  Without webhooks, a workout or session deleted at Oura stays. Oura's daily figures are
  samples keyed by the day, not a daily summary row, so a deletion has no summary figure to
  clear.

## Errors

- Rate limits (HTTP 429, on any endpoint) defer the connection's job until `Retry-After`.
- A 500, 502, 503 or 504 answer is retried twice within the pull, with a growing pause. If it
  persists, the job is retried later (at `Retry-After` when Oura sent one).
- If Oura refuses the access token, the core refreshes it once and pulls again. If Oura refuses
  the refresh token (`invalid_grant`), the connection becomes `needs_reauth` and the user must
  connect again. If it refuses your application (`invalid_client`, a wrong client ID or
  secret), no user is marked: the jobs fail with that error until the keys are fixed.
- **Revocation.** Disconnecting a user or deleting them revokes the access token at Oura
  (`GET /oauth/revoke`), so the grant does not outlive the connection. A failed revocation
  is logged and does not stop the deletion.
- **Partial failure.** A data type that fails (after its retries) does not stop the others: the
  pull stores what the other types returned, then fails with the errors, and the job is retried
  later. Stored data is deduplicated, so the retry adds only what was missing. A rate limit, a
  refused token or a failure to store stops the pull at once.
- When every data type answers 403 (typically a lapsed Oura Membership), the pull fails with
  that error and is retried later; nothing is lost once access is back.
- **Windows.** Oura does not document whether `end_date` is inclusive. The connector asks one
  day more than it needs on each side and lets consecutive 30 day windows share their boundary
  day; the copies this gives are deduplicated. Paging stops on a repeated `next_token`, after 3
  empty pages in a row, or after 500 pages.

## Testing

- `go test ./connectors/oura/...` runs the unit, robustness and contract tests against a fake
  of the API. The contract tests check every fixture and every answer of the fake against
  `connectors/oura/testdata/responses.schema.json`, the response schemas, scopes and endpoints
  taken from Oura's OpenAPI document. That document has no licence for redistribution, so only
  the derived structure is committed. To refresh it from a newer copy:

  ```sh
  go test ./connectors/oura/ -run TestContract -oura-openapi /path/to/openapi.json -oura-openapi-version 1.38
  ```

- Fuzz targets (`FuzzMapSleep`, `FuzzHandleInbound`...) replay their seeds and the crashers in
  `testdata/fuzz` on every run. To search for more:
  `go test -run='^$' -fuzz=FuzzMapSleep -fuzztime=30s ./connectors/oura/`.
- `go test -tags integration -run TestEndToEnd ./connectors/oura/` starts the whole app with a
  database (Docker required), connects a user against the fake, and follows the data through
  the scheduled pull, the ETL, the REST API, update and delete webhooks, and a re-pull.
- **Live check** (opt-in, never in CI): with an access token of your own account,

  ```sh
  OHS_OURA_LIVE_TOKEN=... go test -tags live -run TestLive -v ./connectors/oura/
  ```

  reads the last 7 days (`OHS_OURA_LIVE_DAYS`, at most 30), checks each answer against the
  response schemas, and maps every line through the connector. It only reads, and prints counts
  and schema paths, never a value. Get a token by connecting an account to a test instance, or
  through the OAuth flow of your application.
