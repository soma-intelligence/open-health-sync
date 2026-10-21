# Garmin Connect (account) connector

Slug `garmin_connect`. Users connect their own Garmin Connect account with the email and password
they use in the Garmin Connect app and website. Open Health Sync pulls, on a schedule, what the
Garmin Connect app shows: daily activity, sleep with stages, heart rate, HRV, stress and
activities.

Status: available on `main`, next release.

**This connector uses Garmin's private Connect API, which is not approved by Garmin and is not
supported.** It is a second, separate path from the [Garmin Health API connector](garmin.md),
which needs a Garmin-approved partner app. This one needs no partner access: the user signs in
with their own account. In exchange:

- It can stop working whenever Garmin changes its sign-in or its website.
- Garmin's anti-bot protection may refuse the sign-in or the reads from a server. The connector
  makes an honest request with one stable user agent (`open-health-sync`); it does **not**
  rotate user agents, imitate a browser, or otherwise work around a block. When Garmin blocks it,
  the sign-in fails and the connection does not complete; there is no way around that here.
- Garmin's terms of service may not allow access by other software. Read them before you turn the
  connector on, and tell your users what it does.

The way in comes from [python-garminconnect](https://github.com/cyberjunky/python-garminconnect),
an open source Python library under the MIT license. The connector is a Go re-implementation of
its sign-in flow and its endpoints; no code is copied from it. Tests run against a fake built from
the payload shapes that library reads. Statements it leaves open are marked **(to confirm)**.
Checked on **2026-10-18**.

## How it differs from the Garmin Health API connector

| | `garmin` (Health API) | `garmin_connect` (account) |
|---|---|---|
| Access | A Garmin-approved partner app (OAuth 2.0, PKCE) | The user's own Garmin account, no approval |
| Operator keys | `OHS_GARMIN_CLIENT_ID` and `OHS_GARMIN_CLIENT_SECRET` | None |
| Sign-in | On Garmin's consent page | On a page your instance serves |
| Supported by Garmin | Yes, within the program | No, private API |
| Stability | Stable | Can break when Garmin changes its site |

Run whichever fits. A user connects one of them, not both.

## How it differs from the OAuth connectors

Every pulled source that uses OAuth lets the user sign in on the provider's site, and Open Health
Sync gets a token, never a password. Garmin Connect offers no such sign-in for other apps, so this
connector works like the [Eight Sleep connector](eight-sleep.md):

- **The user types their Garmin Connect password into a page served by your instance**
  (`/oauth/garmin_connect/signin`), not into Garmin's site. Your app never sees it.
- **Your instance keeps the password.** Garmin's bearer token is short lived, so the server signs
  in again with the password when it expires. The password is stored with the connection in the
  refresh token column, sealed with AES-256-GCM under `OHS_ENCRYPTION_KEY` and bound to the row,
  like every OAuth token (see the [security model](../security-model.md)). It is never logged and
  never returned by the API.
- **Disconnecting wipes it**, with the access token. If the user changes their Garmin password,
  the next sign-in fails and the connection goes to `needs_reauth`: they connect again with the
  new one.
- **Two-factor authentication is not supported.** An account with Garmin's two-step verification
  turned on cannot connect through this connector **(to confirm)**.

## 1. Configure the instance

No operator keys. The sign-in uses Garmin's own public mobile client ids, which are built in.
`OHS_ENCRYPTION_KEY` must be set, as for any pulled source. The connector is always listed
(`GET /v1/providers`) unless your [console](../console.md) or tenant settings turn it off. There
is no callback URL to register: the sign-in page lives on your instance, under `OHS_PUBLIC_URL`.

## 2. Connect a user

Your server asks for a connection URL exactly as for an OAuth source (see
[api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/garmin_connect \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

The returned `authorize_url` is your instance's sign-in page
(`$OHS_PUBLIC_URL/oauth/garmin_connect/signin?state=...`). The user enters their Garmin Connect
email and password. A wrong password shows the form again with a message; the link stays valid for
its 10 minutes. On success the user lands on your `redirect_uri` with `status=connected`, or on
the built-in result page, and the last 30 days are pulled. The [hosted connection
page](../connect-page.md) works the same way and says on the page that Garmin asks for a password.

| | |
|---|---|
| Sign-in | `POST https://sso.garmin.com/mobile/api/login` (JSON body with the email and password), which answers a service ticket |
| Token | The ticket is exchanged at `https://diauth.garmin.com/di-oauth2-service/oauth/token` for an OAuth2 bearer |
| Calls | `Authorization: Bearer <token>` on `https://connectapi.garmin.com` |
| Expired token | The server signs in again with the stored password and retries once |
| Refused password | The connection is marked `needs_reauth` |
| Blocked by Garmin (HTTP 403, captcha, two-factor) | The sign-in fails; the connection does not complete |

## 3. How data arrives

The connector pulls the Connect service endpoints the website reads, for the signed-in user:

- `GET /usersummary-service/v2/dailysummaries?startDate=&endDate=` over the window, one object per
  day;
- `GET /sleep-service/sleep/dailySleepData?date=` per day;
- `GET /hrv-service/hrv/{date}` per day;
- `GET /activitylist-service/activities/search/activities?start=&limit=&startDate=&endDate=` over
  the window, paged.

Requests are paced (one a second by default) so a backfill does not hammer the site. Garmin Connect
has no notifications for other apps: new data arrives with the regular pulls. A day Garmin
re-scores is pulled again later and replaces the stored copy.

**Rate limits.** Garmin publishes none for this API. HTTP 429 backs off for the time its
`Retry-After` header asks (one minute without one), for every Garmin Connect connection of the
instance.

**Backfill.** The first pull covers the last 30 days. A wider pull covers at most 90 days.

## Data produced

| Garmin Connect field | Canonical records |
|---|---|
| Daily summary `totalSteps`, `totalDistanceMeters`, `activeKilocalories` + `bmrKilocalories`, `floorsAscended`, `restingHeartRate` | `DailySummary` per day and the matching day-total samples (`step_count`, `distance_walking_running`, `active_energy_burned`, `basal_energy_burned` and their sum as `total_energy_burned`, `flights_climbed`, `resting_heart_rate`) |
| Daily summary `highStressDuration` | `stress_high_time` (seconds, stored in minutes). `averageStressLevel`, `maxStressLevel`, `minHeartRate`, `maxHeartRate` and the intensity minutes stay in the summary metadata |
| Sleep `dailySleepDTO`: `sleepTimeSeconds`, `deepSleepSeconds`, `lightSleepSeconds`, `remSleepSeconds`, `awakeSleepSeconds`, start and end timestamps | `SleepSession` per night (bed time, total and per-stage seconds) |
| Sleep `sleepScores.overall.value`, `avgSleepHRV`, `averageSpO2Value`, `averageRespirationValue` | The session's sleep score (0 to 100), HRV average (ms), SpO2 average and respiratory rate; `avgSleepHRV` also becomes one nightly `hrv_rmssd` sample |
| Sleep `sleepLevels` (`startGMT`, `endGMT`, `activityLevel`) | `SleepPhase` rows. The stage of `activityLevel` follows the common community reading (0 deep, 1 light, 2 REM, 3 awake); Garmin publishes no specification for it, so an unknown value is not stored as a phase while the stage-second totals above stay authoritative **(to confirm)** |
| HRV `hrvSummary.lastNightAvg` | One `hrv_rmssd` sample (ms) over the night |
| Activity `activityId`, `activityName`, `activityType.typeKey`, `startTimeGMT`, `duration`, `distance`, `calories`, `averageHR`, `maxHR`, `averageSpeed`, `elevationGain` | `Workout` with the canonical activity type, duration, distance, energy, heart rate, speed and elevation gain. The device is named from `deviceId` |

Readings outside plausible ranges (a heart rate of 0) are skipped. An activity type Garmin names
that has no canonical match is stored as `other`, with the Garmin key kept in metadata.

Not pulled: per-minute heart rate, stress and SpO2 series, Body Battery, training readiness,
weight and body composition, GPS routes, and the structured-workout library. The sibling
[Garmin Health API connector](garmin.md) covers some of these for partner apps.

**Provenance.** Every record carries `provider: garmin_connect` and the source name `Garmin`, and
the raw payload is kept. In FHIR output each Observation has a
[Provenance](../fhir.md#provenance) naming Garmin, like any other source.

## Points to confirm

From python-garminconnect, not from Garmin. If your account shows something else, open an
[issue](https://github.com/soma-intelligence/open-health-sync/issues) with the shape (never the
values):

- The `sleepLevels` `activityLevel` to stage mapping.
- Whether an account with two-factor verification can connect.
- The bearer token lifetime, and any rate limit.
- That the daily-summary, sleep, HRV and activity shapes above match Garmin's current responses.

## Testing

`go test ./connectors/garminconnect/...` runs the sign-in, the pulls and the mapping against
`connectors/garminconnect/garminconnectfake`, an httptest fake answering from invented data. The
sign-in page is covered by `internal/collect/signin_integration_test.go`.

## Sources

Checked on 2026-10-18.

- [cyberjunky/python-garminconnect](https://github.com/cyberjunky/python-garminconnect) (MIT): the
  library whose sign-in flow (`garminconnect/client`) and endpoints this connector follows.
