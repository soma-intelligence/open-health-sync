# Plan: Health Connect reader for the Kotlin SDK

Spec: [specs/health-connect-reader.md](../specs/health-connect-reader.md).

1. Core SDK tests first (`sdks/kotlin`): `uploadHealthConnectPart` route, headers, retries and
   errors, with the existing fake transport; `HealthConnectPartBuilder` grouping by type and day,
   splitting by size, numbering across the batch.
2. Core SDK code: an internal part grouper shared by `AppleHealthPartBuilder` and
   `HealthConnectPartBuilder` (the Apple Health builder's behavior and API unchanged), the
   upload method, and the docs comment of each.
3. New Gradle build `sdks/kotlin-healthconnect`: Android library plugin, the same AGP and Kotlin
   versions as the example, `includeBuild("../kotlin")` for the core SDK, `connect-client`
   1.1.0, WorkManager, and for tests `connect-testing`, `kotlin-test` and coroutines test.
4. Mapping tests first: build the records behind each line of
   `connectors/healthconnect/testdata/upload.ndjson` and compare the JSON with that file read
   from the repository; one test per enum (sleep stage, exercise type, specimen source, meal
   type, relation to meal, body position, measurement location, flow, recording method, device
   type).
5. `HealthConnectLines` (records to lines) and the enum tables.
6. Sync tests first with `FakeHealthConnectClient`, an in-memory `ChangesTokenStore` and a fake
   transport, for each case of the spec's Requirements; then `HealthConnectSync`,
   `ChangesTokenStore` (in memory and `SharedPreferences`), `SyncReport`, `permissions()`.
7. `HealthConnectSyncWorker` and `schedule()`, with a test that the worker calls `sync` and maps
   errors to retry or failure.
8. Example app: the Health Connect screen, manifest entries (read permissions, rationale
   activity, `VIEW_PERMISSION_USAGE` alias, `queries` for the Health Connect package), the
   worker subclass. `settings.gradle.kts` includes the new build.
9. `test/e2e/sdks.sh`: the Kotlin step uploads one Health Connect part built with the core SDK
   and checks coverage.
10. CI: the SDKs workflow runs `./gradlew testDebugUnitTest` in `sdks/kotlin-healthconnect`; the
    examples workflow already builds the app.
11. Docs: sdks/kotlin.md, connectors/health-connect.md (status), examples/android/README.md,
    CHANGELOG.

No server change, no migration. New dependencies only in the new Android library: the spec
lists them and why.
