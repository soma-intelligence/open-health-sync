// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk

import java.io.IOException
import java.net.URI
import java.net.URLDecoder
import java.net.URLEncoder
import java.time.Instant
import java.time.format.DateTimeFormatter
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.withContext
import kotlinx.serialization.KSerializer
import kotlinx.serialization.Serializable
import kotlinx.serialization.SerialName
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray

/**
 * A time window and page for the list routes. [start] and [end] take an RFC 3339 time or a
 * `YYYY-MM-DD` date; the window is half open (start <= time < end).
 */
public data class Range(
    val start: String? = null,
    val end: String? = null,
    val cursor: String? = null,
    val limit: Int? = null,
) {
    public companion object {
        /** A window between two instants, written as RFC 3339 UTC. */
        public fun between(start: Instant?, end: Instant?, limit: Int? = null): Range =
            Range(start?.let(DateTimeFormatter.ISO_INSTANT::format), end?.let(DateTimeFormatter.ISO_INSTANT::format), limit = limit)
    }
}

/**
 * The Open Health Sync REST API (api/openapi.yaml). Every call suspends and runs its I/O on
 * [Dispatchers.IO].
 *
 * In an app, build it with a user token your server minted (`tokenProvider` lets you fetch a new
 * one when it expires). Never ship a tenant API key (`ohs_sk_`) in an app.
 */
