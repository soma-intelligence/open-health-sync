# React Native SDK

`open-health-sync-react-native` has two parts:

- **A TypeScript client** for the [REST API](../api.md). It runs in React Native 0.72+ (either of
  its JavaScript engines), Expo, Node 18+ and browsers. It calls the global `fetch`, depends on
  nothing but `fflate` (gzip), and has no React dependency, so the same code works in an app, in
  a Node backend and in tests.
- **A native module**, `open-health-sync-react-native/health`, that reads HealthKit on iOS and
  Health Connect on Android and uploads what changed, in the foreground and in the background.
  It wraps the [Swift SDK](swift.md#healthkit-sync) and the
  [Kotlin SDK](kotlin.md#health-connect-sync), so the mapping of samples to upload lines is
  theirs, not rewritten in JavaScript. It needs a native build (not Expo Go) and the New
  Architecture: see [Health store sync](#health-store-sync).

The source is in [`sdks/react-native`](../../sdks/react-native). It is AGPL-3.0-only, like the
server ([LICENSE](../../LICENSE)).

What all the SDKs share (routes, paging, errors, the part builder, which credential goes where)
is in the [SDK overview](README.md). This page covers what is specific to TypeScript.

## Install

Nothing is published yet, and the package is marked `private` so it cannot be published by
accident. Build it from this repository and install it from the folder:

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git
cd open-health-sync/sdks/react-native
npm install && npm run build
cd /path/to/your/app
npm install /path/to/open-health-sync/sdks/react-native
```

`package.json` has a `react-native` field and export condition pointing at `src/index.ts`, so
Metro bundles the TypeScript source directly; Node and web bundlers use the ESM build in
`dist/` with its type declarations. With a local folder install, Metro may need the folder in
`watchFolders` of `metro.config.js`.

## Create a client

```ts
import { OhsClient } from "open-health-sync-react-native";

const client = new OhsClient({
  baseUrl: "https://ohs.example.org",
  // Called before every request: ask your backend for an upload-scope user token,
  // cache it until it expires.
  tokenProvider: async () => {
    const res = await fetch("https://api.your-app.example/ohs-token", { credentials: "include" });
    const { token } = await res.json();
    return token;
  },
});
```

On your backend, give `token: process.env.OHS_KEY` (the tenant key) instead. Options:

| Option | Default | |
|---|---|---|
| `baseUrl` | required | Base URL of the instance |
| `token` or `tokenProvider` | one is required | Bearer credential, fixed or fetched before each request |
| `fetch` | global `fetch` | Any `fetch` compatible function; tests pass a fake |
| `userAgent` | `open-health-sync-react-native/0.1.0` | Browsers may drop this header |
| `sleep` | `setTimeout` | Wait between upload retries; tests pass a fake |
| `maxUploadAttempts` | 5 | Attempts per upload part, retries included |

**Which credential.** Of the routes this SDK calls, a user token (`ohs_ut_`) can call one only:
`POST /v1/ingest/apple-health`, with the `upload` scope. Every other one (users, tokens,
reads, coverage, freshness, FHIR, connections and `startConnection`) needs the tenant key and
answers 403 to a user token. So the work splits in two:

- **Your backend** holds the tenant key and runs this same package under Node: it creates users,
  mints upload tokens, reads data, polls coverage and starts connections.
- **The app** holds only an upload-scope user token, minted by your backend, and uploads parts
  with it. It gets data and authorize URLs from your backend, through your own API.

The read and connection sections below therefore show backend code.

## Read data

Reads run on your backend, with the tenant key. Every list method returns one page,
`{ data, nextCursor }`. Pass `nextCursor` back as `cursor`,
or let `paginate` walk the pages:

```ts
import { OhsClient, paginate, parseTimestamp } from "open-health-sync-react-native";

const backend = new OhsClient({ baseUrl: "https://ohs.example.org", token: process.env.OHS_KEY! });

const first = await backend.listSamples(userId, { type: "heart_rate", start: "2026-01-10", limit: 500 });

for await (const s of paginate((cursor) =>
  backend.listSamples(userId, { type: "heart_rate", start: "2026-01-10", cursor }),
)) {
  console.log(s.id, parseTimestamp(s.startDate));
}
```

`collectAll(fetchPage)` gathers every record into an array. `start` and `end` take an RFC 3339
or `YYYY-MM-DD` string, sent as is, or a `Date`, sent as RFC 3339 UTC.

| Method | Route |
|---|---|
| `listProviders()` | `GET /v1/providers` |
| `createUser({ externalRef?, idempotencyKey? })`, `listUsers({ cursor?, limit? })`, `getUser(id)` | users |
| `deleteUser(id)` (returns the job id), `getDeletion(jobId)` | user deletion |
| `mintUserToken(id, scopes, ttlSeconds?)` | `POST /v1/users/{id}/tokens` |
| `listConnections(id)`, `getFreshness(id)`, `startConnection(id, provider, redirectUri?)`, `deleteConnection(id, provider)` | connections |
| `listSamples(id, { type, ...range })`, `listCategorySamples(id, { type?, ...range })` | samples |
| `listSleep(id, range)`, `listSleepPhases(id, sessionId, range)` | sleep |
| `listWorkouts(id, { type?, ...range })`, `getWorkoutRoute(id, workoutId, range)` | workouts |
| `listNutrition(id, range)`, `listDaily(id, range)` | nutrition, daily summaries |
| `getCoverage(id, { provider?, start?, end? })` | coverage |
| `searchFhirObservations(id, { type?, code?, category?, ...range })`, `readFhirObservation(id, observationId)` | FHIR R4 |
| `uploadAppleHealthPart(part)`, `uploadParts(parts)` | `POST /v1/ingest/apple-health` |

Records have the fields of the OpenAPI schemas in camelCase (`start_date` becomes `startDate`).
`metadata`, `nutrients` and coverage `metrics` are kept as the server sent them, keys
included. Fields the server adds later are left out, not an error.

**Timestamps stay strings.** Time fields are RFC 3339 strings as the server writes them, up to
nine fractional digits. A `Date` holds milliseconds only, and React Native's default engine does not parse more than
three fractional digits reliably, so nothing is converted for you. `parseTimestamp(s)` trims
the fraction to milliseconds and returns a `Date`. `date` fields (daily summaries, coverage)
are `YYYY-MM-DD` strings.

**Ids** are int64 on the server and `number` here, exact up to 2^53 - 1.

**FHIR.** `searchFhirObservations` returns `{ raw, observations, nextCursor }`: the Bundle as
sent, its Observation resources, and the `cursor` of its `next` link (null on the last page).
It sends `Accept: application/fhir+json`. See [FHIR](../fhir.md) for the mapping.

## Connect a provider

Your backend starts the connection with the tenant key and returns the URL to the app through
your own API; the app opens it in the browser:

```ts
// On your backend (tenant key)
const url = await backend.startConnection(userId, "oura", "myapp://ohs/connected");

// In the app, with the URL your backend returned
import { Linking } from "react-native";
await Linking.openURL(url);
```

After consent the browser lands on `myapp://ohs/connected?status=connected&provider=oura` (or
`status=error`). Without a `redirectUri` the server shows its own result page.
`listConnections` and `getFreshness` tell how each connection is doing.

## Upload

The upload route takes one gzip NDJSON part per call
([contract](../connectors/apple-health.md#upload-contract)). Each line is a JSON object in the
[line format](../connectors/apple-health.md#line-formats). `AppleHealthPartBuilder` groups lines
into parts by type and local day, splits them at 8 MiB of NDJSON, numbers them across the
batch and gzips them:

```ts
import { AppleHealthPartBuilder, localDay, newBatchId } from "open-health-sync-react-native";

const builder = new AppleHealthPartBuilder(newBatchId("app"));
const start = new Date("2026-01-10T14:30:00Z");
builder.add("HKQuantityTypeIdentifierHeartRate", localDay(start, "Europe/Paris"), {
  quantityType: "HKQuantityTypeIdentifierHeartRate",
  startDate: start.toISOString(),
  endDate: start.toISOString(),
  value: 72,
  unit: "count/min",
  metadata: { HKTimeZone: "Europe/Paris" },
});

// In the app, with the upload-token client from "Create a client"
const results = await client.uploadParts(builder.build()); // [{ key, duplicate }]
```

- `localDay(instant, timeZone)` gives the `YYYY-MM-DD` day in an IANA zone. Use the zone you put
  in `HKTimeZone`, so the part's day matches the day the server files the sample under.
- `newBatchId(prefix)` gives `prefix-<UTC time>-<10 random characters>`. Use a new one per run;
  re-sending the same parts with the same batch is safe (`duplicate: true`).
- `build()` returns parts with `type`, `day`, `batch`, `partNumber`, `body` (gzip bytes, a
  `Uint8Array`), `lineCount` and `uncompressedBytes`. Invalid batch, type or day throws a
  `RangeError`.

Processing is asynchronous. Your backend polls `getCoverage(userId)` until `pendingJobs` is 0,
then `rejectedRecords` above 0 means some lines were not stored.

## Health store sync

`open-health-sync-react-native/health` reads the device's health store and uploads what changed
since the last run. One API on both platforms:

```ts
import { healthSync } from "open-health-sync-react-native/health";

if (await healthSync.isAvailable()) {
  // Once, and again whenever your backend mints a new upload token.
  await healthSync.configure({ baseUrl: "https://ohs.example.org", userToken });

  await healthSync.requestAuthorization(["steps", "heartRate", "sleep"], { background: true });

  const report = await healthSync.sync(["steps", "heartRate", "sleep"]);
  if (!report.complete) console.log(report.uploadError?.code, report.partsPending, report.pendingTypes);

  await healthSync.enableBackgroundSync({ types: ["steps", "heartRate", "sleep"] });
}
```

It is a separate entry point because it imports React Native; the main entry stays usable in
Node and in browsers. Underneath, iOS runs the Swift SDK's
[`HealthKitSync`](swift.md#healthkit-sync) and uploads to `POST /v1/ingest/apple-health`;
Android runs the Kotlin SDK's [`HealthConnectSync`](kotlin.md#health-connect-sync) and uploads
to `POST /v1/ingest/health-connect`. Those pages describe what a sync reads and sends; this one
covers what the React Native layer adds.

### Requirements

- React Native 0.80 or later with the New Architecture. The module is a Turbo Native Module with
  a codegen spec ([`src/health/NativeOpenHealthSync.ts`](../../sdks/react-native/src/health/NativeOpenHealthSync.ts))
  and has no old-architecture fallback. It was built and run with 0.88.1; earlier versions are
  not tested.
- iOS 15.1 or later, Android API 26 or later (Health Connect's floor).
- A native build. In Expo that is a development build (`expo prebuild`, `expo run:ios`), not
  Expo Go, where `healthSync.isAvailable()` answers `false` and every other call rejects with
  `not_linked`.

**Autolinking.** Installing the package links the native module into every app, also one that
only uses the HTTP client. Such an app can leave it out with a `react-native.config.js`:

```js
module.exports = {
  dependencies: { "open-health-sync-react-native": { platforms: { ios: null, android: null } } },
};
```

### Where the native SDKs come from

Neither native SDK is on a registry yet, so each build takes it from a checkout of this
repository.

**iOS.** The Swift SDK is a Swift package (the `Package.swift` at the root of the repository),
not a pod. The podspec adds its `OpenHealthSync` and `OpenHealthSyncHealthKit` libraries with
React Native's `spm_dependency`, from the first of these that applies when `pod install` runs:

1. `OHS_SWIFT_SDK_PATH`: the root of a checkout on disk.
2. The checkout the package was installed from. `npm install /path/to/sdks/react-native` links
   the folder into `node_modules`, and the podspec finds the repository root two levels up.
   Nothing to set in that case.
3. The repository URL, at the version in `OHS_SWIFT_SDK_VERSION`, else at the branch in
   `OHS_SWIFT_SDK_BRANCH` (`main` when neither is set). This is the path for a copy of the
   package that no longer sits in a checkout; it needs a branch or tag that has the root
   `Package.swift`.

React Native prints a warning about Swift packages with static linking. The build below used
the default static linking and linked.

**Android.** The module depends on `org.openhealthsync:open-health-sync-health-connect:0.1.0`.
Publish it, and the client it depends on, to a Maven repository on disk with the init script the
package ships (it changes nothing in `sdks/kotlin`):

```sh
cd open-health-sync/sdks/kotlin
ANDROID_HOME=~/Library/Android/sdk ./gradlew \
  -I ../react-native/android/local-maven.init.gradle publishAllPublicationsToOhsLocalRepository
```

That fills `sdks/react-native/android/local-maven/` (not committed), where the module's
`build.gradle` looks by default and which it adds to the app's repositories for the
`org.openhealthsync` group. To keep the repository elsewhere, publish with
`-Pohs.localMavenDir=/some/dir` and give the app `ohs.sdkMavenRepo=/some/dir` in
`gradle.properties` (or `OHS_SDK_MAVEN_REPO`). An app whose `settings.gradle` forbids project
repositories (`FAIL_ON_PROJECT_REPOS`) declares that Maven repository there itself.

### Set up the app

With Expo, add the config plugin to `app.json` and run `expo prebuild`:

```json
{
  "plugins": [
    [
      "open-health-sync-react-native",
      {
        "healthShareUsageDescription": "Your steps, heart rate and sleep are sent to your care team.",
        "types": ["steps", "heartRate", "sleep"]
      }
    ]
  ]
}
```

| Option | Default | |
|---|---|---|
| `healthShareUsageDescription` | a generic sentence | iOS: shown when access is asked. Write your own |
| `backgroundDelivery` | `true` | iOS: add the background delivery entitlement |
| `types` | every type Android has | Android: the types whose read permission is declared |
| `backgroundRead` | `true` | Android: declare `READ_HEALTH_DATA_IN_BACKGROUND` |
| `history` | `false` | Android: declare `READ_HEALTH_DATA_HISTORY` |
| `rationaleActivity` | `.MainActivity` | Android: the activity Health Connect opens to explain the permissions |

Without Expo, do the same by hand.

**iOS.**

1. In Xcode, add the HealthKit capability to the app target (the `com.apple.developer.healthkit`
   entitlement) and, for background sync, tick *Background Delivery* under it
   (`com.apple.developer.healthkit.background-delivery`).
2. Add `NSHealthShareUsageDescription` to `Info.plist`. The module only reads, so
   `NSHealthUpdateUsageDescription` is not needed.
3. `cd ios && pod install`.

Nothing goes in `AppDelegate`: the module restarts HealthKit's observer queries itself at the
end of every launch, which is when HealthKit expects them.

**Android.**

1. Set `minSdkVersion` to 26 or higher in `android/build.gradle`.
2. In `AndroidManifest.xml`, declare the read permission of each type you sync (the
   [Kotlin page's table](kotlin.md#record-types) names them) and the screen that explains your
   use of the data, which Health Connect requires before it shows its permission screen:

```xml
<uses-permission android:name="android.permission.health.READ_STEPS" />
<uses-permission android:name="android.permission.health.READ_HEART_RATE" />
<uses-permission android:name="android.permission.health.READ_SLEEP" />
<!-- Only for background sync, and for data older than 30 days before the first grant: -->
<uses-permission android:name="android.permission.health.READ_HEALTH_DATA_IN_BACKGROUND" />
<uses-permission android:name="android.permission.health.READ_HEALTH_DATA_HISTORY" />

<application>
    <activity android:name=".MainActivity" android:exported="true">
        <!-- ... the filters already there ... -->
        <!-- Android 13 and lower -->
        <intent-filter>
            <action android:name="androidx.health.ACTION_SHOW_PERMISSIONS_RATIONALE" />
        </intent-filter>
    </activity>
    <!-- Android 14 and higher -->
    <activity-alias
        android:name="ViewPermissionUsageActivity"
        android:exported="true"
        android:permission="android.permission.START_VIEW_PERMISSION_USAGE"
        android:targetActivity=".MainActivity">
        <intent-filter>
            <action android:name="android.intent.action.VIEW_PERMISSION_USAGE" />
            <category android:name="android.intent.category.HEALTH_PERMISSIONS" />
        </intent-filter>
    </activity-alias>
</application>
```

   The rationale screen must show your privacy policy. `.MainActivity` works when your app shows
   it on that intent; a dedicated activity is the other way.

The module declares no permission itself, so the app never asks for more than it uses. It does
declare one `ContentProvider` that holds no data: Android creates it at process start, which is
how a background job finds the credentials in a process where React Native never ran. Nothing
goes in `MainApplication`.

### Data types

Types are named once, in [`src/health/types.ts`](../../sdks/react-native/src/health/types.ts),
with what backs each name on each platform. One list can be passed on both platforms: a type
the platform does not have is left out. Leave the list out to mean every type the server stores,
which on iOS is more than the names below (every quantity and category type of the Swift SDK's
table).

| `HealthDataType` | iOS (HealthKit) | Android (Health Connect) |
|---|---|---|
| `steps` | `StepCount` | `Steps` |
| `heartRate` | `HeartRate` | `HeartRate` |
| `restingHeartRate` | `RestingHeartRate` | `RestingHeartRate` |
| `heartRateVariability` | `HeartRateVariabilitySDNN` | `HeartRateVariabilityRmssd` |
| `oxygenSaturation` | `OxygenSaturation` | `OxygenSaturation` |
| `respiratoryRate` | `RespiratoryRate` | `RespiratoryRate` |
| `bodyTemperature` | `BodyTemperature` | `BodyTemperature` |
| `bloodPressure` | `BloodPressureSystolic`, `BloodPressureDiastolic` | `BloodPressure` |
| `bloodGlucose` | `BloodGlucose` | `BloodGlucose` |
| `weight` | `BodyMass` | `Weight` |
| `height` | `Height` | `Height` |
| `bodyFat` | `BodyFatPercentage` | `BodyFat` |
| `activeEnergy` | `ActiveEnergyBurned` | `ActiveCaloriesBurned` |
| `distance` | `DistanceWalkingRunning` | `Distance` |
| `sleep` | `SleepAnalysis` | `SleepSession` |
| `workouts` | workouts and their routes | `ExerciseSession` |
| `menstrualFlow` | `MenstrualFlow` | `MenstruationFlow` |
| `intermenstrualBleeding` | `IntermenstrualBleeding` | `IntermenstrualBleeding` |
| `nutrition` | every dietary quantity, water included | `Nutrition` |
| `hydration` | `DietaryWater` | `Hydration` |
| `totalEnergy` | no | `TotalCaloriesBurned` |
| `basalMetabolicRate` | no | `BasalMetabolicRate` |
| `basalEnergy` | `BasalEnergyBurned` | no |
| `walkingHeartRateAverage`, `vo2Max`, `flightsClimbed`, `distanceCycling`, `distanceSwimming`, `exerciseTime`, `standTime` | the quantity of that name | no |
| `bodyMassIndex`, `leanBodyMass`, `waistCircumference`, `basalBodyTemperature`, `wristTemperature` | the quantity of that name | no |
| `environmentalAudioExposure`, `headphoneAudioExposure`, `mindfulSession`, `audiogram` | the type of that name | no |
| `heartRhythm` | ECG recordings and heartbeat series | no |
| `stateOfMind` | iOS 18 and later | no |

`heartRateVariability` is SDNN on iOS and RMSSD on Android: two measures, each stored under its
own name. `supportedTypes("ios" | "android")` and `isTypeSupported(type, platform)` read the
table; `HEALTH_DATA_TYPES` is the table itself.

### API

| Call | |
|---|---|
| `healthSync.platform` | `"ios"`, `"android"`, or `null` where the module is not linked |
| `isAvailable()` | Whether the health store can be used. Never rejects |
| `availability()` | `"available"`, `"providerUpdateRequired"` (Android 13 and lower: install or update Health Connect) or `"unavailable"` |
| `configure({ baseUrl, userToken })` | Hands the credentials to the native side |
| `isConfigured()`, `clearCredentials()` | Whether credentials are kept; sign-out |
| `requestAuthorization(types?, { background?, history? })` | Shows the system permission screen |
| `sync(types?)` | Reads what changed and uploads it; resolves a `SyncReport` |
| `flush({ force? })` | Sends what an earlier sync left behind; resolves a `SyncReport` |
| `enableBackgroundSync({ types?, intervalMinutes?, requireUnmeteredNetwork?, frequency? })` | Keeps syncing while the app is closed |
| `disableBackgroundSync()` | Stops it; what is queued is kept |
| `pendingParts()` | Android: parts waiting in the queue on disk. Always 0 on iOS |
| `lastBackgroundSync()` | The result of the last background run, kept across restarts, or `null` |
| `addBackgroundSyncListener(listener)` | Called after each background run while JavaScript is alive; returns `{ remove() }` |

**Credentials.** `configure` takes the base URL and a user token with the `upload` scope, minted
by your backend. A tenant key (`ohs_sk_`) is refused before it reaches the native side. The
native side keeps both so a background run can upload without JavaScript:

- iOS: two Keychain items, readable after the first unlock since boot and never copied to a
  backup or another device.
- Android: the token encrypted with an AES-GCM key held by the Android Keystore, the
  ciphertext in the app's private preferences. The Kotlin SDK has no store of its own; this is
  the module's.

Neither side logs them, and no error message carries the token. When a token expires, uploads
answer 401: the report's `uploadError.code` is `unauthorized`, nothing is lost, and the next
`configure` with a fresh token lets `flush` or the next sync send what waited. Because a
background run cannot ask your backend for a token, mint upload tokens that live long enough,
and call `configure` each time the app opens.

`clearCredentials()` is the sign-out: it removes the credentials, stops background sync, and
forgets the queue (Android) and the sync positions (both), so the next user of the device
starts from a full read and nothing read for one user is uploaded as another.

**Authorization.** On Android the result lists the requested types the user granted, and
whether background and history reads were granted when asked (`null` when not asked; `false`
too when this device's Health Connect does not offer them). On iOS `granted` is `null`:
HealthKit never tells an app whether read access was granted, and a declined type just returns
nothing.

**The sync report** is one typed object on both platforms:

| Field | |
|---|---|
| `platform` | `"ios"` or `"android"` |
| `batchIds` | The batch ids used |
| `partsUploaded`, `duplicateParts` | Parts the server took; of those, the ones it already had |
| `partsPending`, `partsDropped` | Android: parts left in the queue; parts the server refused for good. 0 on iOS |
| `linesByType`, `deletionsByType` | Counts by the platform's own type name (`HKQuantityTypeIdentifierHeartRate`, `HeartRate`) |
| `deletionsPendingByType` | iOS: deletions kept until the server says it applies them |
| `skippedRecords` | Records the server would not store as they are |
| `typesWithoutPermission` | Android: types skipped because their permission is not granted |
| `expiredChangesTokenTypes` | Android: types read again over a window after a long gap |
| `pendingTypes` | iOS: type identifiers not synced because an upload failed before their turn |
| `unavailableTypes` | Requested types this OS version does not have |
| `uploadError` | `{ code, message, status?, requestId? }` or `null`; `code` is `network`, `unauthorized`, `forbidden`, `rate_limited`, `server`, `api` or `unknown` |
| `complete` | `true` when nothing is left behind |

An upload that fails does not reject `sync`: the report says what is left. A read that fails
does reject, with a `HealthSyncError` whose `code` is one of `not_linked`, `unavailable`,
`not_configured`, `invalid_argument`, `no_activity`, `in_progress`, `permission_denied`,
`health_store`, `storage`, `decode` or `unknown`.

**Offline.** The two platforms differ, because the two native SDKs do:

- Android has a queue on disk. A sync with no network reads Health Connect once, keeps the
  parts, and reports `partsPending`. `flush()` sends them when their retry wait is over;
  `flush({ force: true })` sends them now.
- iOS has no queue. An upload that fails leaves the type's anchor where it was, so the same
  samples are read again later, and the module remembers the types that did not get their turn
  (`pendingTypes`). `flush()` syncs those types again; `pendingParts()` is always 0.

On both, call `flush()` when the network comes back or the app comes to the foreground.

**Background sync.**

- iOS turns on HealthKit background delivery (`frequency`: `immediate` by default, `hourly`,
  `daily`, `weekly`; iOS decides in the end). The setting is remembered, and the observer
  queries are started again at every launch, including a launch in the background. It does not
  retry on its own after a failed wake: see [catching up](swift.md#healthkit-sync) on the Swift
  page, and call `flush()` or `sync()` when the app opens.
- Android schedules a WorkManager job every `intervalMinutes` (60 by default, 15 at least), with
  a network (`requireUnmeteredNetwork` for Wi-Fi only), across restarts. It reads only when the
  user granted background reads (`requestAuthorization(types, { background: true })`); without
  that grant a run still uploads the queue.

A listener only hears runs that happen while JavaScript is alive. A run in a process where
React Native never started is kept instead: read it with `lastBackgroundSync()` when the app
opens.

```ts
const subscription = healthSync.addBackgroundSyncListener((event) => {
  if (event.ok && event.report.uploadError?.code === "unauthorized") refreshUploadToken();
});
// later
subscription.remove();
```

### What was built and run

With React Native 0.88.1, Xcode 27.2 and the Android SDK (compile SDK 37), in a new app made by
the React Native CLI with this package installed from the folder:

- Android: codegen and `:open-health-sync-react-native:compileDebugKotlin`, then
  `:app:assembleDebug` (arm64-v8a), against the Kotlin SDK published to the local Maven
  repository as above. Not run on a device or an emulator.
- iOS: `pod install` (codegen, the Swift package added from the checkout), then `xcodebuild` of
  the app for the simulator. Run on a simulator: the module loads, `configure` writes the
  Keychain, `isConfigured`, `flush`, `pendingParts`, `lastBackgroundSync`, the listener and
  `clearCredentials` answer, and `sync` rejects with HealthKit's own error because the test app
  had no HealthKit entitlement.
- The Expo plugin was loaded through `@expo/config-plugins`; `expo prebuild` was not run.

Not verified: a sync that reads real samples and uploads them, on either platform; background
delivery on a device; the Health Connect permission screen; the repository-URL path of the
Swift package.

## Building upload lines yourself

The health module is the way to read a device. The client's part builder stays for data that
comes from somewhere else (your own sensors, an import): each line follows the
[line format](../connectors/apple-health.md#line-formats), and
[Upload](#upload) above sends the parts. The server also has `POST /v1/ingest/health-connect`
for Health Connect records in their own shape
([contract and line format](../connectors/health-connect.md)); the TypeScript client has no part
builder for it, the native module uploads to it through the Kotlin SDK.

## Errors and retries

A non-2xx answer throws `OhsApiError` with `status`, `title`, `detail`, `type`, `requestId`
(the body's `request_id`, else the `X-Request-Id` header) and `retryAfterSeconds`. A body that
is not JSON still gives the status. Quote `requestId` in a bug report. The message never holds
the token or a health value.

```ts
import { OhsApiError } from "open-health-sync-react-native";

try {
  await backend.getUser(userId);
} catch (e) {
  if (e instanceof OhsApiError && e.status === 404) {
    // gone, or not this tenant's
  } else {
    throw e;
  }
}
```

`uploadAppleHealthPart` retries 429, 503, other 5xx answers and network errors: it waits
`Retry-After` seconds when sent, else 1 s doubling up to 30 s, and stops after 5 attempts in
all. Any other 4xx throws at once (a 401 means the token expired: the token provider is called
again on the next request). Other methods do not retry. A 2xx answer of an unexpected shape
throws `OhsDecodeError`.

## Run the tests

Node 22 or later:

```sh
cd sdks/react-native
npm install
npm run lint   # tsc --noEmit, strict, source and tests
npm test       # compiles to dist-test/ and runs node:test
npm run build  # ESM and declarations in dist/
```

The tests use a fake `fetch`: they check methods, paths, query strings and headers, decode a
fixture of every record type, map errors, exercise the retries, walk pages, and gunzip the
built parts. The health sync tests use a fake native module (`test/fake-native.ts`): they check
the type table, the names and options handed to the native side, the decoding of reports,
authorization results and background events, and every error code. The Expo plugin's manifest
and plist changes are tested as plain functions.

The package does not install React Native: `types/react-native.d.ts` declares the few names
`src/health` uses, so `npm run lint` and `npm run build` work without it. What compiled against
a real React Native is listed under [What was built and run](#what-was-built-and-run).

## Run the example

[`example/quickstart.ts`](../../sdks/react-native/example/quickstart.ts) runs against a live
instance with a tenant key. It creates a user, mints a user token, uploads one heart rate line
with it, polls coverage until `pendingJobs` is 0 (at most 120 s), lists the user's `heart_rate`
samples and exits 1 unless there is exactly one. It prints no token and no health value.

```sh
cd sdks/react-native
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... npm run example
```
