// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk.healthconnect

import java.io.File
import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue
import org.openhealthsync.sdk.HealthConnectPart

private fun part(n: Int, body: String = "body-$n") =
    HealthConnectPart("Steps", "2026-03-02", "android-abc", n, body.toByteArray(), lines = n + 1)

class UploadQueueTest {
    @Test
    fun keepsOrderAndSurvivesARestart() {
        val dir = tempDir("queue")
        val q = UploadQueue(dir)
        assertNull(q.peek())
        assertEquals(0, q.size)
        repeat(3) { q.enqueue(part(it)) }
        assertEquals(3, q.size)
        assertEquals("body-0body-1body-2".length.toLong(), q.sizeBytes)

        // Another instance on the same directory: what a process started later sees.
        val again = UploadQueue(dir)
        val first = assertNotNull(again.peek())
        assertEquals(0, first.part.partNumber)
        assertEquals("Steps", first.part.type)
        assertEquals("2026-03-02", first.part.day)
        assertEquals("android-abc", first.part.batch)
        assertEquals(1, first.part.lines)
        assertContentEquals("body-0".toByteArray(), first.part.body)
        assertEquals(0, first.attempts)
        again.remove(first)
        assertEquals(1, again.peek()!!.part.partNumber)

        // A part added after the restart goes to the end, never before an older one.
        again.enqueue(part(7))
        val order = generateSequence { again.peek()?.also(again::remove) }.map { it.part.partNumber }.toList()
        assertEquals(listOf(1, 2, 7), order)
        assertEquals(0, again.size)
        assertTrue(dir.listFiles()!!.isEmpty())
    }

    @Test
    fun aFailedRoundWaitsLongerEachTime() {
        var now = 1_000_000L
        val dir = tempDir("queue")
        val q = UploadQueue(dir) { now }
        var queued = q.enqueue(part(0))
        assertEquals(0, queued.notBeforeMillis)
        queued = q.markFailed(queued)
        assertEquals(1, queued.attempts)
        assertEquals(now + 30_000, queued.notBeforeMillis)
        now += 30_000
        queued = q.markFailed(queued)
        assertEquals(now + 60_000, queued.notBeforeMillis)
        // The state is on disk.
        val read = UploadQueue(dir) { now }.peek()!!
        assertEquals(2, read.attempts)
        assertEquals(queued.notBeforeMillis, read.notBeforeMillis)

        assertEquals(0, UploadQueue.backoffMillis(0))
        assertEquals(listOf(30_000L, 60_000L, 120_000L, 240_000L), (1..4).map(UploadQueue::backoffMillis))
        assertEquals(UploadQueue.MAX_BACKOFF_MILLIS, UploadQueue.backoffMillis(8))
        assertEquals(UploadQueue.MAX_BACKOFF_MILLIS, UploadQueue.backoffMillis(500))
    }

    @Test
    fun dropsWhatAnInterruptedWriteLeftAndDamagedParts() {
        val dir = tempDir("queue")
        UploadQueue(dir).apply {
            enqueue(part(0))
            enqueue(part(1))
        }
        val files = dir.listFiles()!!.map { it.name }.sorted()
        assertEquals(4, files.size)
        // A body whose meta file was never written, and a meta file cut short.
        File(dir, "0000000000000000099.gz").writeText("orphan")
        File(dir, "0000000000000000098.json.tmp").writeText("{")
        File(dir, files.first { it.endsWith(".json") }).writeText("{\"type\":")

        val q = UploadQueue(dir)
        assertEquals(1, q.peek()!!.part.partNumber, "the damaged part is dropped, the next one served")
        assertEquals(1, q.size)
        assertFalse(File(dir, "0000000000000000099.gz").exists())
        assertFalse(File(dir, "0000000000000000098.json.tmp").exists())
        q.clear()
        assertNull(q.peek())
    }
}

class ChangesTokenStoreTest {
    @Test
    fun fileStorePersistsAndForgets() {
        val file = File(tempDir("tokens"), "nested/tokens.json")
        val store = FileChangesTokenStore(file)
        assertNull(store.token("Steps"))
        store.setToken("Steps", "t1")
        store.setToken("BloodGlucose", "t2")
        store.setToken("Steps", "t3")
        assertEquals("t3", FileChangesTokenStore(file).token("Steps"))
        assertEquals("t2", FileChangesTokenStore(file).token("BloodGlucose"))
        store.setToken("Steps", null)
        store.setToken("Never", null)
        assertNull(FileChangesTokenStore(file).token("Steps"))
        assertEquals("t2", FileChangesTokenStore(file).token("BloodGlucose"))
        assertFalse(File(file.path + ".tmp").exists())
    }

    @Test
    fun anUnreadableFileIsNoTokenAtAll() {
        val file = File(tempDir("tokens"), "tokens.json")
        file.writeText("not json")
        val store = FileChangesTokenStore(file)
        assertNull(store.token("Steps"))
        store.setToken("Steps", "t1")
        assertEquals("t1", FileChangesTokenStore(file).token("Steps"))
    }

    @Test
    fun memoryStore() {
        val store = InMemoryChangesTokenStore()
        store.setToken("Steps", "t1")
        assertEquals("t1", store.token("Steps"))
        store.setToken("Steps", null)
        assertNull(store.token("Steps"))
    }
}

class HealthConnectSyncWorkerTest {
    @Test
    fun retriesOnlyWhenAnUploadFailedWithPartsPending() {
        assertFalse(HealthConnectSyncWorker.shouldRetry(SyncReport()))
        // Parts waiting for their retry time: the next period takes them.
        assertFalse(HealthConnectSyncWorker.shouldRetry(SyncReport(partsPending = 2)))
        assertTrue(HealthConnectSyncWorker.shouldRetry(SyncReport(partsPending = 2, uploadError = java.io.IOException("offline"))))
    }

    @Test
    fun typesGoThroughTheJobInput() {
        val types = setOf(HealthConnectRecordType.BLOOD_GLUCOSE, HealthConnectRecordType.STEPS)
        val names = HealthConnectSyncWorker.typeNames(types)
        assertEquals("Steps,BloodGlucose", names)
        assertEquals(types, HealthConnectSyncWorker.typesOf(names))
        // An input from an older version naming a type that is gone, or no input at all.
        assertEquals(setOf(HealthConnectRecordType.STEPS), HealthConnectSyncWorker.typesOf("Steps,Gone,"))
        assertEquals(HealthConnectSync.allSupportedTypes, HealthConnectSyncWorker.typesOf(null))
        assertEquals(HealthConnectSync.allSupportedTypes, HealthConnectSyncWorker.typesOf(""))
        assertEquals(22, HealthConnectSync.allSupportedTypes.size)
    }
}
