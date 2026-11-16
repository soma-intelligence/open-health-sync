// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk.healthconnect

import androidx.health.connect.client.records.ActiveCaloriesBurnedRecord
import androidx.health.connect.client.records.BasalMetabolicRateRecord
import androidx.health.connect.client.records.BloodGlucoseRecord
import androidx.health.connect.client.records.BloodPressureRecord
import androidx.health.connect.client.records.BodyFatRecord
import androidx.health.connect.client.records.BodyTemperatureMeasurementLocation
import androidx.health.connect.client.records.BodyTemperatureRecord
import androidx.health.connect.client.records.DistanceRecord
import androidx.health.connect.client.records.ExerciseSessionRecord
import androidx.health.connect.client.records.HeartRateRecord
import androidx.health.connect.client.records.HeartRateVariabilityRmssdRecord
import androidx.health.connect.client.records.HeightRecord
import androidx.health.connect.client.records.HydrationRecord
import androidx.health.connect.client.records.IntermenstrualBleedingRecord
import androidx.health.connect.client.records.MealType
import androidx.health.connect.client.records.MenstruationFlowRecord
import androidx.health.connect.client.records.NutritionRecord
import androidx.health.connect.client.records.OxygenSaturationRecord
import androidx.health.connect.client.records.Record
import androidx.health.connect.client.records.RespiratoryRateRecord
import androidx.health.connect.client.records.RestingHeartRateRecord
import androidx.health.connect.client.records.SleepSessionRecord
import androidx.health.connect.client.records.StepsRecord
import androidx.health.connect.client.records.TotalCaloriesBurnedRecord
import androidx.health.connect.client.records.Vo2MaxRecord
import androidx.health.connect.client.records.WeightRecord
import androidx.health.connect.client.records.metadata.Device
import androidx.health.connect.client.records.metadata.Metadata
import androidx.health.connect.client.units.BloodGlucose
import androidx.health.connect.client.units.Energy
import androidx.health.connect.client.units.Length
import androidx.health.connect.client.units.Mass
import androidx.health.connect.client.units.Percentage
import androidx.health.connect.client.units.Power
import androidx.health.connect.client.units.Pressure
import androidx.health.connect.client.units.Temperature
import androidx.health.connect.client.units.Volume
import java.lang.reflect.Modifier
import java.time.Instant
import java.time.ZoneId
import java.time.ZoneOffset
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive

private fun t(s: String): Instant = Instant.parse(s)
private val CET: ZoneOffset = ZoneOffset.ofHours(1)
private const val AUTO = Metadata.RECORDING_METHOD_AUTOMATICALLY_RECORDED
private const val MANUAL = Metadata.RECORDING_METHOD_MANUAL_ENTRY

/**
 * The records that Health Connect would return for the lines of the server's own fixture,
 * connectors/healthconnect/testdata/upload.ndjson, keyed by record id. The server tests map
 * that file (healthconnect_test.go), so a record that converts to its line, field for field,
 * is a record the server stores as those tests say.
 */
