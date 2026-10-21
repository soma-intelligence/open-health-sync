# Fitbit connector

Slug `fitbit`. Users connect their Google Account (the one their Fitbit or Pixel Watch data lives
in) over Google OAuth; Open Health Sync pulls their data from the
[Google Health API](https://developers.google.com/health) (`health.googleapis.com`, v4) on a
schedule and when Google sends a webhook notification. You create your own Google Cloud project
and OAuth client: nothing is shared between instances.

Status: **available.** You set up your own Google Health API project; until Google verifies it,
it serves at most 100 users (see [Access](#access-read-this-first)). Field names follow the
API's public reference and discovery document, checked on 2026-10-12; points marked
**(to confirm)** are the ones to check first against a live project.

The connector is built on the Google Health API only. The legacy Fitbit Web API
(`api.fitbit.com`) lost support on 2026-09-30 and is turned off on 2026-10-30; it is not used, and
legacy Fitbit tokens cannot be carried over (every user consents again).

## Access: read this first

- **Your own project.** You enable the API and create the OAuth client yourself, as Google's
  [setup page](https://developers.google.com/health/setup) describes (checked on 2026-10-17).
- **100 users while unverified.** All Google Health API scopes are *restricted*. An app that has
  not passed Google's OAuth app verification "is limited to 100 users". More needs verification
  plus an annual third-party security assessment (CASA), quoted at USD 500 to 4,500 and 2 to 6
  weeks ([app verification](https://developers.google.com/health/app-verification)).
- **7 day refresh tokens in Testing.** While the consent screen's publishing status is
  *Testing*, Google expires refresh tokens after 7 days: each user's connection turns
  `needs_reauth` weekly and they must connect again. Publishing the app avoids it; whether an
  unverified app with restricted scopes can be published is **(to confirm)**.
- **Users link first.** A user must link their Google Account to Google Health in the Fitbit
  (Google Health) mobile app before consenting. If they have not, the connect fails with a clear
  message ("this Google Account is not linked to Google Health yet...") and the grant is revoked.

A self-hosted instance for a person or a family fits well within the unverified limits.

## 1. Get your keys

To set up your project ([setup](https://developers.google.com/health/setup)):

1. Create a Google Cloud project (one per environment) and enable the **Google Health API**
   (`health.googleapis.com`).
2. On the OAuth consent screen (Audience page) choose **External**, and while in Testing add each
   user's Google Account as a test user.
3. On the Data Access page add the three read scopes listed in [Scopes](#3-scopes).
4. Create an **OAuth client ID** of type **Web application** and add the authorized redirect URI
   `ohs doctor` prints, which is `<OHS_PUBLIC_URL>/oauth/fitbit/callback`. It must match exactly.
5. Copy the **client ID** and **client secret**.

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_FITBIT_CLIENT_ID=...apps.googleusercontent.com
OHS_FITBIT_CLIENT_SECRET=...
# Optional, see "Webhooks": the subscriber's secret, scheme included.
OHS_FITBIT_WEBHOOK_SECRET="Bearer <long random string>"
# Optional: required (default), if_available or off.
OHS_FITBIT_WEBHOOK_SIGNATURE=required
```

Restart and run `ohs doctor`: it reports the connector as enabled and prints the callback URL.
The client ID and secret are both required. `GET /v1/providers` lists `fitbit` when it is on.

## 3. Scopes

Consent asks for three read scopes, all with the prefix `https://www.googleapis.com/auth/`:

| Scope | Data types pulled |
|---|---|
| `googlehealth.activity_and_fitness.readonly` | steps, distance, floors, active energy, exercise, VO2 max |
| `googlehealth.health_metrics_and_measurements.readonly` | heart rate, resting heart rate, HRV, SpO2, respiratory rate, weight, body fat, height, blood glucose, core body temperature, sleep temperature |
| `googlehealth.sleep.readonly` | sleep |

Nothing else is asked for (no nutrition, ECG, location or profile): every extra restricted scope
makes verification harder. Google lets a user grant some scopes and refuse others. The connector
reads the granted scopes from the token answer and skips the data types of a refused scope; a
consent granting none of the three fails. Which scope covers VO2 max, blood glucose, core body
temperature and sleep temperature is **(to confirm)**; a data type Google answers with 403 is
skipped anyway.

The consent URL uses `access_type=offline` (for a refresh token), `prompt=consent` (so a
reconnect gets a new refresh token) and `include_granted_scopes=true`.

## 4. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/fitbit \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

After consent, Open Health Sync trades the code at `oauth2.googleapis.com/token`, then calls
`users.getIdentity` (`GET /v4/users/me/identity`) and stores the `healthUserId` as the
connection's provider user id: webhooks name users by it. If that call fails for another reason
than an unlinked account, the user is connected without it (pulls work) and it is read at the
next token refresh.

Access tokens last an hour and are refreshed as needed. A refresh token Google refuses
(`invalid_grant`: revoked, or 7 days old in Testing) turns the connection `needs_reauth`.
Disconnecting, or deleting the user, revokes the grant at `oauth2.googleapis.com/revoke`.

## 5. What is pulled

A new connection is backfilled over the last 30 days, then pulled every 6 hours. Each data type is
read with `users.dataTypes.dataPoints.list`
(`GET /v4/users/me/dataTypes/<type>/dataPoints`), page after page (`pageSize` 10,000, or 25 for
`exercise` and `sleep`, the API's maximums; `nextPageToken`). Every page is stored as one raw
object of the data type, one data point per line. The filter per kind of data type:

| Kind | Data types | Filter |
|---|---|---|
| Interval | `steps`, `distance`, `floors`, `active-energy-burned` | `<type>.interval.start_time` in the window |
| Sample | `heart-rate`, `heart-rate-variability`, `oxygen-saturation`, `weight`, `body-fat`, `height`, `vo2-max`, `blood-glucose`, `core-body-temperature` | `<type>.sample_time.physical_time` in the window |
| Daily | `daily-resting-heart-rate`, `daily-heart-rate-variability`, `daily-oxygen-saturation`, `daily-respiratory-rate`, `daily-vo2-max`, `daily-sleep-temperature-derivations` | `<type>.date`, one day wider on each side |
| Session | `exercise` | `exercise.interval.civil_start_time`, one day wider on each side |
| Sleep | `sleep` | `sleep.interval.end_time` in the window |

The filter field names of daily types are written in snake case (`daily_resting_heart_rate.date`)
as on Google's data types page; the list reference shows one example in camel case
(`dailyHeartRateVariability.date`). Which one the API accepts is **(to confirm)**.

The connector uses the default data source family (`all-sources`): points from every source the
user has (tracker, phone, scale, other apps) are pulled, and the source is kept in metadata and
in the content hash, so two sources of the same minute stay apart. Data only exists once the
user's device has synced to the app. How far back history reaches is **(to confirm)**.

## 6. Webhooks

A Google Health API **subscriber** belongs to the Cloud project, not to a user, so Open Health
Sync does not create it. Create it once ([webhooks](https://developers.google.com/health/webhooks))
with `endpointUri` set to `<OHS_PUBLIC_URL>/inbound/fitbit` and `endpointAuthorization.secret`
set to exactly the value of `OHS_FITBIT_WEBHOOK_SECRET` (the value includes its scheme, such as
`Bearer `, and Google sends it verbatim in the `Authorization` header):

```sh
curl -s -X POST "https://health.googleapis.com/v4/projects/$PROJECT_NUMBER/subscribers?subscriberId=ohs-fitbit" \
  -H "Authorization: Bearer $(gcloud auth print-access-token)" -H 'Content-Type: application/json' \
  -d '{"endpointUri":"https://ohs.example.org/inbound/fitbit",
       "endpointAuthorization":{"secret":"Bearer <long random string>"},
       "subscriberConfigs":[{"subscriptionCreatePolicy":"AUTOMATIC",
         "dataTypes":["steps","distance","floors","heart-rate-variability","daily-resting-heart-rate",
                      "daily-heart-rate-variability","daily-oxygen-saturation","daily-respiratory-rate",
                      "weight","body-fat","height","sleep","exercise"]}]}'
```

Set `OHS_FITBIT_WEBHOOK_SECRET` and restart **before** this call: while creating it Google POSTs
`{"type": "verification"}` twice, once with the secret (Open Health Sync answers 200) and once
without (401), and refuses the subscriber if either answer is wrong. With `AUTOMATIC`, every user
who consents is subscribed. The credentials the call needs, and which data types Google accepts in
`subscriberConfigs` (not every type has webhooks; `oxygen-saturation`, `vo2-max` and
`daily-vo2-max` do not), are **(to confirm)**: drop a type the call refuses.

Each notification must carry the secret, compared in constant time. It must also carry a valid
`GOOGLE-HEALTH-API-SIGNATURE`: base64 of a Tink prefix (version byte, 4 byte key id) and an ECDSA
P-256 / SHA-256 signature of the raw body, checked against Google's
[public keyset](https://www.gstatic.com/googlehealthapi/webhooks/webhooks_public_keyset.json)
(fetched, cached for an hour, fetched again when a signature names a key it does not hold; keys
rotate every 30 days). This follows Google's manual verification steps without the Tink library.
To confirm against a live delivery: the base64 alphabet (standard and URL-safe
are both accepted) and the signature encoding (DER per the docs; IEEE P1363 also accepted).
`OHS_FITBIT_WEBHOOK_SIGNATURE` sets the check:

| Value | Behaviour |
|---|---|
| `required` (default) | a missing or bad signature is refused (401); while the keyset cannot be fetched, every notification is refused and Google retries for up to 7 days |
| `if_available` | as `required`, except that while the keyset cannot be fetched the secret alone admits a notification |
| `off` | only the secret is checked; use it if real notifications fail the signature check, and report it |

A notification (one, or a batch of up to 99) only says what changed: user, data type, operation
(`UPSERT` or `DELETE`) and intervals. Each one queues a pull of **that data type only** over its
intervals (a minute wider on each side; for daily data, the day before to two days after), merged
per user and data type. A data type the connector does not pull is acknowledged and ignored.

- **Deletions are not propagated yet.** A `DELETE` notification queues a pull like an upsert, but
  a pull cannot tell what is gone, so data deleted at Google stays stored.
- **Answer code.** Open Health Sync answers an accepted notification with 200; Google documents
  204 and retries "any other status code" **(to confirm** whether a 200 is retried).
- Revoked permissions and account deletions send no notification. Without webhooks nothing
  breaks: the 6 hour pull still collects everything.

## 7. Rate limits

Per [rate limits](https://developers.google.com/health/rate-limits): 300 requests per user per
minute; 86.4 million per project per day and 120,000 per minute; an unverified app 250 per second
in total. An HTTP 429 or 504 defers the connection's job until `Retry-After` (one minute without
one); 500, 502 and 503 are retried a few times first. One pull costs one request per data type
and page: about 21 for a quiet day, more for a long backfill of one-second heart rate.

## Data produced

| Google Health data | Canonical records |
|---|---|
| `steps` (1 minute) | `step_count_intraday` (count) |
| `distance` (1 minute, `millimeters`) | `distance_walking_running_intraday` (m = mm / 1000) |
| `floors` (1 minute) | `flights_climbed` (count) |
| `active-energy-burned` (1 minute, `kcal`) | `active_energy_burned_intraday` (kcal) |
| `heart-rate` (`beatsPerMinute`) | `heart_rate` (bpm); motion context and sensor location in metadata |
| `heart-rate-variability` (RMSSD ms) | `hrv_rmssd` (ms); the standard deviation in metadata |
| `daily-heart-rate-variability` | `hrv_rmssd` for the day (average RMSSD); deep sleep RMSSD, non-REM heart rate and entropy in metadata |
| `daily-resting-heart-rate` | `resting_heart_rate` (bpm) for the day |
| `oxygen-saturation` (`percentage`) | `oxygen_saturation` (percent) |
| `daily-oxygen-saturation` | `oxygen_saturation` for the day (average); bounds in metadata |
| `daily-respiratory-rate` (`breathsPerMinute`) | `respiratory_rate` (stored as "bpm") for the day |
| `weight` (`weightGrams`) | `body_mass` (kg = g / 1000) |
| `body-fat` (`percentage`) | `body_fat_percentage` (percent) |
| `height` (`heightMillimeters`) | `height` (cm = mm / 10) |
| `vo2-max`, `daily-vo2-max` | `vo2_max` (mL/kg/min); cardio fitness level in metadata |
| `blood-glucose` (`bloodGlucoseMilligramsPerDeciliter`) | `blood_glucose` (mg/dL); specimen, meal and timing in metadata |
| `core-body-temperature` (`temperatureCelsius`) | `body_temperature` (celsius) |
| `daily-sleep-temperature-derivations` | `skin_temperature` (nightly, celsius) and `body_temperature_deviation` (nightly minus the 30 day baseline) |
| `sleep` | a sleep session (`minutesAsleep`, `minutesAwake`, deep, REM and light minutes from `stagesSummary`, times 60) and its phases from `stages` |
| `exercise` | a workout: activity from `exerciseType`, duration from `activeDuration`, `caloriesKcal`, distance and elevation (mm / 1000), average speed (mm/s / 1000), average heart rate; steps, run VO2 max, active zone minutes and swim lengths in metadata |

Notes on the mapping:

- **Units in field names.** The API writes units into field names and int64 values as JSON
  strings (`"count": "120"`); both strings and numbers are read. A SpO2 value at or under 1 is
  taken for a fraction and scaled to percent (the API documents 0 to 100).
- **HRV is RMSSD**, stored as `hrv_rmssd`, never `hrv_sdnn`.
- **Time zones.** Every point carries a UTC offset (`startUtcOffset`, `utcOffset`, as
  `"3600s"`), never an IANA name. The metadata keeps `utc_offset` (`+01:00`) and, as
  `timezone`, a zone with that offset (`Etc/GMT-1`, or a place for a half-hour offset), which is
  what the ETL needs to find the local day. Daily points carry a date only: they span that date
  and claim no zone.
- **No daily totals.** Steps, distance, floors and active energy are stored per minute under
  the `*_intraday` names (and `flights_climbed`); daily roll-ups and daily summaries are not
  pulled.
- **Sleep stages.** `AWAKE`, `LIGHT`, `DEEP`, `REM` map to the canonical stages. Classic sleep
  (older devices, naps) reports `ASLEEP` (stored as light) and `RESTLESS` (stored as awake, its
  minutes in metadata as `minutes_restless`); the session keeps `sleep_type`.
- **Exercise types.** About 150 `exerciseType` values map to the canonical activity vocabulary;
  the rest (chores, motor sports...) become `other`, with Google's type and display name in
  metadata.
- **Raw only.** Google data types this connector does not pull (nutrition, hydration, ECG,
  irregular rhythm, active zone minutes, time in heart rate zones, sedentary periods, activity
  level, swim lengths, altitude, basal energy, menstrual and mood data) have no canonical name
  here or need a scope not asked for. Exercise routes (TCX export) are not pulled.
- Which data types a Fitbit tracker (as opposed to a Pixel Watch) produces is **(to confirm)**.

## Updates and deletions

- Minute intervals are replaceable and keyed by source and start: a minute Google revises after
  a later sync replaces the stored value.
- Daily points are replaceable and keyed by date: a recomputed day replaces the stored value.
- Sleep sessions and workouts are keyed by their data point id (the last segment of the resource
  name) and start, and are replaceable: Google reprocessing a night (stages computed later, an
  edit) replaces its figures and phases.
- Measurements (heart rate, weight...) are keyed by source, time and value.
- Deletions are not propagated (see [Webhooks](#6-webhooks)).

## To verify with a real project

- The filter field spelling of daily types, and every scope assignment marked above.
- The webhook signature format (base64 alphabet, DER) and whether a 200 answer stops retries.
- The exact notification `dataType` spelling (kebab case per the docs; snake and camel case are
  accepted too) and batch shape.
- Which data types support webhook subscriptions, and the credentials creating a subscriber needs.
- History depth, and the Testing-mode and publishing limits above.

## Sources

Checked on 2026-10-12.

- Google, [Google Health API](https://developers.google.com/health), [setup](https://developers.google.com/health/setup), [scopes](https://developers.google.com/health/scopes), [app verification](https://developers.google.com/health/app-verification)
- Google, [data types](https://developers.google.com/health/data-types), [REST reference](https://developers.google.com/health/reference/rest) and the API discovery document (`https://health.googleapis.com/$discovery/rest?version=v4`)
- Google, [webhooks](https://developers.google.com/health/webhooks), [rate limits](https://developers.google.com/health/rate-limits)
- Google, [Fitbit Web API migration guide](https://developers.google.com/health/migration)
