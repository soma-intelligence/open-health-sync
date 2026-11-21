// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk.healthconnect

import android.content.Context
import androidx.activity.result.contract.ActivityResultContract
import androidx.health.connect.client.HealthConnectClient
import androidx.health.connect.client.HealthConnectFeatures
import androidx.health.connect.client.PermissionController
import androidx.health.connect.client.permission.HealthPermission
import java.io.File
import java.io.IOException
import java.time.Duration
import java.time.Instant
import java.time.ZoneId
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import org.openhealthsync.sdk.AppleHealthPartBuilder
import org.openhealthsync.sdk.HealthConnectPart
import org.openhealthsync.sdk.HealthConnectPartBuilder
import org.openhealthsync.sdk.OhsApiException
import org.openhealthsync.sdk.OhsClient
import org.openhealthsync.sdk.UploadResult

/** Whether Health Connect can be used on this device. */
public enum class HealthConnectAvailability {
    /** Health Connect is installed and recent enough. */
    AVAILABLE,

    /** The Health Connect app must be installed or updated first (Android 13 and lower). */
    PROVIDER_UPDATE_REQUIRED,

    /** This device cannot run Health Connect. */
    UNAVAILABLE,
}

/** Sends one part to the server. [OhsClient.uploadHealthConnectPart] is the real one. */
public fun interface HealthConnectPartUploader {
    public suspend fun upload(part: HealthConnectPart): UploadResult
}

/** The settings of a [HealthConnectSync]. */
public data class HealthConnectSyncOptions(
    /**
     * How far back the first sync of a type reads. `Duration.ZERO` reads nothing old and only
     * follows changes from then on; null reads everything Health Connect lets the app see.
     * Health Connect itself stops at 30 days before the first permission grant unless the app
     * holds [HealthPermission.PERMISSION_READ_HEALTH_DATA_HISTORY].
     */
    val initialBackfill: Duration? = Duration.ofDays(30),
    /**
     * How far back a type is read again when its changes token has expired (Health Connect
     * keeps changes about 30 days, so this happens after a long time without a sync). Records
     * deleted during the gap are not known and stay on the server.
     */
    val expiredTokenWindow: Duration = Duration.ofDays(30),
    /** The most NDJSON bytes of one part, before compression. The server takes 64 MiB. */
    val maxPartBytes: Int = HealthConnectPartBuilder.DEFAULT_MAX_PART_BYTES,
    /** Lines are written to the queue whenever this many NDJSON bytes are held in memory. */
    val maxBufferedBytes: Long = 2L * HealthConnectPartBuilder.DEFAULT_MAX_PART_BYTES,
    /** The prefix of every batch id. */
    val batchPrefix: String = "android",
    /** The zone of the local day of a record that carries no zone offset; null is the device's. */
    val zone: ZoneId? = null,
) {
    init {
        require(initialBackfill == null || !initialBackfill.isNegative) { "initialBackfill must not be negative" }
        require(!expiredTokenWindow.isNegative) { "expiredTokenWindow must not be negative" }
        require(maxPartBytes > 0) { "maxPartBytes must be positive" }
        require(maxBufferedBytes > 0) { "maxBufferedBytes must be positive" }
    }
}