internal val fixtureRecords: Map<String, Record> = listOf(
    StepsRecord(
        startTime = t("2026-03-02T08:00:00Z"), startZoneOffset = CET, endTime = t("2026-03-02T09:00:00Z"), endZoneOffset = CET,
        count = 1234,
        metadata = meta(
            "st-1", "com.google.android.apps.fitness", AUTO, t("2026-03-02T10:00:00Z"),
            Device(type = Device.TYPE_PHONE, manufacturer = "Google", model = "Pixel 9"),
        ),
    ),
    HeartRateRecord(
        startTime = t("2026-03-02T08:00:00Z"), startZoneOffset = null, endTime = t("2026-03-02T08:02:00Z"), endZoneOffset = null,
        samples = listOf(
            HeartRateRecord.Sample(t("2026-03-02T08:00:00Z"), 61),
            HeartRateRecord.Sample(t("2026-03-02T08:01:00Z"), 63),
        ),
        metadata = meta("hr-1", "com.samsung.android.wear", AUTO),
    ),
    BloodGlucoseRecord(
        time = t("2026-03-02T07:30:00Z"), zoneOffset = CET, metadata = meta("bg-1", "com.freestylelibre.app.de", AUTO),
        level = BloodGlucose.millimolesPerLiter(5.6),
        specimenSource = BloodGlucoseRecord.SPECIMEN_SOURCE_INTERSTITIAL_FLUID,
        mealType = MealType.MEAL_TYPE_UNKNOWN,
        relationToMeal = BloodGlucoseRecord.RELATION_TO_MEAL_FASTING,
    ),
    BloodPressureRecord(
        time = t("2026-03-02T07:00:00Z"), zoneOffset = CET,
        metadata = meta(
            "bp-1", "jp.co.omron.healthcare.omron_connect", AUTO,
            device = Device(type = Device.TYPE_UNKNOWN, manufacturer = "OMRON", model = "M7"),
        ),
        systolic = Pressure.millimetersOfMercury(121.0), diastolic = Pressure.millimetersOfMercury(79.0),
        bodyPosition = BloodPressureRecord.BODY_POSITION_SITTING_DOWN,
        measurementLocation = BloodPressureRecord.MEASUREMENT_LOCATION_LEFT_UPPER_ARM,
    ),
    WeightRecord(
        time = t("2026-03-02T06:50:00Z"), zoneOffset = null, weight = Mass.kilograms(72.4),
        metadata = meta("w-1", "com.withings.wiscale2", MANUAL),
    ),
    HeightRecord(
        time = t("2026-03-01T06:50:00Z"), zoneOffset = null, height = Length.meters(1.81),
        metadata = meta("h-1", "com.google.android.apps.fitness"),
    ),
    SleepSessionRecord(
        startTime = t("2026-03-01T22:30:00Z"), startZoneOffset = CET, endTime = t("2026-03-02T06:30:00Z"), endZoneOffset = null,
        metadata = meta("sl-1", "com.ouraring.oura"),
        stages = listOf(
            SleepSessionRecord.Stage(t("2026-03-01T22:30:00Z"), t("2026-03-01T23:00:00Z"), SleepSessionRecord.STAGE_TYPE_AWAKE_IN_BED),
            SleepSessionRecord.Stage(t("2026-03-01T23:00:00Z"), t("2026-03-02T02:00:00Z"), SleepSessionRecord.STAGE_TYPE_LIGHT),
            SleepSessionRecord.Stage(t("2026-03-02T02:00:00Z"), t("2026-03-02T03:30:00Z"), SleepSessionRecord.STAGE_TYPE_DEEP),
            SleepSessionRecord.Stage(t("2026-03-02T03:30:00Z"), t("2026-03-02T05:00:00Z"), SleepSessionRecord.STAGE_TYPE_REM),
            SleepSessionRecord.Stage(t("2026-03-02T05:00:00Z"), t("2026-03-02T06:30:00Z"), SleepSessionRecord.STAGE_TYPE_OUT_OF_BED),
        ),
    ),
    ExerciseSessionRecord(
        startTime = t("2026-03-02T17:00:00Z"), startZoneOffset = null, endTime = t("2026-03-02T17:45:00Z"), endZoneOffset = null,
        metadata = meta("ex-1", "com.strava"),
        exerciseType = ExerciseSessionRecord.EXERCISE_TYPE_BIKING, title = "Evening ride",
    ),
    MenstruationFlowRecord(
        time = t("2026-03-01T23:30:00Z"), zoneOffset = CET, metadata = meta("mf-1", "com.example.cycles", MANUAL),
        flow = MenstruationFlowRecord.FLOW_LIGHT,
    ),
    MenstruationFlowRecord(
        time = t("2026-03-03T08:00:00Z"), zoneOffset = null, metadata = meta("mf-2", "com.example.cycles"),
        flow = MenstruationFlowRecord.FLOW_UNKNOWN,
    ),
    IntermenstrualBleedingRecord(time = t("2026-03-15T08:00:00Z"), zoneOffset = CET, metadata = meta("ib-1", "com.example.cycles")),
    HydrationRecord(
        startTime = t("2026-03-02T08:00:00Z"), startZoneOffset = CET, endTime = t("2026-03-02T08:01:00Z"), endZoneOffset = null,
        volume = Volume.liters(0.25), metadata = meta("hy-1", "com.example.water", MANUAL),
    ),
    NutritionRecord(
        startTime = t("2026-03-02T12:00:00Z"), startZoneOffset = CET, endTime = t("2026-03-02T12:30:00Z"), endZoneOffset = null,
        metadata = meta("nu-1", "com.example.food", MANUAL),
        name = "Lentil soup", mealType = MealType.MEAL_TYPE_LUNCH,
        energy = Energy.kilocalories(420.0), energyFromFat = Energy.kilocalories(90.0),
        protein = Mass.grams(24.0), totalCarbohydrate = Mass.grams(55.0), totalFat = Mass.grams(10.0),
        saturatedFat = Mass.grams(1.5), dietaryFiber = Mass.grams(16.0), sugar = Mass.grams(6.0),
        sodium = Mass.grams(0.8), caffeine = Mass.grams(0.0), iron = Mass.grams(0.0066),
        vitaminC = Mass.grams(0.012), vitaminD = Mass.grams(0.000002),
    ),
    TotalCaloriesBurnedRecord(
        startTime = t("2026-03-02T00:00:00Z"), startZoneOffset = CET, endTime = t("2026-03-02T01:00:00Z"), endZoneOffset = null,
        energy = Energy.kilocalories(85.5), metadata = meta("tc-1", "com.sec.android.app.shealth", AUTO),
    ),
    BasalMetabolicRateRecord(
        time = t("2026-03-02T06:50:00Z"), zoneOffset = CET, basalMetabolicRate = Power.kilocaloriesPerDay(1650.0),
        metadata = meta("bm-1", "com.withings.wiscale2", AUTO),
    ),
).associateBy { it.metadata.id }

