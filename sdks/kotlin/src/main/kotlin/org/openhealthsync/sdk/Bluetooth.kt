// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk

import java.io.ByteArrayOutputStream
import java.time.Instant
import java.time.ZoneId
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import java.util.UUID
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * The Bluetooth SIG numbers of the standard health profiles, and the few bytes a client writes
 * to a device. Values the device sends are not decoded here: they go up as the bytes it sent and
 * the server decodes them (docs/connectors/bluetooth.md).
 */
public object GattUuids {
    // Services.
    public val HEART_RATE_SERVICE: UUID = uuid16(0x180d)
    public val BLOOD_PRESSURE_SERVICE: UUID = uuid16(0x1810)
    public val WEIGHT_SCALE_SERVICE: UUID = uuid16(0x181d)
    public val BODY_COMPOSITION_SERVICE: UUID = uuid16(0x181b)
    public val GLUCOSE_SERVICE: UUID = uuid16(0x1808)
    public val HEALTH_THERMOMETER_SERVICE: UUID = uuid16(0x1809)
    public val PULSE_OXIMETER_SERVICE: UUID = uuid16(0x1822)
    public val DEVICE_INFORMATION_SERVICE: UUID = uuid16(0x180a)
    public val BATTERY_SERVICE: UUID = uuid16(0x180f)
    public val CURRENT_TIME_SERVICE: UUID = uuid16(0x1805)

    /** The health services to scan for. */
    public val HEALTH_SERVICES: List<UUID> = listOf(
        HEART_RATE_SERVICE, BLOOD_PRESSURE_SERVICE, WEIGHT_SCALE_SERVICE, BODY_COMPOSITION_SERVICE,
        GLUCOSE_SERVICE, HEALTH_THERMOMETER_SERVICE, PULSE_OXIMETER_SERVICE,
    )

    // Characteristics.
    public val HEART_RATE_MEASUREMENT: UUID = uuid16(0x2a37)
    public val BLOOD_PRESSURE_MEASUREMENT: UUID = uuid16(0x2a35)
    public val WEIGHT_MEASUREMENT: UUID = uuid16(0x2a9d)
    public val BODY_COMPOSITION_MEASUREMENT: UUID = uuid16(0x2a9c)
    public val GLUCOSE_MEASUREMENT: UUID = uuid16(0x2a18)
    public val TEMPERATURE_MEASUREMENT: UUID = uuid16(0x2a1c)
    public val PLX_SPOT_CHECK_MEASUREMENT: UUID = uuid16(0x2a5e)
    public val PLX_CONTINUOUS_MEASUREMENT: UUID = uuid16(0x2a5f)
    public val RECORD_ACCESS_CONTROL_POINT: UUID = uuid16(0x2a52)
    public val BATTERY_LEVEL: UUID = uuid16(0x2a19)
    public val CURRENT_TIME: UUID = uuid16(0x2a2b)
    public val MANUFACTURER_NAME: UUID = uuid16(0x2a29)
    public val MODEL_NUMBER: UUID = uuid16(0x2a24)
    public val FIRMWARE_REVISION: UUID = uuid16(0x2a26)

    /** The Client Characteristic Configuration descriptor, written to subscribe on Android. */
    public val CLIENT_CHARACTERISTIC_CONFIG: UUID = uuid16(0x2902)

    /** The characteristics whose values are uploaded: subscribe to these. */
    public val MEASUREMENTS: List<UUID> = listOf(
        HEART_RATE_MEASUREMENT, BLOOD_PRESSURE_MEASUREMENT, WEIGHT_MEASUREMENT,
        BODY_COMPOSITION_MEASUREMENT, GLUCOSE_MEASUREMENT, TEMPERATURE_MEASUREMENT,
        PLX_SPOT_CHECK_MEASUREMENT, PLX_CONTINUOUS_MEASUREMENT,
    )

    private const val BASE_LOW = -0x7fffff7fa064cb05L // 8000-00805f9b34fb
    private const val BASE_HIGH_LOW = 0x1000L // -0000-1000

