# Kotlin SDK

The Kotlin client for the [REST API](../api.md), for Android apps and JVM backends. Coroutines
for every call, kotlinx.serialization for the models, `HttpURLConnection` underneath, so it runs on
Android API 26+ and any JVM 11+ with no HTTP library. It follows the shape every SDK shares
([overview](README.md)). AGPL-3.0-only, like the server ([LICENSE](../../LICENSE)).

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

The upload route takes HealthKit-shaped lines
([line formats](../connectors/apple-health.md#line-formats)), so on Android it serves data you
already hold in that shape, for example lines converted from a Health app export. Reading Health
Connect is planned for this SDK.

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

## Errors and retries

A non-2xx answer throws `OhsApiException` with `status`, `title`, `detail`, `type`, `requestId`
(quote it in a bug report) and `retryAfterSeconds`. A network failure throws `IOException`.
Uploads are retried on 429, 503, other 5xx answers and network errors, waiting `Retry-After` when
the server sends it and 1, 2, 4, 8 seconds otherwise, up to 5 attempts (`maxUploadAttempts`).
Other calls are not retried. Neither the token nor a health value appears in an exception message.

## Run the tests

```sh
cd sdks/kotlin
./gradlew test
```

The tests use a fake transport, no server.

## Run the example

[`src/example/kotlin/Quickstart.kt`](../../sdks/kotlin/src/example/kotlin/Quickstart.kt) runs the
README quickstart: it creates a user, mints a token, uploads one heart rate reading, waits for
processing and reads it back. Point it at a test instance, never at real users' data:

```sh
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... ./gradlew runExample
```

CI runs it, and the other SDKs' examples, against a server built from the same commit
(`test/e2e/sdks.sh`).
