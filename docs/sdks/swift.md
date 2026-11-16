# Swift SDK

The Swift client for the [REST API](../api.md), for iOS and watchOS apps, macOS, and Swift
backends on Linux. `async`/`await` for every call, `Codable` models, `URLSession` underneath, no
dependencies. A second library, `OpenHealthSyncHealthKit`, reads HealthKit and uploads what changed
since the last run. It follows the shape every SDK shares ([overview](README.md)). AGPL-3.0-only, like the server
([LICENSE](../../LICENSE)).

| Library | Runs on | What it holds |
|---|---|---|
| `OpenHealthSync` | iOS 15+, macOS 12+, watchOS 8+, Linux | the client, the models, the part builder, the upload line types |
| `OpenHealthSyncHealthKit` | iOS 15+, watchOS 8+, macOS 13+ | `HealthKitSync` ([below](#healthkit-sync)); empty on Linux |
| `OpenHealthSyncBluetooth` | iOS 15+, macOS 12+ | `BluetoothConnector` for standard Bluetooth health devices ([below](#bluetooth-devices)); empty on Linux |

## Install

Add the package with Swift Package Manager, by the URL of this repository:

```swift
// Package.swift of your app or backend
dependencies: [
    .package(url: "https://github.com/soma-intelligence/open-health-sync", from: "0.3.0"),
],
targets: [
    .target(name: "MyApp", dependencies: [
        .product(name: "OpenHealthSync", package: "open-health-sync"),
        .product(name: "OpenHealthSyncHealthKit", package: "open-health-sync"), // iOS apps
    ]),
]
```

In Xcode, *File > Add Package Dependencies*, paste the URL and pick the libraries.

The URL works from the first version tag that contains the `Package.swift` at the root of the
repository, which is `v0.3.0`: the tags before it have no manifest there and cannot be resolved.
The root manifest only exposes the three libraries; the sources stay in `sdks/swift`. Swift
Package Manager clones the whole repository, server included, and builds only the Swift targets.

Before that tag, or to work on the SDK, depend on a checkout by path. The package is then named
after the folder:

```swift
dependencies: [
    .package(path: "path/to/open-health-sync"),
],
// products as above, package: "open-health-sync"
```

In Xcode, *File > Add Package Dependencies > Add Local* and pick the checkout.

## Create a client

Which client holds which credential ([why](README.md#which-credential-goes-where)):

- **In the app**, the client holds a user token with the `upload` scope that your backend minted,
  never the tenant key. Give it a token provider: it is called before each request, so it can
  return a cached token and fetch a new one when it is about to expire. This client uploads;
  every other route answers 403 to a user token.
- **On your backend**, the client holds the tenant key and makes every other call: creating
  users, minting tokens, reading data, starting a provider connection.

```swift
import OpenHealthSync

// App
let uploads = OHSClient(
    baseURL: URL(string: "https://ohs.example.org")!,
    tokenProvider: { try await MyBackend.shared.uploadToken() } // your own function
)

// Backend (Swift on Linux or macOS)
let ohs = OHSClient(
    baseURL: URL(string: "https://ohs.example.org")!,
    token: ProcessInfo.processInfo.environment["OHS_KEY"]!)
let user = try await ohs.createUser(externalRef: "customer-42", idempotencyKey: "signup-42")
let token = try await ohs.mintUserToken(userId: user.id, scopes: [.upload]) // hand token.token to the app
```

`transport:` takes any `HTTPTransport` (one `send(URLRequest)` method), for a custom
`URLSession` or a fake in tests. `userAgent:` defaults to `open-health-sync-swift/0.1.0`.

## Read data

Reads take the tenant key, so they run on your backend. Every list call returns one `Page`
(`data`, `nextCursor`). `start` and `end` take an RFC 3339 time or a `YYYY-MM-DD` date as a string,
or a `Date` wrapped in `OHSTime(date)`, sent as RFC 3339 in UTC; the window is half open.

```swift
let page = try await ohs.listSamples(
    userId: userId, type: "heart_rate", start: "2026-01-10", end: "2026-01-11", limit: 500)
for sample in page.data {
    print(sample.startDate, sample.value, sample.unit ?? "")
}

// Every page, fetched as the loop asks for it:
for try await workout in OHSClient.items({ cursor in
    try await ohs.listWorkouts(userId: userId, start: "2026-01-01", cursor: cursor)
}) {
    print(workout.activityType)
}
```

`OHSClient.pages` gives the pages instead of the records, and `OHSClient.allItems` collects them
into an array. The other read calls are `listCategorySamples`, `listSleep`, `listSleepPhases`,
`listWorkouts`, `getWorkoutRoute`, `listNutrition`, `listDaily`, `getCoverage`,
`listConnections`, `getFreshness`, and, for the heart: `listECGRecordings` and `getECGRecording`
(one recording with its voltages as two parallel arrays, `samples.offsetsS` and
`samples.voltagesUv`), `listRRIntervals` (beat to beat intervals) and `listHRV` (RMSSD, SDNN and
pNN50 per heartbeat series; nil when too few intervals were accepted). Times are `Date`, read with up to nine fractional digits;
`date` fields stay `YYYY-MM-DD` strings because they are the user's local day. `metadata` and
`nutrients` are `[String: JSONValue]`.

Statistics are not paged: count, sum, mean, min and max per hour or per local day, one row per
interval from the best ranked source (`view: .all` gives one series per source instead).

```swift
let stats = try await ohs.sampleStats(
    userId: userId, type: "step_count", start: "2026-01-01", end: "2026-02-01",
    interval: .day, timeZone: "Europe/Paris")
for bucket in stats.buckets {
    print(bucket.start, bucket.sum)
}
```

FHIR R4 Observations come back as JSON, with the next cursor already parsed:

```swift
let bundle = try await ohs.searchFHIRObservations(userId: userId, category: "vital-signs")
for observation in bundle.observations {
    print(observation["id"]?.stringValue ?? "")
}
// bundle.nextCursor goes back as `cursor:`; bundle.raw is the whole Bundle.
let one = try await ohs.readFHIRObservation(userId: userId, observationId: "qs-1234")
```

## Connect a provider

Starting a connection takes the tenant key too: your backend calls `startConnection` and gives
the authorize URL to the app, which opens it. After consent the user lands on your `redirectUri`
with `status=connected` or `status=error`.

```swift
// Backend
let authorizeURL = try await ohs.startConnection(
    userId: userId, provider: "oura", redirectUri: "myapp://ohs/connected")

// App, with the URL it got from your backend
let session = ASWebAuthenticationSession(
    url: URL(string: authorizeURL)!, callbackURLScheme: "myapp") { callback, _ in
    // callback?.query holds status and provider
}
session.start()
```

`deleteConnection(userId:provider:)` disconnects; `listConnections` and `getFreshness` tell how up
to date each source is.

To let the user pick among several providers without a screen of your own, create a link to the
[hosted connection page](../connect-page.md) instead. The token in the link is the page's only
credential: give it to this user only.

```swift
// Backend
let page = try await ohs.createConnectSession(
    userId: userId, redirectUri: "myapp://ohs/connected", providers: ["oura", "withings"],
    appName: "My App", ttlSeconds: 900)
// page.url goes to the app, which opens it the same way; page.expiresAt says until when.
```

## Tags and self reports

A [time tag](../time-tags.md) is an interval that gives context to the data (an illness, a trip):
`createTimeTag`, `listTimeTags`, `getTimeTag`, `updateTimeTag` and `deleteTimeTag`, with the tenant
key. In a change, a member left alone stays as it is, and `.clear` sends null.

```swift
let tag = try await ohs.createTimeTag(
    userId: userId, tag: NewTimeTag(kind: .travel, startAt: "2026-03-01", label: "Tokyo", timeZone: "Asia/Tokyo"))
var change = TimeTagChange()
change.endAt = .set("2026-03-10")
_ = try await ohs.updateTimeTag(userId: userId, tagId: tag.id, change: change)
```

[Self reports](../self-reports.md) are the person's own ratings (mood, stress, energy, sleep
quality, soreness, exertion, pain). `createSelfReports` also works from the app, with a user token
holding the `upload` scope; the reports are read back as samples of type `reported_<kind>`.

```swift
let result = try await uploads.createSelfReports(
    userId: userId,
    reports: [NewSelfReport(id: UUID().uuidString, kind: .mood, value: 4, startAt: Date(),
                            timeZone: TimeZone.current.identifier)])
```

Give each report an `id` of your own: sending it again then replaces the value instead of
recording it twice.

## Upload

The app uploads HealthKit-shaped lines as parts ([line formats](../connectors/apple-health.md#line-formats)).
`HealthKitSync` ([below](#healthkit-sync)) does all of it; to send lines of your own, build them
with `QuantityLine`, `CategoryLine`, `WorkoutLine`, `CorrelationLine`, `HeartbeatSeriesLine`,
`AudiogramLine` or `StateOfMindLine`, or as a raw `[String: JSONValue]`, and let `AppleHealthPartBuilder` cut the parts:

```swift
let now = Date()
var builder = try AppleHealthPartBuilder(batch: AppleHealthPartBuilder.newBatchId(prefix: "myapp"))
try builder.add(QuantityLine(
    quantityType: "HKQuantityTypeIdentifierHeartRate",
    startDate: now, endDate: now, value: 72, unit: "count/min",
    metadata: ["HKTimeZone": .string(TimeZone.current.identifier)]))
let results = try await uploads.uploadParts(builder.build())
```

A typed line goes in the part of its local day in its `HKTimeZone` (the device zone when it has
none); `add(type:day:line:)` takes the day yourself. Parts hold one type and one day, at most
8 MiB of NDJSON before gzip, numbered from 0 across the batch. `AppleHealthUnits.wireUnit(forQuantityType:)`
gives the unit to write each quantity type in, one the server converts to its storage unit; it is
nil for a type the server does not store. Use a fresh batch id per run: the same bytes sent again
under the same key answer `duplicate: true`, other bytes a 409.

After the upload, your backend polls `getCoverage` until `pendingJobs` is 0, or calls
`waitUntilProcessed(userId:timeout:pollInterval:)`, which does that.

## HealthKit sync

`HealthKitSync` reads HealthKit with anchored queries and uploads the samples added and deleted
since the last run, with the app's upload client.

**Set up the app.** In Xcode, add the HealthKit capability (the `com.apple.developer.healthkit`
entitlement) and, for background delivery, tick *Background Delivery* under it. Add
`NSHealthShareUsageDescription` to `Info.plist`, the sentence iOS shows when it asks for access.
The SDK only reads, so `NSHealthUpdateUsageDescription` is not needed.

```swift
import HealthKit
import OpenHealthSync
import OpenHealthSyncHealthKit

let store = HKHealthStore()
let types = HealthKitSync.allSupportedTypes  // everything the server stores
let healthSync = HealthKitSync(store: store, client: uploads)

try await healthSync.requestAuthorization(for: types)
let report = try await healthSync.sync(types: types)
print(report.linesUploaded, report.partsUploaded, report.skippedSamples)
```

`requestAuthorization` shows the system sheet once. HealthKit never tells an app whether read
access was granted: a declined type returns no samples.

**Which types.** `HealthKitSync.allSupportedTypes` is every type the server stores that the OS
version has: quantities, nutrition, categories and sleep, workouts and their routes, ECG
recordings, heartbeat series, audiograms and states of mind (iOS 18). It is built from one table,
`AppleHealthTypes`, which is also what the sync reads samples by, so the set and the sync cannot
disagree; a test compares the table with the server's own. To ask for less, take the subsets
(`quantityTypes`, `nutritionTypes`, `categoryTypes`, `workoutTypes`, `heartRhythmTypes`) or build a
set by hand:

```swift
let types = HealthKitSync.workoutTypes.union(HealthKitSync.heartRhythmTypes).union([
    HKQuantityType.quantityType(forIdentifier: .heartRate)!,
    HKCategoryType.categoryType(forIdentifier: .sleepAnalysis)!,
])
```

A workout route is only readable when workouts are too, so keep the two together. Correlations
(blood pressure, food) are not in `allSupportedTypes`: their members are quantity samples of types
that are, and sending a food correlation next to its nutrients would store the meal twice. You can
still sync `HKCorrelationType` for blood pressure instead of the two quantity types; its members
then travel in one line. `requestAuthorization` asks for the member types in its place, since
HealthKit refuses an authorization request for a correlation type.

**Background delivery.** Call `enableBackgroundDelivery` at every launch, after authorization,
from `application(_:didFinishLaunchingWithOptions:)`. It turns on HealthKit's background delivery
for each type and keeps an observer query that runs `sync` when HealthKit reports new samples, even
when the app was not running. The token provider is called then, so it must work without the user
in front of the app (keep the token in the keychain and refresh it with your backend).

```swift
try await healthSync.enableBackgroundDelivery(for: types, frequency: .immediate) { result in
    if case .failure(let error) = result { print("sync failed: \(error)") }
}
```

iOS decides how often it actually wakes the app; `.immediate` is a request, and step counts are
delivered at most hourly whatever you ask.

**Catching up.** Background delivery covers new and deleted samples while the device is unlocked
enough for HealthKit to be read. It does not retry: when a wake finds no network, an expired token
or a locked device, the sync fails, the anchor stays where it was, and nothing runs again until
HealthKit reports another change of that type or the user opens the app. It also stops after the
user force quits the app, until the next launch. Two things close the gap, and the SDK needs no
helper for them because a catch-up is one more `sync` call:

1. Call `sync` when the app comes to the foreground.
2. Schedule a background refresh with `BGTaskScheduler`. Add the *Background fetch* mode and your
   task identifier under `BGTaskSchedulerPermittedIdentifiers` in `Info.plist`, register before
   the end of `application(_:didFinishLaunchingWithOptions:)`, and schedule again from the task.

```swift
import BackgroundTasks

let catchUpTask = "com.example.myapp.health-catch-up"

func registerCatchUp(_ healthSync: HealthKitSync, types: Set<HKSampleType>) {
    BGTaskScheduler.shared.register(forTaskWithIdentifier: catchUpTask, using: nil) { task in
        scheduleCatchUp()  // the next one
        let work = Task {
            do {
                _ = try await healthSync.sync(types: types)
                task.setTaskCompleted(success: true)
            } catch {
                task.setTaskCompleted(success: false)
            }
        }
        task.expirationHandler = { work.cancel() }
    }
}

func scheduleCatchUp() {
    let request = BGAppRefreshTaskRequest(identifier: catchUpTask)
    request.earliestBeginDate = Date(timeIntervalSinceNow: 4 * 3600)
    try? BGTaskScheduler.shared.submit(request)
}
```

A sync cut short by the expiration is safe: the anchor of a type only moves once its round is
uploaded. HealthKit cannot be read while the device is locked, so a refresh that runs then fails
and the next one catches up.

**What is sent.** One line per new sample, in the [line format](../connectors/apple-health.md#line-formats):

| HealthKit sample | Line |
|---|---|
| `HKQuantitySample` | `quantityType`, `value` and `unit` in the unit of `AppleHealthUnits` (`count/min` for heart rates, `%` for oxygen saturation as a 0 to 1 fraction, `kg`, `m`, `kcal`, `degC`, `mmHg`, `ms`, `mg/dL`...). Dietary types become nutrition logs on the server |
| `HKCategorySample` | `categoryType` and its integer `value`; sleep analysis becomes sleep sessions and phases |
| `HKWorkout` | `workoutActivityType` raw value, `durationS`, `totalDistance` in metres, `totalEnergyBurned` in kilocalories, heart rate average, minimum and maximum (iOS 16+) |
| `HKCorrelation` | `correlationType` and its quantity `objects` (blood pressure, food) |
| `HKWorkoutRoute` | one line per GPS point (`timestamp`, `latitude`, `longitude`, and `altitude`, `speed`, `course` when valid), read with `HKWorkoutRouteQuery`. Every part starts with the line of the route's workout, which is how the server knows whose points they are |
| `HKElectrocardiogram` | the recording (`classification` and `symptomsStatus` raw values, `averageHeartRate`, `samplingFrequency` in hertz, `numberOfVoltageMeasurements`), then one line per voltage (`timeSinceSampleStart`, `value` in volts), read with `HKElectrocardiogramQuery`, in the same part |
| `HKHeartbeatSeriesSample` | one line with the whole series in `heartbeats` (`timeSinceSeriesStart`, `precededByGap`), read with `HKHeartbeatSeriesQuery`; the server stores the RR intervals |
| `HKAudiogramSample` | the sensitivity points (`frequency` in hertz, `leftEarSensitivity` and `rightEarSensitivity` in dBHL) |
| `HKStateOfMind` (iOS 18) | the raw values of its kind, labels and associations, and its valence |

Every line carries `startDate`, `endDate`, `sourceName`, `sourceBundleId`, `device`, `deviceModel`,
`productType`, `deviceManufacturer`, the hardware, software and OS versions, `wasUserEntered`, the
sample `uuid`, and `metadata`: HealthKit's metadata with numbers and booleans kept, dates as
RFC 3339 and quantities as text (`1.25 m/s`). When HealthKit recorded no `HKTimeZone`, the device
zone is written there, so the server counts the sample on the user's local day. A sample of a
type that is not in `AppleHealthTypes`, or whose unit does not fit, is left out and counted in
`report.skippedSamples`. GPS points and voltages carry only their own fields.

**Routes and ECG.** These follow the same anchors, deletions and resume rules as the other types,
with two differences. A round reads at most `seriesQueryLimit` of them (25), so the anchor is
saved often. And their data is never held whole: points and voltages are read a batch at a time
and uploaded in parts of at most `seriesPartBytes` of NDJSON (2 MiB), one batch id per route or
recording; the query is held back while an upload is in flight. An Apple Watch ECG fits in one
part. A long route takes several, and each repeats the workout line, which the server stores once.
When the workout of a route cannot be read (workouts not authorized) its points go alone, and the
server attaches them to a stored workout that overlaps them in time, or drops them when there is
none. A heartbeat series of more than 5000 beats is cut into several lines that share one beat at
each cut, so every interval is still computed once. The server keeps no record under the UUID of a
route, and does not apply the deletion of an ECG or a heartbeat series (see the connector page);
their deletion lines are sent like the others.

**Deleted and edited samples.** The anchored query also returns the samples deleted since the
anchor (`HKDeletedObject`). Each becomes a [deletion line](../connectors/apple-health.md#deletions),
`{"deletedUuid":"<uuid>"}`, and the server removes what it stored from that sample. HealthKit
never edits a sample in place: an app that edits a weight or a blood pressure reading deletes the
old sample and saves a new one, so without the deletion both would stay stored. A round's
deletions go once, in a part of their own dated today, after its samples. `report.deletionsUploaded`
counts them. Sleep stage deletions are sent but change nothing stored (see the connector page).

Deletions are sent only to a server whose upload answers list `deletion_lines`, learnt from the
first upload of the run (`healthSync.features`). Until then, and against an older server, they
are kept beside the anchor (`<type>.pendingDeletions` in the `AnchorStore`), counted in
`report.deletionsPendingByType`, and sent by a later sync once the server lists the feature. So a
new app against an old server loses no deletion, and catches up after the server's upgrade.

**Anchors.** Each type has its own `HKQueryAnchor`, archived in `UserDefaults` by default (pass
another `AnchorStore` to keep them elsewhere). A sync reads at most `queryLimit` objects (5000),
new and deleted samples together, per round, uploads them under a fresh batch id, and saves the
new anchor only after every part of that round, samples and deletions, is uploaded. If the app is
killed or an upload fails, the next sync reads the same samples and deletions again; the server
keeps each record once and applies a deletion once, so nothing is doubled or lost. Pending
deletions are saved before the anchor moves. `resetAnchor(for:)` makes the next
sync send a type's whole history; once, after upgrading from 0.2.0, it makes the history stored
then deletable (each sample leaves its UUID on its row). Calls to `sync` run one after the other.

## Bluetooth devices

Heart rate straps, blood pressure cuffs, scales, glucose meters, thermometers and pulse oximeters
that use the standard Bluetooth health profiles upload straight from the app, with no vendor app
in between ([devices and mapping](../connectors/bluetooth.md)). The app forwards what each device
sends as bytes; the server decodes it.

**Set up the app.** Add the `OpenHealthSyncBluetooth` product to the target and
`NSBluetoothAlwaysUsageDescription` to `Info.plist`, the sentence iOS shows when it asks for
Bluetooth. To keep receiving from connected devices while the app is in the background, add
`bluetooth-central` to `UIBackgroundModes`.

```swift
import OpenHealthSync
import OpenHealthSyncBluetooth

let connector = try BluetoothConnector(deviceUser: 1) { event in
    switch event {
    case .connected(let name): print("\(name) connected")
    case .note(let name, let text): print("\(name): \(text)")  // for example, pair the device first
    default: break
    }
}
connector.start()

// Later, for example when the app moves to the background:
if let part = try connector.takePart() {
    _ = try await uploads.uploadBluetoothPart(part)
}
```

`BluetoothConnector` scans for devices that advertise a health service (or, with `deviceNames`,
for names containing one of them), connects, reads Device Information, sets the device clock
through the Current Time service when the device allows it, subscribes to every measurement, and
asks a glucose meter for the records after the last one recorded (all of them the first time). It
reconnects when a device comes back in range. `deviceUser` is this person's user index on a shared
scale or cuff, so the server stores only their readings. Events arrive on the connector's own
queue: `.value` carries each value as received, for a live view. Cuffs and meters usually need
pairing: iOS shows its prompt on the first subscription.

`takePart()` returns what was recorded since the last call as one `BluetoothPart`, or nil. Keep
the part until `uploadBluetoothPart` succeeds: it is `Codable`, so it can wait on disk, and the
same part sent again is answered `duplicate: true`.

To feed values from code of your own, record them in a `BluetoothRecorder`; it does not decode
either:

```swift
var recorder = try BluetoothRecorder()
let session = BluetoothRecorder.newSession()  // one per connection to one device
try recorder.record(
    characteristic: BluetoothGATT.heartRateMeasurement, value: Data([0x00, 72]),
    session: session, device: BluetoothDevice(name: BluetoothGATT.publicName(advertisedName)))
let part = try recorder.makePart()
```

`BluetoothGATT` holds the service and characteristic numbers, `publicName` (an advertised name
without a serial-like last word), and the bytes to write: `reportStoredRecords(after:)` for a
glucose meter and `currentTimeValue(_:)` for a device clock. Never send a device address or a
serial number: the format has no field for them.

## Errors and retries

A non-2xx answer throws `OHSAPIError`: `status`, `title`, `detail`, `type`, `requestId` (from the
problem body, else the `X-Request-Id` header; quote it to the operator) and `retryAfterSeconds`. A
body that is not JSON still gives the status. Arguments the SDK rejects before sending, and 2xx
bodies it cannot read, throw `OHSError`. Neither ever holds the token or a health value.

```swift
do {
    _ = try await uploads.uploadAppleHealthPart(part)
} catch let error as OHSAPIError where error.status == 401 {
    // the token expired or lost its scope: get a new one from your backend
}
```

Uploads are retried on 429, 503, other 5xx answers and network errors: after 1 s, then 2, 4 and
8 s (30 s at most), or after `Retry-After` when the server sends it, 5 attempts in all. Any other
4xx throws at once. Reads are not retried. Pass `sleeper:` to the client to replace the wait.

## Run the tests

```sh
cd sdks/swift
swift test
```

The tests need no server and run on macOS and Linux. They check the requests (method, path,
query, headers), decode an answer for every record type, the error mapping, the retries, paging,
the part builder and its gzip output, and that the upload lines match the server's own fixture
`connectors/applehealth/testdata/ios_upload_lines.ndjson`. The anchored sync (parts, deletions,
anchors, the chunked upload of routes and ECG recordings) runs against fakes, and on macOS the
type table is checked against HealthKit's own identifiers and against the server's
`connectors/applehealth/types.go`. Reading from a real HealthKit store needs a device or a
simulator and has no automated test here.

## Run the example

[`Sources/OHSExample/main.swift`](../../sdks/swift/Sources/OHSExample/main.swift) runs against a
live server with a tenant key: it creates a user, mints an upload token, uploads one heart rate
reading, waits for processing and reads it back.

```sh
cd sdks/swift
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... swift run OHSExample
```

It exits non-zero unless exactly one `heart_rate` sample comes back, and prints no token or value.
