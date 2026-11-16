// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk.healthconnect

import android.content.Context
import androidx.health.connect.client.permission.HealthPermission
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.Data
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequest
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import java.time.Duration
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CancellationException

/**
 * Builds the [HealthConnectSync] a background run uses. Implement it on your `Application`, or
 * set [HealthConnectSync.backgroundFactory].
 */
public fun interface HealthConnectSyncFactory {
    /** The sync to run, or null to skip this run (no user signed in). */
    public fun createHealthConnectSync(context: Context): HealthConnectSync?
}

/**
 * The WorkManager job behind [HealthConnectSync.enableBackgroundDelivery]: one sync of the
 * scheduled types, or only an upload of the queue when the app may not read in the background.
 */
public class HealthConnectSyncWorker(context: Context, params: WorkerParameters) : CoroutineWorker(context, params) {
    override suspend fun doWork(): Result {
        val app = applicationContext
        val factory = HealthConnectSync.backgroundFactory ?: (app as? HealthConnectSyncFactory)
        val sync = try {
            factory?.createHealthConnectSync(app)
        } catch (e: Exception) {
            notify(kotlin.Result.failure(e))
            return Result.failure()
        }
        // Nothing to run with: the next period tries again.
        if (sync == null) return Result.success()
        return try {
            val canRead = HealthPermission.PERMISSION_READ_HEALTH_DATA_IN_BACKGROUND in sync.grantedPermissions()
            val report = if (canRead) sync.sync(typesOf(inputData.getString(KEY_TYPES))) else sync.flush()
            notify(kotlin.Result.success(report))
            if (shouldRetry(report)) Result.retry() else Result.success()
        } catch (e: CancellationException) {
            throw e
        } catch (e: SecurityException) {
            // A permission was taken back: retrying sooner than the next period cannot help.
            notify(kotlin.Result.failure(e))
            Result.failure()
        } catch (e: Exception) {
            notify(kotlin.Result.failure(e))
            Result.retry()
        }
    }

    private fun notify(result: kotlin.Result<SyncReport>) {
        try {
            HealthConnectSync.onBackgroundSync?.invoke(result)
        } catch (_: Exception) {
            // A listener must not fail the job.
        }
    }

    public companion object {
        /** The unique name of the periodic job. */
        public const val WORK_NAME: String = "org.openhealthsync.health-connect.sync"

        internal const val KEY_TYPES = "types"

        /** WorkManager's shortest period. */
        internal val MIN_INTERVAL: Duration = Duration.ofMillis(PeriodicWorkRequest.MIN_PERIODIC_INTERVAL_MILLIS)

        /** A run is tried again sooner when an upload failed for a reason that can pass. */
        internal fun shouldRetry(report: SyncReport): Boolean = report.partsPending > 0 && report.uploadError != null

        /** The wire names kept in the job's input: comma separated, in a stable order. */
        internal fun typeNames(types: Set<HealthConnectRecordType>): String =
            types.sortedBy { it.ordinal }.joinToString(",") { it.wireName }

        /** The types of a job's input; every supported type when it holds none. */
        internal fun typesOf(names: String?): Set<HealthConnectRecordType> {
            val types = names.orEmpty().split(',').mapNotNull(HealthConnectRecordType::fromWireName).toSet()
            return types.ifEmpty { HealthConnectSync.allSupportedTypes }
        }

        internal fun schedule(
            context: Context,
            types: Set<HealthConnectRecordType>,
            repeatInterval: Duration,
            requireUnmeteredNetwork: Boolean,
        ) {
            require(types.isNotEmpty()) { "types must not be empty" }
            val interval = if (repeatInterval < MIN_INTERVAL) MIN_INTERVAL else repeatInterval
            val constraints = Constraints.Builder()
                .setRequiredNetworkType(if (requireUnmeteredNetwork) NetworkType.UNMETERED else NetworkType.CONNECTED)
                .build()
            val request = PeriodicWorkRequest.Builder(HealthConnectSyncWorker::class.java, interval)
                .setConstraints(constraints)
                .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, UploadQueue.BASE_BACKOFF_MILLIS, TimeUnit.MILLISECONDS)
                .setInputData(Data.Builder().putString(KEY_TYPES, typeNames(types)).build())
                .build()
            WorkManager.getInstance(context.applicationContext)
                .enqueueUniquePeriodicWork(WORK_NAME, ExistingPeriodicWorkPolicy.UPDATE, request)
        }

        internal fun cancel(context: Context) {
            WorkManager.getInstance(context.applicationContext).cancelUniqueWork(WORK_NAME)
        }
    }
}
