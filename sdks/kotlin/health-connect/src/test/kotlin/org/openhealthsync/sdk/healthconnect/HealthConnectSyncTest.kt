// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk.healthconnect

import androidx.health.connect.client.records.Record
import androidx.health.connect.client.records.StepsRecord
import androidx.health.connect.client.records.WeightRecord
import androidx.health.connect.client.units.Mass
import java.io.IOException
import java.time.Duration
import java.time.Instant
import java.time.ZoneId
import java.time.ZoneOffset
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertIs
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.openhealthsync.sdk.HealthConnectPart
import org.openhealthsync.sdk.HttpRequest
import org.openhealthsync.sdk.HttpResponse
import org.openhealthsync.sdk.HttpTransport
import org.openhealthsync.sdk.OhsApiException
import org.openhealthsync.sdk.OhsClient
import org.openhealthsync.sdk.UploadResult

private val STEPS = HealthConnectRecordType.STEPS
private val WEIGHT = HealthConnectRecordType.WEIGHT
private val NOW: Instant = Instant.parse("2026-03-10T12:00:00Z")

private fun steps(id: String, start: String = "2026-03-02T08:00:00Z", count: Long = 100, offset: ZoneOffset? = ZoneOffset.ofHours(1)): StepsRecord {
    val at = Instant.parse(start)
    return StepsRecord(at, offset, at.plusSeconds(3600), offset, count, meta(id, "com.example.app"))
}

private fun weight(id: String, at: String = "2026-03-02T06:50:00Z") =
    WeightRecord(Instant.parse(at), null, Mass.kilograms(72.4), meta(id, "com.example.scale"))

/** Health Connect, scripted: what each call was asked and what it answers. */
private class FakeReader : HealthConnectReader {
    var granted: Set<String> = HealthConnectSync.authorizationPermissions()
    val log = mutableListOf<String>()
    var tokenSeq = 0

    /** Window reads: the pages of a type, in order. */
    val windows = HashMap<HealthConnectRecordType, List<List<Record>>>()

    /** The answer to a changes call, by the token asked with. */
    val changesByToken = HashMap<String, HealthConnectChanges>()
    var failRead: Exception? = null

    override suspend fun grantedPermissions(): Set<String> = granted

    override suspend fun changesToken(type: HealthConnectRecordType): String {
        val token = "${type.wireName}-token-${++tokenSeq}"
        log += "token ${type.wireName}"
        return token
    }

    override suspend fun changes(token: String): HealthConnectChanges {
        log += "changes $token"
        failRead?.let { throw it }
        return changesByToken[token] ?: HealthConnectChanges(emptyList(), emptyList(), token, hasMore = false)
    }

    override suspend fun read(type: HealthConnectRecordType, start: Instant, pageToken: String?): HealthConnectPage {
        log += "read ${type.wireName} $start ${pageToken ?: "-"}"
        failRead?.let { throw it }
        val pages = windows[type].orEmpty()
        val index = pageToken?.toInt() ?: 0
        val next = if (index + 1 < pages.size) (index + 1).toString() else null
        return HealthConnectPage(pages.getOrElse(index) { emptyList() }, next)
    }
}

/** The server, scripted: stores what succeeds, fails with what is queued in [failures]. */
private class FakeUploader : HealthConnectPartUploader {
    val stored = mutableListOf<HealthConnectPart>()
    val failures = ArrayDeque<Exception>()
    var calls = 0

    override suspend fun upload(part: HealthConnectPart): UploadResult {
        calls++
        failures.removeFirstOrNull()?.let { throw it }
        val duplicate = stored.any { it.batch == part.batch && it.partNumber == part.partNumber && it.day == part.day }
        stored += part
        return UploadResult("raw/t/u/health_connect/${part.day}/${part.batch}_${part.partNumber}.ndjson.gz", duplicate)
    }
}

private fun problem(status: Int) = OhsApiException(status, null, null, null, null, null)

