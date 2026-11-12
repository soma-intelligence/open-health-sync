# Flutter SDK

Two packages, in one directory:

| Package | What it is | Runs on |
|---|---|---|
| `open_health_sync` ([`sdks/flutter`](../../sdks/flutter)) | The Dart client for the [REST API](../api.md): requests, typed records, upload parts. Plain Dart, no Flutter dependency: `http` for requests and `archive` for gzip | Wherever Dart 3.4+ runs: Flutter on iOS, Android, desktop and web, and Dart backends |
| `open_health_sync_health` ([`sdks/flutter/open_health_sync_health`](../../sdks/flutter/open_health_sync_health)) | A Flutter plugin that reads HealthKit and Health Connect and uploads what changed ([Health store sync](#health-store-sync)). It runs the [Swift](swift.md#healthkit-sync) and [Kotlin](kotlin.md#health-connect-sync) SDKs | Flutter 3.41+, iOS 15+, Android API 26+ |

They are separate so the client keeps working without Flutter: a plugin needs the Flutter SDK
to resolve its dependencies, and the client is also used by Dart servers and tested with the
Dart SDK alone. An app that only calls the API takes the first; an app that syncs the phone's
health store takes the second, and both when it does both. The client follows the shape every
SDK shares ([overview](README.md)). AGPL-3.0-only, like the server ([LICENSE](../../LICENSE)).

## Install

Nothing is published yet (`publish_to: none`). Add the package from this repository, by path or
by git:

```yaml
# pubspec.yaml of your app
dependencies:
  open_health_sync:
    path: ../open-health-sync/sdks/flutter
  # or
  open_health_sync:
    git:
      url: https://github.com/soma-intelligence/open-health-sync
      path: sdks/flutter
```

Then `flutter pub get` (or `dart pub get`). Everything is in one import:

```dart
import 'package:open_health_sync/open_health_sync.dart';
```

## Create a client

In an app, the client holds a user token with the `upload` scope that your server minted, never
the tenant key ([why](README.md#which-credential-goes-where)). On the REST API that token can
upload and nothing else: every other route answers 403 to it. Give it a token provider: it is
called before each request, so it can return a cached token and fetch a new one when it is about
to expire.

```dart
final uploads = OhsClient(
  baseUrl: 'https://ohs.example.org',
  tokenProvider: () => myBackend.currentUploadToken(), // your own Future<String> function
);
```

`OhsClient(baseUrl: ..., token: ...)` takes a fixed credential instead, which suits a Dart
backend holding the tenant key (`ohs` in the examples below), and tests. Give exactly one of the two. `baseUrl` is a `String` or a
`Uri` and may carry a path prefix. Other named arguments:

| Argument | |
|---|---|
| `httpClient` | an `http.Client` to send requests through (a `MockClient` in tests, a client with your own timeouts or proxy). `close()` closes only a client the SDK created itself. |
| `userAgent` | default `open-health-sync-flutter/0.1.0`; `null` sends none. Browsers may drop it. |
| `sleep` | the wait between upload retries, `Future.delayed` by default; tests pass one that returns at once. |

## Read data

Reads take the tenant key, so they run on your backend; the app gets the records from it. Every
list call returns one `Page<T>` (`data`, `nextCursor`). `start` and `end` take an RFC 3339
time or a `YYYY-MM-DD` date as a string, passed as is; the window is half open.
`formatTimestamp(DateTime)` turns a `DateTime` into an RFC 3339 UTC string.

```dart
final page = await ohs.listSamples(
  userId,
  type: 'heart_rate',
  start: '2026-01-10',
  end: formatTimestamp(DateTime.utc(2026, 1, 11)),
);
for (final s in page.data) {
  print('${s.startDate} ${s.value} ${s.unit}');
}

// Every page, as a Stream:
await for (final w in paginate((cursor) =>
    ohs.listWorkouts(userId, start: '2026-01-01', cursor: cursor))) {
  print(w.activityType);
}
```

The other read calls are `listCategorySamples`, `listSleep`, `listSleepPhases`, `listWorkouts`,
`getWorkoutRoute`, `listNutrition`, `listDaily`, `getCoverage`, `listConnections` and
`getFreshness`. Records are immutable classes with `fromJson` factories, and every record type
implements `Provenance` (`provider`, `source`, `device`, `ingestedAt`).

Times are UTC `DateTime`s. The server sends up to nine fractional digits and Dart keeps six at
most, so digits after the microsecond are dropped (on the web, `DateTime` keeps milliseconds).
`date` fields (`DailySummary.date`, `CoverageDay.date`) stay `YYYY-MM-DD` strings because they
are the user's local day. `metadata` and `nutrients` are `Map<String, Object?>`. Record ids are
`int`; on the web a Dart `int` is exact up to 2^53, far above the ids a server hands out.

FHIR R4 Observations come back as JSON maps, with the next cursor already parsed:

```dart
final bundle = await ohs.searchFhirObservations(userId, category: 'vital-signs');
for (final o in bundle.observations) {
  print(o['id']);
}
final next = bundle.nextCursor; // null on the last page
// bundle.raw is the whole Bundle; paginate((c) async =>
//   (await ohs.searchFhirObservations(userId, cursor: c)).page) walks every page.
```

## Connect a provider

Your backend asks for the consent URL with the tenant key and returns it to the app, which opens
it in the browser, for example with the `url_launcher` plugin. After consent the provider sends the user to your
`redirect_uri` with `?status=connected|error&provider=<slug>`.

```dart
// Backend (tenant key)
final url = await ohs.startConnection(userId, 'oura', redirectUri: 'myapp://ohs/connected');
// App, with the URL from your backend
await launchUrl(url, mode: LaunchMode.externalApplication);
```

`listConnections` shows the result; `deleteConnection(userId, 'oura')` disconnects.

## Upload

`AppleHealthPartBuilder` turns lines in the server's
[line format](../connectors/apple-health.md#line-formats) into upload parts: one part per type
and local day, at most 8 MiB of NDJSON each, gzip compressed and numbered across the batch.
Uploading needs a token with the `upload` scope.

```dart
final at = DateTime.now().toUtc();
final builder = AppleHealthPartBuilder(newBatchId('ios'))
  ..add(
    'HKQuantityTypeIdentifierHeartRate',
    localDay(at, utcOffset: Duration.zero),
    {
      'quantityType': 'HKQuantityTypeIdentifierHeartRate',
      'startDate': formatTimestamp(at),
      'endDate': formatTimestamp(at),
      'value': 72,
      'unit': 'count/min',
      'metadata': {'HKTimeZone': 'UTC'},
    },
  );
final results = await uploads.uploadParts(builder.build());
```

`newBatchId(prefix)` gives a fresh batch id per run. `localDay(instant)` is the day in the
device's time zone; `localDay(instant, utcOffset: ...)` the day at a given offset. Dart has no
time zone database of its own: for an IANA zone such as a sample's `HKTimeZone`, get its offset
at that instant from a package such as `timezone`. The builder checks the batch, the type and
the day and throws `ArgumentError` when one is invalid. On your backend, poll `getCoverage(userId)`
until `pendingJobs` is 0 to know the server has processed everything.

## Health store sync

The `open_health_sync_health` plugin reads HealthKit on iOS and Health Connect on Android and
uploads what changed since the last run. It does not read or map anything in Dart: on iOS it
runs the Swift SDK's [`HealthKitSync`](swift.md#healthkit-sync), on Android the Kotlin SDK's
[`HealthConnectSync`](kotlin.md#health-connect-sync), and those two pages say what is read, how
it is cut into parts and what the server stores. The plugin adds one Dart API over both, a
place to keep the credentials, and the wiring that lets a sync run when no Dart code does.

```dart
import 'package:open_health_sync_health/open_health_sync_health.dart';

final health = OpenHealthSyncHealth.instance;

if (await health.isAvailable()) {
  // A user token with the upload scope, minted by your server. Never the tenant key.
  await health.setCredentials(baseUrl: 'https://ohs.example.org', userToken: token);

  const types = {HealthDataType.steps, HealthDataType.heartRate, HealthDataType.sleep};
  await health.requestAuthorization(types, background: true);

  final report = await health.sync(types: types);
  print('${report.lines} records, ${report.partsUploaded} parts, ${report.partsPending} waiting');

  await health.enableBackgroundSync(types: types);
}
```

### Install the plugin

Nothing is published yet. Add the plugin from this repository, by path or by git:

```yaml
# pubspec.yaml of your app
dependencies:
  open_health_sync_health:
    path: ../open-health-sync/sdks/flutter/open_health_sync_health
  # or
  open_health_sync_health:
    git:
      url: https://github.com/soma-intelligence/open-health-sync
      path: sdks/flutter/open_health_sync_health
```

It needs Flutter 3.41 or later, iOS 15 and Android API 26 (`minSdk = 26`, `compileSdk = 36`).
The two native SDKs are not on a registry either, so each platform takes one more step.

### Set up iOS

**The Swift SDK.** The iOS side is a Swift package
([`ios/open_health_sync_health/Package.swift`](../../sdks/flutter/open_health_sync_health/ios/open_health_sync_health/Package.swift))
that depends on the `OpenHealthSyncHealthKit` library of this repository. It is built with
Swift Package Manager, which Flutter uses by default from 3.44 (on 3.41 to 3.43, run
`flutter config --enable-swift-package-manager`). There is no podspec: the Swift SDK has none,
so CocoaPods cannot resolve it.

- From the first version tag that has the `Package.swift` at the root of the repository
  (`v0.3.0`), the package resolves the Swift SDK by the repository URL, with nothing to do.
- Before that tag, or to work on the SDK, point the build at a checkout with the absolute path
  of the repository root in `OHS_SWIFT_SDK_PATH`:

  ```sh
  OHS_SWIFT_SDK_PATH=/path/to/open-health-sync flutter run
  ```

  The variable must be set for every build. A build started from Xcode itself reads Xcode's
  environment, not the shell's: run `launchctl setenv OHS_SWIFT_SDK_PATH /path/to/open-health-sync`
  before starting Xcode.

**The app.** As for the Swift SDK ([set up the app](swift.md#healthkit-sync)):

- In Xcode, add the HealthKit capability to the Runner target. It writes the
  `com.apple.developer.healthkit` entitlement. For [background sync](#background-sync), tick
  *Background Delivery* under it (`com.apple.developer.healthkit.background-delivery`).
- Add `NSHealthShareUsageDescription` to `ios/Runner/Info.plist`, the sentence iOS shows when
  it asks for access. The plugin only reads, so `NSHealthUpdateUsageDescription` is not needed.
  Without the key `requestAuthorization` throws `missing_usage_description` instead of letting
  HealthKit stop the app.
- Set the deployment target of the Runner target to iOS 15 or later.

```xml
<!-- ios/Runner/Info.plist -->
<key>NSHealthShareUsageDescription</key>
<string>Your steps, heart rate and sleep are sent to your account.</string>
```

```xml
<!-- ios/Runner/Runner.entitlements -->
<key>com.apple.developer.healthkit</key>
<true/>
<key>com.apple.developer.healthkit.background-delivery</key>
<true/>
```

### Set up Android

**The Kotlin SDK.** The Android side depends on
`org.openhealthsync:open-health-sync-health-connect:0.1.0`, the
[Health Connect module](kotlin.md#health-connect-sync) of the Kotlin SDK. Until it is
published, include the Kotlin SDK of a checkout as a Gradle build; Gradle then takes the
module from it:

```kotlin
// android/settings.gradle.kts of your app
includeBuild("/path/to/open-health-sync/sdks/kotlin")
```

The Kotlin SDK only builds its Android module when it finds an Android SDK: set `ANDROID_HOME`,
or write `sdk.dir=...` in `sdks/kotlin/local.properties` (the `local.properties` of your app is
not read by an included build).

An included build runs in the same Gradle as your app, with its own Android Gradle plugin, so
the two must agree. The Kotlin SDK is built with the Android Gradle plugin 8.13.2 and Kotlin
2.0.21, and that plugin does not run on Gradle 9.6 or later. This is what was built here:
Gradle 8.14.3, Android Gradle plugin 8.13.2, Kotlin 2.0.21, JDK 17. A new Flutter project
(Android Gradle plugin 9, Gradle 9) has to be set to those versions until the module is
published as an artifact.

In `android/app/build.gradle.kts`, set `minSdk = 26` and `compileSdk = 36`.

**The manifest.** As for the Kotlin SDK ([set up the app](kotlin.md#set-up-the-app)): declare
the read permission of each type you sync (the [table below](#data-types) gives the record
type, and the [Kotlin page](kotlin.md#record-types) its permission), and the screen that
explains your use of the data, which Health Connect requires before it shows the permission
dialog. In a Flutter app that screen can be the Flutter activity itself, which then shows your
privacy policy when it is started with one of these two intents:

```xml
<!-- android/app/src/main/AndroidManifest.xml -->
<uses-permission android:name="android.permission.health.READ_STEPS" />
<uses-permission android:name="android.permission.health.READ_HEART_RATE" />
<uses-permission android:name="android.permission.health.READ_SLEEP" />
<!-- Only for background sync, and for data older than 30 days before the first grant: -->
<uses-permission android:name="android.permission.health.READ_HEALTH_DATA_IN_BACKGROUND" />
<uses-permission android:name="android.permission.health.READ_HEALTH_DATA_HISTORY" />

<application>
    <activity android:name=".MainActivity" android:exported="true">
        <!-- ... the launcher intent filter Flutter wrote ... -->
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

The plugin declares no permission itself, so your app never asks for more than it uses. A type
whose permission is missing from the manifest cannot be asked for: `requestAuthorization`
leaves it out and lists it in `notDeclared`. The plugin's manifest adds one thing, an
`androidx.startup` initializer that prepares background runs; `MainActivity` can stay a plain
`FlutterActivity`.

### The API

One object, `OpenHealthSyncHealth.instance`. Every method is a `Future`; a failure throws
`HealthSyncException` ([below](#plugin-errors)).

| Call | What it does |
|---|---|
| `availability()` | `available`, `updateRequired` (Android 13 and lower: the Health Connect app must be installed or updated) or `unavailable` |
| `isAvailable()` | Whether `availability()` is `available`. False on the web and on desktops |
| `setCredentials(baseUrl:, userToken:)` | Stores where to upload and the user token ([credentials](#credentials)) |
| `clearCredentials()` | Forgets them, for example at sign out |
| `requestAuthorization(types, background:, history:)` | Shows the system screen and returns an `AuthorizationResult` |
| `sync(types:)` | Reads what changed and uploads it; returns a `SyncReport`. Without `types`, every type the native SDK reads |
| `flush(force:)` | Android: uploads the queue without reading. iOS: an empty report |
| `pendingParts()` | Android: how many parts wait in the queue. iOS: 0 |
| `enableBackgroundSync(types:, interval:, requireUnmeteredNetwork:)` | Keeps syncing when the app is not open ([background sync](#background-sync)) |
| `disableBackgroundSync()` | Stops it |
| `backgroundSyncResults` | A `Stream<BackgroundSyncResult>`: the report or the error of each background run |
| `platform` | `HealthPlatform.healthKit`, `HealthPlatform.healthConnect`, or null where there is no store |

`AuthorizationResult` has `requested`, `unsupported` (types this platform does not have),
`notDeclared` (Android, see above), `backgroundGranted` and `historyGranted` (Android, when
asked), and `granted`: the types the user granted on Android, **null on iOS**, where HealthKit
never tells an app whether read access was granted. A declined type just returns no samples.

`SyncReport`:

| Member | iOS | Android |
|---|---|---|
| `batchIds`, `partsUploaded`, `duplicateParts` | Yes | Yes |
| `linesByType`, `lines` | Lines uploaded, by HealthKit identifier | Lines queued, by record type |
| `deletionsByType`, `deletions` | Deletions sent | Deletions queued |
| `deletionsPendingByType` | Deletions kept until the server says it applies them | Empty |
| `skippedRecords` | Samples the server does not store | Records without an id or a finite value |
| `partsPending`, `partsDropped`, `uploadError` | 0, 0, null: there is no queue | The [offline queue](kotlin.md#offline-queue-and-retries) |
| `typesWithoutPermission`, `expiredTokenTypes` | Empty: HealthKit does not say | Yes |
| `unsupportedTypes` | The types asked for that the platform does not have | Same |

### Data types

`HealthDataType` names each type once. Its two lists are the support map: `healthKit` holds
the HealthKit identifiers it stands for, `healthConnect` the Health Connect record types, and
an empty list means the platform does not have it. `isSupportedOn(platform)` and
`HealthDataType.supportedOn(platform)` read it. A type the platform lacks is never an error: it
is left out and reported (`unsupported`, `unsupportedTypes`).

| `HealthDataType` | HealthKit | Health Connect |
|---|---|---|
| `steps` | `StepCount` | `Steps` |
| `heartRate` | `HeartRate` | `HeartRate` |
| `restingHeartRate` | `RestingHeartRate` | `RestingHeartRate` |
| `heartRateVariability` | `HeartRateVariabilitySDNN` | `HeartRateVariabilityRmssd` (another measure) |
| `oxygenSaturation` | `OxygenSaturation` | `OxygenSaturation` |
| `respiratoryRate` | `RespiratoryRate` | `RespiratoryRate` |
| `bodyTemperature` | `BodyTemperature` | `BodyTemperature` |
| `bloodPressure` | `BloodPressureSystolic`, `BloodPressureDiastolic` | `BloodPressure` |
| `bloodGlucose` | `BloodGlucose` | `BloodGlucose` |
| `weight` | `BodyMass` | `Weight` |
| `height` | `Height` | `Height` |
| `bodyFat` | `BodyFatPercentage` | `BodyFat` |
| `activeEnergy` | `ActiveEnergyBurned` | `ActiveCaloriesBurned` |
| `basalEnergy` | `BasalEnergyBurned` | `BasalMetabolicRate` (a rate, not an energy) |
| `totalEnergy` | none | `TotalCaloriesBurned` |
| `distance` | `DistanceWalkingRunning` | `Distance` |
| `sleep` | `SleepAnalysis` | `SleepSession` |
| `workouts` | workouts and their routes | `ExerciseSession` (no route) |
| `menstruation` | `MenstrualFlow` | `MenstruationFlow` |
| `intermenstrualBleeding` | `IntermenstrualBleeding` | `IntermenstrualBleeding` |
| `nutrition` | every dietary quantity type | `Nutrition` |
| `hydration` | `DietaryWater` | `Hydration` |
| `electrocardiogram` | ECG recordings | none |
| `heartbeatSeries` | heartbeat series | none |
| `audiogram` | audiograms | none |
| `stateOfMind` | states of mind (iOS 18) | none |
| `healthKitQuantities` | every quantity type the server stores that is not dietary | none |
| `healthKitCategories` | every category type the server stores | none |

The last two, and `nutrition`, are groups: the Swift SDK resolves them from its own table
(`HealthKitSync.quantityTypes`, `categoryTypes`, `nutritionTypes`), so the plugin never lists
those identifiers a second time. `sync()` without `types` is everything on either platform
(`allSupportedTypes` of the native SDK).

### Credentials

`setCredentials` takes the base URL of your instance and a user token with the `upload` scope
that your server minted ([why not the tenant key](README.md#which-credential-goes-where)). They
are kept on the native side, because a background sync starts when no Dart code runs:

- **iOS**: the token is a generic password in the keychain, readable after the first unlock
  and never copied to another device; the base URL is in `UserDefaults`.
- **Android**: the token is encrypted with an AES key that stays in the Android Keystore, and
  the result and the base URL are in the app's private preferences.

Both native clients read the token again before each request, so call `setCredentials` again
whenever your server gives a new token, and the next upload uses it. A background run with an
expired token loses nothing: on Android the parts stay queued, on iOS the anchor does not move.
The token is never logged and never appears in an error.

### Background sync

`enableBackgroundSync` needs the credentials first, and the background entitlement or
permission of the platform.

- **iOS**: HealthKit background delivery, with an observer query per type that runs a sync when
  HealthKit reports a change, even when the app was not running. HealthKit wants it turned on
  at every launch: the plugin remembers the request and does it when it is registered, so one
  call is enough. `interval` picks the closest HealthKit frequency (immediate under an hour,
  then hourly, daily, weekly); iOS decides when it really wakes the app. It does not retry: a
  wake without network or with a locked device fails, and the next change or the next `sync`
  catches up. So also call `sync()` when your app comes to the foreground. The
  `BGTaskScheduler` catch-up described for the [Swift SDK](swift.md#healthkit-sync) is not
  part of the plugin.
- **Android**: a WorkManager job every `interval` (at least 15 minutes) when a network is up,
  across restarts. It reads only when the user granted reading in the background
  (`requestAuthorization(types, background: true)`, with the permission in the manifest);
  without it a run still uploads the queue. The job runs in a process where Flutter may never
  start, which is why the plugin builds the sync from the stored credentials at process start.

`backgroundSyncResults` gives one `BackgroundSyncResult` per run (`at`, `report` or `error`)
while the Dart side is alive. Runs that ended while nobody listened are kept, 20 at most, and
given to the first listener.

### Plugin errors

`HealthSyncException` carries a `code`, a `message`, and for `api_error` the HTTP `status` and
the server's `requestId`.

| `code` | When |
|---|---|
| `not_configured` | No credentials stored: call `setCredentials` |
| `unavailable` | No health store on this device |
| `unsupported_platform` | The web or a desktop |
| `missing_usage_description` | iOS: `NSHealthShareUsageDescription` is missing |
| `no_activity`, `authorization_in_progress` | Android: the permission screen needs the app in front, one at a time |
| `permission_denied` | The store refused the read |
| `store_locked` | iOS: HealthKit cannot be read while the device is locked |
| `api_error` | The server answered with an error |
| `network_error` | The request did not reach the server |
| `invalid_argument`, `health_store_error` | A refused argument; anything else the store reports |

On iOS a failed upload throws (`api_error`, `network_error`) and the next sync sends the same
samples. On Android it does not: the parts stay in the queue and the report says why in
`uploadError`.

### How it is built

- **Channel.** One method channel (`org.openhealthsync/health`) and one event channel
  (`org.openhealthsync/health/background`), written by hand with the standard codec: maps of
  strings, numbers and lists. The Dart side decodes them into the typed classes above in
  [`report.dart`](../../sdks/flutter/open_health_sync_health/lib/src/report.dart). Pigeon would
  generate the same three sides from one definition; moving to it is a change of plumbing, not
  of the API.
- **iOS**: [`HealthSyncBridge.swift`](../../sdks/flutter/open_health_sync_health/ios/open_health_sync_health/Sources/open_health_sync_health/HealthSyncBridge.swift)
  builds `HealthKitSync` from the stored credentials and resolves the type names.
- **Android**: [`HealthSyncBridge.kt`](../../sdks/flutter/open_health_sync_health/android/src/main/kotlin/org/openhealthsync/flutter/health/HealthSyncBridge.kt)
  does the same with `HealthConnectSync.create`, and sets `HealthConnectSync.backgroundFactory`
  and `onBackgroundSync` from an `androidx.startup` initializer.

Not verified on a device yet. The Dart tests mock the channel; the Android side was compiled
and linted against the Flutter embedding and the Kotlin SDK; the iOS side was compiled against
the Swift SDK with a stand-in for the Flutter framework. The permission screens, the reads, the
background runs and a build inside a real Flutter app have not been run.

## Building lines yourself

Without the plugin, or for data that does not come from the health store, turn each reading
into a line and add it to the builder:

```dart
// A reading: start, end and beats per minute.
Map<String, Object?> heartRateLine(DateTime from, DateTime to, num bpm, String zone) => {
      'quantityType': 'HKQuantityTypeIdentifierHeartRate',
      'startDate': formatTimestamp(from),
      'endDate': formatTimestamp(to),
      'value': bpm,
      'unit': 'count/min',
      'metadata': {'HKTimeZone': zone},
    };

builder.add(
  'HKQuantityTypeIdentifierHeartRate',
  localDay(reading.from),
  heartRateLine(reading.from, reading.to, reading.bpm, deviceZoneName),
);
```

The client uploads to one route, `POST /v1/ingest/apple-health`, which takes HealthKit-shaped
lines only ([line formats](../connectors/apple-health.md#line-formats)). The server also has
`POST /v1/ingest/health-connect`, which takes Health Connect records in their own shape
([contract and line format](../connectors/health-connect.md)); the Dart client has no method
or part builder for it, and the plugin's Android side is what uploads to it.

## Errors and retries

A non-2xx answer throws `OhsApiException` with `status`, `title`, `detail`, `type`, `requestId`
(quote it in a bug report) and `retryAfterSeconds`. A body that is not JSON still gives the
status and the `X-Request-Id` header. A network failure throws `http.ClientException`, and a
response that does not decode throws `FormatException`.

Uploads are retried on 429, 503, other 5xx answers and network errors, waiting `Retry-After`
when the server sends it and 1, 2, 4, 8 seconds otherwise (at most 30), up to 5 attempts
(`maxUploadAttempts`). The token provider is called again before each attempt. Any other 4xx
throws at once, and other calls are not retried. Neither the token nor a health value appears
in an exception message.

## Run the tests

```sh
cd sdks/flutter
dart pub get
dart format --set-exit-if-changed .
dart analyze --fatal-infos
dart test
```

The tests use `package:http/testing` `MockClient`, no server. They need the Dart SDK only, not
Flutter. One test, tagged `vm`, also checks the gzip bodies with `dart:io`. The plugin's
directory is left out of this package's analysis (`analysis_options.yaml`), since its imports
only resolve with the Flutter SDK.

The plugin has its own tests, which need Flutter. They mock the two channels and check what is
sent and how every answer is read, on both platforms:

```sh
cd sdks/flutter/open_health_sync_health
flutter pub get
dart format --output=none --set-exit-if-changed .
flutter analyze --fatal-infos
flutter test
```

## Run the example

[`example/quickstart.dart`](../../sdks/flutter/example/quickstart.dart) runs the README
quickstart: it creates a user, mints a token, uploads one heart rate reading, waits for
processing and reads it back. Point it at a test instance, never at real users' data:

```sh
cd sdks/flutter
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... dart run example/quickstart.dart
```

It exits 1 when it does not read exactly one sample back. It prints counts only, never the token
or a health value.
