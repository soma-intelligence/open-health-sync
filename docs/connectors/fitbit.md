# Fitbit: research notes

Status: **planned, not built**. No Fitbit code ships in this release. This page collects what a
contributor needs to know before writing the connector, as checked on **2026-10-11**. Google is
in the middle of replacing the Fitbit Web API, so check the sources below again before you
start.

Statements we could not confirm from a public Google page are marked **(unverified)**.

## Summary

| | |
|---|---|
| Legacy API | Fitbit Web API (`api.fitbit.com`): support ended **2026-09-30**, turned off on **2026-10-30**. Do not build on it |
| Replacement | [Google Health API](https://developers.google.com/health) (`health.googleapis.com`, v4), the rebuilt Fitbit Web API |
| Can a new developer join today | **No.** Google: "we are not onboarding new projects at this time", with no date |
| OAuth | Google OAuth 2.0 (authorization code, `https://oauth2.googleapis.com/token`); scopes under `https://www.googleapis.com/auth/googlehealth.*`, all restricted |
| Delivery | Pull (REST list and roll-up endpoints) plus webhooks that signal changes; the data is always pulled |
| Limits | 300 requests per user per minute; unverified apps 100 users |
| Recommendation | **Wait** for the Google Health API to onboard new projects. Meanwhile Fitbit data can reach us through Health Connect on Android (planned Kotlin SDK) |

## Which API a new app can use

From [dev.fitbit.com](https://dev.fitbit.com/build/reference/web-api/) and the Google Health API
[migration guide](https://developers.google.com/health/migration) (page updated 2026-09-28):

- "Support for the legacy Fitbit Web API ends on September 30, 2026." After that Google fixes no
  bugs and gives no support.
- On October 30, 2026 the legacy API "will be turned off and will no longer function or sync data
  to or from Fitbit users". That is 19 days after this check: a connector written now would stop
  working before it shipped.
- Legacy tokens do not carry over: "existing access and refresh tokens cannot be transferred", so
  every user consents again on the new API.
- The [Google Health API](https://developers.google.com/health/about) is the only path forward, and
  it is closed to new projects: "While we are not onboarding new projects at this time, we are
  actively working to open access to more developers and will share updates here as availability
  expands."

Google Fit is a separate, deprecated API (see the [connector table](README.md)) and is no way in
either.

## Access: what you need to do

Today there is nothing a new maintainer can do except wait and watch the
[Google Health API page](https://developers.google.com/health) (and its developer newsletter,
linked there). When onboarding opens, the documented setup is:

1. **Create a Google Cloud project** (one per environment; there is no sandbox) and enable the
   Google Health API (`health.googleapis.com`) ([setup](https://developers.google.com/health/setup)).
2. **Create an OAuth client** of type Web application, and register the redirect URI
   `<OHS_PUBLIC_URL>/oauth/fitbit/callback` (the slug is not decided; see below).
3. **Configure the consent screen** (Audience page): user type External. Add test users while
   in Testing.
4. **Pick the read scopes** on the Data Access page.
5. **Know the user limits.** From [setup](https://developers.google.com/health/setup) and
   [app verification](https://developers.google.com/health/app-verification):
   - An unverified app "is limited to 100 users".
   - In Testing mode, refresh tokens expire after 7 days, so each user would reconnect weekly.
     Google says to publish the app to avoid that.
   - More than 100 users needs Google's OAuth app verification plus an annual third-party
     security assessment (CASA, against OWASP ASVS), "ranging from $500 to $4,500 USD", taking
     2 to 6 weeks. All Google Health API scopes are restricted.
6. **Users link first.** A user must link their Google Account to Google Health in the mobile
   app before consent; check it with `users.getIdentity` after the token exchange (an unlinked
   account gets `FAILED_PRECONDITION` / `ACCOUNT_NOT_LINKED`).

### Can a self-hosted instance use it

Probably yes once Google onboards projects, within the unverified limits: each instance creates
its own Cloud project and OAuth client, and a personal or family instance stays well under 100
users. What we could not confirm (**unverified**):

- whether Google will onboard individual developers or only companies;
- whether an unverified app with restricted scopes can be published out of Testing mode (to
  avoid the 7 day refresh token expiry) without completing verification;
- how the unverified-app warning screen looks for these scopes.

A self-hosted operator who must serve more than 100 users would need the annual security
assessment, which is out of reach for most.

## Integration shape

### OAuth

Standard Google OAuth 2.0 for web server apps: authorization code with `access_type=offline` to
get a refresh token, token endpoint `https://oauth2.googleapis.com/token`. Users can grant some
scopes and refuse others; the connector must read the granted scopes from the token response and
treat a refused scope as an empty data type (as the Withings connector does). Revoking uses
Google's standard revoke endpoint (**unverified** for this API, but it is the standard Google
flow).

Read scopes (from [scopes](https://developers.google.com/health/scopes)), all with the prefix
`https://www.googleapis.com/auth/googlehealth.`:

| Scope | Gives |
|---|---|
| `activity_and_fitness.readonly` | steps, distance, floors, calories, active minutes, heart rate zones, exercise, VO2 max |
| `health_metrics_and_measurements.readonly` | heart rate, HRV, resting heart rate, SpO2, respiratory rate, weight, body fat, height, temperature, blood glucose |
| `sleep.readonly` | sleep sessions and stages |
| `nutrition.readonly` | food logs, hydration |
| `ecg.readonly` | ECG recordings |
| `irn.readonly` | irregular rhythm notifications |
| `location.readonly` | exercise routes (**unverified** which data it covers) |
| `profile.readonly`, `settings.readonly` | profile and settings, including the user's time zone (**unverified**) |

A first version should ask for the first three (plus `nutrition.readonly` if wanted) and nothing
else: every extra restricted scope makes verification harder.

### Pull

From the [REST reference](https://developers.google.com/health/reference/rest): base URL
`https://health.googleapis.com`, versions `v4` and `v4beta`. Data is read with
`users.dataTypes.dataPoints.list` (raw points), `rollUp` and `dailyRollUp` (aggregates), and
`reconcile` (Google's merged stream when several sources overlap). Page sizes go up to 10,000
points, but only 25 for `exercise` and `sleep`. Roll-ups span at most 14 days for
`heart-rate`, `calories-in-heart-rate-zone`, `active-minutes` and `total-calories`, 90 days for
the others ([data types](https://developers.google.com/health/data-types)). After a user
connects, history can be queried; the maximum history depth is **unverified**.

Data only exists after the user's device syncs to the Google Health app (at best every 15
minutes while the app is open).

### Webhooks

From [webhooks](https://developers.google.com/health/webhooks):

- A **subscriber** is registered once per Cloud project:
  `POST https://health.googleapis.com/v4/projects/<project-number>/subscribers?subscriberId=...`
  with an HTTPS `endpointUri`, an `endpointAuthorization` secret and, per data type, a
  `subscriptionCreatePolicy` of `AUTOMATIC` (Google subscribes every consenting user) or
  `MANUAL`.
- On create or update Google verifies the endpoint with two POSTs of `{"type": "verification"}`:
  with the secret it must answer 200 or 201, without it 401 or 403.
- Each notification carries the secret in `Authorization` and an ECDSA P-256 signature in
  `GOOGLE-HEALTH-API-SIGNATURE`, verified against Google's
  [public keyset](https://www.gstatic.com/googlehealthapi/webhooks/webhooks_public_keyset.json)
  (keys rotate every 30 days).
- A notification is a change signal: `healthUserId`, `dataType`, `operation` (`UPSERT` or
  `DELETE`) and the affected `intervals`. Batches hold up to 99 messages. The handler answers 204
  at once; Google retries for up to 7 days.
- Not every data type has webhooks (for example `total-calories`, `oxygen-saturation`,
  `vo2-max`, `daily-vo2-max` do not), so a scheduled pull is still needed.
- Revoked permissions and account deletion produce no notification.

### Mapping to this repository's interfaces

| Interface | Google Health API | Notes |
|---|---|---|
| `OAuthConnector` | `AuthorizeURL`, `Exchange`, `Refresh` against Google OAuth; `Fetch` lists data points per data type over the window | Google OAuth needs no PKCE for a confidential web client, so the current contract fits. `Fetch` emits one raw object per data type and page |
| `PushConnector` | `HandleInbound` checks the `Authorization` secret and the signature, answers verification requests, and turns each message into an `InboundEvent` (`healthUserId`, `dataType`, interval as `From`/`To`) | A good fit: notifications are already "pull this window". A `DELETE` operation needs thought: today an inbound event can only trigger a pull, not remove stored records |
| `Subscriber` | Not needed with `AUTOMATIC` subscriptions | The subscriber is per project. Registering it is a one-off operator step (or done at startup through `Configurable`), not per connection |
| `Revoker` | Revoke the Google token on disconnect | |
| `KeyedConnector` | `CLIENT_ID`, `CLIENT_SECRET`, plus the webhook secret and project number if webhooks are used | |

The connection should keep `healthUserId` (from `users.getIdentity`) as the provider user id;
webhooks name users by it.

**Slug.** Users know the devices as Fitbit, but the API is the Google Health API and also serves
Pixel Watch data and manual entries. `fitbit` is the working name; `google_health` may be more
accurate. Decide when building.

## Data types and the canonical model

Data type names and record kinds from [data types](https://developers.google.com/health/data-types);
field names and units from the [dataPoints reference](https://developers.google.com/health/reference/rest/v4/users.dataTypes.dataPoints).
Units are part of the field names (`...Millimeters`, `...Kcal`, `beatsPerMinute`). Fields we
could not read in the reference are marked **(unverified)**. Times come as RFC 3339 instants with
a UTC offset per point (`startUtcOffset`) and a civil (local) time; no IANA zone name per point,
so `Metadata["timezone"]` would come from the user's settings if available (**unverified**), and
the offset should be kept in metadata.

| Google Health data type | Fields and units | Canonical records |
|---|---|---|
| `steps` (interval, 1 minute) | `count` | `step_count_intraday` (count); daily totals from `dailyRollUp` as `step_count` and `DailySummary.StepCount` |
| `distance` (interval, 1 minute) | millimeters (**unverified** field name) | `distance_walking_running_intraday` / `distance_walking_running` (m, divide by 1000) |
| `floors` (interval) | `count` | `flights_climbed`, `DailySummary.FloorsClimbed` |
| `active-energy-burned`, `total-calories` (interval) | kcal (**unverified** field names) | `active_energy_burned_intraday` / `active_energy_burned` (kcal); `DailySummary.ActiveKcal`, `TotalKcal` |
| `heart-rate` (sample, 1 second) | `beatsPerMinute` | `heart_rate` (bpm) |
| `daily-resting-heart-rate` (daily) | `beatsPerMinute` | `resting_heart_rate` (bpm), `DailySummary.RestingHR` |
| `daily-heart-rate-variability` (daily) | `averageHeartRateVariabilityMilliseconds`, `deepSleepRootMeanSquareOfSuccessiveDifferencesMilliseconds` (ms) | RMSSD, not SDNN: `DailySummary.HRVAvgMs`; not `hrv_sdnn`. A new quantity name for RMSSD would be cleaner |
| `heart-rate-variability` (sample) | ms (**unverified** fields) | as above |
| `oxygen-saturation`, `daily-oxygen-saturation` | percent (**unverified** whether 0 to 100 or a fraction) | `oxygen_saturation` (percent, 0 to 100) |
| `daily-respiratory-rate`, `respiratory-rate-sleep-summary` | breaths per minute (**unverified** fields) | `respiratory_rate`; `SleepSession.RespiratoryRate` |
| `daily-sleep-temperature-derivations` | deviation in °C (**unverified**) | `body_temperature_deviation` (celsius) |
| `core-body-temperature`, `skin-temperature-sensors` (v4beta) | °C (**unverified**) | `body_temperature`, `skin_temperature` |
| `sleep` (session) | `stages[]` with `AWAKE`, `LIGHT`, `DEEP`, `REM` (or `ASLEEP`, `RESTLESS` for classic sleep); `summary.minutesAsleep`, `minutesAwake` (minutes) | `SleepSession` (minutes × 60) and `SleepPhase` rows; `ASLEEP` maps to `asleep_core`, `RESTLESS` to `awake` or kept as a label (decide) |
| `exercise` (session) | `exerciseType`, `activeDuration` (seconds), `metricsSummary.caloriesKcal`, `distanceMillimeters`, `averageHeartRateBeatsPerMinute`, `elevationGainMillimeters`, `averageSpeedMillimetersPerSecond`, `runVo2Max` | `Workout` (`ActiveEnergyKcal`, `DistanceM` = mm / 1000, `AvgHeartRate`, `ElevationGainM` = mm / 1000, `AvgSpeedMps` = mm/s / 1000, `VO2Max`). `exerciseType` needs a mapping table. Routes via `exportExerciseTcx` → `RoutePoint` (**unverified** whether it needs `location.readonly`) |
| `vo2-max`, `run-vo2-max`, `daily-vo2-max` | mL/kg/min (**unverified**) | `vo2_max` |
| `weight` | grams (**unverified**) | `body_mass` (kg) |
| `body-fat` | percent (**unverified**) | `body_fat_percentage` |
| `height` | millimeters (**unverified**) | `height` (cm) |
| `blood-glucose` | **unverified** unit | `blood_glucose` (mg/dL; the ETL converts mmol/L) |
| `nutrition-log`, `food`, `hydration-log` | kcal and grams (**unverified**) | `NutritionLog`; hydration has no canonical name yet |
| `electrocardiogram` | **unverified** waveform format | `ECGRecording` and `ECGVoltageSample` if the waveform is returned; separate scope, not in a first version |
| `active-zone-minutes`, `active-minutes`, `time-in-heart-rate-zone`, `sedentary-period`, `activity-level` | minutes | No canonical names; keep raw for now |

Exactly which data types a Fitbit device (as opposed to a Pixel Watch) produces is
**unverified**.

## Rate limits

From [rate limits](https://developers.google.com/health/rate-limits):

| Limit | Value |
|---|---|
| Per project per day | 86.4 million requests |
| Per project per minute | 120,000 requests |
| Per user per minute | 300 requests (5 per second) |
| Unverified app | 250 requests per second in total, at most 100 users at 2.5 per second each |

Exceeding a limit returns HTTP 429; Google asks for exponential backoff on 429 and 504.
`connector.RateLimited` covers it. Per-user limits are generous for a 6 hour pull, but a
backfill over many data types with 25-record pages for sleep and exercise needs pacing.

The legacy Web API allowed 150 requests per user per hour; it no longer matters.

## Recommendation

**Wait, and route through Health Connect meanwhile.**

- The legacy Fitbit Web API shuts down on 2026-10-30: do not build on it.
- The Google Health API is the right target, and its design fits this repository well (OAuth
  pull plus "pull this window" webhooks). But no one can create a project for it today, so we
  could neither test it nor let anyone use it.
- For Android users, the Fitbit / Google Health app can write to Health Connect, which the
  planned Kotlin SDK will read. Which Fitbit data types reach Health Connect is **unverified**.
  iPhone users have no Fitbit path: the Fitbit app does not write to Apple Health
  (**unverified** for the Google Health app).

Revisit when the Google Health API page says it onboards new projects. Write the connector then,
with fixtures recorded from a test project.

## Sources

All checked on 2026-10-11.

- Fitbit, [Web API reference](https://dev.fitbit.com/build/reference/web-api/) (deprecation notice)
- Google, [Google Health API](https://developers.google.com/health) and [about](https://developers.google.com/health/about)
- Google, [Fitbit Web API migration guide](https://developers.google.com/health/migration) (updated 2026-09-28)
- Google, [Get started](https://developers.google.com/health/get-started) and [Set up Google Cloud and OAuth](https://developers.google.com/health/setup)
- Google, [Scopes](https://developers.google.com/health/scopes)
- Google, [App verification](https://developers.google.com/health/app-verification)
- Google, [Data types](https://developers.google.com/health/data-types)
- Google, [REST reference](https://developers.google.com/health/reference/rest) and [dataPoints](https://developers.google.com/health/reference/rest/v4/users.dataTypes.dataPoints)
- Google, [Webhook subscriptions](https://developers.google.com/health/webhooks)
- Google, [Rate limits](https://developers.google.com/health/rate-limits)
