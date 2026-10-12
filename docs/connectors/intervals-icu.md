# intervals.icu connector

Slug `intervals_icu`. Users connect their own intervals.icu account over OAuth; Open Health
Sync pulls their activities and daily wellness records on a schedule and when intervals.icu
sends a webhook. You register your own intervals.icu OAuth application: nothing is shared
between instances.

intervals.icu is a training-analysis service that many athletes already feed from Garmin
Connect, Wahoo, Zwift, Coros, Suunto, Polar and file uploads. One connection here reaches the
data of all of those, and each workout says which upstream it came from.

Status: available, not yet verified with a real account. OAuth apps are approved by the
intervals.icu developer on request. Tests run against a fake of the intervals.icu API built
from the public documentation and forum guides.

## Before you start: access and limits

- **Applying for an app.** Sign in to intervals.icu as the athlete who will own the app and
  fill in the short form at [intervals.icu/oauth/apply](https://intervals.icu/oauth/apply).
  The app appears under **Settings → Apps** as "Pending" and the OAuth flow does not work
  until it is approved; you get an email and an in-app notice. Approval time is not published.
  Apps listed in the public app directory are a separate request to support@intervals.icu.
- **Personal API keys are not used.** intervals.icu also offers a personal API key (basic
  auth) for a single athlete. This connector uses OAuth only, so any athlete can connect.
- **Activities synced from Strava.** intervals.icu does not hand Strava-sourced activities to
  other apps: the API lists them as stubs without figures, and no webhook fires for them. Such
  activities are skipped. Activities that reach intervals.icu directly (Garmin, Wahoo, Zwift,
  uploads, and so on) are collected.
- **Rate limits.** intervals.icu does not publish its API rate limits **(unverified)**. An
  HTTP 429 defers the connection's job until `Retry-After` (one minute when it is missing).

## 1. Create the OAuth application

1. Apply at [intervals.icu/oauth/apply](https://intervals.icu/oauth/apply) (see above).
2. Once approved, open **Settings → Apps**, then **Manage App**. Add the redirect URI
   `<OHS_PUBLIC_URL>/oauth/intervals_icu/callback`, for example
   `https://ohs.example.org/oauth/intervals_icu/callback` (`ohs doctor` prints it).
3. Copy the **client id** and the **client secret**.
4. For webhooks (optional, see section 5), set the webhook URL to
   `<OHS_PUBLIC_URL>/inbound/intervals_icu` on the same page, tick the activity and wellness
   events, and copy the **webhook secret**.

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_INTERVALS_ICU_CLIENT_ID=...
OHS_INTERVALS_ICU_CLIENT_SECRET=...
# Webhooks (optional, see section 5):
# OHS_INTERVALS_ICU_WEBHOOK_SECRET=...
```

Restart, then run `ohs doctor`:

```
ok    connector intervals_icu
      intervals_icu callback: https://ohs.example.org/oauth/intervals_icu/callback
```

`CLIENT_ID` and `CLIENT_SECRET` are required: with only one set, `ohs doctor` fails and the
connector stays off. `WEBHOOK_SECRET` is optional; without it webhooks are refused (HTTP 401)
and data still arrives with the scheduled pulls. `GET /v1/providers` lists `intervals_icu`
when it is on.

## 3. Scopes

The consent page asks for `ACTIVITY:READ,WELLNESS:READ` (intervals.icu separates scopes with
commas).

| Scope | Gives |
|---|---|
| `ACTIVITY:READ` | the athlete's activities |
| `WELLNESS:READ` | the athlete's daily wellness records (weight, resting heart rate, HRV, sleep, steps, and so on) |

Nothing is ever written to intervals.icu.

## 4. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/intervals_icu \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url`. After consent they land on your
`redirect_uri` with `status=connected`. intervals.icu requires the code to be exchanged
within two minutes, which the callback does at once. The token answer names the athlete
(`athlete.id`), stored as the connection's provider user id so webhooks can be matched to it.

A new connection is backfilled over the last 30 days, then pulled every 6 hours. Requests use
athlete id `0`, which intervals.icu reads as "the athlete of this token".

**Tokens.** intervals.icu issues an access token with no refresh token and no stated expiry;
connecting again replaces the previous token. When the API refuses the token (HTTP 401, as
after the athlete revokes the app) there is nothing to refresh, so the connection becomes
`needs_reauth` and the user must connect again. An HTTP 403 is reported as an error but does
not mark the connection: it can also mean a missing scope **(unverified: how intervals.icu
answers a revoked token)**.

Disconnecting the provider calls `DELETE /api/v1/disconnect-app` with the athlete's token,
best effort, which revokes the app and stops webhook delivery for that athlete.

## 5. Webhooks

An OAuth app has one webhook URL for every athlete who authorized it. intervals.icu POSTs JSON
of this shape:

```json
{
  "secret": "<the app's webhook secret>",
  "events": [
    {"athlete_id": "i123456", "type": "ACTIVITY_UPLOADED", "timestamp": "...", "activity": {"id": "...", "start_date": "..."}}
  ]
}
```

The `secret` is compared, in constant time, with `OHS_INTERVALS_ICU_WEBHOOK_SECRET`; a body
without the right secret is refused with 401. A non-2xx answer makes intervals.icu retry with
exponential backoff. An event is only a change signal: it queues a pull, read with the
athlete's own token, for an athlete with an active connection here, so a forged event cannot
inject data.

| Event | What happens |
|---|---|
| `ACTIVITY_*` (`ACTIVITY_UPLOADED`, `ACTIVITY_ANALYZED`, ...) | a pull of activities from one day before to one day after the activity's start; of the last 7 days when the event names no readable start |
| `WELLNESS_*` | a pull of wellness records around the record's day (`wellness.id`), or of the last 3 days |
| anything else (`CALENDAR_UPDATED`, `SPORT_SETTINGS_UPDATED`, ...) | ignored, answered 200 |

Several events of one athlete and data type in one POST are merged into one pull. Some events
(`ACTIVITY_ANALYZED`) arrive about 60 seconds after the change.

What is documented and what is not: the `secret` plus `events` envelope, `athlete_id`,
`type`, `timestamp`, the `activity` object of activity events, and the event names
`ACTIVITY_UPLOADED`, `ACTIVITY_ANALYZED`, `CALENDAR_UPDATED` and `SPORT_SETTINGS_UPDATED`
come from the intervals.icu API cookbook. The wellness event name and its `wellness` object,
and an activity delete event, are **(unverified)**; any `WELLNESS_*` event triggers a wellness
pull whatever it carries.

## Data produced

### Activities → workouts

Each activity of `GET /api/v1/athlete/0/activities` becomes one workout:

| Workout field | From |
|---|---|
| `activity_type` | `type`, which uses Strava's names: `Run`, `TrailRun`, `VirtualRun` → `running`; `Ride`, `GravelRide`, `MountainBikeRide`, `VirtualRide` → `cycling`; `Swim`, `OpenWaterSwim` → `swimming`; `WeightTraining` → `traditional_strength_training`; and so on. An unknown type is `other`; the intervals.icu name is always in `metadata.intervals_type` |
| `start_date`, `end_date` | `start_date` (UTC), or `start_date_local` in the activity's `timezone`; plus `elapsed_time` (s) |
| `duration_s` | `moving_time` (s), or `elapsed_time` when missing; both are in metadata |
| `distance_m`, `avg_speed_mps`, `elevation_gain_m` | `distance` (m), `average_speed` (m/s), `total_elevation_gain` (m). A zero distance or speed is left empty |
| `avg_heart_rate`, `max_heart_rate` | `average_heartrate`, `max_heartrate` (bpm), when recorded |
| `active_energy_kcal` | `calories` (kcal) when given, otherwise `icu_joules` ÷ 1000, the kilojoules of work shown one to one as kilocalories (the usual convention of power-based tools). `metadata.active_energy_source` says which |
| `metadata.timezone` | `timezone` (an IANA name) |
| `metadata.source_provider` | the upstream the activity came from: `source` `GARMIN_CONNECT` → `garmin`, `WAHOO` → `wahoo`, `ZWIFT` → `zwift`, `COROS`, `SUUNTO`, `POLAR`; any other source lowercased (`upload`, `manual`, ...). The verbatim value is `metadata.intervals_source` **(the full list of `source` values is unverified)** |
| `metadata.device_name` | `device_name`, when given |
| `metadata.indoor` | `trainer`, or a virtual type |

Also in metadata: `icu_average_watts`, `icu_training_load` and `max_speed` (as
`average_watts`, `training_load`, `max_speed_mps`). Not collected: the
activity name and description (free text), streams (heart rate, power, GPS series), laps,
intervals and the original files. Workouts are replaced in place when intervals.icu re-sends
an edited or re-analysed activity (same id).

### Wellness → quantity samples

Each daily record of `GET /api/v1/athlete/0/wellness` (its `id` is the athlete's local date)
gives one sample per figure it holds, spanning that date (in UTC: the record carries no time
zone). All intervals.icu units are metric.

| Wellness field | Quantity | Unit |
|---|---|---|
| `weight` | `body_mass` | kg |
| `restingHR` | `resting_heart_rate` | bpm |
| `hrv` (rMSSD) | `hrv_rmssd` | ms |
| `hrvSDNN` | `hrv_sdnn` | ms |
| `sleepScore` | `sleep_score` | score (0-100) |
| `steps` | `step_count` | count |
| `spO2` | `oxygen_saturation` | percent (a value at or below 1 is read as a fraction and stored × 100) |
| `respiration` | `respiratory_rate` | breaths/min (stored as `bpm`) |
| `bodyFat` | `body_fat_percentage` | percent (same fraction rule) |
| `vo2max` | `vo2_max` | mL/kg/min |
| `systolic`, `diastolic` | `blood_pressure_systolic`, `blood_pressure_diastolic` | mmHg |

A null, missing, zero or negative figure gives no sample. A figure revised later the same day
replaces the stored one. Raw only (kept in the raw store and in the samples' metadata, no
canonical name yet): `sleepSecs` (sleep duration), `sleepQuality`, `avgSleepingHR` and
`kcalConsumed`. Not collected: training load figures (`ctl`, `atl`), mood, soreness, fatigue,
menstrual phase and other subjective fields. The wellness record names no upstream device,
so its samples are attributed to `intervals_icu` itself **(unverified: whether the API
exposes a per-field source)**.

## Limitations

- **Unverified.** The connector has not been run against a real intervals.icu account. The
  OAuth flow, the token answer, scopes, athlete id `0` and the webhook envelope follow the
  intervals.icu forum guides; the field names follow the published API and third-party
  clients. Please report anything that differs.
- **Deleted activities** are not applied: no documented webhook names a deletion, and a pull
  cannot tell a deleted activity from one outside the window.
- **Late uploads.** A scheduled pull asks for activities by start date. An activity uploaded
  more than a few days after it was recorded is picked up by its webhook (which carries its
  start) or by a pull whose window covers its start.
- **Sleep sessions.** intervals.icu gives a nightly duration and score but no bedtime, so no
  sleep session is created.