private class Harness(options: HealthConnectSyncOptions = HealthConnectSyncOptions(zone = ZoneId.of("UTC"))) {
    var now: Instant = NOW
    val reader = FakeReader()
    val uploader = FakeUploader()
    val tokens = InMemoryChangesTokenStore()
    val dir = tempDir("sync")
    val queue = UploadQueue(dir) { now.toEpochMilli() }
    val sync = HealthConnectSync(reader, uploader, tokens, queue, options) { now }
}

class HealthConnectSyncTest {
    @Test
    fun firstSyncReadsTheBackfillWindowThenKeepsTheToken() = runTest {
        val h = Harness()
        h.reader.windows[STEPS] = listOf(listOf(steps("st-1"), steps("st-2", "2026-03-02T23:30:00Z")), listOf(steps("st-3")))
        val report = h.sync.sync(setOf(STEPS))

        // The token is taken before the window is read, and the window is 30 days.
        assertEquals(
            listOf("token Steps", "read Steps 2026-02-08T12:00:00Z -", "read Steps 2026-02-08T12:00:00Z 1"),
            h.reader.log,
        )
        assertEquals("Steps-token-1", h.tokens.token("Steps"))
        assertEquals(mapOf("Steps" to 3), report.linesByType)
        assertEquals(3, report.linesQueued)
        assertEquals(2, report.partsUploaded)
        assertEquals(0, report.partsPending)
        assertEquals(0, report.duplicateParts)
        assertNull(report.uploadError)
        assertEquals(1, report.batchIds.size)
        assertTrue(Regex("^android-[a-z0-9]{16}$").matches(report.batchIds.single()))

        // One part per local day: 23:30 UTC at +01:00 belongs to the next day.
        val parts = h.uploader.stored
        assertEquals(listOf("2026-03-02", "2026-03-03"), parts.map { it.day })
        assertEquals(listOf("Steps", "Steps"), parts.map { it.type })
        assertEquals(listOf(0, 1), parts.map { it.partNumber })
        assertEquals(setOf(report.batchIds.single()), parts.map { it.batch }.toSet())
        assertEquals(listOf("st-1", "st-3"), linesOf(parts[0]).map { it.getValue("metadata").jsonObject.getValue("id").jsonPrimitive.content })
        assertEquals(canon(HealthConnectLines.line(steps("st-1"))!!), canon(linesOf(parts[0])[0]))
    }

    @Test
    fun laterSyncsSendChangesAndDeletions() = runTest {
        val h = Harness()
        h.tokens.setToken("Steps", "t0")
        h.reader.changesByToken["t0"] = HealthConnectChanges(listOf(steps("st-1")), listOf("st-0"), "t1", hasMore = true)
        h.reader.changesByToken["t1"] = HealthConnectChanges(listOf(steps("st-2")), listOf("st-9", ""), "t2", hasMore = false)
        val report = h.sync.sync(setOf(STEPS))

        assertEquals(listOf("changes t0", "changes t1"), h.reader.log)
        assertEquals("t2", h.tokens.token("Steps"))
        assertEquals(mapOf("Steps" to 2), report.linesByType)
        assertEquals(mapOf("Steps" to 2), report.deletionsByType)
        assertEquals(2, report.deletionsQueued)
        val (records, deletions) = h.uploader.stored
        assertEquals("Steps", records.type)
        assertEquals("Deletion", deletions.type)
        // A deletion has no time: its part is dated the day of the sync.
        assertEquals("2026-03-10", deletions.day)
        assertEquals(
            listOf(canon(parse("""{"recordType":"Deletion","recordId":"st-0"}""")), canon(parse("""{"recordType":"Deletion","recordId":"st-9"}"""))),
            linesOf(deletions).map(::canon),
        )
        assertEquals(listOf(0, 1), h.uploader.stored.map { it.partNumber })

        // Nothing new: nothing sent, the token stays.
        val idle = h.sync.sync(setOf(STEPS))
        assertEquals(SyncReport(), idle)
        assertEquals("t2", h.tokens.token("Steps"))
        assertEquals(2, h.uploader.calls)
    }

