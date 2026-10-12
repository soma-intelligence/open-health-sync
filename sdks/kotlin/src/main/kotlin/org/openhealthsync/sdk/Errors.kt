// SPDX-License-Identifier: MIT
package org.openhealthsync.sdk

/**
 * A non-2xx answer from the server, with the RFC 9457 problem fields when the body had them.
 * Quote [requestId] when reporting a problem: the server logs carry it too.
 */
public class OhsApiException(
    public val status: Int,
    public val title: String?,
    public val detail: String?,
    public val type: String?,
    public val requestId: String?,
    /** Seconds from `Retry-After`, when the server sent it. */
    public val retryAfterSeconds: Long?,
) : Exception(buildMessage(status, title, detail, requestId)) {
    private companion object {
        fun buildMessage(status: Int, title: String?, detail: String?, requestId: String?): String =
            buildString {
                append("HTTP ").append(status)
                if (!title.isNullOrEmpty()) append(' ').append(title)
                if (!detail.isNullOrEmpty()) append(": ").append(detail)
                if (!requestId.isNullOrEmpty()) append(" (request ").append(requestId).append(')')
            }
    }
}
