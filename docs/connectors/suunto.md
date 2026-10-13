# Suunto connector

Slug `suunto`. Users connect their own Suunto app account over OAuth. Open Health Sync pulls
their workouts from the Suunto Cloud API on a schedule, and again when Suunto sends a signed
notification. You need your own access to the Suunto Cloud API: nothing is shared between
instances.

Status: available. **Partner access required**: Suunto
gives API access only to companies and organizations accepted into its partner program, not
to individuals. Tests run against a fake of the Suunto API built from the public
documentation listed at the end.

Statements that no public Suunto page confirms are marked **(to confirm)**.

## Before you start: getting access

- **Partner program.** Access to the Suunto Cloud API goes through the
  [Suunto partner program](https://www.suunto.com/welcomepartners). Companies and
  organizations that build tools, apps or services, commercial or not, can apply. Suunto
  does not give API access for personal use ([FAQ](https://apizone.suunto.com/faq)). Apply
  with the form linked from that page and choose the Suunto Cloud API. Suunto says it answers
  within about two weeks. Questions go to `partners@suunto.com`.
- **Agreement.** Choosing the Cloud API means signing Suunto's API agreement. Its text is not
  public. Read it before you sign, and check that your use of Open Health Sync fits it.
- **Your own credentials.** Each operator applies for its own access. This repository ships
  no Suunto credentials.
- **Cost.** The [API Zone home page](https://apizone.suunto.com/) says Suunto does not charge
  for API use.
- **Rate limits.** The Development API includes every endpoint but has lower request limits.
  The Production API comes after you submit your app details. Suunto does not publish the
  figures. A third party reports 10 calls per minute and 200 calls per week for the
  Development API (**to confirm**). The FAQ asks partners to avoid unnecessary polling.
  Section 6 lists what each pull costs.

## 1. Set up the API Zone application

Once Suunto accepts you, it invites the developers you listed to
[API Zone](https://apizone.suunto.com/). There:

1. Subscribe to the **Development API** (later, the **Production API**). The subscription has a
   primary and a secondary **subscription key**. Either works, and having two lets you rotate
   a key without downtime.
2. Configure the OAuth application on your profile: the app name, a **client secret** you
   choose, and the **redirect URI**. Set it to `<OHS_PUBLIC_URL>/oauth/suunto/callback`, for
   example `https://ohs.example.org/oauth/suunto/callback` (`ohs doctor` prints it). The
   **client ID** is generated for you.
3. Set the **notification URL** to `<OHS_PUBLIC_URL>/inbound/suunto` and note the
   notification signing secret (**to confirm**: where API Zone shows the notification URL and
   secret is behind the partner login). Notifications are optional; see section 5.

API Zone allows one account per application and one email address per account (FAQ).

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_SUUNTO_CLIENT_ID=...
OHS_SUUNTO_CLIENT_SECRET=...
OHS_SUUNTO_SUBSCRIPTION_KEY=...
# Notifications (optional, see section 5):
# OHS_SUUNTO_NOTIFICATION_SECRET=...
```

Restart, then run `ohs doctor`. It reports the connector as enabled and prints the callback URL:

```
ok    connector suunto
      suunto callback: https://ohs.example.org/oauth/suunto/callback
```

`CLIENT_ID`, `CLIENT_SECRET` and `SUBSCRIPTION_KEY` are required. If any of them is missing,
the connector stays off. `NOTIFICATION_SECRET` is optional. Without it, notifications are
refused (HTTP 401) and data still arrives with the scheduled pulls. `GET /v1/providers`
lists `suunto` when the connector is on.

## 3. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/suunto \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url`
(`https://cloudapi-oauth.suunto.com/oauth/authorize`). They sign in with their Suunto app
account and agree. Suunto grants its default `workout` scope, so the connector sends no
`scope` parameter. After consent the user lands on your `redirect_uri` with
`status=connected`. A new connection is backfilled over the last 30 days, then pulled on
the regular schedule.

How the OAuth calls work:

- **Token endpoint.** `POST https://cloudapi-oauth.suunto.com/oauth/token`, with the client ID
  and secret in HTTP Basic auth. It handles `grant_type=authorization_code` (with `code` and
  `redirect_uri`) and `grant_type=refresh_token`. Suunto's example answer has
  `expires_in: 86400`, so access tokens last 24 hours. They are refreshed before they expire.
  If Suunto refuses the refresh token (HTTP 401, or 400 `invalid_grant`), the connection
  becomes `needs_reauth` and the user must connect again.
- **Provider user id.** The access token is a JWT. Its `user` claim holds the Suunto
  username, which the connector stores as the provider user id. Notifications name the user
  by this username. If the token is not a readable JWT, the connector uses a top-level `user`
  field of the token answer instead (**to confirm** that Suunto sends one).
- **No signature check on the token.** The connector reads the JWT without verifying its
  signature. The token comes straight from Suunto's token endpoint over TLS, in answer to a
  request signed with your client secret, so we already know where it came from. Suunto
  publishes no key to verify it with. The claim is only used to match notifications to a
  connection, never to grant access.
- **Data API.** Every request to `https://cloudapi.suunto.com` sends the user's token as
  `Authorization: Bearer <token>` and your subscription key in the `Ocp-Apim-Subscription-Key`
  header.
- **Disconnect.** Disconnecting calls
  `GET https://cloudapi-oauth.suunto.com/oauth/deauthorize?client_id=...` with the user's token,
  best effort.

## 4. What is pulled

| Data type | Endpoint | Stored as |
|---|---|---|
| `workout` | `GET /v3/workouts?since=<ms>&until=<ms>&limit=100&offset=<n>` | Mapped to workouts (below) |
| `sleep` | `GET /247samples/sleep?from=<ms>&to=<ms>` | Raw only |
| `recovery` | `GET /247samples/recovery?from=<ms>&to=<ms>` | Raw only |
| `daily_activity` | `GET /247/daily-activity-statistics?startdate=...&enddate=...` (daily steps and energy, energy in joules) | Raw only |

- **Workouts.** The list is paged with `limit` and `offset`. A page shorter than `limit` is
  the last. Suunto's reference says `since` and `until` select workouts by last
  modification, so every workout in the answer is kept, even one that started before the
  window. Each is filed under the UTC day it started. The older `/v2/workouts` API is marked
  deprecated and is not used.
- **24/7 data.** Suunto documents these endpoints, their parameters and a 28-day maximum
  window, but not the fields of the answers. The connector stores each answer as is, in
  windows of at most 28 days: one raw line per element of an array answer, or one line for
  an object. It maps nothing from them yet, because Open Health Sync never guesses a field's
  unit. Once the schema is known, these raw objects can be mapped
  by reprocessing. If one of these endpoints answers with an error (for example, one your
  subscription does not include), it is logged and skipped, and workouts still arrive.
- **Not fetched.** FIT exports (`/v3/workouts/{key}/fit`: samples, laps, GPS), the
  per-workout detail, and the 24/7 activity samples (`/247samples/activity`). FIT decoding
  is out of scope, and each of these costs one request per workout or day.

## 5. Notifications

Suunto sends notifications to one URL per application, for every user who authorized it:
`<OHS_PUBLIC_URL>/inbound/suunto`. Suunto expects an answer within 2 seconds (third-party
report, **to confirm**). The route only queues a pull, so it answers right away.

Each notification is a POST. The `X-HMAC-SHA256-Signature` header carries the HMAC-SHA256 of
the raw body, keyed with the notification secret. Header name and algorithm come from a
third-party integration (**to confirm**), and so does the digest encoding: the connector
accepts hex in either case and base64. With `OHS_SUUNTO_NOTIFICATION_SECRET` unset, every
notification is refused. A notification never carries data into the store. It only queues
a pull for a user who has an active connection here, read with that user's own token.

| Notification | What happens |
|---|---|
| `{"type":"WORKOUT_CREATED","username":"...","workout":{"workoutKey":"...","startTime":<ms>}}` | A workout pull, from 2 hours before the workout's start (at most 7 days back, at least 24 hours) until now |
| `username` and `workoutid` only, as a form or as JSON (the shape the API Zone FAQ describes) | A workout pull of the last 24 hours |
| `SUUNTO_247_SLEEP_CREATED`, `SUUNTO_247_RECOVERY_CREATED`, `SUUNTO_247_ACTIVITY_CREATED` | A pull of that 24/7 type over the last 48 hours (stored raw only). The FAQ says activity data has no notifications; third-party code handles these types (**to confirm**) |
| Any other type (`ROUTE_CREATED` and others) | Ignored, answered 200 so Suunto does not retry |
| Missing or wrong signature, no `username`, a body that is not JSON or a form | Refused with 401 |

A notification pulls only the data type it names, so it costs one request (or one per page).

## 6. Rate limits

- An HTTP 429 defers the connection's job until the time in `Retry-After`, or by one minute
  when the header is missing.
- When the call quota is spent, the API gateway answers HTTP 403 "Out of call volume quota.
  Quota will be replenished in hh:mm:ss". The connector defers the job by that delay, capped
  at 24 hours.
- An HTTP 401 that names the subscription key is a configuration error. The pull fails,
  but the connection is **not** marked `needs_reauth`: fix `OHS_SUUNTO_SUBSCRIPTION_KEY`.
  Any other 401 means the user's token was refused, and the connection needs to be
  reconnected.

Cost of one scheduled pull: one workout request per 100 workouts, plus one request for each
24/7 type per 28 days of window, so 4 requests for a normal pull. At the reported
Development API limits (**to confirm**), that is enough for testing with a handful of users,
not for production.

## Data produced

Each workout summary becomes one workout:

| Workout field | From |
|---|---|
| `activity_type` | `activityId`, mapped to the activity names the other connectors use: 1 Running, 22 Trail running, 53 Treadmill → `running`; 2 Cycling, 10 Mountain biking, 52 Indoor cycling, 99 Gravel cycling → `cycling`; 0 Walking → `walking`; 11 Hiking → `hiking`; 21 Swimming → `swimming`; 85 Openwater swimming → `swimming_open_water`; 23 Gym → `traditional_strength_training`; and so on. An unknown id gives `other`. The id is always kept in `metadata.suunto_activity_id`, with Suunto's name in `metadata.suunto_activity`. Suunto does not publish the id list publicly; ours follows a third-party integration's table (**to confirm**) |
| `start_date` | `startTime` (epoch ms) |
| `end_date` | `stopTime` (epoch ms, pauses included); if missing, the start plus `totalTime` |
| `duration_s` | `totalTime` (seconds of timer time, pauses excluded), rounded. The exact value is kept in `metadata.total_time_s` |
| `distance_m` | `totalDistance` (metres). A zero distance (gym, indoor cycling) is left empty |
| `active_energy_kcal` | `energyConsumption`, read as kilocalories with no conversion. Third parties report kcal for workouts (**to confirm**); Suunto documents joules only for the 24/7 data |
| `elevation_gain_m` | `totalAscent` (metres) |
| `avg_heart_rate` | `hrdata.workoutAvgHR`, or `hrdata.avg` |
| `max_heart_rate` | `hrdata.workoutMaxHR`, or `hrdata.hrmax`. `hrdata.max` is reported to be the user's own maximum heart rate setting, not the workout's, so it is never used |
| `avg_speed_mps` | `avgSpeed` (m/s) |
| `metadata.utc_offset` | `timeOffsetInMinutes`, as `+01:00` |
| `metadata.indoor` | set for indoor activity ids (treadmill, indoor cycling, indoor rowing, gym, crosstrainer, indoor sports) |
| other `metadata` | `suunto_workout_id`, `total_descent_m`, `step_count`, `max_speed_mps`, `avg_power_w`, `max_power_w`, `avg_cadence`, `max_altitude_m`, `min_altitude_m`, `min_heart_rate`, `recovery_time_s`, `peak_training_effect`, and the watch as `device` and `device_sw_version` (from `gear`) |

The external id is the `workoutKey`, or the `workoutId` when there is no key. Workouts are
replaceable: an edit made in the Suunto app comes back under the same key and replaces the
stored figures, with no duplicate. Heart rates outside 1 to 300 bpm, and negative figures,
are left empty.

## Limitations

- **Field names.** The JSON field names of the workout summary come from third-party
  integrations of the same API, because Suunto's reference is behind the partner login.
  Something differs from what you see with your account? Open an issue.
- **24/7 data is raw only.** Sleep, recovery and daily activity are stored but not mapped
  (see section 4).
- **No samples, laps or routes.** They are only in the FIT export, which is not fetched.
- **Deleted workouts.** Suunto has no deletion notice we know of. A workout deleted in the
  Suunto app stays stored here.
- **No subscription management.** The notification URL is set once per application in API
  Zone. The connector does not register per-user subscriptions.

## Sources

- [API Zone home](https://apizone.suunto.com/), [How to start](https://apizone.suunto.com/how-to-start)
  and [FAQ](https://apizone.suunto.com/faq): eligibility, OAuth, the subscription key header,
  the `user` claim, token lifetime, the notification fields `username` and `workoutid`.
- The API Zone API catalog (public operation list): the `/v3/workouts` operations with
  `since`, `until`, `limit` and `offset`; the 24/7 endpoints, their parameters, the 28-day
  limit and joules for daily energy; the token, refresh and deauthorize endpoints.
- Third party: the published notes and source code of another open-source project's Suunto
  integration. They are the source for the notification signature header and
  types, the 2-second answer, the reported Development API limits, the `activityId` table
  and the heart rate fields.
