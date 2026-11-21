// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk.healthconnect

import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import kotlinx.serialization.builtins.MapSerializer
import kotlinx.serialization.builtins.serializer
import kotlinx.serialization.json.Json

/**
 * Keeps one Health Connect changes token per record type, under the type's wire name
 * (`Steps`, `BloodGlucose`). [HealthConnectSync] saves a type's token only once everything read
 * before it is in the upload queue on disk, so an interrupted sync starts again from the last
 * token whose changes are safe. The counterpart of the Swift SDK's `AnchorStore`.
 */
public interface ChangesTokenStore {
    public fun token(typeName: String): String?

    /** Saves [token] for [typeName]; null forgets it. */
    public fun setToken(typeName: String, token: String?)
}

/**
 * Stores the tokens in one JSON file, rewritten atomically. The default of
 * [HealthConnectSync.create], in the app's private files directory. A file that cannot be read
 * counts as no token at all: the next sync reads the backfill window again.
 */
public class FileChangesTokenStore(private val file: File) : ChangesTokenStore {
    private val lock = Any()
    private var cache: MutableMap<String, String>? = null

    override fun token(typeName: String): String? = synchronized(lock) { load()[typeName] }

    override fun setToken(typeName: String, token: String?) {
        synchronized(lock) {
            val tokens = load()
            if (token == null) {
                if (tokens.remove(typeName) == null) return
            } else {
                if (tokens.put(typeName, token) == token) return
            }
            save(tokens)
        }
    }

    private fun load(): MutableMap<String, String> {
        cache?.let { return it }
        val read = try {
            if (file.isFile) json.decodeFromString(serializer, file.readText(Charsets.UTF_8)) else emptyMap()
        } catch (_: Exception) {
            emptyMap()
        }
        return LinkedHashMap(read).also { cache = it }
    }

    private fun save(tokens: Map<String, String>) {
        file.parentFile?.mkdirs()
        val tmp = File(file.path + ".tmp")
        tmp.writeText(json.encodeToString(serializer, tokens), Charsets.UTF_8)
        try {
            Files.move(tmp.toPath(), file.toPath(), StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
        } catch (e: IOException) {
            tmp.delete()
            throw e
        }
    }

    private companion object {
        val json = Json { ignoreUnknownKeys = true }
        val serializer = MapSerializer(String.serializer(), String.serializer())
    }
}

/** Keeps tokens in memory only; for tests and previews. */
public class InMemoryChangesTokenStore : ChangesTokenStore {
    private val tokens = HashMap<String, String>()

    override fun token(typeName: String): String? = synchronized(tokens) { tokens[typeName] }

    override fun setToken(typeName: String, token: String?) {
        synchronized(tokens) {
            if (token == null) tokens.remove(typeName) else tokens[typeName] = token
        }
    }
}
