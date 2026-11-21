// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk.healthconnect

import androidx.health.connect.client.HealthConnectClient
import androidx.health.connect.client.changes.DeletionChange
import androidx.health.connect.client.changes.UpsertionChange
import androidx.health.connect.client.records.Record
import androidx.health.connect.client.request.ChangesTokenRequest
import androidx.health.connect.client.request.ReadRecordsRequest
import androidx.health.connect.client.time.TimeRangeFilter
import java.time.Instant

/** One page of the changes since a token. */
public class HealthConnectChanges(
    /** The records added or edited, in the order Health Connect reports them. */
    public val upserted: List<Record>,
    /** The ids of the records deleted. */
    public val deletedIds: List<String>,
    /** The token to ask the next page with, and to keep once [hasMore] is false. */
    public val nextToken: String,
    public val hasMore: Boolean,
    /**
     * True when the token asked with is too old (Health Connect keeps changes about 30 days).
     * The other fields are then meaningless: get a new token and read a time window again.
     */
    public val tokenExpired: Boolean = false,
)

/** One page of a time window read. */
public class HealthConnectPage(
    public val records: List<Record>,
    /** The token of the next page; null on the last one. */
    public val nextPageToken: String?,
)

/**
 * The few Health Connect calls [HealthConnectSync] makes. [ClientHealthConnectReader] is the
 * real one; a test hands the sync a fake.
 */
public interface HealthConnectReader {
    /** The Health Connect permissions the user granted this app. */
    public suspend fun grantedPermissions(): Set<String>

    /** A token from which the later changes of [type] can be read. */
    public suspend fun changesToken(type: HealthConnectRecordType): String

    /** The changes since [token], one page. */
    public suspend fun changes(token: String): HealthConnectChanges

    /**
     * The records of [type] whose time is at or after [start], oldest first, one page;
     * [pageToken] is the [HealthConnectPage.nextPageToken] of the page before.
     */
    public suspend fun read(type: HealthConnectRecordType, start: Instant, pageToken: String?): HealthConnectPage
}

/** [HealthConnectReader] on the androidx [HealthConnectClient]. */
public class ClientHealthConnectReader(
    private val client: HealthConnectClient,
    /** Records per page of a window read; Health Connect allows up to 5000. */
    private val pageSize: Int = 1000,
) : HealthConnectReader {
    init {
        require(pageSize in 1..5000) { "pageSize must be between 1 and 5000" }
    }

    override suspend fun grantedPermissions(): Set<String> = client.permissionController.getGrantedPermissions()

    override suspend fun changesToken(type: HealthConnectRecordType): String =
        client.getChangesToken(ChangesTokenRequest(recordTypes = setOf(type.recordClass)))

    override suspend fun changes(token: String): HealthConnectChanges {
        val res = client.getChanges(token)
        if (res.changesTokenExpired) {
            return HealthConnectChanges(emptyList(), emptyList(), token, hasMore = false, tokenExpired = true)
        }
        val upserted = ArrayList<Record>()
        val deleted = ArrayList<String>()
        for (change in res.changes) {
            when (change) {
                is UpsertionChange -> upserted += change.record
                is DeletionChange -> deleted += change.recordId
            }
        }
        return HealthConnectChanges(upserted, deleted, res.nextChangesToken, res.hasMore)
    }

    override suspend fun read(type: HealthConnectRecordType, start: Instant, pageToken: String?): HealthConnectPage {
        val res = client.readRecords(
            ReadRecordsRequest(
                recordType = type.recordClass,
                timeRangeFilter = TimeRangeFilter.after(start),
                ascendingOrder = true,
                pageSize = pageSize,
                pageToken = pageToken,
            ),
        )
        return HealthConnectPage(res.records, res.pageToken)
    }
}