    /** The full UUID of a 16-bit Bluetooth SIG number (the Bluetooth base UUID). */
    public fun uuid16(short: Int): UUID = UUID((short.toLong() shl 32) or BASE_HIGH_LOW, BASE_LOW)

    /** The 16-bit number of a Bluetooth base UUID, null for any other UUID. */
    public fun shortOf(uuid: UUID): Int? =
        if (uuid.leastSignificantBits == BASE_LOW && (uuid.mostSignificantBits and 0xffff0000ffffffffuL.toLong()) == BASE_HIGH_LOW) {
            (uuid.mostSignificantBits ushr 32).toInt()
        } else {
            null
        }

    /**
     * The Record Access Control Point request for a glucose meter: report all stored records
     * ([after] null), or those whose sequence number is at least [after] + 1.
     */
    public fun reportStoredRecords(after: Int?): ByteArray {
        if (after == null || after !in 0 until 0xffff) return byteArrayOf(0x01, 0x01)
        val from = after + 1
        return byteArrayOf(0x01, 0x03, 0x01, (from and 0xff).toByte(), (from shr 8).toByte())
    }

    /** The sequence number of a Glucose Measurement value. */
    public fun glucoseSequenceNumber(value: ByteArray): Int? =
        if (value.size < 3) null else (value[1].toInt() and 0xff) or ((value[2].toInt() and 0xff) shl 8)

    /**
     * The Current Time value for [instant] as wall time in [zone] (a device clock has no zone, so
     * it is set to the local time, as the device's own app does).
     */
    public fun currentTime(instant: Instant, zone: ZoneId = ZoneId.systemDefault()): ByteArray {
        val t = instant.atZone(zone)
        return byteArrayOf(
            (t.year and 0xff).toByte(), (t.year shr 8).toByte(), t.monthValue.toByte(), t.dayOfMonth.toByte(),
            t.hour.toByte(), t.minute.toByte(), t.second.toByte(), t.dayOfWeek.value.toByte(),
            (t.nano.toLong() * 256 / 1_000_000_000).toInt().toByte(),
            0x01, // adjust reason: manual time update
        )
    }

    /**
     * An advertised name without a last word that looks like a serial number: hex digits after a
     * space or an underscore, at least six, or at least four with a letter among them. "Polar H10
     * 0A1B2C3D" is sent as "Polar H10"; "Edge 1040" is kept.
     */
    public fun publicName(name: String): String {
        val trimmed = name.trim()
        val cut = trimmed.lastIndexOfAny(charArrayOf(' ', '_'))
        if (cut <= 0) return trimmed
        val tail = trimmed.substring(cut + 1)
        val hex = tail.isNotEmpty() && tail.all { it in '0'..'9' || it in 'a'..'f' || it in 'A'..'F' }
        val letter = tail.any { it.isLetter() }
        return if (hex && (tail.length >= 6 || (tail.length >= 4 && letter))) trimmed.substring(0, cut).trim() else trimmed
    }

    /**
     * A device string as a device sends it (UTF-8, often padded with NULs), cleaned: no control
     * characters, trimmed, at most 128 characters.
     */
    public fun deviceText(raw: ByteArray): String =
        String(raw, Charsets.UTF_8).filterNot { it.isISOControl() }.trim().take(128).trim()
}

/** What a device says about itself: never an address or a serial number. */
public data class BluetoothDevice(
    /** The advertised name, without a serial-like last word ([GattUuids.publicName]). */
    val name: String = "",
    val manufacturer: String = "",
    val model: String = "",
    val firmware: String = "",
)

/**
 * One upload part for `POST /v1/ingest/bluetooth`: gzip NDJSON, one line per value. Keep it until
 * the server answers 200, and send the same bytes again on a retry: the server then answers
 * `duplicate: true`.
 */
public class BluetoothPart(
    /** The UTC day of the first line, sent as `X-OHS-Day`. */
    public val day: String,
    /** Sent as `X-OHS-Batch`. */
    public val batch: String,
    /** Sent as `X-OHS-Part`. */
    public val partNumber: Int,
    /** gzip of the NDJSON lines. */
    public val body: ByteArray,
    public val lines: Int,
) {
    override fun toString(): String = "BluetoothPart(day=$day, batch=$batch, part=$partNumber, lines=$lines)"

    public companion object {
        /** `X-OHS-Type` of every part. */
        public const val TYPE: String = "gatt"
    }
}

