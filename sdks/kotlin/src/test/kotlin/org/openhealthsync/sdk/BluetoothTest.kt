// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk

import java.time.Instant
import java.time.ZoneOffset
import java.util.UUID
import java.util.zip.GZIPInputStream
import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json

class BluetoothTest {
    private val arrived = Instant.parse("2026-10-16T19:04:05.120Z")
    private val paris = ZoneOffset.ofHours(2)

    private fun gunzip(b: ByteArray) = String(GZIPInputStream(b.inputStream()).readBytes())

    private fun bytes(vararg v: Int) = ByteArray(v.size) { v[it].toByte() }

    @Test
    fun lineMatchesTheUploadContract() {
        val r = BluetoothRecorder()
        val strap = BluetoothDevice("Polar H10", "Polar Electro Oy", "H10", "5.0.0")
        assertTrue(r.record(GattUuids.HEART_RATE_MEASUREMENT, bytes(0x10, 0x48, 0x00, 0x04, 0x33, 0x03, 0x00, 0x04), "9f2c4e1a7b3d5c60", strap, arrived, paris))
        val part = r.makePart("bt1-test")!!
        assertEquals(0, r.lineCount)
        assertEquals("2026-10-16", part.day)
        assertEquals("bt1-test", part.batch)
        assertEquals(0, part.partNumber)
        assertEquals(1, part.lines)
        val ndjson = gunzip(part.body)
        assertTrue(ndjson.endsWith("\n"))
        assertEquals(
            Json.parseToJsonElement(
                """{"at":"2026-10-16T21:04:05.120+02:00","char":"2a37","value":"1048000433030004","session":"9f2c4e1a7b3d5c60",""" +
                    """"device":{"name":"Polar H10","manufacturer":"Polar Electro Oy","model":"H10","firmware":"5.0.0"}}""",
            ),
            Json.parseToJsonElement(ndjson.trim()),
        )
    }

