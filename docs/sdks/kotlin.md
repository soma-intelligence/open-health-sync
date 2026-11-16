# Kotlin SDK

The Kotlin client for the [REST API](../api.md), for Android apps and JVM backends. Coroutines
for every call, kotlinx.serialization for the models, `HttpURLConnection` underneath, so it runs on
Android API 26+ and any JVM 11+ with no HTTP library. It follows the shape every SDK shares
([overview](README.md)). AGPL-3.0-only, like the server ([LICENSE](../../LICENSE)).

It comes in two modules:

| Module | What it is | Runs on |
|---|---|---|
| `open-health-sync-kotlin` ([`sdks/kotlin`](../../sdks/kotlin)) | The REST client, the upload part builders and the Bluetooth recorder. Plain JVM, no Android class | Android API 26+, any JVM 11+ |
| `open-health-sync-health-connect` ([`sdks/kotlin/health-connect`](../../sdks/kotlin/health-connect)) | An Android library that reads [Health Connect](#health-connect-sync) and uploads what changed, built on the client | Android API 26+ |

## Install

Nothing is published yet. Build the jar from this repository and add it to your project, or
include the directory as a Gradle build:

```kotlin
// settings.gradle.kts of your app
includeBuild("path/to/open-health-sync/sdks/kotlin")

// build.gradle.kts of the module
dependencies {
    implementation("org.openhealthsync:open-health-sync-kotlin:0.1.0")
}
```

The coordinates `org.openhealthsync:open-health-sync-kotlin` are placeholders until the first
release. The library brings `kotlinx-coroutines-core` and `kotlinx-serialization-json`.

An Android app that syncs Health Connect adds the second module, which brings the client with it:

```kotlin
dependencies {
    implementation("org.openhealthsync:open-health-sync-health-connect:0.1.0")
}
```

It brings `androidx.health.connect:connect-client` 1.1.0 and `androidx.work:work-runtime`, so
your app needs `compileSdk` 36 and the Android Gradle plugin 8.9.1 or later. The module is part
of the Gradle build only when an Android SDK is found (`ANDROID_HOME`, `ANDROID_SDK_ROOT` or
`sdk.dir` in `local.properties`), so the client still builds on a machine without one.

## Create a client

Which client holds which credential ([why](README.md#which-credential-goes-where)):

- **In the Android app**, the client holds a user token with the `upload` scope that your backend
  minted, never the tenant key. Give it a token provider: it is called before each request, so it
  can return a cached token and fetch a new one when it is about to expire. This client uploads;
  every other route answers 403 to a user token.
- **On your backend** (any JVM), the client holds the tenant key and makes every other call.

```kotlin
import org.openhealthsync.sdk.OhsClient

// Android app
val uploads = OhsClient(
    baseUrl = "https://ohs.example.org",
    tokenProvider = { myBackend.currentUploadToken() }, // your own suspend function
)

// Backend
val ohs = OhsClient("https://ohs.example.org", System.getenv("OHS_KEY"))
val user = ohs.createUser(externalRef = "customer-42")
val token = ohs.mintUserToken(user.id, listOf("upload")) // hand token.token to the app
```

Pass `transport =` to send requests through OkHttp, Ktor or a fake: an `HttpTransport` is one
function from `HttpRequest` to `HttpResponse`.

## Read data

Reads take the tenant key, so they run on your backend. Every list call returns one `Page`
(`data`, `nextCursor`). `Range` holds the time window and the page: `start` and `end` take an
RFC 3339 time or a `YYYY-MM-DD` date, and the window is half open.

```kotlin
import org.openhealthsync.sdk.Range
import org.openhealthsync.sdk.allPages

val page = ohs.listSamples(userId, type = "heart_rate", Range(start = "2026-01-10", end = "2026-01-11"))
page.data.forEach { println("${it.startDate} ${it.value} ${it.unit}") }

// Every page, as a Flow:
allPages { cursor -> ohs.listWorkouts(userId, range = Range(start = "2026-01-01", cursor = cursor)) }
    .collect { workout -> println(workout.activityType) }
```

The other read calls are `listCategorySamples`, `listSleep`, `listSleepPhases`, `listWorkouts`,
`getWorkoutRoute`, `listNutrition`, `listDaily`, `getCoverage`, `listConnections` and
`getFreshness`. Times are `java.time.Instant`; `date` fields stay `YYYY-MM-DD` strings because they
are the user's local day. `metadata` is a `JsonObject`.

FHIR R4 Observations come back as JSON, with the next cursor already parsed:

```kotlin
val bundle = ohs.searchFhirObservations(userId, category = "vital-signs")
bundle.observations.forEach { println(it["id"]) }
val next = bundle.nextCursor // null on the last page
```

## Connect a provider

Your backend asks for the consent URL with the tenant key and returns it to the app, which opens
it in a Custom Tab. After consent the provider sends the user to your `redirect_uri` with
`?status=connected|error&provider=<slug>`.

```kotlin
// Backend
val url = ohs.startConnection(userId, provider = "oura", redirectUri = "myapp://ohs/connected")

// App, with the URL from your backend
CustomTabsIntent.Builder().build().launchUrl(context, Uri.parse(url))
```

## Upload

`uploadAppleHealthPart` and `uploadParts` send to `POST /v1/ingest/apple-health`, which takes
HealthKit-shaped lines ([line formats](../connectors/apple-health.md#line-formats)), so on
Android they serve data you already hold in that shape, for example lines converted from a Health
app export.

Health Connect data goes to its own route, `POST /v1/ingest/health-connect`. On Android,
[`HealthConnectSync`](#health-connect-sync) reads the store and uploads for you. The pieces
underneath are in the client, for data you hold in the
[Health Connect line format](../connectors/health-connect.md#line-format) already:
`HealthConnectPartBuilder` groups lines into parts (one record type and local day per part, at
most 8 MiB of NDJSON, numbered across the batch) and `uploadHealthConnectPart` sends one, with
the same retries as the Apple Health upload.

```kotlin
import org.openhealthsync.sdk.AppleHealthPartBuilder
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

val builder = AppleHealthPartBuilder(AppleHealthPartBuilder.newBatchId("android"))
builder.add(
    type = "HKQuantityTypeIdentifierHeartRate",
    day = AppleHealthPartBuilder.localDay(at, zone),
    line = buildJsonObject {
        put("quantityType", "HKQuantityTypeIdentifierHeartRate")
        put("startDate", at.toString())
        put("endDate", at.toString())
        put("value", 72)
        put("unit", "count/min")
        put("metadata", buildJsonObject { put("HKTimeZone", zone.id) })
    },
)
val results = uploads.uploadParts(builder.build())
```

The builder makes one part per type and local day, at most 8 MiB of NDJSON each, gzip
compressed and numbered across the batch. On your backend, poll `getCoverage(userId).pendingJobs` until
it is 0 to know the server has processed everything.

## Health Connect sync

`HealthConnectSync`, in the `open-health-sync-health-connect` module, reads Android Health
Connect and uploads what changed since the last run. It is the counterpart of the Swift SDK's
[`HealthKitSync`](swift.md#healthkit-sync), with the same names where the platforms allow it.

### Set up the app

Declare in your manifest the read permission of each record type you sync (the
[table below](#record-types) names them), and the screen that explains your use of the data,
which Health Connect requires before it shows the permission dialog:

```xml
<uses-permission android:name="android.permission.health.READ_STEPS" />
<uses-permission android:name="android.permission.health.READ_BLOOD_GLUCOSE" />
<!-- Only for background sync, and for data older than 30 days before the first grant: -->
<uses-permission android:name="android.permission.health.READ_HEALTH_DATA_IN_BACKGROUND" />
<uses-permission android:name="android.permission.health.READ_HEALTH_DATA_HISTORY" />

<application>
    <activity android:name=".PrivacyPolicyActivity" android:exported="true">
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
        android:targetActivity=".PrivacyPolicyActivity">
        <intent-filter>
            <action android:name="android.intent.action.VIEW_PERMISSION_USAGE" />
            <category android:name="android.intent.category.HEALTH_PERMISSIONS" />
        </intent-filter>
    </activity-alias>
</application>
```

The library declares no permission itself, so your app never asks for more than it uses.

### Check, ask, sync

```kotlin
import org.openhealthsync.sdk.OhsClient
import org.openhealthsync.sdk.healthconnect.HealthConnectAvailability
import org.openhealthsync.sdk.healthconnect.HealthConnectRecordType
import org.openhealthsync.sdk.healthconnect.HealthConnectSync

// 1. Is Health Connect usable here?
when (HealthConnectSync.availability(context)) {
    HealthConnectAvailability.AVAILABLE -> Unit
    HealthConnectAvailability.PROVIDER_UPDATE_REQUIRED -> return // send the user to the Play Store
    HealthConnectAvailability.UNAVAILABLE -> return
}

// 2. Ask for read access (in an Activity or Fragment).
val types = setOf(HealthConnectRecordType.STEPS, HealthConnectRecordType.BLOOD_GLUCOSE)
// or HealthConnectSync.allSupportedTypes
val ask = registerForActivityResult(HealthConnectSync.requestAuthorizationContract()) { granted -> /* ... */ }
ask.launch(HealthConnectSync.authorizationPermissions(types, background = true))

// 3. Sync. The client holds a user token with the upload scope.
val uploads = OhsClient("https://ohs.example.org", tokenProvider = { myBackend.currentUploadToken() })
val sync = HealthConnectSync.create(context, uploads)
val report = sync.sync(types)
println("${report.linesQueued} records, ${report.partsUploaded} parts sent, ${report.partsPending} waiting")
```

What a sync does, per record type:

- **First run**: it reads a backfill window, 30 days by default
  (`HealthConnectSyncOptions(initialBackfill = ...)`; `Duration.ZERO` reads nothing old, `null`
  reads everything). Health Connect itself gives at most 30 days before the first permission
  grant unless the app holds the history permission.
- **Later runs**: it reads the changes since the stored changes token. Records added or edited
  go up as lines; the ids of deleted records go up as `Deletion` lines, which the server applies.
- **Expired token**: Health Connect keeps changes about 30 days. After a longer gap the token is
  replaced and the type read again over `expiredTokenWindow` (30 days). Records deleted during
  the gap are not known and stay on the server.
- **A type without permission** is skipped and listed in `report.typesWithoutPermission`.

Lines are grouped into parts by record type and local day (the record's own zone offset, else
the device zone), at most 8 MiB of NDJSON each (`maxPartBytes`; the server takes 64 MiB), under
one batch id per type and run.

### Offline queue and retries

Every part is written to a queue on disk, in the app's private files, before the changes token
is saved; it leaves the queue when the server has it. So a sync with no network still reads
Health Connect once, and nothing is lost if the app is killed.

- A part that fails with a network error, 401, 403, 408, 429 or a 5xx stays queued. `sync` does
  not throw for it: the report carries `partsPending` and `uploadError`. The next `sync` or
  `flush()` sends it, once its wait is over: 30 seconds after the first failed round, doubled
  each time, at most an hour. `flush(force = true)` skips the wait (the user tapped "sync now").
  These rounds come on top of the client's own 5 attempts within one upload.
- A part the server refuses for good (400, 409, 413 and the other 4xx) is dropped, counted in
  `partsDropped`, and the parts behind it still go up.
- A failed read does throw (`SecurityException` without the permission, `IOException` or
  `RemoteException` when Health Connect is unreachable). What was read before is queued and
  uploaded first, and the token of the type that failed is not moved.

The queue holds gzip NDJSON of health data, protected by the app sandbox and the device's
storage encryption, nothing more. `sync.pendingParts` and `sync.queue.sizeBytes` tell how much
waits; `sync.queue.clear()` empties it, for example when the user signs out.

### Background sync

Health Connect has no push, so background delivery is a periodic WorkManager job that runs the
same sync:

```kotlin
class MyApp : Application() {
    override fun onCreate() {
        super.onCreate()
        // A background run starts in a process where no activity has run: say how to build the sync.
        HealthConnectSync.backgroundFactory = HealthConnectSyncFactory { context ->
            val token = session.uploadTokenOrNull() ?: return@HealthConnectSyncFactory null
            HealthConnectSync.create(context, OhsClient("https://ohs.example.org", token))
        }
    }
}

// Once the permissions are granted:
HealthConnectSync.enableBackgroundDelivery(context, types, repeatInterval = Duration.ofHours(1))
// When the user signs out:
HealthConnectSync.stopObserving(context)
```

The job runs at most every 15 minutes (WorkManager's floor), only with a network, and survives
restarts. It reads only when the user granted `READ_HEALTH_DATA_IN_BACKGROUND`
(`HealthConnectSync.isBackgroundReadAvailable(context)` says whether the device can); without it
a run still uploads the queue, and the reading happens when your app is open and calls `sync`.
`HealthConnectSync.onBackgroundSync` receives each run's report or error.

### Record types

`HealthConnectSync.allSupportedTypes` is every record type the server stores, the same list as
the [connector's table](../connectors/health-connect.md#line-format). A test compares it with
the server's mapper, and compares each line the SDK writes with the server's own test fixture.

| `HealthConnectRecordType` | Record class | Manifest permission (`android.permission.health.`) | Stored as |
|---|---|---|---|
| `STEPS` | `StepsRecord` | `READ_STEPS` | `step_count` |
| `HEART_RATE` | `HeartRateRecord` | `READ_HEART_RATE` | `heart_rate`, one sample per point |
| `RESTING_HEART_RATE` | `RestingHeartRateRecord` | `READ_RESTING_HEART_RATE` | `resting_heart_rate` |
| `HEART_RATE_VARIABILITY_RMSSD` | `HeartRateVariabilityRmssdRecord` | `READ_HEART_RATE_VARIABILITY` | `hrv_rmssd` |
| `OXYGEN_SATURATION` | `OxygenSaturationRecord` | `READ_OXYGEN_SATURATION` | `oxygen_saturation` |
| `RESPIRATORY_RATE` | `RespiratoryRateRecord` | `READ_RESPIRATORY_RATE` | `respiratory_rate` |
| `BODY_TEMPERATURE` | `BodyTemperatureRecord` | `READ_BODY_TEMPERATURE` | `body_temperature` |
| `BLOOD_PRESSURE` | `BloodPressureRecord` | `READ_BLOOD_PRESSURE` | systolic and diastolic, paired |
| `BLOOD_GLUCOSE` | `BloodGlucoseRecord` | `READ_BLOOD_GLUCOSE` | `blood_glucose` |
| `WEIGHT` | `WeightRecord` | `READ_WEIGHT` | `body_mass` |
| `HEIGHT` | `HeightRecord` | `READ_HEIGHT` | `height` |
| `BODY_FAT` | `BodyFatRecord` | `READ_BODY_FAT` | `body_fat_percentage` |
| `ACTIVE_CALORIES_BURNED` | `ActiveCaloriesBurnedRecord` | `READ_ACTIVE_CALORIES_BURNED` | `active_energy_burned` |
| `TOTAL_CALORIES_BURNED` | `TotalCaloriesBurnedRecord` | `READ_TOTAL_CALORIES_BURNED` | `total_energy_burned` |
| `BASAL_METABOLIC_RATE` | `BasalMetabolicRateRecord` | `READ_BASAL_METABOLIC_RATE` | `basal_metabolic_rate` |
| `DISTANCE` | `DistanceRecord` | `READ_DISTANCE` | `distance_walking_running` |
| `SLEEP_SESSION` | `SleepSessionRecord` | `READ_SLEEP` | a sleep session with its phases |
| `EXERCISE_SESSION` | `ExerciseSessionRecord` | `READ_EXERCISE` | a workout |
| `MENSTRUATION_FLOW` | `MenstruationFlowRecord` | `READ_MENSTRUATION` | a `menstrual_flow` category sample |
| `INTERMENSTRUAL_BLEEDING` | `IntermenstrualBleedingRecord` | `READ_INTERMENSTRUAL_BLEEDING` | an `intermenstrual_bleeding` category sample |
| `NUTRITION` | `NutritionRecord` | `READ_NUTRITION` | a nutrition log |
| `HYDRATION` | `HydrationRecord` | `READ_HYDRATION` | a nutrition log with water |

Not read, because the server does not store them: exercise routes, laps and segments, and the
other Health Connect record types (`Vo2Max`, `Speed`, `Power`, `MenstruationPeriod`, body
composition beyond weight, height and body fat, and so on).

### Reference

| Call | What it does |
|---|---|
| `HealthConnectSync.availability(context)` | `AVAILABLE`, `PROVIDER_UPDATE_REQUIRED` or `UNAVAILABLE` |
| `HealthConnectSync.authorizationPermissions(types, background, history)` | The permission strings to ask for |
| `HealthConnectSync.requestAuthorizationContract()` | The `ActivityResultContract` of the permission screen |
| `HealthConnectSync.create(context, client, options)` | A sync on this device, with its tokens and queue in the app's files |
| `sync.grantedPermissions()`, `sync.hasPermissions(types)` | What the user granted |
| `sync.sync(types)` | Reads the changes, queues and uploads them; returns a `SyncReport` |
| `sync.flush(force)` | Uploads the queue without reading |
| `sync.resetAnchor(type)` | Forgets a type's token: the next sync reads its window again |
| `sync.pendingParts`, `sync.queue` | The queue on disk |
| `HealthConnectSync.enableBackgroundDelivery(context, types, repeatInterval, requireUnmeteredNetwork)` | Schedules the periodic job |
| `HealthConnectSync.stopObserving(context)` | Cancels it |
| `HealthConnectSync.backgroundFactory`, `HealthConnectSync.onBackgroundSync` | How a background run builds its sync, and where its result goes |
| `HealthConnectLines.line(record)`, `HealthConnectLines.day(record, zone)` | One record as its upload line, and the local day of its part |

To test your own code without a device, build `HealthConnectSync` from its parts: a
`HealthConnectReader` (the four Health Connect calls it makes), a `HealthConnectPartUploader`,
an `InMemoryChangesTokenStore` and an `UploadQueue` on a temporary directory.

Not verified on a device yet: the library is built and unit tested on the JVM against a fake
reader and the server's fixture. The permission screen, the reads through the real
`HealthConnectClient` and the WorkManager job have not been run on a phone or an emulator.

## Bluetooth devices

Heart rate straps, blood pressure cuffs, scales, glucose meters, thermometers and pulse oximeters
that use the standard Bluetooth health profiles upload straight from the app, with no vendor app
in between ([devices and mapping](../connectors/bluetooth.md)). The app forwards what each device
sends as bytes; the server decodes it.

The SDK is plain JVM, so it holds what does not need Android: `BluetoothRecorder` turns values
into upload parts, `GattUuids` has the service and characteristic UUIDs and the bytes to write,
and `uploadBluetoothPart` sends a part. Feed the recorder from your `BluetoothGattCallback`:

```kotlin
import org.openhealthsync.sdk.BluetoothDevice
import org.openhealthsync.sdk.BluetoothRecorder
import org.openhealthsync.sdk.GattUuids

val recorder = BluetoothRecorder(deviceUser = 1) // or no argument, on a device of your own
val session = BluetoothRecorder.newSession()      // one per connection to one device
val device = BluetoothDevice(name = GattUuids.publicName(advertisedName))

// In onCharacteristicChanged(gatt, characteristic, value):
recorder.record(characteristic.uuid, value, session, device)

// Later, for example from a WorkManager job:
recorder.makePart()?.let { part -> uploads.uploadBluetoothPart(part) }
```

`record` returns false and keeps nothing for a characteristic that is not a measurement (battery
level, a vendor service). `deviceUser` is this person's user index on a shared scale or cuff, so
the server stores only their readings. The recorder is not thread safe: call it from one thread
or under a lock. Keep a part until `uploadBluetoothPart` succeeds; the same part sent again is
answered `duplicate: true`.

What to do on each connection: subscribe to every characteristic in `GattUuids.MEASUREMENTS` the
device serves (write `ENABLE_NOTIFICATION_VALUE` or `ENABLE_INDICATION_VALUE` to its
`CLIENT_CHARACTERISTIC_CONFIG` descriptor), read the Device Information strings into
`BluetoothDevice` with `GattUuids.deviceText`, write `GattUuids.currentTime(Instant.now())` to
`CURRENT_TIME` when the device allows it, and for a glucose meter write
`GattUuids.reportStoredRecords(lastSequence)` to `RECORD_ACCESS_CONTROL_POINT` once its
indications are on (`glucoseSequenceNumber` reads the sequence number of each record). Android
runs one GATT operation at a time per device, so queue them. The
[Android example](../../examples/android/README.md#bluetooth-devices) does all of it in
`BluetoothSync.kt`, with the permissions, and is the code to start from. Never send a device
address or a serial number: the format has no field for them.

## Errors and retries

A non-2xx answer throws `OhsApiException` with `status`, `title`, `detail`, `type`, `requestId`
(quote it in a bug report) and `retryAfterSeconds`. A network failure throws `IOException`.
Uploads are retried on 429, 503, other 5xx answers and network errors, waiting `Retry-After` when
the server sends it and 1, 2, 4, 8 seconds otherwise, up to 5 attempts (`maxUploadAttempts`).
Other calls are not retried. Neither the token nor a health value appears in an exception message.

## Run the tests

```sh
cd sdks/kotlin
./gradlew build   # both modules: compile, unit tests, Android lint
./gradlew test    # unit tests only
```

The tests use a fake transport and a fake Health Connect reader, no server and no device. The
Health Connect module needs JDK 17 and an Android SDK with platform 36; without an SDK
(or with `-Pohs.android=false`) only the client is built and tested.

## Run the example

[`src/example/kotlin/Quickstart.kt`](../../sdks/kotlin/src/example/kotlin/Quickstart.kt) runs the
README quickstart: it creates a user, mints a token, uploads one heart rate reading, waits for
processing and reads it back. Point it at a test instance, never at real users' data:

```sh
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... ./gradlew runExample
```

CI runs it, and the other SDKs' examples, against a server built from the same commit
(`test/e2e/sdks.sh`).
