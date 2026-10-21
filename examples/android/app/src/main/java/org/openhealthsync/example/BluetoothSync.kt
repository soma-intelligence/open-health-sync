// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.example

import android.annotation.SuppressLint
import android.bluetooth.BluetoothGatt
import android.bluetooth.BluetoothGattCallback
import android.bluetooth.BluetoothGattCharacteristic
import android.bluetooth.BluetoothGattDescriptor
import android.bluetooth.BluetoothManager
import android.bluetooth.BluetoothProfile
import android.bluetooth.BluetoothStatusCodes
import android.bluetooth.le.ScanCallback
import android.bluetooth.le.ScanFilter
import android.bluetooth.le.ScanResult
import android.bluetooth.le.ScanSettings
import android.content.Context
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelUuid
import java.time.Instant
import java.util.ArrayDeque
import java.util.UUID
import org.openhealthsync.sdk.BluetoothDevice
import org.openhealthsync.sdk.BluetoothPart
import org.openhealthsync.sdk.BluetoothRecorder
import org.openhealthsync.sdk.GattUuids

/**
 * Connects to standard Bluetooth health devices (heart rate straps, blood pressure cuffs,
 * scales, glucose meters, thermometers, pulse oximeters) with Android's BluetoothGatt and feeds
 * what they send to the SDK's [BluetoothRecorder]. For each device it reads Device Information,
 * sets the device clock when allowed, subscribes to every measurement, and asks a glucose meter
 * for the records after the last one recorded. [takePart] hands the values over for upload.
 *
 * Android runs one GATT operation at a time per device, so each connection keeps a queue.
 * Device addresses stay on the phone: the SDK's lines have no field for them.
 */
@SuppressLint("MissingPermission") // the activity asks for the Bluetooth permissions first
class BluetoothSync(context: Context, private val onEvent: (text: String, recorded: Int) -> Unit) {
    private val app = context.applicationContext
    private val adapter = app.getSystemService(BluetoothManager::class.java)?.adapter
    private val prefs = app.getSharedPreferences("bluetooth", Context.MODE_PRIVATE)
    private val main = Handler(Looper.getMainLooper())
    private val lock = Any()
    private val recorder = BluetoothRecorder()
    private val links = mutableMapOf<String, Link>()

    @Volatile
    private var running = false

    /** Values recorded and not yet taken. */
    val recorded: Int
        get() = synchronized(lock) { recorder.lineCount }

    /** Starts scanning for health devices; false when Bluetooth is off or missing. */
    fun start(): Boolean {
        val scanner = adapter?.takeIf { it.isEnabled }?.bluetoothLeScanner ?: return false
        running = true
        val filters = GattUuids.HEALTH_SERVICES.map { ScanFilter.Builder().setServiceUuid(ParcelUuid(it)).build() }
        val settings = ScanSettings.Builder().setScanMode(ScanSettings.SCAN_MODE_LOW_LATENCY).build()
        scanner.startScan(filters, settings, scanCallback)
        return true
    }

    fun stop() {
        running = false
        adapter?.bluetoothLeScanner?.stopScan(scanCallback)
        synchronized(lock) {
            links.values.forEach { it.gatt?.close() }
            links.clear()
        }
    }

    /** The recorded values as one part to upload; null when there are none. */
    fun takePart(): BluetoothPart? = synchronized(lock) { recorder.makePart() }

    /** Tells the screen, on the main thread, with the number of values recorded so far. */
    private fun say(text: String) {
        val count = recorded
        main.post { onEvent(text, count) }
    }

    private val scanCallback = object : ScanCallback() {
        override fun onScanResult(callbackType: Int, result: ScanResult) {
            val device = result.device
            synchronized(lock) {
                if (!running || links.containsKey(device.address)) return
                val name = result.scanRecord?.deviceName ?: device.name ?: "unnamed device"
                val link = Link(device.address, name)
                links[device.address] = link
                // autoConnect: Android connects whenever the device is in range.
                link.gatt = device.connectGatt(app, true, link, android.bluetooth.BluetoothDevice.TRANSPORT_LE)
            }
        }
    }