    @Test
    fun anExpiredTokenIsReplacedAndTheWindowReadAgain() = runTest {
        val h = Harness(HealthConnectSyncOptions(expiredTokenWindow = Duration.ofDays(7), zone = ZoneId.of("UTC")))
        h.tokens.setToken("Steps", "old")
        h.reader.changesByToken["old"] = HealthConnectChanges(emptyList(), emptyList(), "old", hasMore = false, tokenExpired = true)
        h.reader.windows[STEPS] = listOf(listOf(steps("st-1")))
        val report = h.sync.sync(setOf(STEPS))

        assertEquals(listOf("changes old", "token Steps", "read Steps 2026-03-03T12:00:00Z -"), h.reader.log)
        assertEquals("Steps-token-1", h.tokens.token("Steps"))
        assertEquals(setOf(STEPS), report.expiredTokenTypes)
        assertEquals(mapOf("Steps" to 1), report.linesByType)
        assertEquals(1, report.partsUploaded)
    }

    @Test
    fun backfillWindowOptions() = runTest {
        // Zero: follow changes only.
        val none = Harness(HealthConnectSyncOptions(initialBackfill = Duration.ZERO))
        none.reader.windows[STEPS] = listOf(listOf(steps("st-1")))
        assertEquals(SyncReport(), none.sync.sync(setOf(STEPS)))
        assertEquals(listOf("token Steps"), none.reader.log)
        assertEquals("Steps-token-1", none.tokens.token("Steps"))

        // Null: everything Health Connect gives.
        val all = Harness(HealthConnectSyncOptions(initialBackfill = null))
        all.sync.sync(setOf(STEPS))
        assertEquals(listOf("token Steps", "read Steps 1970-01-01T00:00:00Z -"), all.reader.log)

        // resetAnchor reads the window again.
        all.reader.log.clear()
        all.sync.resetAnchor(STEPS)
        assertNull(all.tokens.token("Steps"))
        all.sync.sync(setOf(STEPS))
        assertEquals(listOf("token Steps", "read Steps 1970-01-01T00:00:00Z -"), all.reader.log)

        assertFailsWith<IllegalArgumentException> { HealthConnectSyncOptions(initialBackfill = Duration.ofDays(-1)) }
        assertFailsWith<IllegalArgumentException> { HealthConnectSyncOptions(maxPartBytes = 0) }
        assertFailsWith<IllegalArgumentException> { Harness(HealthConnectSyncOptions(batchPrefix = "bad prefix")) }
    }

    @Test
    fun aTypeWithoutPermissionIsSkipped() = runTest {
        val h = Harness()
        h.reader.granted = setOf(WEIGHT.readPermission)
        h.reader.windows[WEIGHT] = listOf(listOf(weight("w-1")))
        assertTrue(!h.sync.hasPermissions(setOf(STEPS, WEIGHT)))
        assertTrue(h.sync.hasPermissions(setOf(WEIGHT)))
        val report = h.sync.sync(setOf(STEPS, WEIGHT))
        assertEquals(setOf(STEPS), report.typesWithoutPermission)
        assertEquals(mapOf("Weight" to 1), report.linesByType)
        assertNull(h.tokens.token("Steps"))
        assertTrue(h.reader.log.none { it.contains("Steps") })
    }

