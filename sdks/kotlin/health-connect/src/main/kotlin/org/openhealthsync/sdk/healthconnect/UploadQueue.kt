// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk.healthconnect

import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.long
import kotlinx.serialization.json.put
import org.openhealthsync.sdk.HealthConnectPart

/** One part waiting in the [UploadQueue]. */
public class QueuedPart internal constructor(
    /** The position in the queue: parts are uploaded in increasing order. */
    public val sequence: Long,
    public val part: HealthConnectPart,
    /** How many upload rounds failed for this part. */
    public val attempts: Int,
    /** Epoch milliseconds before which the part is not tried again; 0 when it never failed. */
    public val notBeforeMillis: Long,
)

/**
 * The upload queue on disk: every part read from Health Connect is written here before its
 * changes token is saved, and removed once the server has it. A part survives the app being
 * killed, the phone being offline and the server being down; it is sent again, in order, with
 * a growing wait between rounds ([backoffMillis]).
 *
 * Each part is two files in [directory]: `<sequence>.gz`, the gzip body as it is uploaded, and
 * `<sequence>.json`, its headers and retry state, written last and atomically. The directory
 * must be private to the app (the default is under `Context.filesDir`): the bodies hold health
 * data, compressed and not encrypted beyond what the device's storage does.
 *
 * Safe to call from several threads of one process.
 */
public class UploadQueue(
    public val directory: File,
    private val clock: () -> Long = System::currentTimeMillis,
) {
    private val lock = Any()
    private var nextSequence = -1L
    private var cleaned = false

    /** How many parts wait. */
    public val size: Int get() = synchronized(lock) { sequences().size }

    /** The gzip bytes waiting, all parts together. */
    public val sizeBytes: Long
        get() = synchronized(lock) { sequences().sumOf { body(it).length() } }

    /** Adds [part] at the end. It is on disk when the call returns. */
    @Throws(IOException::class)
    public fun enqueue(part: HealthConnectPart): QueuedPart = synchronized(lock) {
        if (!directory.isDirectory && !directory.mkdirs() && !directory.isDirectory) {
            throw IOException("cannot create the upload queue directory")
        }
        if (nextSequence < 0) nextSequence = (sequences().maxOrNull() ?: 0L) + 1
        val sequence = nextSequence++
        body(sequence).writeBytes(part.body)
        val queued = QueuedPart(sequence, part, attempts = 0, notBeforeMillis = 0)
        writeMeta(queued)
        queued
    }

    /** The oldest part, or null when the queue is empty. A part whose files are damaged is dropped. */
    public fun peek(): QueuedPart? = synchronized(lock) {
        for (sequence in sequences()) {
            val queued = read(sequence)
            if (queued != null) return queued
            delete(sequence)
        }
        null
    }

    /** Removes a part, once the server has it or has refused it for good. */
    public fun remove(queued: QueuedPart) {
        synchronized(lock) { delete(queued.sequence) }
    }

    /**
     * Records a failed round for [queued]: one more attempt, and no new try before
     * [backoffMillis] of that count has passed. Returns the updated entry.
     */
    @Throws(IOException::class)
    public fun markFailed(queued: QueuedPart): QueuedPart = synchronized(lock) {
        val attempts = queued.attempts + 1
        val updated = QueuedPart(queued.sequence, queued.part, attempts, clock() + backoffMillis(attempts))
        writeMeta(updated)
        updated
    }

    /** Removes every part. */
    public fun clear() {
        synchronized(lock) { directory.listFiles()?.forEach { it.delete() } }
    }

    // Files

    private fun body(sequence: Long) = File(directory, name(sequence) + BODY)

    private fun meta(sequence: Long) = File(directory, name(sequence) + META)

    private fun delete(sequence: Long) {
        // The meta file goes first: a part without it does not exist.
        meta(sequence).delete()
        body(sequence).delete()
    }

    /** The sequences that have a meta file, oldest first. Bodies left without one are removed. */
    private fun sequences(): List<Long> {
        val files = directory.listFiles() ?: return emptyList()
        val committed = files.mapNotNull { f ->
            if (f.name.endsWith(META)) f.name.removeSuffix(META).toLongOrNull() else null
        }.sorted()
        if (!cleaned) {
            // First look after a start: clear what an interrupted enqueue left behind.
            cleaned = true
            val known = committed.toHashSet()
            for (f in files) {
                val orphanBody = f.name.endsWith(BODY) && f.name.removeSuffix(BODY).toLongOrNull() !in known
                if (orphanBody || f.name.endsWith(TMP)) f.delete()
            }
        }
        return committed
    }

    private fun read(sequence: Long): QueuedPart? = try {
        val m = json.decodeFromString(JsonObject.serializer(), meta(sequence).readText(Charsets.UTF_8))
        fun text(name: String) = m.getValue(name).jsonPrimitive.content
        val bytes = body(sequence).readBytes()
        if (bytes.isEmpty()) {
            null
        } else {
            QueuedPart(
                sequence,
                HealthConnectPart(
                    type = text("type"),
                    day = text("day"),
                    batch = text("batch"),
                    partNumber = m.getValue("part").jsonPrimitive.int,
                    body = bytes,
                    lines = m.getValue("lines").jsonPrimitive.int,
                ),
                attempts = m.getValue("attempts").jsonPrimitive.int,
                notBeforeMillis = m.getValue("not_before_ms").jsonPrimitive.long,
            )
        }
    } catch (_: Exception) {
        null
    }

    private fun writeMeta(queued: QueuedPart) {
        val m = buildJsonObject {
            put("type", queued.part.type)
            put("day", queued.part.day)
            put("batch", queued.part.batch)
            put("part", queued.part.partNumber)
            put("lines", queued.part.lines)
            put("attempts", queued.attempts)
            put("not_before_ms", queued.notBeforeMillis)
        }
        val target = meta(queued.sequence)
        val tmp = File(target.path + TMP)
        tmp.writeText(json.encodeToString(JsonObject.serializer(), m), Charsets.UTF_8)
        try {
            Files.move(tmp.toPath(), target.toPath(), StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
        } catch (e: IOException) {
            tmp.delete()
            throw e
        }
    }

    public companion object {
        private const val BODY = ".gz"
        private const val META = ".json"
        private const val TMP = ".tmp"
        private val json = Json { ignoreUnknownKeys = true }

        private fun name(sequence: Long) = sequence.toString().padStart(19, '0')

        /** The first wait after a failed round. */
        public const val BASE_BACKOFF_MILLIS: Long = 30_000

        /** The longest wait between two rounds. */
        public const val MAX_BACKOFF_MILLIS: Long = 60 * 60 * 1000

        /**
         * The wait after [attempts] failed rounds: 30 seconds, doubled each time, at most an
         * hour. These rounds come on top of the client's own retries within one upload.
         */
        public fun backoffMillis(attempts: Int): Long {
            if (attempts <= 0) return 0
            val shift = minOf(attempts - 1, 20)
            return minOf(MAX_BACKOFF_MILLIS, BASE_BACKOFF_MILLIS shl shift)
        }
    }
}
