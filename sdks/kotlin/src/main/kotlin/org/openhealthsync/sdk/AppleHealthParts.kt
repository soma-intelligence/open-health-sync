// SPDX-License-Identifier: MIT
package org.openhealthsync.sdk

import java.io.ByteArrayOutputStream
import java.security.SecureRandom
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.zip.GZIPOutputStream
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject

/** One upload part: the bytes are gzip compressed NDJSON of one type and one day. */
public class AppleHealthPart(
    /** HealthKit type identifier, sent as `X-OHS-Type`. */
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
    override fun toString(): String = "AppleHealthPart(type=$type, day=$day, batch=$batch, part=$partNumber, lines=$lines)"
}

/**
 * Groups lines into upload parts the way `ohs import apple-health` does: one HealthKit type and
 * one local day per part, at most [maxPartBytes] of NDJSON per part, part numbers counted from
 * 0 across the whole batch so `(day, batch, part)` never collides. Each line is a JSON object in
 * the server's line format (docs/connectors/apple-health.md#line-formats).
 */
public class AppleHealthPartBuilder(
    public val batch: String,
    public val maxPartBytes: Int = DEFAULT_MAX_PART_BYTES,
) {
    private val groups = LinkedHashMap<Pair<String, String>, MutableList<ByteArray>>()

    init {
        require(NAME.matches(batch)) { "batch must be 1 to 128 letters, digits, '_' or '-'" }
        require(maxPartBytes > 0) { "maxPartBytes must be positive" }
    }

    /** Adds one line of HealthKit type [type] to the part of local day [day] (YYYY-MM-DD). */
    public fun add(type: String, day: String, line: JsonObject): AppleHealthPartBuilder {
        require(NAME.matches(type)) { "type must be 1 to 128 letters, digits, '_' or '-'" }
        require(DAY.matches(day)) { "day must be YYYY-MM-DD" }
        val bytes = (compactJson.encodeToString(JsonObject.serializer(), line) + "\n").toByteArray(Charsets.UTF_8)
        groups.getOrPut(type to day) { mutableListOf() }.add(bytes)
        return this
    }

    /** The parts, in the order their type and day were first added. */
    public fun build(): List<AppleHealthPart> {
        val parts = mutableListOf<AppleHealthPart>()
        for ((key, lines) in groups) {
            val buf = ByteArrayOutputStream()
            var count = 0
            fun flush() {
                if (count == 0) return
                parts += AppleHealthPart(key.first, key.second, batch, parts.size, gzip(buf.toByteArray()), count)
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
        return parts
    }

    public companion object {
        /** 8 MiB of NDJSON, as the command line import uses. */
        public const val DEFAULT_MAX_PART_BYTES: Int = 8 * 1024 * 1024

        private val NAME = Regex("^[A-Za-z0-9_-]{1,128}$")
        private val DAY = Regex("^\\d{4}-\\d{2}-\\d{2}$")
        private val compactJson = Json { prettyPrint = false }
        private val random = SecureRandom()
        private const val ALPHABET = "abcdefghijklmnopqrstuvwxyz0123456789"

        /** The local calendar day of [instant] in [zone], YYYY-MM-DD: the part's day. */
        public fun localDay(instant: Instant, zone: ZoneId): String =
            DateTimeFormatter.ISO_LOCAL_DATE.format(instant.atZone(zone))

        /** A fresh batch id: [prefix], a dash and 16 random characters. */
        public fun newBatchId(prefix: String = "kt"): String {
            val suffix = CharArray(16) { ALPHABET[random.nextInt(ALPHABET.length)] }
            val id = "$prefix-${String(suffix)}"
            require(NAME.matches(id)) { "prefix must be letters, digits, '_' or '-', at most 111 characters" }
            return id
        }

        internal fun gzip(bytes: ByteArray): ByteArray {
            val out = ByteArrayOutputStream()
            GZIPOutputStream(out).use { it.write(bytes) }
            return out.toByteArray()
        }
    }
}
