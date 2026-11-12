# Open Health Sync for Kotlin

Two modules:

- **`open-health-sync-kotlin`** (this directory): the Kotlin client for the Open Health Sync REST
  API, for Android (API 26+) and the JVM (11+). Plain JVM, no Android class.
- **`open-health-sync-health-connect`** ([`health-connect/`](health-connect)): an Android library
  that reads Health Connect and uploads what changed to `POST /v1/ingest/health-connect`:
  permissions, incremental sync with changes tokens, an upload queue on disk with retries, and a
  WorkManager job for the background. It covers the 22 record types the server stores.

```sh
./gradlew build       # both modules: compile, unit tests, Android lint
./gradlew test        # unit tests, no server and no device
./gradlew runExample  # the quickstart, with OHS_URL and OHS_KEY set
```

The Health Connect module needs JDK 17 and an Android SDK (`ANDROID_HOME`, `ANDROID_SDK_ROOT`
or `sdk.dir` in `local.properties`) with platform 36. Without one it is left out of the build
and the client builds alone; `-Pohs.android=false` leaves it out on purpose.

Guide: [docs/sdks/kotlin.md](../../docs/sdks/kotlin.md). Licence: AGPL-3.0-only, like the server ([LICENSE](../../LICENSE)).
