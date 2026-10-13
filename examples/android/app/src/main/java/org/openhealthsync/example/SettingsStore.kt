// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.example

import android.content.Context

/**
 * Server URL and upload token, kept in app-private SharedPreferences.
 *
 * This is a demo. A real app should keep the token in EncryptedSharedPreferences or encrypt it
 * with a key from the Android Keystore, and fetch it from its own backend rather than ask the
 * user to paste it.
 */
class SettingsStore(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

    var serverUrl: String
        get() = prefs.getString(KEY_URL, null) ?: DEFAULT_URL
        set(value) {
            prefs.edit().putString(KEY_URL, value.trim()).apply()
        }

    var token: String
        get() = prefs.getString(KEY_TOKEN, null) ?: ""
        set(value) {
            prefs.edit().putString(KEY_TOKEN, value.trim()).apply()
        }

    companion object {
        /** The host machine as seen from the Android emulator. */
        const val DEFAULT_URL = "http://10.0.2.2:8080"

        private const val PREFS_NAME = "ohs_example"
        private const val KEY_URL = "server_url"
        private const val KEY_TOKEN = "upload_token"
    }
}
