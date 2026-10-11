# Oura connector

Slug `oura`. Users connect their own Oura account over OAuth; Open Health Sync pulls their data
from the Oura Cloud API v2 on a schedule. You register your own Oura API application: nothing is
shared between instances.

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
```

Restart, then run `ohs doctor`. It reports the connector as enabled and prints the callback URL
to register:

```
ok    connector oura
      oura callback: https://ohs.example.org/oauth/oura/callback
```

Both keys are required: with only one set, `ohs doctor` fails and the connector stays off.
`GET /v1/providers` lists `oura` when it is on.

## 3. Scopes

The consent page asks for:

| Scope | Gives |
|---|---|
| `personal` | the Oura user id, read once at connect (nothing else of the profile is stored) |
| `daily` | daily activity, sleep periods, daily readiness |
| `heartrate` | the heart rate time series |
| `workout` | workouts |
| `spo2` | daily SpO2 averages |

Oura lets the user untick scopes at consent. A data type whose scope was refused answers 403; it
is skipped for that user with a warning in the log, and the other types are still pulled.

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

## 5. Webhooks

Not used yet. Oura can notify an application of new, updated and deleted documents, but its
webhooks do not fit how Open Health Sync handles push today:

- Oura subscriptions belong to the application, not to a user, while the core subscribes and
  unsubscribes per connection.
- Oura verifies a subscription by calling the callback with a `challenge` it expects echoed back
  in the body; the core's `/inbound/{provider}` probe answers with an empty 200.
- A delete event names one Oura document, and the pipeline has no way to remove a stored record
  by its provider id.

The 6 hour pull collects everything in the meantime.

## Data produced

| Oura data | Canonical records |
|---|---|
| Daily activity | quantity samples per local day: `step_count`, `active_energy_burned`, `distance_walking_running` (Oura's equivalent walking distance); the activity score, total calories and time per intensity in metadata |
| Sleep | a sleep session per sleep period (nights, naps, rest), with its stages from the 5 minute hypnogram (`deep`, `light`, `rem`, `awake`), average HRV, lowest heart rate (as resting heart rate) and average breathing rate. The main sleep of a day also gives `resting_heart_rate` and `respiratory_rate` samples |
| Daily readiness | `readiness_score` (0 to 100), `readiness_hrv_balance` (the HRV balance contributor, an index, not milliseconds), `body_temperature_deviation` (degrees Celsius); the other contributors in metadata |
| Daily SpO2 | `oxygen_saturation`, the night's average in percent; the breathing disturbance index in metadata |
| Heart rate | `heart_rate` samples, with what the ring was doing (`awake`, `rest`, `sleep`, `workout`...) in metadata |
| Workouts | workouts, with the activity in the canonical vocabulary (`other` when there is no match; Oura's own name in metadata) |

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

- **Daily documents** (activity, readiness, SpO2) change while the day is open and after a late
  ring sync. Their samples are replaceable: the newest value replaces the stored one.
- **Sleep periods and workouts** are identified by their Oura id and start. A re-sent copy is
  not stored twice. The `resting_heart_rate` and `respiratory_rate` samples of a night follow a
  corrected copy. The session row itself and a workout keep their first version: the pipeline
  does not replace stored sessions or workouts yet, and a copy whose start moved is stored as
  a second session.
- **Deletes.** A sleep period Oura marks `deleted` is not stored. A document deleted after it was
  stored stays stored: the pull only sees what Oura still lists, and the pipeline has no way to
  remove a record by its provider id.

## Errors

- Rate limits (HTTP 429) defer the connection's job until `Retry-After`.
- If Oura refuses the access or refresh token the connection becomes `needs_reauth` and the user
  must connect again.
- When every data type answers 403 (typically a lapsed Oura Membership), the pull fails with
  that error and is retried later; nothing is lost once access is back.