    private inner class Link(private val address: String, name: String) : BluetoothGattCallback() {
        @Volatile
        var gatt: BluetoothGatt? = null

        @Volatile
        private var device = BluetoothDevice(name = GattUuids.publicName(name))

        @Volatile
        private var session = BluetoothRecorder.newSession()

        private val ops = ArrayDeque<(BluetoothGatt) -> Boolean>()
        private var busy = false

        override fun onConnectionStateChange(g: BluetoothGatt, status: Int, newState: Int) {
            when (newState) {
                BluetoothProfile.STATE_CONNECTED -> {
                    session = BluetoothRecorder.newSession()
                    synchronized(ops) {
                        ops.clear()
                        busy = false
                    }
                    g.discoverServices()
                }
                BluetoothProfile.STATE_DISCONNECTED -> {
                    say("${device.name} disconnected")
                    if (running) g.connect()
                }
            }
        }

        override fun onServicesDiscovered(g: BluetoothGatt, status: Int) {
            if (status != BluetoothGatt.GATT_SUCCESS) {
                say("${device.name}: service discovery failed (status $status)")
                return
            }
            val chars = g.services.flatMap { it.characteristics }
            fun find(uuid: UUID) = chars.firstOrNull { it.uuid == uuid }
            fun can(c: BluetoothGattCharacteristic, property: Int) = c.properties and property != 0

            // Device Information first, so every line carries it.
            for (u in listOf(GattUuids.MANUFACTURER_NAME, GattUuids.MODEL_NUMBER, GattUuids.FIRMWARE_REVISION)) {
                find(u)?.takeIf { can(it, BluetoothGattCharacteristic.PROPERTY_READ) }?.let { c ->
                    enqueue { it.readCharacteristic(c) }
                }
            }
            find(GattUuids.CURRENT_TIME)?.takeIf { can(it, BluetoothGattCharacteristic.PROPERTY_WRITE) }?.let { c ->
                enqueue { write(it, c, GattUuids.currentTime(Instant.now())) }
            }
            enqueue {
                say("${device.name} connected")
                false
            }
            find(GattUuids.BATTERY_LEVEL)?.let { c ->
                if (can(c, BluetoothGattCharacteristic.PROPERTY_READ)) enqueue { it.readCharacteristic(c) }
                subscribe(c)
            }
            for (u in GattUuids.MEASUREMENTS) find(u)?.let { subscribe(it) }
            // Glucose: ask for the records after the last one recorded, all of them the first time.
            if (find(GattUuids.GLUCOSE_MEASUREMENT) != null) {
                find(GattUuids.RECORD_ACCESS_CONTROL_POINT)?.let { c ->
                    subscribe(c)
                    val last = prefs.getInt(glucoseKey(), -1).takeIf { it >= 0 }
                    enqueue { write(it, c, GattUuids.reportStoredRecords(last)) }
                }
            }
            next(g)
        }

        private fun subscribe(c: BluetoothGattCharacteristic) {
            val notify = c.properties and BluetoothGattCharacteristic.PROPERTY_NOTIFY != 0
            val indicate = c.properties and BluetoothGattCharacteristic.PROPERTY_INDICATE != 0
            if (!notify && !indicate) return
            enqueue { g ->
                g.setCharacteristicNotification(c, true)
                val d = c.getDescriptor(GattUuids.CLIENT_CHARACTERISTIC_CONFIG) ?: return@enqueue false
                val value = if (notify) BluetoothGattDescriptor.ENABLE_NOTIFICATION_VALUE else BluetoothGattDescriptor.ENABLE_INDICATION_VALUE
                writeDescriptor(g, d, value)
            }
        }

        override fun onCharacteristicRead(g: BluetoothGatt, c: BluetoothGattCharacteristic, value: ByteArray, status: Int) {
            if (status == BluetoothGatt.GATT_SUCCESS) received(c, value)
            done(g)
        }

        @Deprecated("Android 12 and earlier")
        override fun onCharacteristicRead(g: BluetoothGatt, c: BluetoothGattCharacteristic, status: Int) {
            if (Build.VERSION.SDK_INT >= 33) return // the call above handled it
            @Suppress("DEPRECATION")
            onCharacteristicRead(g, c, c.value ?: ByteArray(0), status)
        }

        override fun onCharacteristicChanged(g: BluetoothGatt, c: BluetoothGattCharacteristic, value: ByteArray) {
            received(c, value)
        }

        @Deprecated("Android 12 and earlier")
        override fun onCharacteristicChanged(g: BluetoothGatt, c: BluetoothGattCharacteristic) {
            if (Build.VERSION.SDK_INT >= 33) return
            @Suppress("DEPRECATION")
            received(c, c.value ?: ByteArray(0))
        }

        override fun onCharacteristicWrite(g: BluetoothGatt, c: BluetoothGattCharacteristic, status: Int) {
            done(g)
        }

        override fun onDescriptorWrite(g: BluetoothGatt, d: BluetoothGattDescriptor, status: Int) {
            if (status != BluetoothGatt.GATT_SUCCESS && d.characteristic.uuid in GattUuids.MEASUREMENTS) {
                say("${device.name}: cannot subscribe (status $status), pair the device first")
            }
            done(g)
        }

        private fun received(c: BluetoothGattCharacteristic, value: ByteArray) {
            when (c.uuid) {
                GattUuids.MANUFACTURER_NAME -> device = device.copy(manufacturer = GattUuids.deviceText(value))
                GattUuids.MODEL_NUMBER -> device = device.copy(model = GattUuids.deviceText(value))
                GattUuids.FIRMWARE_REVISION -> device = device.copy(firmware = GattUuids.deviceText(value))
                GattUuids.BATTERY_LEVEL -> value.firstOrNull()?.let { say("${device.name}: battery ${it.toInt() and 0xff}%") }
                else -> {
                    val stored = try {
                        synchronized(lock) { recorder.record(c.uuid, value, session, device) }
                    } catch (e: IllegalArgumentException) {
                        false
                    }
                    if (!stored) return
                    if (c.uuid == GattUuids.GLUCOSE_MEASUREMENT) {
                        GattUuids.glucoseSequenceNumber(value)?.let { prefs.edit().putInt(glucoseKey(), it).apply() }
                    }
                    say("${device.name}: ${describe(c.uuid, value)}")
                }
            }
        }

        private fun glucoseKey() = "glucose.$address"

        private fun enqueue(op: (BluetoothGatt) -> Boolean) {
            synchronized(ops) { ops.addLast(op) }
        }

        /** Runs queued operations until one waits for its callback. */
        private fun next(g: BluetoothGatt) {
            while (true) {
                val op = synchronized(ops) {
                    if (busy) return
                    ops.pollFirst()?.also { busy = true }
                } ?: return
                val waiting = try {
                    op(g)
                } catch (e: SecurityException) {
                    false
                }
                if (waiting) return
                synchronized(ops) { busy = false }
            }
        }

        private fun done(g: BluetoothGatt) {
            synchronized(ops) { busy = false }
            next(g)
        }
    }