/**
 * The record types the fixture has no line for. Their records and lines follow the shapes the
 * server's unit tests feed its mapper (connectors/healthconnect/coverage_test.go: instantLine
 * in TestMapInstantTypes and TestMapInstantOffsetsAndLocation, line in TestMapIntervalTypes).
 */
internal val coverageCases: List<Pair<Record, String>> = run {
    val at = t("2026-05-04T06:30:00Z")
    val est = ZoneOffset.ofHours(-5)
    fun instant(type: String, field: String) =
        """{"recordType":"$type","metadata":{"id":"id-$type","dataOrigin":"com.example.app"},"time":"2026-05-04T06:30:00Z","zoneOffset":"-05:00",$field}"""
    val start = t("2026-05-04T10:00:00Z")
    val end = t("2026-05-04T10:30:00Z")
    val garmin = "com.garmin.android.apps.connectmobile"
    fun interval(type: String, field: String) =
        """{"recordType":"$type","metadata":{"id":"iv-1","dataOrigin":"$garmin"},"startTime":"2026-05-04T10:00:00Z","endTime":"2026-05-04T10:30:00Z","startZoneOffset":"+01:00",$field}"""
    listOf(
        RestingHeartRateRecord(at, est, 54, meta("id-RestingHeartRate", "com.example.app")) to
            instant("RestingHeartRate", """"beatsPerMinute":54"""),
        HeartRateVariabilityRmssdRecord(at, est, 41.5, meta("id-HeartRateVariabilityRmssd", "com.example.app")) to
            instant("HeartRateVariabilityRmssd", """"heartRateVariabilityMillis":41.5"""),
        OxygenSaturationRecord(at, est, Percentage(97.0), meta("id-OxygenSaturation", "com.example.app")) to
            instant("OxygenSaturation", """"percentage":97"""),
        RespiratoryRateRecord(at, est, 14.5, meta("id-RespiratoryRate", "com.example.app")) to
            instant("RespiratoryRate", """"rate":14.5"""),
        BodyTemperatureRecord(at, est, meta("id-BodyTemperature", "com.example.app"), Temperature.celsius(36.6)) to
            instant("BodyTemperature", """"temperatureCelsius":36.6"""),
        BodyFatRecord(at, est, Percentage(21.3), meta("id-BodyFat", "com.example.app")) to
            instant("BodyFat", """"percentage":21.3"""),
        DistanceRecord(start, CET, end, null, Length.meters(4200.0), meta("iv-1", garmin)) to
            interval("Distance", """"distanceMeters":4200"""),
        ActiveCaloriesBurnedRecord(start, CET, end, null, Energy.kilocalories(180.5), meta("iv-1", garmin)) to
            interval("ActiveCaloriesBurned", """"energyKilocalories":180.5"""),
    )
}