/**
 * Collects the values devices send, as upload lines, and turns them into parts. Feed it from
 * Android's `BluetoothGattCallback` (see examples/android) or anything that receives
 * characteristic values. It does not decode them. Not thread safe: call it from one thread.
 *
 * @param deviceUser this person's user index (0 to 254) on a shared scale or cuff: readings of
 *   other users are then not stored.
 */
public class BluetoothRecorder(public val deviceUser: Int? = null) {
    private val buf = ByteArrayOutputStream()
    private var first: Instant? = null

    /** Values recorded since the last part. */
    public var lineCount: Int = 0
        private set

    init {
        require(deviceUser == null || deviceUser in 0..254) { "deviceUser must be 0 to 254" }
    }

    /**
     * Records one value as it arrived. Values of characteristics that are not uploaded (battery
     * level, a vendor service) are ignored: the result is false.
     *
     * @param session one id per connection to one device ([newSession]).
     * @param at when the value arrived, written with the UTC offset of [zone].
     */
    public fun record(
        characteristic: UUID,
        value: ByteArray,
        session: String,
        device: BluetoothDevice? = null,
        at: Instant = Instant.now(),
        zone: ZoneId = ZoneId.systemDefault(),
    ): Boolean {
        val short = GattUuids.shortOf(characteristic)
        if (short == null || characteristic !in GattUuids.MEASUREMENTS) return false
        require(value.size in 1..MAX_VALUE_BYTES) { "a value holds 1 to $MAX_VALUE_BYTES bytes" }
        require(SESSION.matches(session)) { "session must be 8 to 64 letters, digits, '_' or '-'" }
        val line = buildJsonObject {
            put("at", AT.format(at.atZone(zone)))
            put("char", "%04x".format(short))
            put("value", value.joinToString("") { "%02x".format(it.toInt() and 0xff) })
            put("session", session)
            if (device != null && device != BluetoothDevice()) {
                put("device", buildJsonObject {
                    for ((k, v) in listOf("name" to device.name, "manufacturer" to device.manufacturer, "model" to device.model, "firmware" to device.firmware)) {
                        if (v.isNotEmpty()) put(k, v.take(128))
                    }
                })
            }
            if (deviceUser != null) put("device_user", deviceUser)
        }
        buf.write((json.encodeToString(JsonObject.serializer(), line) + "\n").toByteArray(Charsets.UTF_8))
        lineCount++
        if (first == null) first = at
        return true
    }

    /** The recorded lines as one part, and an empty recorder; null when nothing was recorded. */
    public fun makePart(batch: String = newBatchId()): BluetoothPart? {
        require(BATCH.matches(batch)) { "batch must be 1 to 128 letters, digits, '_' or '-'" }
        val start = first ?: return null
        val part = BluetoothPart(
            AppleHealthPartBuilder.localDay(start, ZoneOffset.UTC), batch, 0,
            AppleHealthPartBuilder.gzip(buf.toByteArray()), lineCount,
        )
        buf.reset()
        lineCount = 0
        first = null
        return part
    }

    public companion object {
        /** The longest value a line may carry, the ATT maximum attribute length. */
        public const val MAX_VALUE_BYTES: Int = 512

        private val SESSION = Regex("^[A-Za-z0-9_-]{8,64}$")
        private val BATCH = Regex("^[A-Za-z0-9_-]{1,128}$")
        private val AT = DateTimeFormatter.ofPattern("uuuu-MM-dd'T'HH:mm:ss.SSSxxx")
        private val json = Json { prettyPrint = false }

        /** A new session id: 16 hex digits. */
        public fun newSession(): String = UUID.randomUUID().toString().replace("-", "").take(16)

        /** A fresh batch id: `bt1-` and 16 random characters. */
        public fun newBatchId(): String = AppleHealthPartBuilder.newBatchId("bt1")
    }
}