public class OhsClient(
    baseUrl: String,
    private val tokenProvider: suspend () -> String,
    private val transport: HttpTransport = UrlConnectionTransport(),
    private val userAgent: String = "open-health-sync-kotlin/$VERSION",
    private val maxUploadAttempts: Int = 5,
    private val sleep: suspend (Long) -> Unit = { delay(it) },
) {
    /** A client with a fixed bearer credential. */
    public constructor(baseUrl: String, token: String, transport: HttpTransport = UrlConnectionTransport()) :
        this(baseUrl, { token }, transport)

    private val base = baseUrl.trimEnd('/')

    init {
        require(base.startsWith("http://") || base.startsWith("https://")) { "baseUrl must be an http or https URL" }
        require(maxUploadAttempts >= 1) { "maxUploadAttempts must be at least 1" }
    }

    // Providers

    /** Connectors enabled on this instance. */
    public suspend fun listProviders(): List<Provider> =
        get("/v1/providers", serializer = DataDto.serializer(Provider.serializer())).data

    // Users (tenant key)

    /** Creates an end user. A retry with the same [idempotencyKey] gets the first answer back. */
    public suspend fun createUser(externalRef: String? = null, idempotencyKey: String? = null): User {
        val body = if (externalRef == null) "{}" else json.encodeToString(CreateUserBody.serializer(), CreateUserBody(externalRef))
        val headers = idempotencyKey?.let { mapOf("Idempotency-Key" to it) } ?: emptyMap()
        return decode(User.serializer(), call("POST", "/v1/users", jsonBody(body), headers))
    }

    public suspend fun listUsers(cursor: String? = null, limit: Int? = null): Page<User> =
        page("/v1/users", User.serializer(), listOf("cursor" to cursor, "limit" to limit?.toString()))

    public suspend fun getUser(userId: String): User = get("/v1/users/${seg(userId)}", serializer = User.serializer())

    /** Starts the hard delete of a user; returns the job id to poll with [getDeletion]. */
    public suspend fun deleteUser(userId: String): String =
        decode(DeletionAccepted.serializer(), call("DELETE", "/v1/users/${seg(userId)}")).jobId

    public suspend fun getDeletion(jobId: String): DeletionJob =
        get("/v1/deletions/${seg(jobId)}", serializer = DeletionJob.serializer())

    /**
     * Mints a user token with [scopes] (`upload`, `connect`, `read`). Server side only: it needs
     * the tenant key. [ttlSeconds] null means one hour.
     */
    public suspend fun mintUserToken(userId: String, scopes: List<String>, ttlSeconds: Long? = null): UserToken {
        val body = buildJsonObject {
            putJsonArray("scopes") { scopes.forEach { add(kotlinx.serialization.json.JsonPrimitive(it)) } }
            if (ttlSeconds != null) put("ttl_seconds", ttlSeconds)
        }
        return decode(UserToken.serializer(), call("POST", "/v1/users/${seg(userId)}/tokens", jsonBody(body.toString())))
    }

    // Connections

    public suspend fun listConnections(userId: String): List<Connection> =
        get("/v1/users/${seg(userId)}/connections", serializer = DataDto.serializer(Connection.serializer())).data

    public suspend fun getFreshness(userId: String): Freshness =
        get("/v1/users/${seg(userId)}/freshness", serializer = Freshness.serializer())

    /** Returns the provider's consent URL: open it in a browser tab. */
    public suspend fun startConnection(userId: String, provider: String, redirectUri: String? = null): String {
        val body = buildJsonObject { if (redirectUri != null) put("redirect_uri", redirectUri) }
        val res = call("POST", "/v1/users/${seg(userId)}/connections/${seg(provider)}", jsonBody(body.toString()))
        return decode(AuthorizeUrl.serializer(), res).authorizeUrl
    }

    public suspend fun deleteConnection(userId: String, provider: String) {
        call("DELETE", "/v1/users/${seg(userId)}/connections/${seg(provider)}")
    }

    // Read

    public suspend fun listSamples(userId: String, type: String, range: Range = Range()): Page<Sample> =
        page("/v1/users/${seg(userId)}/samples", Sample.serializer(), listOf("type" to type) + range.params())

    public suspend fun listCategorySamples(userId: String, type: String? = null, range: Range = Range()): Page<CategorySample> =
        page("/v1/users/${seg(userId)}/category-samples", CategorySample.serializer(), listOf("type" to type) + range.params())

    public suspend fun listSleep(userId: String, range: Range = Range()): Page<SleepSession> =
        page("/v1/users/${seg(userId)}/sleep", SleepSession.serializer(), range.params())

    public suspend fun listSleepPhases(userId: String, sessionId: Long, range: Range = Range()): Page<SleepPhase> =
        page("/v1/users/${seg(userId)}/sleep/$sessionId/phases", SleepPhase.serializer(), range.params())

    public suspend fun listWorkouts(userId: String, type: String? = null, range: Range = Range()): Page<Workout> =
        page("/v1/users/${seg(userId)}/workouts", Workout.serializer(), listOf("type" to type) + range.params())

    public suspend fun getWorkoutRoute(userId: String, workoutId: Long, range: Range = Range()): Page<RoutePoint> =
        page("/v1/users/${seg(userId)}/workouts/$workoutId/route", RoutePoint.serializer(), range.params())

    public suspend fun listNutrition(userId: String, range: Range = Range()): Page<NutritionLog> =
        page("/v1/users/${seg(userId)}/nutrition", NutritionLog.serializer(), range.params())

    public suspend fun listDaily(userId: String, range: Range = Range()): Page<DailySummary> =
        page("/v1/users/${seg(userId)}/daily", DailySummary.serializer(), range.params())

    /** Records per provider and local day, and the jobs still processing. */
    public suspend fun getCoverage(userId: String, provider: String? = null, start: String? = null, end: String? = null): Coverage =
        get(
            "/v1/users/${seg(userId)}/coverage",
            listOf("provider" to provider, "start" to start, "end" to end),
            Coverage.serializer(),
        )

    // FHIR

    /** The user's records as FHIR R4 Observations, one Bundle page. */
    public suspend fun searchFhirObservations(
        userId: String,
        type: String? = null,
        code: String? = null,
        category: String? = null,
        range: Range = Range(),
    ): FhirBundle {
        val q = listOf("type" to type, "code" to code, "category" to category) + range.params()
        val raw = decode(JsonObject.serializer(), call("GET", "/v1/users/${seg(userId)}/fhir/Observation" + query(q), accept = FHIR_JSON))
        val entries = (raw["entry"] as? JsonArray)?.map { it.jsonObject["resource"]!!.jsonObject } ?: emptyList()
        val next = (raw["link"] as? JsonArray)
            ?.map { it.jsonObject }
            ?.firstOrNull { it["relation"]?.jsonPrimitive?.contentOrNull == "next" }
            ?.get("url")?.jsonPrimitive?.contentOrNull
        return FhirBundle(raw, entries, next?.let(::cursorOf))
    }

    /** One FHIR R4 Observation, by the id the Bundle gives (`qs-1234`, `bp-...`, `sleep-...`). */
    public suspend fun readFhirObservation(userId: String, observationId: String): JsonObject =
        decode(JsonObject.serializer(), call("GET", "/v1/users/${seg(userId)}/fhir/Observation/${seg(observationId)}", accept = FHIR_JSON))

    // Upload (user token with the upload scope)

    /**
     * Uploads one Apple Health part. 429, 503, other 5xx answers and network errors are retried
     * with backoff (honouring `Retry-After`), up to `maxUploadAttempts` attempts; any other
     * error is thrown at once.
     */
    public suspend fun uploadAppleHealthPart(part: AppleHealthPart): UploadResult =
        upload("apple-health", part.type, part.day, part.batch, part.partNumber, part.body)

    /**
     * Uploads one part of Bluetooth device values ([BluetoothRecorder]), with the retries of
     * [uploadAppleHealthPart]. Send the same part again until it is stored: a retry is answered
     * `duplicate: true`.
     */
    public suspend fun uploadBluetoothPart(part: BluetoothPart): UploadResult =
        upload("bluetooth", BluetoothPart.TYPE, part.day, part.batch, part.partNumber, part.body)

    private suspend fun upload(route: String, type: String, day: String, batch: String, partNumber: Int, body: ByteArray): UploadResult {
        val headers = mapOf(
            "Content-Type" to "application/gzip",
            "X-OHS-Type" to type,
            "X-OHS-Day" to day,
            "X-OHS-Batch" to batch,
            "X-OHS-Part" to partNumber.toString(),
        )
        var attempt = 1
        while (true) {
            val wait: Long
            try {
                return decode(UploadResult.serializer(), call("POST", "/v1/ingest/$route", body, headers))
            } catch (e: OhsApiException) {
                val retryable = e.status == 429 || e.status >= 500
                if (!retryable || attempt >= maxUploadAttempts) throw e
                wait = e.retryAfterSeconds?.let { it * 1000 } ?: backoff(attempt)
            } catch (e: IOException) {
                if (attempt >= maxUploadAttempts) throw e
                wait = backoff(attempt)
            }
            sleep(wait)
            attempt++
        }
    }

    /** Uploads [parts] one after the other; stops at the first part that fails. */
    public suspend fun uploadParts(parts: List<AppleHealthPart>): List<UploadResult> = parts.map { uploadAppleHealthPart(it) }

    // Plumbing

    private fun backoff(attempt: Int): Long = minOf(30_000L, 1000L shl (attempt - 1))

    private suspend fun <T> get(path: String, params: List<Pair<String, String?>> = emptyList(), serializer: KSerializer<T>): T =
        decode(serializer, call("GET", path + query(params)))

    private suspend fun <T> page(path: String, item: KSerializer<T>, params: List<Pair<String, String?>>): Page<T> {
        val dto = get(path, params, PageDto.serializer(item))
        return Page(dto.data, dto.nextCursor)
    }

    private suspend fun call(
        method: String,
        pathAndQuery: String,
        body: ByteArray? = null,
        extraHeaders: Map<String, String> = emptyMap(),
        accept: String = "application/json",
    ): HttpResponse {
        val headers = LinkedHashMap<String, String>()
        headers["Authorization"] = "Bearer " + tokenProvider()
        headers["Accept"] = accept
        headers["User-Agent"] = userAgent
        if (body != null) headers["Content-Type"] = "application/json"
        headers.putAll(extraHeaders)
        val req = HttpRequest(method, base + pathAndQuery, headers, body)
        val res = withContext(Dispatchers.IO) { transport.send(req) }
        if (res.status !in 200..299) throw problemOf(res)
        return res
    }

    private fun jsonBody(s: String) = s.toByteArray(Charsets.UTF_8)

    private fun <T> decode(serializer: KSerializer<T>, res: HttpResponse): T =
        json.decodeFromString(serializer, res.body.toString(Charsets.UTF_8))

    private fun Range.params() = listOf("start" to start, "end" to end, "cursor" to cursor, "limit" to limit?.toString())

    @Serializable
    private data class CreateUserBody(@SerialName("external_ref") val externalRef: String)

    @Serializable
    private data class DeletionAccepted(@SerialName("job_id") val jobId: String)

    @Serializable
    private data class AuthorizeUrl(@SerialName("authorize_url") val authorizeUrl: String)

    @Serializable
    private data class ProblemDto(
        val type: String? = null,
        val title: String? = null,
        val status: Int? = null,
        val detail: String? = null,
        @SerialName("request_id") val requestId: String? = null,
    )

    public companion object {
        public const val VERSION: String = "0.1.0"
        private const val FHIR_JSON = "application/fhir+json"

        internal val json = Json {
            ignoreUnknownKeys = true
            explicitNulls = false
            coerceInputValues = true
        }

        internal fun problemOf(res: HttpResponse): OhsApiException {
            val p = try {
                json.decodeFromString(ProblemDto.serializer(), res.body.toString(Charsets.UTF_8))
            } catch (_: Exception) {
                null
            }
            return OhsApiException(
                status = res.status,
                title = p?.title,
                detail = p?.detail,
                type = p?.type,
                requestId = p?.requestId ?: res.header("X-Request-Id"),
                retryAfterSeconds = res.header("Retry-After")?.trim()?.toLongOrNull(),
            )
        }

        private fun seg(s: String): String = URLEncoder.encode(s, "UTF-8").replace("+", "%20")

        private fun query(params: List<Pair<String, String?>>): String {
            val set = params.filter { it.second != null }
            if (set.isEmpty()) return ""
            return set.joinToString("&", prefix = "?") { (k, v) -> "$k=${seg(v!!)}" }
        }

        /** The `cursor` query parameter of a FHIR `next` link. */
        internal fun cursorOf(url: String): String? {
            val q = URI(url).rawQuery ?: return null
            for (kv in q.split('&')) {
                val i = kv.indexOf('=')
                if (i > 0 && kv.substring(0, i) == "cursor") return URLDecoder.decode(kv.substring(i + 1), "UTF-8")
            }
            return null
        }
    }
}

/**
 * Walks every page of a list call: `allPages { client.listSamples(id, "heart_rate", Range(cursor = it)) }`.
 */
public fun <T> allPages(fetch: suspend (cursor: String?) -> Page<T>): Flow<T> = flow {
    var cursor: String? = null
    do {
        val page = fetch(cursor)
        for (item in page.data) emit(item)
        cursor = page.nextCursor
    } while (cursor != null)
}