class HealthConnectLinesTest {
    private val fixtureLines = fixtureText().lines().filter { it.isNotBlank() }.map(::parse)

    @Test
    fun fixtureIsTheServersOwn() {
        // Outside the repository the copy is all there is; inside, it must not drift.
        val server = repoFile("connectors/healthconnect/testdata/upload.ndjson") ?: return
        assertEquals(server.readText(Charsets.UTF_8), fixtureText(), "copy the server fixture again")
    }

    @Test
    fun everyFixtureLineIsReproducedFieldForField() {
        var compared = 0
        for (line in fixtureLines) {
            val type = line.getValue("recordType").jsonPrimitive.content
            if (type == "Deletion") {
                val id = line.getValue("recordId").jsonPrimitive.content
                assertEquals(canon(line), canon(HealthConnectLines.deletionLine(id)))
                compared++
                continue
            }
            val id = line.getValue("metadata").jsonObject.getValue("id").jsonPrimitive.content
            if (HealthConnectRecordType.fromWireName(type) == null) {
                // The fixture's line of a type the server does not store (Vo2Max).
                assertEquals("Vo2Max", type)
                continue
            }
            val record = assertNotNull(fixtureRecords[id], "no record for fixture line $id")
            val got = assertNotNull(HealthConnectLines.line(record), "no line for $id")
            assertEquals(canon(line), canon(got), "line of $id")
            compared++
        }
        assertEquals(fixtureRecords.size + 1, compared)
    }

    @Test
    fun typesWithoutAFixtureLineFollowTheServerTestShapes() {
        for ((record, want) in coverageCases) {
            val got = assertNotNull(HealthConnectLines.line(record))
            assertEquals(canon(parse(want)), canon(got), want)
        }
    }

    @Test
    fun everyRecordTypeHasAMappingTest() {
        val tested = (fixtureRecords.values + coverageCases.map { it.first }).mapNotNull(HealthConnectRecordType::of).toSet()
        assertEquals(HealthConnectRecordType.entries.toSet(), tested)
        for (type in HealthConnectRecordType.entries) {
            assertEquals(type.recordClass.simpleName, type.wireName + "Record")
            assertEquals(type, HealthConnectRecordType.fromWireName(type.wireName))
            assertTrue(type.readPermission.startsWith("android.permission.health.READ_"), type.readPermission)
        }
    }