    @Test
    fun deviceUserAndTheUtcDayOfTheFirstLine() {
        val r = BluetoothRecorder(deviceUser = 1)
        val at = Instant.parse("2026-10-17T03:00:00Z")
        r.record(GattUuids.BLOOD_PRESSURE_MEASUREMENT, bytes(0x00), "abcdefgh", at = at, zone = ZoneOffset.ofHours(-5))
        r.record(GattUuids.WEIGHT_MEASUREMENT, bytes(0x00, 0x10, 0x27), "abcdefgh", at = at.plusSeconds(86_400), zone = ZoneOffset.UTC)
        val part = r.makePart("bt1-x")!!
        assertEquals("2026-10-17", part.day)
        assertEquals(2, part.lines)
        val lines = gunzip(part.body).trim().split("\n")
        assertEquals(
            Json.parseToJsonElement("""{"at":"2026-10-16T22:00:00.000-05:00","char":"2a35","value":"00","session":"abcdefgh","device_user":1}"""),
            Json.parseToJsonElement(lines[0]),
        )
        assertTrue(lines[1].contains(""""at":"2026-10-18T03:00:00.000+00:00""""), lines[1])
        assertNull(r.makePart())
    }

    @Test
    fun valuesThatAreNotUploaded() {
        val r = BluetoothRecorder()
        assertFalse(r.record(GattUuids.BATTERY_LEVEL, bytes(80), "abcdefgh"))
        assertFalse(r.record(UUID.fromString("6a4e2401-667b-11e3-949a-0800200c9a66"), bytes(1), "abcdefgh"))
        assertEquals(0, r.lineCount)
        assertFailsWith<IllegalArgumentException> { r.record(GattUuids.HEART_RATE_MEASUREMENT, ByteArray(0), "abcdefgh") }
        assertFailsWith<IllegalArgumentException> { r.record(GattUuids.HEART_RATE_MEASUREMENT, ByteArray(513), "abcdefgh") }
        assertFailsWith<IllegalArgumentException> { r.record(GattUuids.HEART_RATE_MEASUREMENT, bytes(0), "short") }
        assertFailsWith<IllegalArgumentException> { BluetoothRecorder(deviceUser = 255) }
        assertFailsWith<IllegalArgumentException> { r.makePart("bad batch") }
    }

    @Test
    fun uuids() {
        assertEquals(UUID.fromString("00002a37-0000-1000-8000-00805f9b34fb"), GattUuids.HEART_RATE_MEASUREMENT)
        assertEquals(0x180d, GattUuids.shortOf(GattUuids.HEART_RATE_SERVICE))
        assertNull(GattUuids.shortOf(UUID.fromString("6a4e2401-667b-11e3-949a-0800200c9a66")))
        assertEquals(16, BluetoothRecorder.newSession().length)
        assertTrue(BluetoothRecorder.newBatchId().startsWith("bt1-"))
    }

    @Test
    fun recordAccessControlPoint() {
        assertContentEquals(bytes(0x01, 0x01), GattUuids.reportStoredRecords(null))
        assertContentEquals(bytes(0x01, 0x03, 0x01, 42, 0), GattUuids.reportStoredRecords(41))
        assertContentEquals(bytes(0x01, 0x03, 0x01, 0x00, 0x02), GattUuids.reportStoredRecords(0x01ff))
        assertContentEquals(bytes(0x01, 0x01), GattUuids.reportStoredRecords(0xffff))
        assertEquals(42, GattUuids.glucoseSequenceNumber(bytes(0x02, 0x2a, 0x00, 0xea)))
        assertNull(GattUuids.glucoseSequenceNumber(bytes(0x02, 0x2a)))
    }

    @Test
    fun currentTime() {
        // Friday 2026-10-16 21:04:05.5 in UTC+2.
        assertContentEquals(
            bytes(0xea, 0x07, 10, 16, 21, 4, 5, 5, 128, 0x01),
            GattUuids.currentTime(Instant.parse("2026-10-16T19:04:05.500Z"), paris),
        )
    }

    @Test
    fun namesAndDeviceText() {
        assertEquals("Polar H10", GattUuids.publicName("Polar H10 0A1B2C3D"))
        assertEquals("BLEsmart", GattUuids.publicName("BLEsmart_00000397"))
        assertEquals("TICKR", GattUuids.publicName("TICKR 1A2B"))
        assertEquals("Edge 1040", GattUuids.publicName("Edge 1040"))
        assertEquals("HRM-Pro", GattUuids.publicName("HRM-Pro"))
        assertEquals("OMRON HEALTHCARE", GattUuids.deviceText("OMRON HEALTHCARE\u0000\u0000".toByteArray()))
        assertEquals("H10", GattUuids.deviceText("  H10\r\n".toByteArray()))
        assertEquals(128, GattUuids.deviceText("é".repeat(200).toByteArray()).length)
    }

    @Test
    fun uploadsToTheBluetoothRoute() = runTest {
        val t = object : HttpTransport {
            val requests = mutableListOf<HttpRequest>()
            override fun send(request: HttpRequest): HttpResponse {
                requests += request
                return HttpResponse(200, emptyMap(), """{"key":"k","duplicate":true}""".toByteArray())
            }
        }
        val r = BluetoothRecorder()
        r.record(GattUuids.HEART_RATE_MEASUREMENT, bytes(0x00, 72), "abcdefgh", at = arrived, zone = paris)
        val part = r.makePart("bt1-test")!!
        val res = OhsClient("https://ohs.example.org", { "ohs_ut_test" }, t).uploadBluetoothPart(part)
        assertTrue(res.duplicate)
        val req = t.requests.single()
        assertEquals("POST", req.method)
        assertEquals("https://ohs.example.org/v1/ingest/bluetooth", req.url)
        assertEquals("gatt", req.headers["X-OHS-Type"])
        assertEquals("2026-10-16", req.headers["X-OHS-Day"])
        assertEquals("bt1-test", req.headers["X-OHS-Batch"])
        assertEquals("0", req.headers["X-OHS-Part"])
        assertEquals("application/gzip", req.headers["Content-Type"])
        assertContentEquals(part.body, req.body)
    }
}
