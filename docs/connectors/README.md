# Connectors

A connector teaches Open Health Sync one source. Eleven are available: [Apple Health](apple-health.md),
[Withings](withings.md), [Oura](oura.md), [WHOOP](whoop.md), [Strava](strava.md), [Polar](polar.md),
[Garmin](garmin.md), [Fitbit](fitbit.md), [Suunto](suunto.md), [Ultrahuman](ultrahuman.md) and
[intervals.icu](intervals-icu.md).
Every other source below is **planned**: it is not built yet, and no code for it runs today. To build one, start with
[writing a connector](../writing-a-connector.md).

## Supported and planned sources

Status:

- **Available**: ships in this release.
- **Planned**: on the roadmap, not built yet.
- **Via Health Connect**: no server-side path we intend to use; the data would come through
  Android's Health Connect and the planned Kotlin SDK. Planned, not built yet.
- **No public API**: the vendor publishes no official API we can build on.

Integration paths, as checked in October 2026 (vendors change their programs, so check the
linked tracking issue or the vendor's developer site before you start):

- **Cloud API, OAuth**: the server pulls from the vendor's cloud with the user's consent, and
  may receive webhooks. A self-hosted instance needs its own developer app with the vendor.
- **Partner program**: a cloud API, but only for companies the vendor approves.
- **On-device**: the data lives on the phone (HealthKit on iOS, Health Connect or a vendor SDK
  on Android). A mobile app has to read it and upload it.

| Provider | Status | Integration path | Data types | Notes | Tracking |
|---|---|---|---|---|---|
| Apple Health | Available | On-device (HealthKit). Your iOS app uploads gzip NDJSON parts to `POST /v1/ingest/apple-health` | Quantities, categories, sleep, workouts and routes, nutrition; ECG and heartbeat series stored | No SDK ships yet: your app implements the [upload contract](apple-health.md). A Swift SDK is planned. `ohs import apple-health` loads a Health app export, history included. Data that other apps and devices write to HealthKit arrives this way too | |
| Withings | Available, not yet verified with a real account | Cloud API, OAuth 2.0, scheduled pulls and push notifications | Body measurements, activity, sleep, workouts, ECG | Bring your own Withings developer app ([guide](withings.md)) | |
| Oura | Available, not yet verified with a real account | Cloud API v2, OAuth 2.0, scheduled pulls, optional webhooks (updates and deletions) | Sleep and stages, sleep score, readiness, daily activity, stress, heart rate, SpO2, VO2 max, workouts, meditation and breathing sessions | Bring your own Oura API application ([guide](oura.md)). Personal access tokens were retired at the end of 2025. Gen3 and later rings need an active Oura Membership for API access. An app needs Oura's approval past its first 10 users | [#5](https://github.com/soma-intelligence/open-health-sync/issues/5) |
| Whoop | Available, not yet verified with a real account | Cloud API v2, OAuth 2.0, scheduled pulls and webhooks (recovery, sleep, workout) | Cycles (strain), recovery (score, resting heart rate, HRV as RMSSD, SpO2, skin temperature), sleep with stage durations, workouts, body measurements | Bring your own app from the WHOOP Developer Dashboard ([guide](whoop.md)). The public API has no raw heart rate series, no hypnogram and no ECG | [#6](https://github.com/soma-intelligence/open-health-sync/issues/6) |
| Ultrahuman | Available, not yet verified with a real account | Partnership API, OAuth 2.0, scheduled daily pulls (no webhooks) | Sleep with stages, heart rate, HRV (RMSSD), skin temperature and deviation, steps, resting heart rate, VO2 max; glucose with the M1 CGM | Not self-serve: Partnership API keys come through Ultrahuman partner onboarding ([guide](ultrahuman.md)). The personal-token API is not supported yet. Recovery, movement and metabolic scores stay raw only | |
| Garmin | Available, not yet verified with a real account | Partner program (Garmin Connect Developer Program: Health API), OAuth 2.0 with PKCE, pulls by upload window, ping or push notifications | Daily summaries, steps, distance, energy, floors, resting and continuous heart rate, 15 minute epochs, sleep with phases, workouts, body composition, HRV (RMSSD), SpO2, respiration, VO2 max, blood pressure, skin temperature deviation | For approved business developers only; some data needs a commercial license fee. New access requests have been paused since spring 2026, per Garmin's replies to applicants. Stress and Body Battery stay raw only; Garmin's own backfill request is not used yet ([guide](garmin.md)) | [#9](https://github.com/soma-intelligence/open-health-sync/issues/9) |
| Fitbit | Available, not yet verified with a real account | Cloud API (Google Health API v4), Google OAuth 2.0 (restricted scopes), scheduled pulls and signed webhook notifications | Activity (steps, distance, floors, active energy), heart rate, resting heart rate, HRV (RMSSD), SpO2, respiratory rate, sleep with stages, workouts, weight, body fat, height, VO2 max, blood glucose, temperature | Google is not onboarding new Google Health API projects yet. Bring your own Google Cloud project and OAuth client ([guide](fitbit.md)). Unverified apps: 100 users, 7 day refresh tokens while in Testing. The legacy Fitbit Web API (off 2026-10-30) is not used. Deletions are not propagated yet | [#9](https://github.com/soma-intelligence/open-health-sync/issues/9) |
| Polar | Available, not yet verified with a real account | Cloud API (AccessLink v3), OAuth 2.0, scheduled pulls and webhooks | Exercises, daily activity, sleep with hypnogram, Nightly Recharge, continuous heart rate, physical info | Self-serve: bring your own AccessLink client from `admin.polaraccesslink.com` ([guide](polar.md)); each user is registered after consent. The API only serves recent data (28 to 30 days for most types) | [#8](https://github.com/soma-intelligence/open-health-sync/issues/8) |
| Strava | Available, not yet verified with a real account | Cloud API v3, OAuth 2.0, scheduled pulls and webhook events | Activities as workouts (summary figures; no streams or routes yet) | Bring your own Strava API application ([guide](strava.md)). A new app serves 1 athlete, 10 after a self-upgrade, more after Strava's review. The 2026 API terms forbid AI training on API data and restrict intermediary platforms; third parties report a paid Strava subscription is now needed for API access | [#7](https://github.com/soma-intelligence/open-health-sync/issues/7) |
| intervals.icu | Available, not yet verified with a real account | Cloud API v1, OAuth 2.0, scheduled pulls and webhooks (activity, wellness) | Activities as workouts, keeping their upstream source (Garmin, Wahoo, Zwift, Coros, Suunto, Polar, uploads); daily wellness: weight, resting heart rate, HRV (RMSSD and SDNN), sleep score, steps, SpO2, respiration, body fat, VO2 max, blood pressure | Apply for an OAuth app at intervals.icu; the intervals.icu developer approves it ([guide](intervals-icu.md)). No refresh tokens: a revoked token means reconnecting. Activities synced from Strava are not available through the API | |
| Coros | Planned | Cloud API, OAuth 2.0; a partner API with webhooks for established platforms | Activities, health metrics, fitness assessments | Partner access is requested from COROS (api@coros.com); full docs are shared after registration | |
| Suunto | Available, not yet verified with a real account | Partner program (Suunto Cloud API), OAuth 2.0 with an API Zone subscription key, scheduled pulls and signed notifications | Workouts (summary figures); 24/7 sleep, recovery and daily activity stored raw only | Companies and organizations apply to Suunto's partner program and sign its API agreement; not for personal use. Bring your own API Zone app and subscription key ([guide](suunto.md)). No FIT samples, laps or routes yet | |
| Dexcom | Planned | Cloud API v3, OAuth 2.0 (scope `offline_access`) | Estimated glucose values (EGVs), events, calibrations, devices, alerts | Free sandbox; production beyond a few users needs Dexcom's approval. Separate hosts for the US, EU and Japan | |
| FreeStyle Libre, Omron | Planned | Not researched yet | Glucose; blood pressure | Contributions of research notes are welcome | |
| Health Connect | Planned | On-device (Android). Needs the planned Kotlin SDK in your Android app | Steps, heart rate, sleep, exercise, nutrition, body measurements and more | By default an app reads back only 30 days before its first permission grant; older data needs the history permission. Many Android apps write here, which is how Samsung Health, Fitbit and Zepp data can reach it | |
| Samsung Health | Via Health Connect | On-device only: the Samsung Health Data SDK (partner approval needed to distribute), or Health Connect, which Samsung Health writes to | Activity, sleep, heart rate, skin temperature | The older Samsung Health SDK for Android was deprecated on 2025-07-31. We plan to read Samsung Health data through Health Connect, which needs no Samsung partnership | |
| Google Fit | Deprecated by Google, use Health Connect (planned Kotlin SDK) | None to build on | | Google closed sign-ups on 2024-05-01 and supports the Fit APIs only until the end of 2026. It points Android apps to Health Connect and cloud apps to the Google Health API | |
| Zepp / Amazfit | Via Health Connect | On-device: the Zepp app writes to Health Connect on Android and to Apple Health on iOS | Steps, heart rate, sleep, workouts, calories | We found no official public API. On iPhone, what the Zepp app writes to HealthKit is ordinary Apple Health data | |
| Eight Sleep | No public API | None | | Eight Sleep publishes no official developer API. Community clients call the private app API, which is unsupported and can change without notice; we will not build on it | |

## Contributing a connector

The open connector issues are labeled
[`connector`](https://github.com/soma-intelligence/open-health-sync/issues?q=is%3Aissue%20is%3Aopen%20label%3Aconnector),
and the smaller ones
[`good first issue`](https://github.com/soma-intelligence/open-health-sync/issues?q=is%3Aissue%20is%3Aopen%20label%3A%22good%20first%20issue%22).
Read [writing a connector](../writing-a-connector.md) first, and use the
[connector request form](https://github.com/soma-intelligence/open-health-sync/issues/new/choose)
for a source that is not listed. Tests use recorded fixtures with invented values and a fake of
the provider API; no test calls a real provider.
