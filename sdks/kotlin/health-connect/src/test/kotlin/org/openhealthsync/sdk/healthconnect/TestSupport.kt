// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk.healthconnect

import androidx.health.connect.client.records.metadata.DataOrigin
import androidx.health.connect.client.records.metadata.Device
import androidx.health.connect.client.records.metadata.Metadata
import java.io.File
import java.nio.file.Files
import java.time.Instant
import java.util.zip.GZIPInputStream
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonObject
import org.openhealthsync.sdk.HealthConnectPart

/**
 * A record's metadata as Health Connect returns it on a read: with the id, the writing app and
 * the modification time, which only the platform sets. The androidx factory methods cannot
 * set them (the constructor is internal), so the test goes through reflection.
 */
internal fun meta(
    id: String,
    origin: String = "",
    method: Int = Metadata.RECORDING_METHOD_UNKNOWN,
    lastModified: Instant = Instant.EPOCH,
    device: Device? = null,
    clientRecordId: String? = null,
): Metadata {
    val ctor = Metadata::class.java.getDeclaredConstructor(
        Int::class.javaPrimitiveType, String::class.java, DataOrigin::class.java, Instant::class.java,
        String::class.java, Long::class.javaPrimitiveType, Device::class.java,
    )
    ctor.isAccessible = true
    return ctor.newInstance(method, id, DataOrigin(origin), lastModified, clientRecordId, 0L, device)
}

/** JSON as plain values, every number a Double, so `121` and `121.0` compare equal. */
internal fun canon(e: JsonElement): Any? = when (e) {
    is JsonNull -> null
    is JsonObject -> e.mapValues { canon(it.value) }
    is JsonArray -> e.map(::canon)
    is JsonPrimitive -> if (e.isString) e.content else e.content.toDoubleOrNull() ?: e.content
}

internal fun parse(line: String): JsonObject = Json.parseToJsonElement(line).jsonObject

/** The lines of a part, parsed. */
internal fun linesOf(part: HealthConnectPart): List<JsonObject> =
    GZIPInputStream(part.body.inputStream()).readBytes().toString(Charsets.UTF_8)
        .lines().filter { it.isNotBlank() }.map(::parse)

internal fun tempDir(prefix: String): File = Files.createTempDirectory(prefix).toFile()

/** The repository root, when the tests run inside the repository; null otherwise. */
internal fun repoFile(path: String): File? {
    val root = System.getProperty("ohs.repoRoot") ?: return null
    return File(root, path).takeIf { it.isFile }
}

/** The copy of connectors/healthconnect/testdata/upload.ndjson the mapping tests compare with. */
internal fun fixtureText(): String =
    object {}.javaClass.getResource("/health-connect-upload.ndjson")!!.readText(Charsets.UTF_8)
