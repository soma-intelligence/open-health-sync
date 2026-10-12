// SPDX-License-Identifier: AGPL-3.0-only

// The README quickstart with the Kotlin SDK: create a user, mint a token, upload one heart rate
// reading, wait for processing and read it back. Run against a test instance:
//   OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... ./gradlew runExample
import java.time.Instant
import java.time.ZoneOffset
import java.time.temporal.ChronoUnit
import kotlin.system.exitProcess
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.openhealthsync.sdk.AppleHealthPartBuilder
import org.openhealthsync.sdk.OhsClient

fun main(): Unit = runBlocking {
    val url = System.getenv("OHS_URL") ?: "http://localhost:8080"
    val key = System.getenv("OHS_KEY") ?: error("set OHS_KEY to a tenant API key")

    // Server side: the tenant key creates the user and mints the app's token.
    val server = OhsClient(url, key)
    val user = server.createUser("kotlin-example-" + AppleHealthPartBuilder.newBatchId("u"))
    val token = server.mintUserToken(user.id, listOf("upload"))
    println("user ${user.id} created, token minted")

    // App side: only the user token, which can upload and nothing else.
    val app = OhsClient(url, token.token)
    val now = Instant.now().truncatedTo(ChronoUnit.SECONDS)
    val line = buildJsonObject {
        put("quantityType", "HKQuantityTypeIdentifierHeartRate")
        put("startDate", now.toString())
        put("endDate", now.toString())
        put("value", 72)
        put("unit", "count/min")
        put("sourceName", "Kotlin example")
        put("metadata", buildJsonObject { put("HKTimeZone", "UTC") })
    }
    val parts = AppleHealthPartBuilder(AppleHealthPartBuilder.newBatchId("kt"))
        .add("HKQuantityTypeIdentifierHeartRate", AppleHealthPartBuilder.localDay(now, ZoneOffset.UTC), line)
        .build()
    val results = app.uploadParts(parts)
    println("${results.size} part uploaded")

    val deadline = System.currentTimeMillis() + 120_000
    // Server side again: reading needs the tenant key.
    while (server.getCoverage(user.id).pendingJobs > 0) {
        if (System.currentTimeMillis() > deadline) {
            println("processing did not finish in 120 s")
            exitProcess(1)
        }
        delay(1000)
    }
    val samples = server.listSamples(user.id, "heart_rate").data
    println("heart_rate samples: ${samples.size}")
    if (samples.size != 1) exitProcess(1)
}
