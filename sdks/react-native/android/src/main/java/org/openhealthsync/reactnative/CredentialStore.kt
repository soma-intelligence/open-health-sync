// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.reactnative

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Keeps the base URL and the user token between runs, so a background job can upload in a
 * process where JavaScript never ran. The token is encrypted with an AES-GCM key that lives
 * in the Android Keystore and never leaves it; the ciphertext is in the app's private
 * preferences. Nothing here is ever logged.
 *
 * A device backup restores the preferences but not the key: the token then cannot be read,
 * and the store answers as if it held none.
 */
internal class CredentialStore(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    @Volatile
    private var cachedToken: String? = null

    val baseUrl: String? get() = prefs.getString(KEY_BASE_URL, null)?.takeIf { it.isNotEmpty() }

    val isConfigured: Boolean get() = baseUrl != null && token() != null

    @Synchronized
    fun save(baseUrl: String, token: String) {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val sealed = cipher.iv + cipher.doFinal(token.toByteArray(Charsets.UTF_8))
        prefs.edit()
            .putString(KEY_BASE_URL, baseUrl)
            .putString(KEY_TOKEN, Base64.encodeToString(sealed, Base64.NO_WRAP))
            .apply()
        cachedToken = token
    }

    /** The user token, or null when none is kept or it can no longer be decrypted. */
    @Synchronized
    fun token(): String? {
        cachedToken?.let { return it }
        val sealed = prefs.getString(KEY_TOKEN, null) ?: return null
        return try {
            val bytes = Base64.decode(sealed, Base64.NO_WRAP)
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, bytes, 0, IV_BYTES))
            String(cipher.doFinal(bytes, IV_BYTES, bytes.size - IV_BYTES), Charsets.UTF_8).also { cachedToken = it }
        } catch (_: Exception) {
            null
        }
    }

    @Synchronized
    fun clear() {
        cachedToken = null
        prefs.edit().remove(KEY_BASE_URL).remove(KEY_TOKEN).apply()
        try {
            keyStore().deleteEntry(KEY_ALIAS)
        } catch (_: Exception) {
            // The ciphertext is gone already; a key left behind protects nothing.
        }
    }

    var lastBackgroundSync: String
        get() = prefs.getString(KEY_LAST_BACKGROUND, null).orEmpty()
        set(value) {
            prefs.edit().putString(KEY_LAST_BACKGROUND, value).apply()
        }

    fun clearLastBackgroundSync() {
        prefs.edit().remove(KEY_LAST_BACKGROUND).apply()
    }

    private fun keyStore(): KeyStore = KeyStore.getInstance(KEYSTORE).apply { load(null) }

    private fun key(): SecretKey {
        (keyStore().getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        val spec = KeyGenParameterSpec.Builder(KEY_ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
            .build()
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE).apply { init(spec) }.generateKey()
    }

    private companion object {
        const val PREFS = "org.openhealthsync.reactnative"
        const val KEY_BASE_URL = "base_url"
        const val KEY_TOKEN = "user_token"
        const val KEY_LAST_BACKGROUND = "last_background_sync"
        const val KEYSTORE = "AndroidKeyStore"
        const val KEY_ALIAS = "org.openhealthsync.reactnative.credentials"
        const val TRANSFORMATION = "AES/GCM/NoPadding"
        const val IV_BYTES = 12
        const val TAG_BITS = 128
    }
}