    private companion object {
        @Suppress("DEPRECATION")
        fun write(g: BluetoothGatt, c: BluetoothGattCharacteristic, value: ByteArray): Boolean =
            if (Build.VERSION.SDK_INT >= 33) {
                g.writeCharacteristic(c, value, BluetoothGattCharacteristic.WRITE_TYPE_DEFAULT) == BluetoothStatusCodes.SUCCESS
            } else {
                c.writeType = BluetoothGattCharacteristic.WRITE_TYPE_DEFAULT
                c.value = value
                g.writeCharacteristic(c)
            }

        @Suppress("DEPRECATION")
        fun writeDescriptor(g: BluetoothGatt, d: BluetoothGattDescriptor, value: ByteArray): Boolean =
            if (Build.VERSION.SDK_INT >= 33) {
                g.writeDescriptor(d, value) == BluetoothStatusCodes.SUCCESS
            } else {
                d.value = value
                g.writeDescriptor(d)
            }

        /** A short live text: heart rate in bpm (the server decodes everything for storage). */
        fun describe(uuid: UUID, value: ByteArray): String {
            if (uuid == GattUuids.HEART_RATE_MEASUREMENT && value.size >= 2) {
                val sixteenBit = value[0].toInt() and 0x01 != 0
                val bpm = if (sixteenBit && value.size >= 3) {
                    (value[1].toInt() and 0xff) or ((value[2].toInt() and 0xff) shl 8)
                } else {
                    value[1].toInt() and 0xff
                }
                return "$bpm bpm"
            }
            return "value received"
        }
    }
}
