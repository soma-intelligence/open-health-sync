// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk

import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL

/** One HTTP request as the client sends it. */
public class HttpRequest(
    public val method: String,
    public val url: String,
    public val headers: Map<String, String>,
    public val body: ByteArray? = null,
)

/** One HTTP response. Header names are lower case. */
public class HttpResponse(
    public val status: Int,
    public val headers: Map<String, String>,
    public val body: ByteArray,
) {
    /** The value of a header, whatever the case of [name]. */
    public fun header(name: String): String? = headers[name.lowercase()]
}

/**
 * Sends requests. The default, [UrlConnectionTransport], runs on Android and any JVM; supply
 * your own to use OkHttp or Ktor, or a fake in tests. Blocking: the client calls it on
 * [kotlinx.coroutines.Dispatchers.IO]. Throw [IOException] for a network failure.
 */
public fun interface HttpTransport {
    @Throws(IOException::class)
    public fun send(request: HttpRequest): HttpResponse
}

/** [HttpTransport] on [HttpURLConnection], with no dependency. */
public class UrlConnectionTransport(
    private val connectTimeoutMillis: Int = 15_000,
    private val readTimeoutMillis: Int = 60_000,
) : HttpTransport {
    override fun send(request: HttpRequest): HttpResponse {
        val conn = URL(request.url).openConnection() as HttpURLConnection
        try {
            conn.requestMethod = request.method
            conn.connectTimeout = connectTimeoutMillis
            conn.readTimeout = readTimeoutMillis
            conn.instanceFollowRedirects = false
            conn.useCaches = false
            for ((k, v) in request.headers) conn.setRequestProperty(k, v)
            val body = request.body
            if (body != null) {
                conn.doOutput = true
                conn.setFixedLengthStreamingMode(body.size)
                conn.outputStream.use { it.write(body) }
            }
            val status = conn.responseCode
            val stream: InputStream? = if (status >= 400) conn.errorStream else conn.inputStream
            val bytes = stream?.use { it.readBytes() } ?: ByteArray(0)
            val headers = buildMap {
                for ((k, v) in conn.headerFields) {
                    if (k != null && v.isNotEmpty()) put(k.lowercase(), v.first())
                }
            }
            return HttpResponse(status, headers, bytes)
        } finally {
            conn.disconnect()
        }
    }
}