    /** The record types the server maps, read from its mapper; the enum must be exactly those. */
    @Test
    fun typesAreExactlyTheOnesTheServerMaps() {
        val mapper = repoFile("connectors/healthconnect/mapper.go")?.readText(Charsets.UTF_8) ?: return
        val server = HashSet<String>()
        for (table in listOf("instants", "intervals")) {
            val body = mapper.substringAfter("var $table = map[string]instant{").substringBefore("\n}")
            Regex("^\\s*\"(\\w+)\":", RegexOption.MULTILINE).findAll(body).forEach { server += it.groupValues[1] }
        }
        Regex("r\\.RecordType == \"(\\w+)\"").findAll(mapper).forEach { server += it.groupValues[1] }
        assertTrue(server.remove("Deletion"), "the server no longer takes Deletion lines")
        assertEquals(server.sorted(), HealthConnectRecordType.entries.map { it.wireName }.sorted())
        // The mass fields of a Nutrition line, each sent as <field>Grams.
        val masses = mapper.substringAfter("var nutritionMasses = map[string]string{").substringBefore("\n}")
        val serverMasses = Regex("\"(\\w+)Grams\":").findAll(masses).map { it.groupValues[1] }.toSortedSet()
        assertEquals(40, serverMasses.size)
        val all = NutritionRecord(
            startTime = t("2026-03-02T12:00:00Z"), startZoneOffset = null, endTime = t("2026-03-02T12:30:00Z"), endZoneOffset = null,
            metadata = meta("nu-all"),
            biotin = Mass.grams(1.0), caffeine = Mass.grams(1.0), calcium = Mass.grams(1.0), chloride = Mass.grams(1.0),
            cholesterol = Mass.grams(1.0), chromium = Mass.grams(1.0), copper = Mass.grams(1.0), dietaryFiber = Mass.grams(1.0),
            folate = Mass.grams(1.0), folicAcid = Mass.grams(1.0), iodine = Mass.grams(1.0), iron = Mass.grams(1.0),
            magnesium = Mass.grams(1.0), manganese = Mass.grams(1.0), molybdenum = Mass.grams(1.0),
            monounsaturatedFat = Mass.grams(1.0), niacin = Mass.grams(1.0), pantothenicAcid = Mass.grams(1.0),
            phosphorus = Mass.grams(1.0), polyunsaturatedFat = Mass.grams(1.0), potassium = Mass.grams(1.0),
            protein = Mass.grams(1.0), riboflavin = Mass.grams(1.0), saturatedFat = Mass.grams(1.0), selenium = Mass.grams(1.0),
            sodium = Mass.grams(1.0), sugar = Mass.grams(1.0), thiamin = Mass.grams(1.0), totalCarbohydrate = Mass.grams(1.0),
            totalFat = Mass.grams(1.0), transFat = Mass.grams(1.0), unsaturatedFat = Mass.grams(1.0), vitaminA = Mass.grams(1.0),
            vitaminB12 = Mass.grams(1.0), vitaminB6 = Mass.grams(1.0), vitaminC = Mass.grams(1.0), vitaminD = Mass.grams(1.0),
            vitaminE = Mass.grams(1.0), vitaminK = Mass.grams(1.0), zinc = Mass.grams(1.0),
        )
        val sent = HealthConnectLines.line(all)!!.keys.filter { it.endsWith("Grams") }.map { it.removeSuffix("Grams") }.toSortedSet()
        assertEquals(serverMasses, sent)
    }

    @Test
    fun optionalFieldsAndUnknownEnums() {
        // measurementLocation is sent when known, as in TestMapInstantOffsetsAndLocation.
        val temp = BodyTemperatureRecord(
            t("2026-05-04T06:30:00Z"), ZoneOffset.ofHoursMinutes(5, 30), meta("bt-1"), Temperature.celsius(36.9),
            BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_FOREHEAD,
        )
        assertEquals(
            canon(parse("""{"recordType":"BodyTemperature","metadata":{"id":"bt-1"},"time":"2026-05-04T06:30:00Z","zoneOffset":"+05:30","temperatureCelsius":36.9,"measurementLocation":"forehead"}""")),
            canon(HealthConnectLines.line(temp)!!),
        )
        // An unknown body position or location is left out; UTC is written "Z", as the server reads it.
        val bp = BloodPressureRecord(
            t("2026-05-04T06:30:00Z"), ZoneOffset.UTC, meta("bp-2", clientRecordId = "cuff-7"),
            Pressure.millimetersOfMercury(118.0), Pressure.millimetersOfMercury(76.0),
        )
        assertEquals(
            canon(parse("""{"recordType":"BloodPressure","metadata":{"id":"bp-2","clientRecordId":"cuff-7"},"time":"2026-05-04T06:30:00Z","zoneOffset":"Z","systolicMillimetersOfMercury":118,"diastolicMillimetersOfMercury":76}""")),
            canon(HealthConnectLines.line(bp)!!),
        )
        // A nutrition record with no figure is still a meal: it goes up with its meal type alone.
        val meal = NutritionRecord(
            startTime = t("2026-03-02T12:00:00Z"), startZoneOffset = null, endTime = t("2026-03-02T12:30:00Z"), endZoneOffset = null,
            metadata = meta("nu-2"),
        )
        assertEquals(
            canon(parse("""{"recordType":"Nutrition","metadata":{"id":"nu-2"},"startTime":"2026-03-02T12:00:00Z","endTime":"2026-03-02T12:30:00Z","mealType":"unknown"}""")),
            canon(HealthConnectLines.line(meal)!!),
        )
    }

