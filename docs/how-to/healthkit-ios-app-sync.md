# Sync HealthKit data from your iOS app to your own backend

HealthKit has no server API, so your iOS app has to send the data out. With the Open Health Sync
Swift SDK, `HealthKitSync` reads HealthKit with anchored queries and uploads the samples added
and deleted since the last run to `POST /v1/ingest/apple-health` on a server you run, in the
background when iOS wakes the app. Your backend then reads the data over the REST API, in SQL or
as FHIR, with no HealthKit code of its own.

## 1. Add the package

The SDK is not on any package registry yet: add it from a checkout of the
repository, by path.

```swift
// Package.swift
dependencies: [
    .package(path: "path/to/open-health-sync/sdks/swift"),
],
targets: [
    .target(name: "MyApp", dependencies: [
        .product(name: "OpenHealthSync", package: "OpenHealthSync"),
        .product(name: "OpenHealthSyncHealthKit", package: "OpenHealthSync"),
    ]),
]
```

In Xcode: *File > Add Package Dependencies > Add Local*, then pick `sdks/swift`.
`OpenHealthSyncHealthKit` runs on iOS 15+, watchOS 8+ and macOS 13+.

## 2. Set up the app target

- Add the HealthKit capability (`com.apple.developer.healthkit`) and tick *Background Delivery*.
- Add `NSHealthShareUsageDescription` to `Info.plist`. The SDK only reads, so
  `NSHealthUpdateUsageDescription` is not needed.

## 3. Hand the app an upload token

The app holds a user token with the `upload` scope, never the tenant key. Your backend creates
the user and mints the token; the app's client asks your backend for it through a token
provider, called before each request:

```swift
// Backend (Swift on Linux or macOS), with the tenant key
let user = try await ohs.createUser(externalRef: "customer-42", idempotencyKey: "signup-42")
let token = try await ohs.mintUserToken(userId: user.id, scopes: [.upload])

// App
let uploads = OHSClient(
    baseURL: URL(string: "https://ohs.example.org")!,
    tokenProvider: { try await MyBackend.shared.uploadToken() })
```

The token provider also runs during background wakes, so keep the token in the keychain and
refresh it with your backend without the user in front of the app.

## 4. Ask for access and sync

```swift
import HealthKit
import OpenHealthSyncHealthKit

let store = HKHealthStore()
let types: Set<HKSampleType> = [
    HKQuantityType.quantityType(forIdentifier: .heartRate)!,
    HKQuantityType.quantityType(forIdentifier: .stepCount)!,
    HKCategoryType.categoryType(forIdentifier: .sleepAnalysis)!,
    HKCorrelationType.correlationType(forIdentifier: .bloodPressure)!,
    HKObjectType.workoutType(),
]
let healthSync = HealthKitSync(store: store, client: uploads)
try await healthSync.requestAuthorization(for: types)
let report = try await healthSync.sync(types: types)
```

HealthKit never tells an app whether read access was granted: a declined type returns no
samples. Ask for blood pressure through the correlation type so systolic and diastolic travel
together. `report` counts lines and parts uploaded and samples skipped (a type the server does
not store).

## 5. Turn on background delivery

Call this at every launch, after authorization, from
`application(_:didFinishLaunchingWithOptions:)`:

```swift
try await healthSync.enableBackgroundDelivery(for: types, frequency: .immediate) { result in
    if case .failure(let error) = result { print("sync failed: \(error)") }
}
```

iOS decides how often it wakes the app; step counts come at most hourly. HealthKit data
can't be read while the device is locked, so expect wakes to land once it isn't
([on-phone stores](../background-sync.md#on-phone-stores-apple-health-health-connect)). A
`BGAppRefreshTask` is a backstop for missed wakes.

## What it handles for you

| Concern | How |
|---|---|
| Only new data | one `HKQueryAnchor` per type, saved only after every part of a round is uploaded |
| Crashes and retries | the next sync reads the same samples again; the server keeps each record once |
| Deletions and edits | `HKDeletedObject` becomes a `{"deletedUuid":"..."}` line, so an edited reading is not stored twice |
| Large histories | at most 5000 objects per round (`queryLimit`), parts of at most 8 MiB of NDJSON |
| Local days | the device zone goes into `HKTimeZone` when HealthKit recorded none |

`resetAnchor(for:)` makes the next sync send a type's whole history. Line formats are in the
[Apple Health connector guide](../connectors/apple-health.md#line-formats).

## 6. Read it on your backend

After an upload, call `waitUntilProcessed(userId:timeout:pollInterval:)`, or poll `getCoverage`
until `pendingJobs` is 0, then read with the tenant key (`listSamples`, `listSleep`,
`listWorkouts`, `searchFHIRObservations`). `GET /v1/users/{id}/freshness` marks the source
`stale` after 26 hours without an upload, so you can nudge the user to open the app. The
[iOS example](../../examples/ios/README.md) wires all of this into a small SwiftUI app.

## React Native or Flutter

Those SDKs do not read HealthKit themselves. A community module (`react-native-health`, or the
Flutter `health` package) reads it, you turn each reading into a HealthKit-shaped line, and the
SDK's part builder uploads it. Anchors, deletions and background delivery are then yours to
write. See [React Native](../sdks/react-native.md) and [Flutter](../sdks/flutter.md).

## Related

- [Import Apple Health data into PostgreSQL](apple-health-to-postgres.md)
- [Send Android Health Connect data to your own server](health-connect-to-server.md)
- [Record a Bluetooth heart rate strap, blood pressure cuff or scale without a phone](bluetooth-devices.md)
- [Swift SDK](../sdks/swift.md#healthkit-sync)
- [README](../../README.md)
