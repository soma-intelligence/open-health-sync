// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.reactnative

import android.content.ContentProvider
import android.content.ContentValues
import android.database.Cursor
import android.net.Uri

/**
 * Holds no data. Android creates every provider at process start, before
 * `Application.onCreate`, which is the one place that also runs when WorkManager starts the
 * app for a background sync: [HealthSyncRuntime.install] is called from here so the app
 * needs no code of its own.
 */
class OpenHealthSyncInitProvider : ContentProvider() {
    override fun onCreate(): Boolean {
        context?.let(HealthSyncRuntime::install)
        return true
    }

    override fun query(uri: Uri, projection: Array<out String>?, selection: String?, selectionArgs: Array<out String>?, sortOrder: String?): Cursor? = null

    override fun getType(uri: Uri): String? = null

    override fun insert(uri: Uri, values: ContentValues?): Uri? = null

    override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?): Int = 0

    override fun update(uri: Uri, values: ContentValues?, selection: String?, selectionArgs: Array<out String>?): Int = 0
}
