# Garmin: research notes

Status: **planned, not built**. No Garmin code ships in this release. This page collects what a
contributor needs to know before writing the connector, as checked on **2026-10-11**. Garmin
changes its program without much notice: check the sources below again before you start.

Statements we could not confirm from a public Garmin page are marked **(unverified)**.

## Summary

| | |
|---|---|
| Program | [Garmin Connect Developer Program](https://developer.garmin.com/gc-developer-program/overview/): Health API, Activity API, Women's Health API (data out of Garmin Connect); Training and Courses APIs (data into it) |
| Who can join | Businesses only, after Garmin approves an application |
| Can a new developer join today | **No, in practice.** Garmin has paused the review of new access requests since spring 2026, with no reopening date (reported by developers and the press, not announced publicly by Garmin) |
| OAuth | OAuth 2.0 authorization code with PKCE (S256) |
| Delivery | Garmin calls you: "ping/pull" (a notification with a callback URL to fetch) or "push" (the data in the request body), chosen per app |
| Backfill | Requested through the API after consent; the user must grant the historical data permission |
| Cost | No program fee; some metrics need a license fee for commercial use |
| Recommendation | **Wait.** Do not build until Garmin accepts new applications and someone with an approved app can test against it |

## Access: what you need to do

1. **Be a business.** The [program FAQ](https://developer.garmin.com/gc-developer-program/program-faq/)
   says the program "is only for business use". There is no personal or hobbyist tier, and no
   personal access token.
2. **Apply** through the request form linked from the
   [overview page](https://developer.garmin.com/gc-developer-program/overview/). The FAQ still
   says Garmin confirms an application "within two business days" and that an integration
   "typically takes between 1 and 4 weeks". That text is out of date (see below).
3. **Build against an evaluation app.** An approved company gets an evaluation app with
   throttled access to production data, meant for internal development. Third parties report
   that Garmin may disable an evaluation app used for production
   ([Open Wearables docs](https://openwearables.io/docs/providers/garmin-api-integration.md)).
4. **Pass Garmin's partner verification** and request a production key before real users
   connect (same source; the exact checklist is behind the partner login, **unverified**).
5. **Check licensing.** The FAQ: "There are no licensing or maintenance fees for access ... Access
   to some metrics may require a license fee payment or minimum device order quantity for
   commercial use." The [Health API page](https://developer.garmin.com/gc-developer-program/health-api/)
   says "Commercial use requires a license fee payment". Which metrics, and how much, is not
   public (**unverified**).

### The 2026 pause

Garmin has not announced a pause on a public page: the overview only says "Stay tuned for more
updates on the program". The evidence is replies Garmin sent to applicants:

- A Garmin reply quoted on the
  [Garmin developer forum](https://forums.garmin.com/developer/fit-sdk/f/discussion/435484/developer-onboarding-blocked/2054626)
  (received in July 2026): "we have temporarily paused the review and approval of new API access
  requests". Another applicant was told in early August that there was no timeline.
- [the5krunner, 2026-09-14](https://the5krunner.com/2026/09/14/garmin-developer-api-access-paused/)
  reports correspondence from 20 May 2026 in which Garmin described "a significant redesign and
  modernisation of the API program" and said queued requests could not be processed; an
  application from March 2026 was still pending on 11 September.
- [Terra, 2026-09-07](https://tryterra.co/blog/garmin-connect-developer-program-pause) reports
  that the application form was replaced by an "Under Construction" notice. Existing approved
  integrations keep working; nothing says otherwise.

### Can a self-hosted instance use it

Only if the person running the instance is an approved Garmin business developer. Garmin keys are
issued to a company for its own app, and a connector in this repository cannot ship shared keys
(every instance brings its own, as with [Withings](withings.md)). So:

- A company that already has an approved Garmin app could configure its own keys on its own
  instance, once the connector exists.
- An individual self-hosting for themselves or their family cannot get keys today, and could not
  before the pause either (business use only).

Garmin owners who cannot get API access can still export activity files (FIT) from Garmin Connect
by hand; an upload path for FIT files is a separate idea, not covered here.

## Integration shape

### OAuth 2.0 with PKCE

From Garmin's [OAuth 2.0 PKCE specification](https://developerportal.garmin.com/sites/default/files/OAuth2PKCE.pdf)
(PDF, checked 2026-10-11):

| | |
|---|---|
| Authorize | `GET https://connect.garmin.com/oauth2Confirm` with `response_type=code`, `client_id`, `code_challenge`, `code_challenge_method=S256`, optional `redirect_uri` and `state` |
| Token | `POST https://connectapi.garmin.com/di-oauth2-service/oauth/token` |
| Scopes | Not chosen by the app. The API sections are set when the app is created; the user ticks the permissions at consent (for example `HEALTH_EXPORT`, `ACTIVITY_EXPORT`, and the historical data permission backfill needs) |
| Access token | The example response has `expires_in: 86400` (24 hours); the same document says elsewhere access tokens last three months. Treat 24 hours as the safe value (**unverified** which is right) |
| Refresh token | About 90 days (`refresh_token_expires_in: 7775998`); a new one comes with every refresh |
| User id | `GET https://apis.garmin.com/wellness-api/rest/user/id` |
| Granted permissions | `GET https://apis.garmin.com/wellness-api/rest/user/permissions`; changes arrive as a `userPermissionsChange` notification |
| Disconnect | `DELETE https://apis.garmin.com/wellness-api/rest/user/registration` |

A connector built from these notes gets erasure on disconnect for free: a disconnect with
`?delete_data=true` erases everything Open Health Sync holds from the provider for that user (see
[erasing one provider's data](../api.md#erasing-one-providers-data)). A deregistration
notification should lead to the same erasure.

The FAQ says "All APIs in the Developer Program use OAUTH 2.0". Older integrations used OAuth
1.0a; we found no public date for when OAuth 1.0a stops working (**unverified**). A new
connector should implement OAuth 2.0 only.

### Ping/pull or push

Both Garmin API pages offer a "Ping/Pull or Push Architecture" chosen per app in Garmin's
developer tools, with a callback URL per data type. From third-party integrations (Garmin's own
spec is behind the partner login, so this is **unverified** against it):

- **Ping**: Garmin posts `{"dailies": [{"userId": "...", "callbackURL": "https://apis.garmin.com/wellness-api/rest/dailies?..."}]}`.
  The server then fetches the callback URL with the user's token.
- **Push**: the same envelope with the summaries inline.
- Garmin retries a notification that does not get HTTP 200 quickly, so the handler must answer
  at once and work asynchronously, and processing must be idempotent.
- Garmin also notifies deregistrations and permission changes.
- Data exists only after the user's device syncs with Garmin Connect.

[Open Wearables](https://openwearables.io/blog/garmin-connect-api-developer-guide-activities-health-metrics)
states there is no general polling endpoint: you learn about new data only through
notifications. (Older versions of the Health API had pull endpoints filtered by upload time;
whether they are still open to new apps is **unverified**.)

### Backfill

Garmin offers a backfill request per data type after consent, and the results arrive through the
same notifications. The user must grant the historical data permission, which is off by default
on the consent screen; without it backfill requests get HTTP 403. Third parties report about one
month of history per backfill and that each type can usually be backfilled once
([Open Wearables](https://openwearables.io/docs/providers/garmin-api-integration.md)). The exact
maximum range is **unverified**.

### Mapping to this repository's interfaces

| Interface | Garmin | Fit |
|---|---|---|
| `OAuthConnector` | `AuthorizeURL`, `Exchange`, `Refresh` against the endpoints above. `Fetch` fetches ping callback URLs, or Garmin's pull endpoints if they remain available, and requests backfill on the first window | **Gap**: Garmin requires PKCE. `AuthorizeURL(state, redirect)` and `Exchange(ctx, code, redirect)` have no place to keep the code verifier between the two calls, and `Exchange` does not receive `state`. The core needs a small change (store a verifier with the OAuth state, or pass `state` to `Exchange`) |
| `PushConnector` | `HandleInbound` parses ping notifications into `InboundEvent`s (Garmin user id, data type, time window) | **Gap**: an `InboundEvent` only asks for a pull of a window. Ping callback URLs are short-lived and are not a time window; push bodies carry the data itself, which `HandleInbound` cannot store. Either extend the contract so an inbound handler can hand raw payloads to a `RawSink`, or keep a queue of callback URLs for `Fetch`. Decide this before writing code |
| `Subscriber` | Not needed | Garmin's callback URLs are set once per app in the developer portal, not per user. They should point at `<OHS_PUBLIC_URL>/inbound/garmin` |
| `PostConnectHook` | Request backfill after a user connects | |
| `Revoker` | `DELETE .../user/registration` on disconnect and user deletion | |
| `KeyedConnector` | `CLIENT_ID`, `CLIENT_SECRET` | As for Withings |

Garmin does not sign notifications in any way we could find documented (**unverified**). Third
parties check that the notification names a known Garmin user id and treat it only as a hint to
fetch, which matches how the Withings connector treats its unsigned notifications.

## Data types and the canonical model

Field names come from Garmin payload examples published by third parties
([Open Wearables webhook reference](https://www.mintlify.com/yocxy2/open-wearables/api/webhooks/garmin))
and from the data type lists on Garmin's API pages. Fields marked * we know from older public
copies of the Health API spec and could not check against the current one (**unverified**).
Garmin times are Unix seconds (`startTimeInSeconds`) plus an offset to local time
(`startTimeOffsetInSeconds`); Garmin gives no IANA zone name, so `Metadata["timezone"]` cannot be
set, and the offset should be kept in metadata. The ETL then falls back to UTC for the local day
unless the core learns to use a fixed offset.

| Garmin summary | Key fields and units | Canonical records |
|---|---|---|
| Dailies | `steps` (count), `distanceInMeters`, `activeKilocalories`, `bmrKilocalories`*, `floorsClimbed`*, `restingHeartRateInBeatsPerMinute`*, `averageStressLevel`* | `DailySummary` (`StepCount`, `DistanceM`, `ActiveKcal`, `TotalKcal` = active + BMR, `FloorsClimbed`, `RestingHR`); quantity samples `step_count`, `distance_walking_running` (m), `active_energy_burned` (kcal), `basal_energy_burned` (kcal), `flights_climbed`, `resting_heart_rate` (bpm), all `Replaceable` while the day is open. Stress has no canonical name yet |
| Dailies heart rate samples | `timeOffsetHeartRateSamples`* (bpm keyed by seconds from start) | `heart_rate` (bpm) quantity samples |
| Epochs | 15 minute slices: `steps`, `distanceInMeters`, `activeKilocalories`* | `step_count_intraday`, `distance_walking_running_intraday` (m), `active_energy_burned_intraday` (kcal) |
| Sleeps | `durationInSeconds`, `deepSleepDurationInSeconds`, `lightSleepDurationInSeconds`, `remSleepInSeconds`, `awakeDurationInSeconds`, `sleepLevelsMap`* (phase intervals), sleep score* | `SleepSession` (seconds map one to one) and `SleepPhase` rows (`deep`, `light`, `rem`, `awake`) |
| Activities | `activityType`, `durationInSeconds`, `distanceInMeters`, `activeKilocalories`, `averageHeartRateInBeatsPerMinute`, `maxHeartRateInBeatsPerMinute`, `averageSpeedInMetersPerSecond`*, `totalElevationGainInMeters`* | `Workout`. `activityType` needs a mapping table to canonical activity types |
| Activity details / FIT files | Samples and GPS track; FIT, GPX, TCX files ([Activity API](https://developer.garmin.com/gc-developer-program/activity-api/)) | `RoutePoint` rows; per-second heart rate as `heart_rate` |
| Body compositions | `weightInGrams`*, `bodyFatInPercent`*, `muscleMassInGrams`*, `boneMassInGrams`*, `bodyWaterInPercent`*, `bodyMassIndex`* | `body_mass` (kg, divide grams by 1000), `body_fat_percentage` (percent), `muscle_mass` (kg), `bone_mass` (kg), `body_mass_index` (kg/m2). Body water arrives as a percent; `water_mass` is stored in kg, so it needs the weight to convert or a new name |
| HRV | Nightly average `lastNightAvg`* (ms, RMSSD) | Garmin's value is RMSSD, not SDNN, so not `hrv_sdnn`. `SleepSession.HRVAvgMs` or `DailySummary.HRVAvgMs`, or a new quantity name |
| Pulse ox | `timeOffsetSpo2Values`* (percent) | `oxygen_saturation` (percent, 0 to 100) |
| Respiration | `timeOffsetEpochToBreaths`* (breaths per minute) | `respiratory_rate` (stored unit `bpm` means breaths per minute here) |
| Stress details, Body Battery | Stress 0 to 100, Body Battery 0 to 100 | No canonical names. Proposed: new quantity names with unit `index`, or keep raw only |
| Blood pressure | `systolic`*, `diastolic`* (mmHg), `pulse`* (bpm) | `blood_pressure_systolic`, `blood_pressure_diastolic` (mmHg), `heart_rate` |
| User metrics | `vo2Max`* (mL/kg/min), fitness age* | `vo2_max` |
| Skin temperature | Deviation from baseline in °C* | `body_temperature_deviation` (celsius) |
| Women's Health API | Menstrual cycle and pregnancy data | Category samples; needs a design decision, not in a first version |

Enhanced beat-to-beat intervals are listed on the Health API page. If they arrive as RR intervals
in milliseconds they map to `HeartbeatSample` (**unverified** format and license).

## Rate limits

Garmin publishes no numbers on its public pages; the Health API page says only "throttled access"
for evaluation apps. Open Wearables reports 100 requests per minute and budgets its backfill
inside it (**unverified**, and it may be the evaluation limit). Since data is mostly delivered by
Garmin calling us, request volume is mainly callback fetches and backfill. Handle HTTP 429 with
`connector.RateLimited` as usual.

## Recommendation

**Wait.** Three things block a useful connector today:

1. No one can get new keys: the program is paused for new applicants, and it never served
   individuals.
2. We cannot test against Garmin: the spec and sandbox are behind the partner login, and our
   fixture tests need real payload shapes.
3. The push model needs a contract change (inbound payloads, PKCE verifier) that should be
   designed with a real integration in hand.

Revisit when Garmin reopens applications, or when a company with an approved Garmin app offers to
test. Health Connect is not a way around it for most users: Garmin Connect on Android writes only
a limited set of data to Health Connect (**unverified** which types), and on iPhone Garmin Connect
writes some data to Apple Health, which the [Apple Health](apple-health.md) path already receives.

## Sources

All checked on 2026-10-11.

- Garmin, [Connect Developer Program overview](https://developer.garmin.com/gc-developer-program/overview/)
- Garmin, [Program FAQ](https://developer.garmin.com/gc-developer-program/program-faq/)
- Garmin, [Health API](https://developer.garmin.com/gc-developer-program/health-api/)
- Garmin, [Activity API](https://developer.garmin.com/gc-developer-program/activity-api/)
- Garmin, [OAuth 2.0 PKCE specification (PDF)](https://developerportal.garmin.com/sites/default/files/OAuth2PKCE.pdf)
- Garmin developer forum, [Developer onboarding blocked](https://forums.garmin.com/developer/fit-sdk/f/discussion/435484/developer-onboarding-blocked/2054626) (applicants quoting Garmin's replies)
- the5krunner, [Garmin Freezes Developer API](https://the5krunner.com/2026/09/14/garmin-developer-api-access-paused/), 2026-09-14
- Terra, [Garmin's Connect Developer Program is paused](https://tryterra.co/blog/garmin-connect-developer-program-pause), 2026-09-07
- Open Wearables, [Garmin integration docs](https://openwearables.io/docs/providers/garmin-api-integration.md) and [developer guide](https://openwearables.io/blog/garmin-connect-api-developer-guide-activities-health-metrics)