    @Test
    fun offlinePartsWaitOnDiskAndGoUpLater() = runTest {
        val h = Harness()
        h.reader.windows[STEPS] = listOf(listOf(steps("st-1"), steps("st-2", "2026-03-04T08:00:00Z")))
        h.uploader.failures += IOException("offline")
        val first = h.sync.sync(setOf(STEPS))

        // Read once, queued, token kept: the data is safe on disk, not lost and not read again.
        assertEquals(2, first.partsPending)
        assertEquals(0, first.partsUploaded)
        assertIs<IOException>(first.uploadError)
        assertEquals("Steps-token-1", h.tokens.token("Steps"))
        assertEquals(2, h.sync.pendingParts)
        assertEquals(1, h.uploader.calls)

        // Before the retry time nothing is tried.
        h.now = NOW.plusSeconds(10)
        val early = h.sync.sync(setOf(STEPS))
        assertEquals(2, early.partsPending)
        assertNull(early.uploadError)
        assertEquals(1, h.uploader.calls)

        // A process started later, on the same directory, sends them once the wait is over.
        h.now = NOW.plusSeconds(31)
        val later = HealthConnectSync(h.reader, h.uploader, h.tokens, UploadQueue(h.dir) { h.now.toEpochMilli() }) { h.now }
        val done = later.flush()
        assertEquals(2, done.partsUploaded)
        assertEquals(0, done.partsPending)
        assertEquals(listOf("2026-03-02", "2026-03-04"), h.uploader.stored.map { it.day })
        assertEquals(listOf(0, 1), h.uploader.stored.map { it.partNumber })
    }

    @Test
    fun flushWithForceIgnoresTheWaitAndBackoffGrows() = runTest {
        val h = Harness()
        h.reader.windows[STEPS] = listOf(listOf(steps("st-1")))
        h.uploader.failures += listOf(problem(503), problem(429), problem(401))
        assertEquals(503, (h.sync.sync(setOf(STEPS)).uploadError as OhsApiException).status)
        assertEquals(NOW.toEpochMilli() + 30_000, h.queue.peek()!!.notBeforeMillis)

        // The user asks again: the wait is skipped; a second failure doubles it.
        assertEquals(429, (h.sync.flush(force = true).uploadError as OhsApiException).status)
        assertEquals(2, h.queue.peek()!!.attempts)
        assertEquals(NOW.toEpochMilli() + 60_000, h.queue.peek()!!.notBeforeMillis)

        // A refused token keeps the part too: the app can renew it.
        val refused = h.sync.flush(force = true)
        assertEquals(1, refused.partsPending)
        assertEquals(0, refused.partsDropped)

        val ok = h.sync.flush(force = true)
        assertEquals(1, ok.partsUploaded)
        assertEquals(0, ok.partsPending)
    }

    @Test
    fun aPartTheServerRefusesForGoodLeavesTheQueue() = runTest {
        val h = Harness()
        h.reader.windows[STEPS] = listOf(listOf(steps("st-1"), steps("st-2", "2026-03-04T08:00:00Z")))
        h.uploader.failures += problem(400)
        val report = h.sync.sync(setOf(STEPS))
        // The bad part is dropped and the one behind it still goes up.
        assertEquals(1, report.partsDropped)
        assertEquals(1, report.partsUploaded)
        assertEquals(0, report.partsPending)
        assertNull(report.uploadError)
        assertEquals(listOf("2026-03-04"), h.uploader.stored.map { it.day })

        for (status in listOf(400, 404, 409, 413, 422)) assertTrue(HealthConnectSync.isPermanent(status), "$status")
        for (status in listOf(200, 401, 403, 408, 429, 500, 503)) assertTrue(!HealthConnectSync.isPermanent(status), "$status")
    }

    @Test
    fun aFailedReadThrowsButKeepsWhatWasReadBefore() = runTest {
        val h = Harness()
        h.tokens.setToken("Weight", "w0")
        h.reader.windows[STEPS] = listOf(listOf(steps("st-1")))
        // Steps (first in order) is read from its window; Weight then fails on its changes.
        val failing = object : HealthConnectReader by h.reader {
            override suspend fun changes(token: String): HealthConnectChanges = throw SecurityException("no background read")
        }
        val sync = HealthConnectSync(failing, h.uploader, h.tokens, h.queue, HealthConnectSyncOptions(zone = ZoneId.of("UTC"))) { h.now }
        assertFailsWith<SecurityException> { sync.sync(setOf(WEIGHT, STEPS)) }
        assertEquals("Steps-token-1", h.tokens.token("Steps"))
        assertEquals("w0", h.tokens.token("Weight"), "the token of the type that failed is not moved")
        assertEquals(listOf("Steps"), h.uploader.stored.map { it.type })
        assertEquals(0, h.queue.size)
    }

