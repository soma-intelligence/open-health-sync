// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk

import java.io.ByteArrayOutputStream
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * One upload part of `POST /v1/ingest/health-connect`: the bytes are gzip compressed NDJSON,
 * one Health Connect record per line (docs/connectors/health-connect.md#line-format).
 */
public class HealthConnectPart(
    /** The record type of the lines (`Steps`, `BloodGlucose`, `Deletion`), sent as `X-OHS-Type`. */
    public val type: String,
    /** YYYY-MM-DD, sent as `X-OHS-Day`. */
    public val day: String,
    /** Sent as `X-OHS-Batch`. */
    public val batch: String,
    /** Sent as `X-OHS-Part`. */
    public val partNumber: Int,
    /** gzip of the NDJSON lines. */
    public val body: ByteArray,
    /** How many lines the part holds. */
    public val lines: Int,
) {
    override fun toString(): String = "HealthConnectPart(type=$type, day=$day, batch=$batch, part=$partNumber, lines=$lines)"
}

/**
 * Groups Health Connect lines into upload parts: one record type and one local day per part,
 * at most [maxPartBytes] of NDJSON per part, gzip compressed, part numbers counted from 0
 * across the whole batch, so `(day, batch, part)` never collides whatever the type.
 *
 * It can be drained as it is filled: [drain] returns the parts of everything added so far and
 * later parts keep counting from there, so a long read never holds all its lines in memory.
 * Not thread safe.
 */
public class HealthConnectPartBuilder(
    public val batch: String,
    public val maxPartBytes: Int = DEFAULT_MAX_PART_BYTES,
) {
    private val groups = LinkedHashMap<Pair<String, String>, MutableList<ByteArray>>()
    private var nextPart = 0

    /** The NDJSON bytes added and not drained yet. */
    public var bufferedBytes: Long = 0
        private set

    init {
        require(NAME.matches(batch)) { "batch must be 1 to 128 letters, digits, '_' or '-'" }
        require(maxPartBytes > 0) { "maxPartBytes must be positive" }
    }

    /**
     * Adds one line of record type [type] to the part of local day [day] (YYYY-MM-DD). The line
     * must hold a `recordType`. Returns false, and keeps nothing, for a line longer than
     * [MAX_LINE_BYTES]: the server would refuse the whole part.
     */
    public fun add(type: String, day: String, line: JsonObject): Boolean {
        require(NAME.matches(type)) { "type must be 1 to 128 letters, digits, '_' or '-'" }
        require(DAY.matches(day)) { "day must be YYYY-MM-DD" }
        val recordType = (line["recordType"] as? JsonPrimitive)?.takeIf { it.isString }?.content
        require(!recordType.isNullOrEmpty()) { "line must hold a recordType" }
        val bytes = (compactJson.encodeToString(JsonObject.serializer(), line) + "\n").toByteArray(Charsets.UTF_8)
        if (bytes.size - 1 > MAX_LINE_BYTES) return false
        groups.getOrPut(type to day) { mutableListOf() }.add(bytes)
        bufferedBytes += bytes.size
        return true
    }

    /** Adds the deletion of the record [recordId], in the `Deletion` part of local day [day]. */
    public fun addDeletion(day: String, recordId: String): Boolean = add(DELETION, day, deletionLine(recordId))

    /**
     * The parts of every line added since the last call, in the order their type and day were
     * first added, and forgets those lines.
     */
    public fun drain(): List<HealthConnectPart> {
        val parts = mutableListOf<HealthConnectPart>()
        for ((key, lines) in groups) {
            val buf = ByteArrayOutputStream()
            var count = 0
            fun flush() {
                if (count == 0) return
                parts += HealthConnectPart(key.first, key.second, batch, nextPart++, AppleHealthPartBuilder.gzip(buf.toByteArray()), count)
                buf.reset()
                count = 0
            }
            for (line in lines) {
                if (count > 0 && buf.size() + line.size > maxPartBytes) flush()
                buf.write(line)
                count++
            }
            flush()
        }
        groups.clear()
        bufferedBytes = 0
        return parts
    }

    /** [drain], under the name the other builders use. */
    public fun build(): List<HealthConnectPart> = drain()

    public companion object {
        /** 8 MiB of NDJSON per part, as the Apple Health builder uses. The server takes 64 MiB. */
        public const val DEFAULT_MAX_PART_BYTES: Int = 8 * 1024 * 1024

        /** The longest line the server reads (connectors/healthconnect MaxLineBytes). */
        public const val MAX_LINE_BYTES: Int = 32 * 1024 * 1024

        /** The record type, and `X-OHS-Type`, of deletion lines. */
        public const val DELETION: String = "Deletion"

        private val NAME = Regex("^[A-Za-z0-9_-]{1,128}$")
        private val DAY = Regex("^\\d{4}-\\d{2}-\\d{2}$")
        private val compactJson = Json { prettyPrint = false }

        /** The line that removes the stored records of the Health Connect record [recordId]. */
        public fun deletionLine(recordId: String): JsonObject {
            require(recordId.isNotEmpty()) { "recordId must not be empty" }
            return buildJsonObject {
                put("recordType", DELETION)
                put("recordId", recordId)
            }
        }
    }
}