    @Test
    fun recordsTheServerWouldNotStoreGiveNoLine() {
        val at = t("2026-03-02T08:00:00Z")
        // A type outside the server's list.
        assertNull(HealthConnectLines.line(Vo2MaxRecord(at, null, meta("vo-1"), 45.0)))
        // No id: the server fails the line at processing.
        assertNull(HealthConnectLines.line(WeightRecord(at, null, Mass.kilograms(70.0), meta(""))))
    }

    @Test
    fun dayIsTheLocalDayOfTheRecord() {
        // 23:30 UTC at +01:00 is already the next day.
        assertEquals("2026-03-02", HealthConnectLines.day(fixtureRecords.getValue("mf-1"), ZoneId.of("UTC")))
        // No offset on the record: the given zone decides.
        val weight = fixtureRecords.getValue("w-1")
        assertEquals("2026-03-02", HealthConnectLines.day(weight, ZoneId.of("UTC")))
        assertEquals("2026-03-01", HealthConnectLines.day(weight, ZoneId.of("America/Los_Angeles")))
        // An interval is dated by its start.
        assertEquals("2026-03-01", HealthConnectLines.day(fixtureRecords.getValue("sl-1"), ZoneId.of("UTC")))
    }

    /**
     * Each enum table against the constants of the androidx class: the wire value is the
     * constant name without its prefix, in lower case (docs/connectors/health-connect.md).
     */
    @Test
    fun enumTablesCoverEveryAndroidxConstant() {
        fun check(owner: Class<*>, prefix: String, wire: (Int) -> String, min: Int) {
            val constants = owner.declaredFields.filter {
                Modifier.isStatic(it.modifiers) && it.type == Int::class.javaPrimitiveType && it.name.startsWith(prefix)
            }
            assertTrue(constants.size >= min, "${owner.simpleName} $prefix: ${constants.size} constants")
            for (c in constants) {
                c.isAccessible = true
                assertEquals(c.name.removePrefix(prefix).lowercase(), wire(c.getInt(null)), c.name)
            }
        }
        check(ExerciseSessionRecord::class.java, "EXERCISE_TYPE_", HealthConnectLines::exerciseType, 61)
        check(SleepSessionRecord::class.java, "STAGE_TYPE_", HealthConnectLines::sleepStage, 8)
        check(BloodGlucoseRecord::class.java, "SPECIMEN_SOURCE_", HealthConnectLines::specimenSource, 7)
        check(BloodGlucoseRecord::class.java, "RELATION_TO_MEAL_", HealthConnectLines::relationToMeal, 5)
        check(BloodPressureRecord::class.java, "BODY_POSITION_", HealthConnectLines::bodyPosition, 5)
        check(BloodPressureRecord::class.java, "MEASUREMENT_LOCATION_", HealthConnectLines::bloodPressureLocation, 5)
        check(BodyTemperatureMeasurementLocation::class.java, "MEASUREMENT_LOCATION_", HealthConnectLines::bodyTemperatureLocation, 11)
        check(MealType::class.java, "MEAL_TYPE_", HealthConnectLines::mealType, 5)
        check(MenstruationFlowRecord::class.java, "FLOW_", HealthConnectLines::menstruationFlow, 4)
        check(Device::class.java, "TYPE_", HealthConnectLines::deviceType, 9)
        // The recording method follows the server's documented values, not the constant names.
        assertNull(HealthConnectLines.recordingMethod(Metadata.RECORDING_METHOD_UNKNOWN))
        assertEquals("active", HealthConnectLines.recordingMethod(Metadata.RECORDING_METHOD_ACTIVELY_RECORDED))
        assertEquals("automatic", HealthConnectLines.recordingMethod(Metadata.RECORDING_METHOD_AUTOMATICALLY_RECORDED))
        assertEquals("manual", HealthConnectLines.recordingMethod(Metadata.RECORDING_METHOD_MANUAL_ENTRY))
    }
}
