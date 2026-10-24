# Garmin connector

Slug `garmin`. Users connect their own Garmin Connect account over OAuth 2.0 with PKCE. Open
Health Sync pulls their Health API summaries on a schedule, and again when Garmin sends a
notification. You bring your own Garmin app keys: nothing is shared between instances.

Status: available, for the **business tiers** of Garmin's program (Starter and Enterprise, see
[Access](#access-who-can-get-keys)). It targets the ping/pull mode that Enterprise apps get; a
Starter app is push only and has no history, so only part of the connector applies to it. The
free **Personal API** that Garmin opened on 2026-10-21 is not supported yet: a connector for it
is planned, not built. The connector follows public sources: Garmin's OAuth 2.0 PKCE
specification, Garmin's API pages, and payload examples that third parties have published.
Tests run against a fake of the Garmin API built from them.

Statements that no public Garmin page confirms are marked **(to confirm)**, and figures that only
the press reports are marked **(reported)**. Research checked on **2026-10-24**. Garmin changes
its program without much notice, so check the sources at the end again before you apply.

## Access: who can get keys

On 2026-10-21 Garmin reopened the
[Connect Developer Program](https://developer.garmin.com/gc-developer-program/overview/) with
three tiers, after pausing new applications from spring 2026. From Garmin's overview page:

| | Personal | Starter (business) | Enterprise (business) |
|---|---|---|---|
| Who | One person, with their own Garmin Connect data | Organizations and individual developers starting out, after a [request](https://www.garmin.com/en-US/forms/StarterGarminConnectDeveloperAccess/) | Companies operating at scale |
| APIs | Activity (JSON and FIT), Health, Women's Health | Activity, Courses, Health, Training, Women's Health | The same, plus Golf |
| Delivery | Pull only | **Push only** | Push or ping/pull |
| Historical data | Not stated | **Not included** | One month or more |
| Users | Your own account | Limited | Unlimited |
| Support | Not stated | Not stated | Included |
| Key | Self-serve, created in [Garmin Connect settings](https://connect.garmin.com/app/settings/personalApiToken) | Business app keys | Business app keys |
| Works with this connector | **No** (planned connector, below) | Partly: push only, see [Starter apps](#starter-apps-push-only) | Yes, what the connector was written for |

**Fees.** Garmin's pages show no prices; the Enterprise column only says commercial terms are
tailored. The press reports that Starter is free up to about 1,000 users, that Enterprise has a
USD 1,000 entry fee credited against a bill per monthly active user, and that existing partners
move to Enterprise with billing from 2027-04-01 (**reported**, by
[DC Rainmaker](https://www.dcrainmaker.com/2026/10/garmin-connect-api-revamps-connect-developer.html)
among others). Garmin's earlier program pages also said some metrics need a license fee for
commercial use. Checking your license terms is your responsibility.

**Personal API.** Any Garmin user can create a key (expiring after 30, 90 or 180 days or one
year) and exchange it for a 24 hour token, with no refresh token
([Personal FAQ](https://developer.garmin.com/gc-developer-program/personal/personal-faq/),
[Personal API](https://developer.garmin.com/gc-developer-program/personal/personal-api)). Garmin
publishes an official Python client,
[garmin/connect-personal-api-python](https://github.com/garmin/connect-personal-api-python)
(Apache-2.0, created 2026-10-20). Garmin's FAQ says the Personal API "is not intended for
commercial applications serving multiple end users". A `garmin_personal` connector on this API,
for one person's own instance, is **planned, not built**. Until it lands, use the paths in
[Without business keys](#without-business-keys).

### Starter apps: push only

A Starter app gets data only as push notifications, from the day the user connects. For this
connector that means:

- Configure every summary type in **push** mode, pointing at `/inbound/garmin`, and set
  `OHS_GARMIN_INBOUND_TOKEN` (see [Trust](#notifications)): with push, the notification body is
  what gets stored.
- The **30 day pull after connect does not apply**: a Starter app has no historical data. The
  connector does not know the tier yet and still tries that pull and the scheduled pulls; expect
  them to fail or be skipped as not granted. A setting that turns them off for Starter is planned.
- Backfill requests need Enterprise.

### History: the 2026 pause

From spring 2026 Garmin stopped reviewing new access requests. Replies quoted on the
[Garmin developer forum](https://forums.garmin.com/developer/fit-sdk/f/discussion/435484/developer-onboarding-blocked/2054626)
said Garmin had "temporarily paused the review and approval of new API access requests", and
[the5krunner, 2026-09-14](https://the5krunner.com/2026/09/14/garmin-developer-api-access-paused/)
reported correspondence from 20 May 2026 about "a significant redesign and modernisation of the
API program". The 2026-10-21 relaunch ended the pause. Existing approved integrations kept working
throughout.

### Garmin's terms

Garmin's [developer program agreement](https://developerportal.garmin.com/sites/default/files/Garmin%20Connect%20Developer%20Program%20Agreement.pdf)
binds the company that accepts it. Read it before you sign. Points that shape how you run this
connector:

- **Your key is yours.** It may not be shared or transferred, which matches how this repository
  works: every instance registers its own app.
- **Downstream sharing needs Garmin's approval.** Section 5(o) of the copy we read forbids
  "developing a Licensee application programming interface to provide End User Data to
  third-party websites, software applications, platforms, services, or products that have not
  been approved by Garmin in writing". Open Health Sync serves data over REST, MCP, webhooks,
  exports and FHIR. Using it as your own backend is one thing; feeding Garmin data to other
  companies' apps through it is what this clause covers. Check with Garmin before you do. The
  press reports that Garmin's October 2026 revisions made this stricter (**reported**; the
  revised text is not public), and Intervals Pro closed on 2026-10-12 over it.
- **Consent and withdrawal.** Users give express consent and need an easy way to withdraw it.
  Disconnecting in Open Health Sync deletes the Garmin registration (below).
- **Attribution.** Garmin's [API brand guidelines](https://developer.garmin.com/downloads/brand/Garmin-Developer-API-Brand-Guidelines.pdf)
  ask for Garmin attribution next to the data, including in exports and in systems that receive
  it over an API. Every record keeps `provider: garmin` and the source name `Garmin`, and a
  workout keeps the device that recorded it.
- **AI processing.** The agreement asks for a transparency statement and explicit consent before
  Garmin data is processed by AI systems. If your users connect an agent through the
  [MCP server](../mcp.md), say so in your privacy policy.

### Can a self-hosted instance use it

This connector needs business keys (Starter or Enterprise), issued to an organization for its
own app; this repository cannot ship shared keys (every instance brings its own, as with
[Withings](withings.md)). So:

- A company with a Garmin business app configures its own keys on its own instance.
- An individual self-hosting for themselves can now get a free Personal API key, but this
  connector does not use it. The planned Personal API connector is the path for that case.

### Without business keys

Garmin owners can export their activity files (FIT) from Garmin Connect, one activity or the
whole account, and import them with `ohs import files`. That path uses no Garmin API: see
[activity files](activity-files.md). On iPhone, Garmin Connect writes some data to Apple Health,
which the [Apple Health](apple-health.md) path already receives. On Android, Garmin Connect
writes a limited set of data to Health Connect (**to confirm** which types), and the
[Health Connect](health-connect.md) path tags it with the source `garmin`.

## 1. Get your keys

In Garmin's developer portal, for your Starter or Enterprise app:

1. Enable the Health API (and the Activity API if you have it) for the app.
2. Set the OAuth **redirect URI** to the address `ohs doctor` prints:
   `<OHS_PUBLIC_URL>/oauth/garmin/callback`.
3. Copy the **consumer key** (client id) and **consumer secret** (client secret).
4. Set the notification URL of every summary type you enable (see
   [Notifications](#notifications)) to `<OHS_PUBLIC_URL>/inbound/garmin`, with
   `?token=<OHS_GARMIN_INBOUND_TOKEN>` added if you set one. On Enterprise, prefer **ping/pull**
   mode; a Starter app only has **push**.

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_GARMIN_CLIENT_ID=...
OHS_GARMIN_CLIENT_SECRET=...
# Optional: a long random secret that every notification URL carries as ?token=...
OHS_GARMIN_INBOUND_TOKEN=...
```

Or save the same keys from the [admin console](../console.md), with no restart. Keys set in the
environment win.

Restart, then run `ohs doctor`. It reports the connector as enabled and prints the callback URL:

```
ok    connector garmin
      garmin callback: https://ohs.example.org/oauth/garmin/callback
```

`CLIENT_ID` and `CLIENT_SECRET` are required. If either is missing, the connector stays off.
`INBOUND_TOKEN` is optional. `GET /v1/providers` lists `garmin` when the connector is on.

**Do not rotate the client secret while a user is mid-connect**: the PKCE code verifier is
derived from it (below), so a consent started under the old secret fails its exchange and the
user just connects again.

## 3. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/garmin \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url`. They sign in to Garmin Connect and tick the
permissions they share. After consent the user lands on your `redirect_uri` with
`status=connected`. A new connection is pulled over the last 30 days of uploads, then on the
regular schedule and on Garmin's notifications. On a Starter app (push only, no history) those
pulls do not apply: data arrives from Garmin's push notifications, from the day of connection.

### OAuth 2.0 with PKCE

From Garmin's [OAuth 2.0 PKCE specification](https://developerportal.garmin.com/sites/default/files/OAuth2PKCE_1.pdf)
(PDF, revised 2025-07-23):

| | |
|---|---|
| Authorize | `GET https://connect.garmin.com/oauth2Confirm` with `response_type=code`, `client_id`, `code_challenge`, `code_challenge_method=S256`, `redirect_uri`, `state` |
| Token | `POST https://connectapi.garmin.com/di-oauth2-service/oauth/token`, client id and secret in the form. The 2025-07-23 revision of the specification names `https://diauth.garmin.com/di-oauth2-service/oauth/token`; the connector still calls the older host, and moving to the new one is planned |
| Scopes | Not chosen by the app. The API sections are set when the app is created; the user ticks the permissions at consent (for example `HEALTH_EXPORT`, `ACTIVITY_EXPORT`, and the historical data permission) |
| Access token | The example response has `expires_in: 86400` (24 hours); the same document says elsewhere access tokens last three months. The connector trusts `expires_in` (**to confirm** which is right) |
| Refresh token | About 90 days (`refresh_token_expires_in: 7775998`); a new one comes with every refresh and replaces the old one |
| User id | `GET https://apis.garmin.com/wellness-api/rest/user/id`, read right after the exchange; notifications are matched to connections by it |
| Disconnect | `DELETE https://apis.garmin.com/wellness-api/rest/user/registration`, called when a user disconnects or is deleted |

How the connector keeps the PKCE verifier: Open Health Sync stores nothing per consent for a
connector, but it hands the OAuth `state` back at the callback. The verifier is derived from it:
`base64url(HMAC-SHA256(client_secret, "ohs-garmin-pkce:" + state))`, 43 characters, and the
challenge is its S256 hash. The state is random and single use and the secret never leaves the
server, so the verifier is as unguessable as a random one.

A refresh that Garmin refuses marks the connection `needs_reauth`, and the user connects again.
The FAQ says "All APIs in the Developer Program use OAUTH 2.0". Older integrations used OAuth
1.0a; this connector implements OAuth 2.0 only.

Disconnecting keeps the data already collected, unless the disconnect is
`DELETE /v1/users/{id}/connections/garmin?delete_data=true`, which erases everything held from
Garmin for that user (see [erasing one provider's data](../api.md#erasing-one-providers-data)).

## 4. How data arrives

### Pulls

The connector pulls `GET https://apis.garmin.com/wellness-api/rest/<type>` with
`uploadStartTimeInSeconds` and `uploadEndTimeInSeconds` and the user's bearer token. The window
is **upload time** (when the device synced to Garmin Connect), not the time the data describes,
and one request covers at most 24 hours, so a pull is cut in 24 hour windows: the 30 day pull
after a user connects is 30 requests per summary type (360 in all, paced at about 100 a minute).
A type the user did not grant (HTTP 400 or 403) is skipped for the rest of that pull. HTTP 401
marks the connection for reconnection (after one refresh attempt); HTTP 429 backs off.

These pull endpoints come from older public copies of the Health API specification. A
third-party developer guide states there is no general polling endpoint for new apps and that
data arrives only through notifications. Whether apps created today can still call the pull
endpoints is **to confirm**. If they cannot, push mode (below) delivers the data. Garmin's
overview page now lists ping/pull for Enterprise only: a Starter app is push only.

### Notifications

Garmin calls a URL per summary type, set once per app in Garmin's portal (not per user). Point
them all at `<OHS_PUBLIC_URL>/inbound/garmin`. The body is `{"<summaryType>": [items]}`; both
modes Garmin offers are handled:

- **Ping** (recommended): each item carries `userId` and a `callbackURL`. The connector reads
  only the `uploadStartTimeInSeconds` and `uploadEndTimeInSeconds` of the callback URL and queues
  a pull of that upload window, for that summary type only, with the user's own token. The
  callback URL itself is never fetched. Pings of one user and type in one body are merged.
- **Push**: each item carries the summary itself. Items are stored as received (one raw line per
  item) for every connection of their `userId`, then mapped like pulled data. Each item must be
  an object with a `userId` and a `summaryId`, or the whole notification is refused (401). A
  `userAccessToken` field (sent by older integrations) is removed before storing.
- `deregistrations` and `userPermissionsChange` are answered 200 and ignored: the next pull with
  a revoked grant marks the connection for reconnection, and a type no longer granted is
  skipped.
- Summary types the connector does not handle (activity details, Move IQ, women's health and
  others) are answered 200 and ignored.
- A body over 1 MiB is refused (413), and so is one with more than 5000 items. If Garmin batches
  more than that in push mode, use ping mode (**to confirm** how large Garmin's batches get).

The notification keys accepted are the summary type names below, plus `allDayRespiration`,
`pulseOx`, `bodyComposition` and `bloodPressure` as aliases (**to confirm** which spelling
Garmin uses for each).

**Trust.** Garmin does not sign notifications in any way we could find documented
(**to confirm**). A ping is harmless: at worst it makes the server pull a connected user's own
data again. A push body, however, is stored as data: anyone who knows a connected user's Garmin
user id could store invented summaries for that user. To close that, set
`OHS_GARMIN_INBOUND_TOKEN` to a long random value and add `?token=<value>` to every notification
URL in Garmin's portal; notifications without it are refused (401). Whether Garmin keeps a query
string on a notification URL is **to confirm**: check a first delivery. Or configure the app
for ping mode, where nothing is stored from the body.

Garmin retries a notification that does not get HTTP 200 quickly; the route answers at once and
pulls in the background. Data exists only after the user's device syncs with Garmin Connect.

### Backfill

After a user connects, the core pulls the last 30 days of **upload** windows. Data uploaded to
Garmin before that is not reached. Garmin also offers a backfill request per data type (results
arrive as notifications; the user must grant the historical data permission, off by default;
third parties report about one month per request and one backfill per type). Backfill and
historical data are Enterprise only: a Starter app has neither, so the 30 day pull does not apply
to it. The connector does not send backfill requests yet.

## 5. Rate limits

Garmin publishes no numbers on its public pages. A third-party integration reports 100 requests per minute
(**to confirm**, and it may differ between Starter and Enterprise). The connector paces its requests at about
100 a minute and backs off on HTTP 429 (`Retry-After` when Garmin sends it). A connection's
notifications pull one summary type each, so most traffic after the first 30 days is one or
two requests per sync of the user's device.

## Data produced

Field names come from Garmin payload examples published by third parties and older public
copies of the Health API specification (**to confirm** against Garmin's current
specification). Garmin times are Unix seconds plus an offset to local time
(`startTimeOffsetInSeconds`). Garmin gives no IANA zone name: every record keeps the offset as
`utc_offset_seconds` in metadata, and when it is a whole hour also a fixed `Etc/GMT±H` zone as
`timezone`, so the local day is right (daylight saving changes inside a record are not known).

| Garmin summary (raw data type) | Canonical records |
|---|---|
| Dailies (`dailies`) | `DailySummary` (`StepCount`, `DistanceM`, `ActiveKcal`, `TotalKcal` = active + BMR, `FloorsClimbed`, `RestingHR`); samples `step_count`, `distance_walking_running` (m), `active_energy_burned` (kcal), `basal_energy_burned` (kcal), `flights_climbed`, `resting_heart_rate` (bpm), all replaced while the day is open; `timeOffsetHeartRateSamples` as `heart_rate` (bpm). Stress figures stay in metadata |
| Epochs (`epochs`) | `step_count_intraday`, `distance_walking_running_intraday` (m), `active_energy_burned_intraday` (kcal) per 15 minute slice. Garmin sends one epoch per activity type of a slice; each is its own sample |
| Sleeps (`sleeps`) | `SleepSession` (deep, light, REM, awake seconds; total = deep + light + REM; `overallSleepScore.value` as score; averages of the night's SpO2 and respiration) and `SleepPhase` rows from `sleepLevelsMap` (`deep`, `light`, `rem`, `awake`) |
| Activities (`activities`) | `Workout` (duration, distance, active kcal, average and max heart rate, average speed, elevation gain), keyed by `activityId`, with `deviceName` as the device (`Garmin forerunner955`). `activityType` goes through a mapping table (`RUNNING` → `running`, `LAP_SWIMMING` → `swimming`, ...); an unknown type is `other`, with Garmin's name in metadata |
| Body compositions (`bodyComps`) | `body_mass`, `muscle_mass`, `bone_mass` (grams divided by 1000, stored in kg), `body_fat_percentage` (percent), `body_mass_index` (kg/m2). Body water arrives as a percent while `water_mass` is stored in kg: kept in metadata only |
| HRV (`hrv`) | `lastNightAvg` as `hrv_rmssd` (ms): Garmin's value is RMSSD, never stored as `hrv_sdnn`. The five minute values stay raw |
| Pulse ox (`pulseox`) | `timeOffsetSpo2Values` as `oxygen_saturation` (percent) |
| Respiration (`respiration`) | `timeOffsetEpochToBreaths` as `respiratory_rate` (stored unit `bpm`, breaths per minute) |
| User metrics (`userMetrics`) | `vo2Max` as `vo2_max` (mL/kg/min) over the calendar date; fitness age in metadata |
| Blood pressure (`bloodPressures`) | `blood_pressure_systolic`, `blood_pressure_diastolic` (mmHg), `pulse` as `heart_rate` (bpm) |
| Skin temperature (`skinTemp`) | `avgDeviationCelsius` as `body_temperature_deviation` (celsius) |
| Stress details, Body Battery (`stressDetails`) | **Raw only**: no canonical names for stress or Body Battery |

Not pulled: activity details and FIT files (GPS tracks), Move IQ, health snapshots, women's
health, enhanced beat-to-beat intervals. Values outside plausible ranges (a heart rate of 0, an
SpO2 of 12 percent) are skipped.

**Provenance.** Every record carries `provider: garmin` and the source name `Garmin`, and the raw
summary is kept (without any access token it carried). In FHIR output each Observation has a
[Provenance](../fhir.md#provenance) naming Garmin, like any other source.

## Points to confirm

Garmin's partner specification is behind its developer login, so these come from public and
third-party sources. If your Garmin app shows something else, open an
[issue](https://github.com/soma-intelligence/open-health-sync/issues):

- That apps created today can call the upload-window pull endpoints, and the 24 hour limit.
- The notification keys, item fields and summary field names listed above.
- That notifications reach a URL with a `?token=` query string.
- Which access token lifetime applies, and that refresh tokens rotate as the PDF shows.
- The status Garmin answers for a summary type the user did not grant (400 or 403).
- The rate limits of Starter and Enterprise apps.
- Which summary types each tier gets: the public Health API page no longer names HRV, skin
  temperature or user metrics summaries, which the connector still requests.

## Sources

Checked on 2026-10-24.

- Garmin, [Connect Developer Program overview](https://developer.garmin.com/gc-developer-program/overview/) (tiers)
- Garmin, [Program FAQ](https://developer.garmin.com/gc-developer-program/enterprise/program-faq/)
- Garmin, [Health API](https://developer.garmin.com/gc-developer-program/enterprise/health-api/)
- Garmin, [Activity API](https://developer.garmin.com/gc-developer-program/enterprise/activity-api/)
- Garmin, [Personal API](https://developer.garmin.com/gc-developer-program/personal/personal-api) and [Personal FAQ](https://developer.garmin.com/gc-developer-program/personal/personal-faq/)
- Garmin, [connect-personal-api-python](https://github.com/garmin/connect-personal-api-python), the official Personal API client (Apache-2.0)
- Garmin, [OAuth 2.0 PKCE specification, 2025-07-23 (PDF)](https://developerportal.garmin.com/sites/default/files/OAuth2PKCE_1.pdf)
- Garmin, [Connect Developer Program Agreement (PDF)](https://developerportal.garmin.com/sites/default/files/Garmin%20Connect%20Developer%20Program%20Agreement.pdf)
- Garmin, [Developer API brand guidelines (PDF)](https://developer.garmin.com/downloads/brand/Garmin-Developer-API-Brand-Guidelines.pdf)
- DC Rainmaker, [Garmin Connect API revamp](https://www.dcrainmaker.com/2026/10/garmin-connect-api-revamps-connect-developer.html), October 2026 (fees, reported)
- the5krunner, [Garmin API and third-party apps](https://the5krunner.com/2026/10/14/garmin-api-intervals-third-party-apps/), 2026-10-14 (downstream sharing, Intervals Pro)
- Garmin developer forum, [Developer onboarding blocked](https://forums.garmin.com/developer/fit-sdk/f/discussion/435484/developer-onboarding-blocked/2054626) (the 2026 pause)
- the5krunner, [Garmin Freezes Developer API](https://the5krunner.com/2026/09/14/garmin-developer-api-access-paused/), 2026-09-14
- Published documentation of third-party Garmin integrations (notification shapes, backfill
  range, payload examples, rate limits)