    @Test
    fun partsStayUnderTheSizeLimitAndTheTokenFollowsTheQueue() = runTest {
        // Each Steps line is about 230 bytes: one per part, and a flush after every page.
        val options = HealthConnectSyncOptions(maxPartBytes = 300, maxBufferedBytes = 200, zone = ZoneId.of("UTC"))
        val h = Harness(options)
        h.tokens.setToken("Steps", "t0")
        h.reader.changesByToken["t0"] = HealthConnectChanges(listOf(steps("st-1"), steps("st-2")), emptyList(), "t1", hasMore = true)
        h.reader.changesByToken["t1"] = HealthConnectChanges(listOf(steps("st-3")), emptyList(), "t2", hasMore = true)
        // The third page fails: what the first two gave is queued and their token kept.
        val failing = object : HealthConnectReader by h.reader {
            override suspend fun changes(token: String): HealthConnectChanges =
                if (token == "t2") throw IOException("health connect went away") else h.reader.changes(token)
        }
        val sync = HealthConnectSync(failing, h.uploader, h.tokens, h.queue, options) { h.now }
        assertFailsWith<IOException> { sync.sync(setOf(STEPS)) }
        assertEquals("t2", h.tokens.token("Steps"))
        val parts = h.uploader.stored
        assertEquals(listOf(1, 1, 1), parts.map { it.lines })
        // One batch for the type, part numbers never reused across flushes.
        assertEquals(1, parts.map { it.batch }.toSet().size)
        assertEquals(listOf(0, 1, 2), parts.map { it.partNumber })
    }

    @Test
    fun recordsThatCannotBeSentAreCounted() = runTest {
        val h = Harness()
        h.reader.windows[WEIGHT] = listOf(listOf(weight(""), weight("w-1")))
        val report = h.sync.sync(setOf(WEIGHT))
        assertEquals(1, report.skippedRecords)
        assertEquals(mapOf("Weight" to 1), report.linesByType)
    }

    @Test
    fun uploadsThroughTheRealClient() = runTest {
        val requests = mutableListOf<HttpRequest>()
        val transport = HttpTransport { req ->
            requests += req
            HttpResponse(200, emptyMap(), """{"key":"k","duplicate":false}""".toByteArray())
        }
        val h = Harness()
        h.reader.windows[WEIGHT] = listOf(listOf(weight("w-1")))
        val client = OhsClient("https://ohs.example.org", { "ohs_ut_test" }, transport)
        val sync = HealthConnectSync(h.reader, client, h.tokens, h.queue, HealthConnectSyncOptions(batchPrefix = "app", zone = ZoneId.of("UTC")))
        val report = sync.sync(setOf(WEIGHT))
        assertEquals(1, report.partsUploaded)
        val req = requests.single()
        assertEquals("https://ohs.example.org/v1/ingest/health-connect", req.url)
        assertEquals("Weight", req.headers["X-OHS-Type"])
        assertEquals("2026-03-02", req.headers["X-OHS-Day"])
        assertEquals(report.batchIds.single(), req.headers["X-OHS-Batch"])
        assertTrue(req.headers.getValue("X-OHS-Batch").startsWith("app-"))
        assertEquals("0", req.headers["X-OHS-Part"])
    }

    @Test
    fun permissionsForTheTypes() {
        val all = HealthConnectSync.authorizationPermissions()
        assertEquals(22, all.size)
        assertTrue(all.all { it.startsWith("android.permission.health.READ_") })
        val some = HealthConnectSync.authorizationPermissions(setOf(STEPS, WEIGHT), background = true, history = true)
        assertEquals(
            listOf(
                "android.permission.health.READ_STEPS",
                "android.permission.health.READ_WEIGHT",
                "android.permission.health.READ_HEALTH_DATA_IN_BACKGROUND",
                "android.permission.health.READ_HEALTH_DATA_HISTORY",
            ),
            some.toList(),
        )
    }
}
