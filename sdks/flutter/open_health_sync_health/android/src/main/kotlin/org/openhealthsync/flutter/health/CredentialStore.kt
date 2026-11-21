// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.flutter.health

import android.content.Context
import android.content.SharedPreferences
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Where to upload and with which user token, kept between runs because a background sync
 * starts in a process where no Dart code has run.
 *
 * The token is encrypted with an AES key that never leaves the Android Keystore; the
 * ciphertext and the base URL are in the app's private preferences. Nothing here is logged.
 */
internal class CredentialStore(context: Context) {
    private val prefs: SharedPreferences =
        context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** The base URL of the server; null when no credentials are stored. */
    val baseUrl: String? get() = prefs.getString(KEY_BASE_URL, null)

    /**
     * The user token; null when none is stored or it can no longer be decrypted (the
     * preferences were restored on another device, whose keystore has no such key).
     */
    fun token(): String? {
        val stored = prefs.getString(KEY_TOKEN, null) ?: return null
        return try {
            decrypt(stored)
        } catch (_: Exception) {
            null
        }
    }

    fun save(baseUrl: String, token: String) {
        prefs.edit().putString(KEY_BASE_URL, baseUrl).putString(KEY_TOKEN, encrypt(token)).apply()
    }

    fun clear() {
        prefs.edit().remove(KEY_BASE_URL).remove(KEY_TOKEN).apply()
        try {
            keyStore().deleteEntry(KEY_ALIAS)
        } catch (_: Exception) {
            // The key is of no use without the ciphertext, which is gone.
        }
    }

    private fun encrypt(token: String): String {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val sealed = cipher.doFinal(token.toByteArray(Charsets.UTF_8))
        val iv = cipher.iv
        return Base64.encodeToString(byteArrayOf(iv.size.toByte()) + iv + sealed, Base64.NO_WRAP)
    }

    private fun decrypt(stored: String): String {
        val bytes = Base64.decode(stored, Base64.NO_WRAP)
        val ivSize = bytes[0].toInt()
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, bytes, 1, ivSize))
        return String(cipher.doFinal(bytes, 1 + ivSize, bytes.size - 1 - ivSize), Charsets.UTF_8)
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
        const val PREFS = "org.openhealthsync.flutter.health"
        const val KEY_BASE_URL = "base_url"
        const val KEY_TOKEN = "user_token"
        const val KEY_ALIAS = "org.openhealthsync.flutter.health.token"
        const val KEYSTORE = "AndroidKeyStore"
        const val TRANSFORMATION = "AES/GCM/NoPadding"
        const val TAG_BITS = 128
    }
}