/** What one [HealthConnectSync.sync] or [HealthConnectSync.flush] did. */
public data class SyncReport(
    /** The batch ids used, one per record type that had something to send. */
    val batchIds: List<String> = emptyList(),
    /** Parts the server stored or already had. */
    val partsUploaded: Int = 0,
    /** Of [partsUploaded], the parts the server already had. */
    val duplicateParts: Int = 0,
    /** Parts still in the queue on disk, waiting for the network or the server. */
    val partsPending: Int = 0,
    /** Parts the server refused for good (a 4xx that a retry cannot fix); they left the queue. */
    val partsDropped: Int = 0,
    /** Lines read and queued, by record type (`Steps`). Deletion lines are not counted here. */
    val linesByType: Map<String, Int> = emptyMap(),
    /** Deletions read and queued, by the record type they were reported for. */
    val deletionsByType: Map<String, Int> = emptyMap(),
    /** Records left out: no id, a value that is not a finite number, or a line too long. */
    val skippedRecords: Int = 0,
    /** Types not read because the user has not granted their read permission. */
    val typesWithoutPermission: Set<HealthConnectRecordType> = emptySet(),
    /** Types whose changes token had expired and that were read again over a time window. */
    val expiredTokenTypes: Set<HealthConnectRecordType> = emptySet(),
    /**
     * Why the upload stopped with parts still pending: an `IOException` when offline, an
     * [OhsApiException] for a 401, 403, 429 or 5xx. Null when the queue was emptied or is only
     * waiting for its retry time.
     */
    val uploadError: Exception? = null,
) {
    /** Lines read and queued, deletions excluded. */
    val linesQueued: Int get() = linesByType.values.sum()

    /** Deletions read and queued. */
    val deletionsQueued: Int get() = deletionsByType.values.sum()
}

/**
 * Reads Health Connect and uploads what changed since the last run to
 * `POST /v1/ingest/health-connect`. The counterpart of the Swift SDK's `HealthKitSync`.
 *
 * For each record type, the first sync reads a backfill window
 * ([HealthConnectSyncOptions.initialBackfill]); later syncs read the changes since the stored
 * changes token: the records added or edited become upload lines, grouped into parts by type
 * and local day, and the ids of the deleted records follow as `Deletion` lines. A token that
 * has expired is replaced, and the type read again over a time window.
 *
 * Every part is written to the [queue] on disk before the token is saved, then uploaded in
 * order. A part that cannot be sent (no network, server down, token refused) stays in the
 * queue and is sent by a later [sync] or [flush], with a growing wait; nothing is read twice
 * and nothing is lost. The server keeps each record once, so a part sent twice is harmless.
 *
 * Build one with [create]. Calls are serialized: a sync started while another runs waits.
 */
