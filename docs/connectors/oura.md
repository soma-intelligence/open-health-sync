# Oura connector

Slug `oura`. Users connect their own Oura account over OAuth; Open Health Sync pulls their data
from the Oura Cloud API v2 on a schedule, and on Oura's webhooks when you subscribe to them. You
register your own Oura API application: nothing is shared between instances.

This connector is available but has not yet been verified with a real Oura account. It was built
from Oura's published API documentation and is tested against a fake of the API.

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
| `spo2` | daily SpO2 averages |

Oura lets the user untick scopes at consent. A data type whose scope was refused answers 403; it
is skipped for that user with a warning in the log, and the other types are still pulled. A user
who connected before the `session` scope was requested gets no sessions until they connect
again. Which scope Oura requires for daily stress and VO2 max is not stated clearly in its
documentation; if your users get 403 on them, check the scopes your application may request.

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
`x-oura-timestamp` header followed by the raw body, keyed by your client secret. A notification
whose signature is missing or wrong, or whose timestamp is more than an hour old or 5 minutes
ahead of the server clock, is refused (401).

| Event | Effect |
|---|---|
| `create`, `update` of any data type | a pull of the last 3 days for that Oura user |
| `delete` of a `sleep`, `workout` or `session` | the record of that Oura id is removed (see [updates and deletes](#updates-and-deletes)) |
| `delete` of a daily document, anything else | acknowledged (200), ignored: the next copy of the day replaces a daily document |

A notification names the Oura user by the id read at connect. A connection made while Oura
refused the `personal` scope has no Oura user id and gets no webhook; the scheduled pull still
covers it.

The signature scheme and the challenge come from Oura's webhook documentation as published by
third parties; this part has not been checked against a live Oura subscription. If your
notifications are refused with a valid secret, please report it.

## Data produced

| Oura data | Canonical records |
|---|---|
| Daily activity | quantity samples per local day: `step_count`, `active_energy_burned`, `distance_walking_running` (Oura's equivalent walking distance); the activity score, total calories and time per intensity in metadata |
| Sleep | a sleep session per sleep period (nights, naps, rest), with its stages from the 5 minute hypnogram (`deep`, `light`, `rem`, `awake`), average HRV, lowest heart rate (as resting heart rate) and average breathing rate. The main sleep of a day also gives `resting_heart_rate` and `respiratory_rate` samples |
| Daily readiness | `readiness_score` (0 to 100), `readiness_hrv_balance` (the HRV balance contributor, an index, not milliseconds), `body_temperature_deviation` (degrees Celsius); the other contributors in metadata |
| Daily SpO2 | `oxygen_saturation`, the night's average in percent; the breathing disturbance index in metadata |
| Heart rate | `heart_rate` samples, with what the ring was doing (`awake`, `rest`, `sleep`, `workout`...) in metadata |
| Workouts | workouts, with the activity in the canonical vocabulary (`other` when there is no match; Oura's own name in metadata) |
| Daily sleep | `sleep_score` (Oura's 0 to 100 daily sleep score) per local day; the contributors in metadata. It is a daily figure, not written into a sleep session |
| Daily stress | `stress_high_time` and `recovery_high_time`, the minutes of the day Oura classed as high stress and high recovery (sent in seconds); Oura's day summary (`restored`, `normal`, `stressful`) in metadata. A UTC day, as the document has no time |
| Sessions | a `mindful_session` category sample (the record Apple Health's mindful minutes use) per meditation, breathing or relaxation session, with the session type as its label and the mood in metadata. Naps, rest and body status sessions are not stored (a nap also arrives as a sleep period); the heart rate and HRV series of a session stay in the raw store |
| VO2 max | `vo2_max` in mL/kg/min, Oura's estimate for the day (endpoint `vO2_max`) |

Notes:

- **HRV.** Oura's sleep HRV is an RMSSD. It fills the session's HRV column, marked
  `"hrv_method": "rmssd"` in metadata, and is never stored as `hrv_sdnn`.
- **Time zones.** Oura gives a UTC offset, not a zone name. The connector records the offset
  (`utc_offset`) and, when it is a whole number of hours, the matching fixed-offset zone as
  `timezone` (`Etc/GMT-1` for +01:00), so a record lands on the user's local day. A half hour
  offset falls back to UTC days.
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
  corrected copy too.
- **Deletes.** A sleep period Oura marks `deleted`, or one the user rejected (type `rest`),
  removes the stored session of that Oura id, with its phases and its `resting_heart_rate` and
  `respiratory_rate` samples, and a tombstone keeps any later copy of it from being stored. A
  pull sees such a period as long as Oura still lists it. With webhooks on, a `delete`
  notification of a sleep period, workout or session does the same: the notification is stored
  verbatim as a raw object (data type `webhook`) for every connection of that Oura user, and
  the ETL removes the record, even when the deletion is processed before the record itself.
  Without webhooks, a workout or session deleted at Oura stays.

## Errors

- Rate limits (HTTP 429) defer the connection's job until `Retry-After`.
- If Oura refuses the access or refresh token the connection becomes `needs_reauth` and the user
  must connect again.
- When every data type answers 403 (typically a lapsed Oura Membership), the pull fails with
  that error and is retried later; nothing is lost once access is back.
