# WHOOP connector

Slug `whoop`. Users connect their own WHOOP account over OAuth; Open Health Sync pulls their
data from the WHOOP API v2 on a schedule and when WHOOP sends a webhook. You register your own
WHOOP developer application: nothing is shared between instances.

Status: available, not yet verified with a real WHOOP account. The connector follows the
published API documentation and is tested against a fake of the API.

## 1. Get your keys

1. Sign in to the WHOOP Developer Dashboard (`developer-dashboard.whoop.com`) with a WHOOP
   account and create a team if asked. Dashboard labels change from time to time; the fields
   below are what matters.
2. Create an app. Select the scopes listed in [section 3](#3-scopes); the consent page can only
   ask for scopes the app has.
3. Add the **redirect URL** (the OAuth callback) printed by `ohs doctor`, which is
   `<OHS_PUBLIC_URL>/oauth/whoop/callback`. It must match exactly, including the scheme, so give
   a real instance an https `OHS_PUBLIC_URL`.
4. Set the **webhook URL** to `<OHS_PUBLIC_URL>/inbound/whoop` and choose the **v2** model
   version (v1 webhooks are retired).
5. Copy the **client ID** and the **client secret**.

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_WHOOP_CLIENT_ID=...
OHS_WHOOP_CLIENT_SECRET=...
```

Restart, then run `ohs doctor`. It reports the connector as enabled and prints the callback URL
to register:

```
ok    connector whoop
      whoop callback: https://ohs.example.org/oauth/whoop/callback
```

Both keys are required: with only one set, `ohs doctor` fails and the connector stays off.
`GET /v1/providers` lists `whoop` when it is on.

## 3. Scopes

The consent page asks for:

| Scope | Gives |
|---|---|
| `read:cycles` | physiological cycles: day strain, energy, average and maximum heart rate |
| `read:recovery` | recovery score, resting heart rate, HRV (RMSSD), SpO2, skin temperature |
| `read:sleep` | sleeps and naps with stage durations |
| `read:workout` | workouts with strain, heart rate, energy, distance |
| `read:body_measurement` | height, weight, maximum heart rate |
| `read:profile` | the WHOOP user id, which webhooks name. The name and email the profile also returns are not stored |
| `offline` | a refresh token, so the connection outlives the one hour access token |

## 4. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/whoop \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url`. After consent they land on your `redirect_uri`
with `status=connected`. A new connection is backfilled over the last 30 days in 7 day
windows, then pulled every 6 hours.

WHOOP rotates the refresh token on every refresh and invalidates the previous access token.
Open Health Sync stores the new pair each time. If WHOOP refuses the refresh token
(`invalid_grant`) the connection becomes `needs_reauth` and the user must connect again. A
refused client (`invalid_client`, a wrong secret) is reported as an error and does not touch
the connections.

## 5. Webhooks

WHOOP sends every webhook of your app to the one URL set in the dashboard,
`<OHS_PUBLIC_URL>/inbound/whoop`, for all users. There is nothing to subscribe per user, so no
subscription row is created when a user connects.

Each delivery is signed: `X-WHOOP-Signature` is the base64 HMAC-SHA256 of the
`X-WHOOP-Signature-Timestamp` header followed by the raw body, keyed by your client secret.
Open Health Sync recomputes it and refuses (401) a delivery whose signature is missing or wrong,
or whose timestamp is more than 5 minutes from the server clock, so keep the clock synchronized.

| Event | Effect |
|---|---|
| `recovery.updated`, `sleep.updated`, `workout.updated` | a pull of the last 3 days for that user |
| `recovery.deleted`, `sleep.deleted`, `workout.deleted` | acknowledged (200), not applied: see [updates and deletions](#updates-and-deletions) |
| anything else | acknowledged (200), ignored |

A webhook names one record, but a pull job covers a time window, so an update pulls the last 3
days, which covers late scoring and recent edits. The 6 hour pull covers the rest. Cycles and
body measurements have no webhooks; the scheduled pull picks them up.

For webhooks to arrive, `OHS_PUBLIC_URL` must be reachable from the internet. Without it nothing
breaks: the 6 hour pull still collects everything.

Rotating the client secret: change it in the dashboard and in `.env` together and restart.
Deliveries signed with the other secret in between are refused, and the scheduled pull makes up
for them.

## 6. Limits

WHOOP allows 100 requests a minute and 10,000 a day per app by default (an increase can be
requested in the dashboard). A pull pages through each collection 25 records at a time and
spaces its requests 600 ms apart. On HTTP 429 the connection's job is deferred until
`Retry-After`, or `X-RateLimit-Reset` when WHOOP sends only that, and one minute when it sends
neither.

## Data produced

| WHOOP data | Canonical records |
|---|---|
| Cycle | quantity samples `strain` and `heart_rate` (the cycle average, `count/min`, stored as `bpm`); the day's summary gets `strain_score` and `total_kcal` |
| Recovery | quantity samples `resting_heart_rate`, `hrv_rmssd`, `oxygen_saturation`, `skin_temperature`; the day's summary gets `recovery_score` and `resting_hr` |
| Sleep | a sleep session per sleep or nap: stage durations (slow wave sleep is `deep`), sleep performance as `sleep_score`, respiratory rate; plus a `respiratory_rate` sample |
| Workout | a workout (activity in the shared vocabulary, energy, distance, average and maximum heart rate, elevation gain) and a `workout_strain` sample |
| Body measurement | `body_mass` and `height` samples dated the day they were read; the maximum heart rate is kept in their metadata |

Notes on the mapping:

- **HRV.** WHOOP reports the RMSSD of the night. It is stored as `hrv_rmssd`, never as
  `hrv_sdnn`, which holds a different statistic (SDNN runs higher for the same heart). The
  generic `hrv_avg_ms` of daily summaries and sleep sessions is left empty.
- **Energy.** A cycle's kilojoules count all the energy of the day, resting metabolism
  included, so they become the summary's `total_kcal`, not `active_energy_burned`. A workout's
  kilojoules become the workout's `active_energy_kcal`, as for every provider.
- **Day.** A WHOOP cycle starts when the user falls asleep. Its day is the local date twelve
  hours after the start, so a cycle that starts at 23:30 belongs to the next day, the day WHOOP
  shows it on. The cycle and the recovery of a day fill one daily summary row.
- **Time zone.** WHOOP gives an offset such as `+01:00`, not a zone name. A whole hour offset is
  stored as its IANA `Etc/GMT` name (`+01:00` is `Etc/GMT-1`) in `timezone`; the offset itself
  is kept as `timezone_offset`. A half hour offset has no such name and the local day falls back
  to UTC.
- **Scoring.** A sleep or workout still `PENDING_SCORE` is stored with its times (and, for a
  workout, its sport) and no figures; the scored copy WHOOP sends later replaces it (see below).
  A recovery or cycle still `PENDING_SCORE` gives nothing until scored. `UNSCORABLE` sleeps and
  workouts are stored without figures. `score_state` in metadata says which.
- **Not available.** The public API has no heart rate series, no hypnogram (only the time in
  each stage) and no ECG.

## Updates and deletions

- A re-scored cycle or recovery, a cycle whose strain is still growing, and a changed body
  measurement replace the stored figures: those quantity samples and the daily summary are
  identified by the WHOOP record and the quantity, not by the value.
- Sleep sessions and workouts are identified by their WHOOP id and are replaceable: a scored or
  re-scored copy replaces the stored figures (a figure it leaves out keeps its stored value),
  along with its strain and respiratory rate samples, and a copy whose start the user edited
  replaces the stored record of the same id instead of adding a second one.
- Deletions are not applied. The core gives a connector no way to remove one stored record, so
  a `*.deleted` webhook is acknowledged and logged, and the record stays until the user is
  deleted.
