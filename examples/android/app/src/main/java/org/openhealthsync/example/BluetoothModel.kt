// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.example

import android.app.Application
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.AndroidViewModel
import org.openhealthsync.sdk.BluetoothPart
import org.openhealthsync.sdk.OhsClient

/**
 * The Bluetooth card's state. It lives in a view model so that the connections and the values
 * not yet uploaded survive a screen rotation.
 */
class BluetoothModel(app: Application) : AndroidViewModel(app) {
    /** The last thing a device sent or said, for the live line. */
    var live by mutableStateOf("")
        private set

    /** Values recorded and not yet taken for upload. */
    var recorded by mutableIntStateOf(0)
        private set

    var scanning by mutableStateOf(false)
        private set

    var state by mutableStateOf<UploadState>(UploadState.Idle)
        private set

    /** A part taken for upload and not stored yet: the next upload sends the same bytes again. */
    private var pending by mutableStateOf<BluetoothPart?>(null)

    private val sync = BluetoothSync(app) { text, count ->
        live = text
        recorded = count
    }

    val canUpload: Boolean
        get() = recorded > 0 || pending != null

    fun start() {
        scanning = sync.start()
        live = if (scanning) "Looking for devices" else "Turn Bluetooth on first."
    }

    fun stop() {
        sync.stop()
        scanning = false
        live = ""
    }

    fun refused() {
        live = "The app needs the Bluetooth permission to find devices."
    }

    /** Uploads the recorded values. A part that was not stored stays pending for the next tap. */
    suspend fun upload(serverUrl: String, token: String) {
        val part = pending ?: sync.takePart() ?: return
        pending = part
        recorded = sync.recorded
        state = UploadState.Running
        val result = uploadState {
            val stored = OhsClient(serverUrl, token).uploadBluetoothPart(part)
            listOf(PartOutcome(part.partNumber, BluetoothPart.TYPE, part.day, stored.duplicate))
        }
        if (result is UploadState.Done) pending = null
        state = result
    }

    override fun onCleared() {
        sync.stop()
    }
}
