# Spec: Health Connect reader for the Kotlin SDK

Status: draft for review (2026-10-17). Guides: [connectors/health-connect.md](../connectors/health-connect.md),
[sdks/kotlin.md](../sdks/kotlin.md).

## Problem

The server has taken Health Connect records since v0.1.0 (`POST /v1/ingest/health-connect`, line
format in [connectors/health-connect.md](../connectors/health-connect.md)), but nothing in this
repository reads Health Connect. An Android developer has to write the reading, the mapping to
lines, the change tracking and the upload themselves, while an iOS developer gets all of it from
`HealthKitSync` in the Swift SDK. The Android example app only uploads a heart rate typed by
hand.

This gap matters more now that Google Fit is going away. Google closed the Fit APIs to new
developers on 2024-05-01 and turns them off at the end of 2026. Google Fit, Samsung Health,
Fitbit, Oura, OMRON connect and CGM apps all write to Health Connect, so on Android the reader is
how their data reaches Open Health Sync.

## Integration path, as checked on 2026-10-17

- `androidx.health.connect:connect-client` 1.1.0 is the current stable release (2025-10-08). The
  Health Connect API is part of the platform on Android 14 and later. On Android 8 to 13 it is
  the Health Connect app from the Play Store.
- An app reads only the record types the user granted. By default it reads data from 30 days
  before its first grant. Older data needs the history permission
  (`PERMISSION_READ_HEALTH_DATA_HISTORY`), and reading while the app is in the background needs
  `PERMISSION_READ_HEALTH_DATA_IN_BACKGROUND`. Both are gated by feature availability.
- Changes are read with a changes token (`getChangesToken`, then `getChanges` until `hasMore`
  is false). Google recommends one token per record type. A `DeletionChange` carries only the
  record id. An unused token expires within 30 days (`changesTokenExpired`).
- `androidx.health.connect:connect-testing` (1.0.0-alpha03) has a `FakeHealthConnectClient` for
  unit tests.

## Scope

1. **Core Kotlin SDK** (`sdks/kotlin`, plain JVM, unchanged dependencies):
   `OhsClient.uploadHealthConnectPart(part)` posts one part to `/v1/ingest/health-connect` with
   the same retries as the Apple Health upload. `HealthConnectPartBuilder` groups lines into
   parts: one record type and one local day per part, part numbers unique across the batch.
   It shares its grouping code with `AppleHealthPartBuilder`.
2. **New Android library `sdks/kotlin-healthconnect`** (`org.openhealthsync:open-health-sync-healthconnect`,
   placeholder coordinates like the core SDK). It depends on the core SDK, `connect-client`
   1.1.0 and WorkManager:
   - `HealthConnectLines`: one Health Connect record to one line in the server's format, for
     every record type the server maps (table below). The type-specific enums are written as
     the lowercase constant name without its prefix.
   - `HealthConnectSync`: `sync(types)` reads what changed since the last run for each type,
     uploads it, and returns a `SyncReport` (batch ids, parts, duplicates, lines per type,
     deletions, skipped records), like `HealthKitSync` on iOS. `permissions(types, history,
     background)` lists the permission strings for the request contract.
   - `ChangesTokenStore`: the token and the last sync time per record type, kept in
     `SharedPreferences` by default.
   - `HealthConnectSyncWorker`: an abstract WorkManager worker the app subclasses (it returns
     its `HealthConnectSync`), and `HealthConnectSync.schedule(context, interval)` for periodic
     background sync when the background permission is granted.
3. **Android example** (`examples/android`): a Health Connect screen that shows availability,
   asks for the permissions, runs **Sync now** and shows the report, and turns background sync
   on or off. The manifest declares the read permissions, the permissions rationale activity and
   the Android 14 `VIEW_PERMISSION_USAGE` alias that Health Connect requires.
4. **Docs**: a Health Connect section in [sdks/kotlin.md](../sdks/kotlin.md), the status line of
   [connectors/health-connect.md](../connectors/health-connect.md), the example README (an
   emulator with Google Fit, or the Health Connect Toolbox, as data sources), and CHANGELOG.

## How one sync works, per record type

1. **No token yet** (first sync of the type, or the type was just granted): get a changes token
   first, then read the records of the last `backfill` (default 30 days, the default read window)
   with `readRecords`, page by page. Getting the token before reading means nothing written
   during the read is missed: it comes back as a change next time, and the server stores it once.
2. **Token**: `getChanges` until `hasMore` is false. An `UpsertionChange` of this type becomes a
   line. A `DeletionChange` becomes a `Deletion` line with its record id. A change written by the
   app itself is ignored.
3. **Expired token**: as a first sync, reading from the last successful sync time (30 days at
   most) instead of the full backfill.
4. Lines are grouped by local day. The day is that of `time` or `startTime` in the record's own
   zone offset, or in the device zone without one. `Deletion` lines go to the current day. Parts
   go up under a fresh batch id (`android-` and 16 random characters).