public class HealthConnectSync(
    public val reader: HealthConnectReader,
    private val uploader: HealthConnectPartUploader,
    public val tokens: ChangesTokenStore,
    public val queue: UploadQueue,
    public val options: HealthConnectSyncOptions = HealthConnectSyncOptions(),
    private val now: () -> Instant = Instant::now,
) {
    /** A sync that uploads with [client], whose token provider returns a user token with the `upload` scope. */
    public constructor(
        reader: HealthConnectReader,
        client: OhsClient,
        tokens: ChangesTokenStore,
        queue: UploadQueue,
        options: HealthConnectSyncOptions = HealthConnectSyncOptions(),
    ) : this(reader, HealthConnectPartUploader(client::uploadHealthConnectPart), tokens, queue, options)

    init {
        // Fails here rather than at the first sync.
        AppleHealthPartBuilder.newBatchId(options.batchPrefix)
    }

    /** How many parts wait in the queue on disk. */
    public val pendingParts: Int get() = queue.size

    /** The Health Connect permissions the user granted this app. */
    public suspend fun grantedPermissions(): Set<String> = reader.grantedPermissions()

    /** Whether the user granted read access to every type of [types]. */
    public suspend fun hasPermissions(types: Set<HealthConnectRecordType> = allSupportedTypes): Boolean =
        grantedPermissions().containsAll(authorizationPermissions(types))

    /**
     * Reads the changes of [types] and uploads them, then whatever was already waiting in the
     * queue. A type whose read permission is missing is skipped and listed in
     * [SyncReport.typesWithoutPermission].
     *
     * A failed upload does not throw: the parts stay queued and the report carries
     * [SyncReport.partsPending] and [SyncReport.uploadError]. A failed read does throw (a
     * `SecurityException` for a read in the background without the background permission, an
     * `IOException` or `RemoteException` when Health Connect is unreachable); what was read
     * before it is already queued.
     */
    public suspend fun sync(types: Set<HealthConnectRecordType> = allSupportedTypes): SyncReport = mutex.withLock {
        val report = Report()
        val granted = reader.grantedPermissions()
        var readError: Exception? = null
        try {
            for (type in types.sortedBy { it.ordinal }) {
                if (type.readPermission !in granted) {
                    report.typesWithoutPermission += type
                    continue
                }
                syncType(type, report)
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            readError = e
        }
        // What is queued goes up even when a type could not be read.
        drain(report, force = false)
        readError?.let { throw it }
        report.toReport(queue.size)
    }

    /**
     * Uploads the parts waiting in the queue, without reading Health Connect. With [force] the
     * retry wait of a part that failed before is ignored: use it when the user asks for a sync
     * or the network just came back.
     */
    public suspend fun flush(force: Boolean = false): SyncReport = mutex.withLock {
        val report = Report()
        drain(report, force)
        report.toReport(queue.size)
    }

    /**
     * Forgets the changes token of [type]: the next sync reads its backfill window again (the
     * server keeps each record once). Parts already queued are kept.
     */
    public fun resetAnchor(type: HealthConnectRecordType) {
        tokens.setToken(type.wireName, null)
    }

    // One type

    private suspend fun syncType(type: HealthConnectRecordType, report: Report) {
        val batch = AppleHealthPartBuilder.newBatchId(options.batchPrefix)
        val round = Round(type, HealthConnectPartBuilder(batch, options.maxPartBytes), report)
        var token: String = tokens.token(type.wireName) ?: run {
            // The token is taken first, so a record written while the window is read is not
            // missed: it comes back as a change, and the server keeps it once.
            val fresh = reader.changesToken(type)
            readWindow(round, options.initialBackfill)
            round.commit(fresh)
            return
        }
        while (true) {
            val page = reader.changes(token)
            if (page.tokenExpired) {
                val fresh = reader.changesToken(type)
                report.expiredTokenTypes += type
                readWindow(round, options.expiredTokenWindow)
                round.commit(fresh)
                return
            }
            for (record in page.upserted) round.add(record)
            for (id in page.deletedIds) round.addDeletion(id)
            token = page.nextToken
            if (!page.hasMore) {
                round.commit(token)
                return
            }
            if (round.builder.bufferedBytes >= options.maxBufferedBytes) round.commit(token)
        }
    }

    /** Reads the records of the round's type from [window] ago; null is everything, zero nothing. */
    private suspend fun readWindow(round: Round, window: Duration?) {
        if (window != null && window.isZero) return
        val start = if (window == null) Instant.EPOCH else now().minus(window)
        var pageToken: String? = null
        do {
            val page = reader.read(round.type, start, pageToken)
            for (record in page.records) round.add(record)
            if (round.builder.bufferedBytes >= options.maxBufferedBytes) round.flush()
            pageToken = page.nextPageToken
        } while (!pageToken.isNullOrEmpty())
    }

    /** The lines of one type being read, on their way to the queue. */
    private inner class Round(val type: HealthConnectRecordType, val builder: HealthConnectPartBuilder, val report: Report) {
        private val zone = options.zone ?: ZoneId.systemDefault()

        fun add(record: androidx.health.connect.client.records.Record) {
            val line = HealthConnectLines.line(record)
            if (line == null || !builder.add(type.wireName, HealthConnectLines.day(record, zone), line)) {
                report.skippedRecords++
                return
            }
            report.linesByType.merge(type.wireName, 1, Int::plus)
        }

        fun addDeletion(recordId: String) {
            if (recordId.isEmpty()) return
            // A deletion has no time of its own: its part is dated today, like the Swift SDK's.
            builder.addDeletion(AppleHealthPartBuilder.localDay(now(), zone), recordId)
            report.deletionsByType.merge(type.wireName, 1, Int::plus)
        }

        /** Writes the lines held in memory to the queue. */
        suspend fun flush() {
            val parts = builder.drain()
            if (parts.isEmpty()) return
            withContext(Dispatchers.IO) { for (part in parts) queue.enqueue(part) }
            if (builder.batch !in report.batchIds) report.batchIds += builder.batch
        }

        /** [flush], then saves [token]: everything read before it is now on disk. */
        suspend fun commit(token: String) {
            flush()
            tokens.setToken(type.wireName, token)
        }
    }

    // Upload

    private suspend fun drain(report: Report, force: Boolean) {
        while (true) {
            val queued = withContext(Dispatchers.IO) { queue.peek() } ?: return
            // The queue is in order, so a part that must wait holds back the ones after it.
            if (!force && queued.notBeforeMillis > now().toEpochMilli()) return
            val failure: Exception = try {
                val result = uploader.upload(queued.part)
                withContext(Dispatchers.IO) { queue.remove(queued) }
                report.partsUploaded++
                if (result.duplicate) report.duplicateParts++
                continue
            } catch (e: OhsApiException) {
                if (isPermanent(e.status)) {
                    // The same bytes would be refused again: keeping the part would block the queue.
                    withContext(Dispatchers.IO) { queue.remove(queued) }
                    report.partsDropped++
                    continue
                }
                e
            } catch (e: IOException) {
                e
            }
            withContext(Dispatchers.IO) { queue.markFailed(queued) }
            report.uploadError = failure
            return
        }
    }

    private class Report {
        val batchIds = ArrayList<String>()
        var partsUploaded = 0
        var duplicateParts = 0
        var partsDropped = 0
        val linesByType = LinkedHashMap<String, Int>()
        val deletionsByType = LinkedHashMap<String, Int>()
        var skippedRecords = 0
        val typesWithoutPermission = LinkedHashSet<HealthConnectRecordType>()
        val expiredTokenTypes = LinkedHashSet<HealthConnectRecordType>()
        var uploadError: Exception? = null

        fun toReport(pending: Int) = SyncReport(
            batchIds.toList(), partsUploaded, duplicateParts, pending, partsDropped, linesByType.toMap(),
            deletionsByType.toMap(), skippedRecords, typesWithoutPermission.toSet(), expiredTokenTypes.toSet(), uploadError,
        )
    }

    public companion object {
        /** One Health Connect store per process, so one sync at a time whatever the instance. */
        private val mutex = Mutex()

        /** Every record type the server stores. Pass it, or a subset, to [sync] and the permission calls. */
        public val allSupportedTypes: Set<HealthConnectRecordType> = HealthConnectRecordType.entries.toSet()

        /**
         * A 4xx that sending the same part again cannot fix: a malformed part (400), other
         * bytes under a key already taken (409), a part too large (413). 401 and 403 are not
         * in it (the token can be renewed, the connector turned back on), nor 408 and 429.
         */
        internal fun isPermanent(status: Int): Boolean =
            status in 400..499 && status != 401 && status != 403 && status != 408 && status != 429

        /** Whether Health Connect can be used on this device. Check it before anything else. */
        public fun availability(context: Context): HealthConnectAvailability =
            when (HealthConnectClient.getSdkStatus(context)) {
                HealthConnectClient.SDK_AVAILABLE -> HealthConnectAvailability.AVAILABLE
                HealthConnectClient.SDK_UNAVAILABLE_PROVIDER_UPDATE_REQUIRED -> HealthConnectAvailability.PROVIDER_UPDATE_REQUIRED
                else -> HealthConnectAvailability.UNAVAILABLE
            }

        /**
         * The permissions to ask the user for in order to sync [types]: one read permission per
         * type, plus the background read permission with [background] (needed by
         * [enableBackgroundDelivery]) and the history permission with [history] (data older
         * than 30 days before the first grant). Each must also be declared in the app's
         * manifest. The counterpart of the Swift SDK's `authorizationTypes(for:)`.
         */
        public fun authorizationPermissions(
            types: Set<HealthConnectRecordType> = allSupportedTypes,
            background: Boolean = false,
            history: Boolean = false,
        ): Set<String> {
            val permissions = LinkedHashSet<String>()
            for (type in types.sortedBy { it.ordinal }) permissions += type.readPermission
            if (background) permissions += HealthPermission.PERMISSION_READ_HEALTH_DATA_IN_BACKGROUND
            if (history) permissions += HealthPermission.PERMISSION_READ_HEALTH_DATA_HISTORY
            return permissions
        }

        /**
         * The contract that shows the Health Connect permission screen. Register it with
         * `registerForActivityResult`, launch it with [authorizationPermissions]; the result is
         * the set of permissions granted. The counterpart of `requestAuthorization(for:)`.
         */
        public fun requestAuthorizationContract(): ActivityResultContract<Set<String>, Set<String>> =
            PermissionController.createRequestPermissionResultContract()

        /** Whether this device's Health Connect can be read while the app is in the background. */
        public fun isBackgroundReadAvailable(context: Context): Boolean =
            feature(context, HealthConnectFeatures.FEATURE_READ_HEALTH_DATA_IN_BACKGROUND)

        /** Whether this device's Health Connect can give data older than 30 days before the first grant. */
        public fun isHistoryReadAvailable(context: Context): Boolean =
            feature(context, HealthConnectFeatures.FEATURE_READ_HEALTH_DATA_HISTORY)

        private fun feature(context: Context, feature: Int): Boolean =
            availability(context) == HealthConnectAvailability.AVAILABLE &&
                HealthConnectClient.getOrCreate(context).features.getFeatureStatus(feature) ==
                HealthConnectFeatures.FEATURE_STATUS_AVAILABLE

        /**
         * Builds the [HealthConnectSync] of a background run. Set it in `Application.onCreate`,
         * before WorkManager starts a worker: a background run happens in a process where no
         * activity has run. Return null when no user is signed in; the run is then skipped.
         * An `Application` that implements [HealthConnectSyncFactory] is found without it.
         */
        @Volatile
        @JvmStatic
        public var backgroundFactory: HealthConnectSyncFactory? = null

        /** Called after each background run with its report, or the error that stopped it. */
        @Volatile
        @JvmStatic
        public var onBackgroundSync: ((Result<SyncReport>) -> Unit)? = null

        /**
         * Schedules a periodic WorkManager job that syncs [types] every [repeatInterval] (at
         * least 15 minutes, WorkManager's floor), when the network is up, across restarts of
         * the app and the phone. Call it once the permissions are granted; calling it again
         * updates the schedule. The counterpart of the Swift SDK's `enableBackgroundDelivery`:
         * Health Connect has no push, so the job polls the changes tokens.
         *
         * The job needs [backgroundFactory] (or an `Application` implementing
         * [HealthConnectSyncFactory]) to build the sync, and reads only when the user granted
         * `PERMISSION_READ_HEALTH_DATA_IN_BACKGROUND`; without it a run still uploads what
         * waits in the queue.
         */
        public fun enableBackgroundDelivery(
            context: Context,
            types: Set<HealthConnectRecordType> = allSupportedTypes,
            repeatInterval: Duration = Duration.ofHours(1),
            requireUnmeteredNetwork: Boolean = false,
        ) {
            HealthConnectSyncWorker.schedule(context, types, repeatInterval, requireUnmeteredNetwork)
        }

        /** Cancels the job scheduled by [enableBackgroundDelivery]. Queued parts are kept. */
        public fun stopObserving(context: Context) {
            HealthConnectSyncWorker.cancel(context)
        }

        /** The directory [create] keeps its changes tokens and upload queue in. */
        public fun defaultDirectory(context: Context): File = File(context.filesDir, "open-health-sync/health-connect")

        /**
         * A sync on this device's Health Connect, with its changes tokens and upload queue in
         * the app's private files ([defaultDirectory]). [client] holds a user token with the
         * `upload` scope. Throws `IllegalStateException` when [availability] is not
         * [HealthConnectAvailability.AVAILABLE].
         */
        public fun create(
            context: Context,
            client: OhsClient,
            options: HealthConnectSyncOptions = HealthConnectSyncOptions(),
        ): HealthConnectSync {
            val app = context.applicationContext
            check(availability(app) == HealthConnectAvailability.AVAILABLE) { "Health Connect is not available on this device" }
            val dir = defaultDirectory(app)
            return HealthConnectSync(
                reader = ClientHealthConnectReader(HealthConnectClient.getOrCreate(app)),
                client = client,
                tokens = FileChangesTokenStore(File(dir, "tokens.json")),
                queue = UploadQueue(File(dir, "queue")),
                options = options,
            )
        }
    }
}
