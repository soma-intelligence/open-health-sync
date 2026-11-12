// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.flutter.health

import android.content.Context
import androidx.startup.Initializer

/**
 * Runs at process start (androidx.startup, declared in the manifest of this library), so a
 * background sync that WorkManager starts without any activity finds how to build its sync.
 */
public class BackgroundInitializer : Initializer<Unit> {
    override fun create(context: Context) {
        HealthSyncBridge.install()
    }

    override fun dependencies(): List<Class<out Initializer<*>>> = emptyList()
}