5. The new token and the sync time are saved only once every part of the type is uploaded. An
   interrupted sync sends the same records again next time, and the server stores them once:
   a sample is identified by record id and start (connectors/health-connect.md#mapping-notes).

Types are synced one after the other, and one `sync` call runs at a time.

## Record types

The server maps these, so the reader reads them (connectors/health-connect.md#line-format):

| Health Connect record | Line fields |
|---|---|
| `StepsRecord` | `count` |
| `HeartRateRecord` | `samples[].time`, `samples[].beatsPerMinute` |
| `RestingHeartRateRecord` | `beatsPerMinute` |
| `HeartRateVariabilityRmssdRecord` | `heartRateVariabilityMillis` |
| `OxygenSaturationRecord` | `percentage` |
| `RespiratoryRateRecord` | `rate` |
| `BodyTemperatureRecord` | `temperatureCelsius`, `measurementLocation` |
| `BloodPressureRecord` | `systolicMillimetersOfMercury`, `diastolicMillimetersOfMercury`, `bodyPosition`, `measurementLocation` |
| `BloodGlucoseRecord` | `levelMillimolesPerLiter`, `specimenSource`, `mealType`, `relationToMeal` |
| `WeightRecord` | `weightKilograms` |
| `HeightRecord` | `heightMeters` |
| `BodyFatRecord` | `percentage` |
| `ActiveCaloriesBurnedRecord` | `energyKilocalories` |
| `TotalCaloriesBurnedRecord` | `energyKilocalories` |
| `BasalMetabolicRateRecord` | `basalMetabolicRateKilocaloriesPerDay` |
| `DistanceRecord` | `distanceMeters` |
| `SleepSessionRecord` | `title`, `stages[]` (`startTime`, `endTime`, `stage`) |
| `ExerciseSessionRecord` | `exerciseType`, `title` |
| `MenstruationFlowRecord` | `flow` |
| `IntermenstrualBleedingRecord` | none |
| `NutritionRecord` | `name`, `mealType`, `energyKilocalories`, `energyFromFatKilocalories`, every mass as `<field>Grams` (`Mass.inGrams`, the server converts each to its nutrient's unit) |
| `HydrationRecord` | `volumeLiters` |

Every line carries `recordType`, the times and zone offsets, and `metadata` (`id`,
`dataOrigin` package name, `lastModifiedTime`, `clientRecordId`, `recordingMethod`, `device`).
Units come from the androidx unit classes (`Mass.inKilograms`, `Pressure.inMillimetersOfMercury`,
`BloodGlucose.inMillimolesPerLiter`, ...), never from a display string.

Not read, because the server does not map them yet: `Vo2Max`, `ExerciseRoute`, `Power`, `Speed`
and the other types. Adding one is a server change first (a mapper entry and its tests), then
one line here.

## Out of scope

- Writing to Health Connect. The reader only reads.
- Any type the server does not map (above).
- Publishing the library to Maven Central: the core SDK is not published either.
- The changes token per data origin, or filtering by source app: the server keeps the source
  (`dataOrigin`), and source priorities rank sources after the fact.
- Instrumented tests on an emulator in CI (see Requirements).

## Standards

Nothing new on the server. Units and codes are the ones the Health Connect mapper already uses:
`step_count` count, `heart_rate` bpm, `body_mass` kg, glucose stored in mg/dL and coded LOINC
99504-3 when `specimenSource` is `interstitial_fluid`. The reader's job is to write exactly the
lines the server documents.

## Requirements

- Tests first.
- **Mapping against the server's own fixture.** For the records behind each line of
  `connectors/healthconnect/testdata/upload.ndjson`, `HealthConnectLines` produces the same JSON
  fields and values. That file is what the server's mapper tests read, so the two sides cannot
  drift apart unnoticed. Every enum value of the supported types also has a test.
- **Sync logic** with `FakeHealthConnectClient` and a fake transport: first sync (token taken
  before the read, saved after the upload), changes with upserts and deletions, the app's own
  records skipped, expired token, an upload failure leaves the token unchanged, parts split by
  local day and by size, one `sync` at a time.
- **Core SDK**: `uploadHealthConnectPart` sends the route, headers and retries like the Apple
  Health upload.
- **End to end**: `test/e2e/sdks.sh` uploads Health Connect parts built with the core SDK to a
  live stack and reads them back through coverage. The Android side runs only in unit tests,
  because an emulator with Health Connect is too heavy for every pull request.
- CI: the library's unit tests run in the SDKs workflow, and the example builds in the examples
  workflow.
- Minimum Android 8.0 (API 26), compile SDK 35, like the example. No new dependency in the core
  SDK. The new library adds `connect-client`, WorkManager and, for tests only, `connect-testing`.

## Open questions

1. **A separate Android library** (`sdks/kotlin-healthconnect`, recommended), or turning the
   whole Kotlin SDK into an Android library? Separate keeps the core SDK usable on servers and
   mirrors `OpenHealthSyncHealthKit` on iOS.
2. **First sync window**: 30 days by default (recommended: it is what Health Connect allows
   without the history permission), with an option to request the history permission and read
   everything?
3. **Background sync in this change** (recommended, `HealthConnectSyncWorker`), or foreground
   only first and background in a follow-up?
