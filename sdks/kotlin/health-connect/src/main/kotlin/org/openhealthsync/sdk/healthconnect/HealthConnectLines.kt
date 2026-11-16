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
import androidx.health.connect.client.records.WeightRecord
import androidx.health.connect.client.records.metadata.Device
import androidx.health.connect.client.records.metadata.Metadata
import androidx.health.connect.client.units.Mass
import java.time.Instant
import java.time.ZoneId
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonObjectBuilder
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import org.openhealthsync.sdk.HealthConnectPartBuilder

/**
 * Converts Health Connect records into the upload lines of `POST /v1/ingest/health-connect`
 * (docs/connectors/health-connect.md#line-format): field names follow the androidx record
 * classes with the unit in the name, times are `Instant.toString()`, enum values are the
 * constant name without its prefix, in lower case.
 */
public object HealthConnectLines {
    /**
     * The upload line of [record]. Null for a record the server would not store or would refuse:
     * a class outside [HealthConnectRecordType], a record without an id, or one holding a value
     * that is not a finite number (JSON has no NaN).
     */
    public fun line(record: Record): JsonObject? {
        val type = HealthConnectRecordType.of(record) ?: return null
        if (record.metadata.id.isEmpty() || timing(record) == null) return null
        var finite = true
        fun JsonObjectBuilder.number(name: String, value: Double) {
            if (value.isNaN() || value.isInfinite()) finite = false else put(name, value)
        }
        val line = buildJsonObject {
            put("recordType", type.wireName)
            put("metadata", metadata(record.metadata))
            val timing = timing(record)!!
            if (timing.end == null) {
                put("time", timing.start.toString())
                timing.startOffset?.let { put("zoneOffset", it.toString()) }
            } else {
                put("startTime", timing.start.toString())
                put("endTime", timing.end.toString())
                timing.startOffset?.let { put("startZoneOffset", it.toString()) }
                timing.endOffset?.let { put("endZoneOffset", it.toString()) }
            }
            when (record) {
                is StepsRecord -> put("count", record.count)
                is HeartRateRecord -> putJsonArray("samples") {
                    for (sample in record.samples) {
                        add(
                            buildJsonObject {
                                put("time", sample.time.toString())
                                put("beatsPerMinute", sample.beatsPerMinute)
                            },
                        )
                    }
                }
                is RestingHeartRateRecord -> put("beatsPerMinute", record.beatsPerMinute)
                is HeartRateVariabilityRmssdRecord -> number("heartRateVariabilityMillis", record.heartRateVariabilityMillis)
                is OxygenSaturationRecord -> number("percentage", record.percentage.value)
                is RespiratoryRateRecord -> number("rate", record.rate)
                is BodyTemperatureRecord -> {
                    number("temperatureCelsius", record.temperature.inCelsius)
                    known(bodyTemperatureLocation(record.measurementLocation))?.let { put("measurementLocation", it) }
                }
                is BloodPressureRecord -> {
                    number("systolicMillimetersOfMercury", record.systolic.inMillimetersOfMercury)
                    number("diastolicMillimetersOfMercury", record.diastolic.inMillimetersOfMercury)
                    known(bodyPosition(record.bodyPosition))?.let { put("bodyPosition", it) }
                    known(bloodPressureLocation(record.measurementLocation))?.let { put("measurementLocation", it) }
                }
                is BloodGlucoseRecord -> {
                    number("levelMillimolesPerLiter", record.level.inMillimolesPerLiter)
                    put("specimenSource", specimenSource(record.specimenSource))
                    put("mealType", mealType(record.mealType))
                    put("relationToMeal", relationToMeal(record.relationToMeal))
                }
                is WeightRecord -> number("weightKilograms", record.weight.inKilograms)
                is HeightRecord -> number("heightMeters", record.height.inMeters)
                is BodyFatRecord -> number("percentage", record.percentage.value)
                is ActiveCaloriesBurnedRecord -> number("energyKilocalories", record.energy.inKilocalories)
                is TotalCaloriesBurnedRecord -> number("energyKilocalories", record.energy.inKilocalories)
                is BasalMetabolicRateRecord ->
                    number("basalMetabolicRateKilocaloriesPerDay", record.basalMetabolicRate.inKilocaloriesPerDay)
                is DistanceRecord -> number("distanceMeters", record.distance.inMeters)
                is SleepSessionRecord -> {
                    record.title?.let { put("title", it) }
                    putJsonArray("stages") {
                        for (stage in record.stages) {
                            add(
                                buildJsonObject {
                                    put("startTime", stage.startTime.toString())
                                    put("endTime", stage.endTime.toString())
                                    put("stage", sleepStage(stage.stage))
                                },
                            )
                        }
                    }
                }
                is ExerciseSessionRecord -> {
                    put("exerciseType", exerciseType(record.exerciseType))
                    record.title?.let { put("title", it) }
                }
                is MenstruationFlowRecord -> put("flow", menstruationFlow(record.flow))
                is IntermenstrualBleedingRecord -> Unit
                is NutritionRecord -> {
                    record.name?.let { put("name", it) }
                    put("mealType", mealType(record.mealType))
                    record.energy?.let { number("energyKilocalories", it.inKilocalories) }
                    record.energyFromFat?.let { number("energyFromFatKilocalories", it.inKilocalories) }
                    for ((field, mass) in nutritionMasses(record)) {
                        if (mass != null) number(field + "Grams", mass.inGrams)
                    }
                }
                is HydrationRecord -> number("volumeLiters", record.volume.inLiters)
            }
        }
        return if (finite) line else null
    }

    /** The line that removes the stored records of the Health Connect record [recordId]. */
    public fun deletionLine(recordId: String): JsonObject = HealthConnectPartBuilder.deletionLine(recordId)

    /**
     * The local day (YYYY-MM-DD) of [record], the `X-OHS-Day` of its part: the day of its time,
     * or start time, at the record's own zone offset, or in [zone] when it has none.
     */
    public fun day(record: Record, zone: ZoneId = ZoneId.systemDefault()): String {
        val timing = timing(record)
        val at = timing?.start ?: record.metadata.lastModifiedTime
        return DateTimeFormatter.ISO_LOCAL_DATE.format(at.atZone(timing?.startOffset ?: zone))
    }

    /** When a record happened: an instant ([end] null) or an interval. */
    private class Timing(val start: Instant, val startOffset: ZoneOffset?, val end: Instant? = null, val endOffset: ZoneOffset? = null)

    /**
     * The times of [record]; null for a class outside [HealthConnectRecordType]. Spelled out
     * per class: the instant and interval interfaces of the androidx library are not public.
     */
    private fun timing(record: Record): Timing? = when (record) {
        is StepsRecord -> Timing(record.startTime, record.startZoneOffset, record.endTime, record.endZoneOffset)
        is HeartRateRecord -> Timing(record.startTime, record.startZoneOffset, record.endTime, record.endZoneOffset)
        is RestingHeartRateRecord -> Timing(record.time, record.zoneOffset)
        is HeartRateVariabilityRmssdRecord -> Timing(record.time, record.zoneOffset)
        is OxygenSaturationRecord -> Timing(record.time, record.zoneOffset)
        is RespiratoryRateRecord -> Timing(record.time, record.zoneOffset)
        is BodyTemperatureRecord -> Timing(record.time, record.zoneOffset)
        is BloodPressureRecord -> Timing(record.time, record.zoneOffset)
        is BloodGlucoseRecord -> Timing(record.time, record.zoneOffset)
        is WeightRecord -> Timing(record.time, record.zoneOffset)
        is HeightRecord -> Timing(record.time, record.zoneOffset)
        is BodyFatRecord -> Timing(record.time, record.zoneOffset)
        is ActiveCaloriesBurnedRecord -> Timing(record.startTime, record.startZoneOffset, record.endTime, record.endZoneOffset)
        is TotalCaloriesBurnedRecord -> Timing(record.startTime, record.startZoneOffset, record.endTime, record.endZoneOffset)
        is BasalMetabolicRateRecord -> Timing(record.time, record.zoneOffset)
        is DistanceRecord -> Timing(record.startTime, record.startZoneOffset, record.endTime, record.endZoneOffset)
        is SleepSessionRecord -> Timing(record.startTime, record.startZoneOffset, record.endTime, record.endZoneOffset)
        is ExerciseSessionRecord -> Timing(record.startTime, record.startZoneOffset, record.endTime, record.endZoneOffset)
        is MenstruationFlowRecord -> Timing(record.time, record.zoneOffset)
        is IntermenstrualBleedingRecord -> Timing(record.time, record.zoneOffset)
        is NutritionRecord -> Timing(record.startTime, record.startZoneOffset, record.endTime, record.endZoneOffset)
        is HydrationRecord -> Timing(record.startTime, record.startZoneOffset, record.endTime, record.endZoneOffset)
        else -> null
    }

    private fun metadata(m: Metadata): JsonObject = buildJsonObject {
        put("id", m.id)
        if (m.dataOrigin.packageName.isNotEmpty()) put("dataOrigin", m.dataOrigin.packageName)
        // The epoch is the placeholder of a record Health Connect has not stamped.
        if (m.lastModifiedTime != Instant.EPOCH) put("lastModifiedTime", m.lastModifiedTime.toString())
        m.clientRecordId?.let { put("clientRecordId", it) }
        recordingMethod(m.recordingMethod)?.let { put("recordingMethod", it) }
        m.device?.let { device ->
            putJsonObject("device") {
                put("type", deviceType(device.type))
                device.manufacturer?.let { put("manufacturer", it) }
                device.model?.let { put("model", it) }
            }
        }
    }

    /** The mass fields of [NutritionRecord], under the names the server reads with `Grams`. */
    private fun nutritionMasses(r: NutritionRecord): List<Pair<String, Mass?>> = listOf(
        "biotin" to r.biotin,
        "caffeine" to r.caffeine,
        "calcium" to r.calcium,
        "chloride" to r.chloride,
        "cholesterol" to r.cholesterol,
        "chromium" to r.chromium,
        "copper" to r.copper,
        "dietaryFiber" to r.dietaryFiber,
        "folate" to r.folate,
        "folicAcid" to r.folicAcid,
        "iodine" to r.iodine,
        "iron" to r.iron,
        "magnesium" to r.magnesium,
        "manganese" to r.manganese,
        "molybdenum" to r.molybdenum,
        "monounsaturatedFat" to r.monounsaturatedFat,
        "niacin" to r.niacin,
        "pantothenicAcid" to r.pantothenicAcid,
        "phosphorus" to r.phosphorus,
        "polyunsaturatedFat" to r.polyunsaturatedFat,
        "potassium" to r.potassium,
        "protein" to r.protein,
        "riboflavin" to r.riboflavin,
        "saturatedFat" to r.saturatedFat,
        "selenium" to r.selenium,
        "sodium" to r.sodium,
        "sugar" to r.sugar,
        "thiamin" to r.thiamin,
        "totalCarbohydrate" to r.totalCarbohydrate,
        "totalFat" to r.totalFat,
        "transFat" to r.transFat,
        "unsaturatedFat" to r.unsaturatedFat,
        "vitaminA" to r.vitaminA,
        "vitaminB12" to r.vitaminB12,
        "vitaminB6" to r.vitaminB6,
        "vitaminC" to r.vitaminC,
        "vitaminD" to r.vitaminD,
        "vitaminE" to r.vitaminE,
        "vitaminK" to r.vitaminK,
        "zinc" to r.zinc,
    )

    // Enum values. Each table lists the constants of connect-client 1.1.0; a value a later
    // version adds is sent as "unknown" (exercise types as "other_workout") until it is listed.

    /** `manual` is the value the server documents for RECORDING_METHOD_MANUAL_ENTRY. */
    internal fun recordingMethod(value: Int): String? = when (value) {
        Metadata.RECORDING_METHOD_ACTIVELY_RECORDED -> "active"
        Metadata.RECORDING_METHOD_AUTOMATICALLY_RECORDED -> "automatic"
        Metadata.RECORDING_METHOD_MANUAL_ENTRY -> "manual"
        else -> null
    }

    internal fun deviceType(value: Int): String = when (value) {
        Device.TYPE_WATCH -> "watch"
        Device.TYPE_PHONE -> "phone"
        Device.TYPE_SCALE -> "scale"
        Device.TYPE_RING -> "ring"
        Device.TYPE_HEAD_MOUNTED -> "head_mounted"
        Device.TYPE_FITNESS_BAND -> "fitness_band"
        Device.TYPE_CHEST_STRAP -> "chest_strap"
        Device.TYPE_SMART_DISPLAY -> "smart_display"
        else -> UNKNOWN
    }

    internal fun bodyTemperatureLocation(value: Int): String = when (value) {
        BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_ARMPIT -> "armpit"
        BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_FINGER -> "finger"
        BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_FOREHEAD -> "forehead"
        BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_MOUTH -> "mouth"
        BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_RECTUM -> "rectum"
        BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_TEMPORAL_ARTERY -> "temporal_artery"
        BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_TOE -> "toe"
        BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_EAR -> "ear"
        BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_WRIST -> "wrist"
        BodyTemperatureMeasurementLocation.MEASUREMENT_LOCATION_VAGINA -> "vagina"
        else -> UNKNOWN
    }

    internal fun bloodPressureLocation(value: Int): String = when (value) {
        BloodPressureRecord.MEASUREMENT_LOCATION_LEFT_WRIST -> "left_wrist"
        BloodPressureRecord.MEASUREMENT_LOCATION_RIGHT_WRIST -> "right_wrist"
        BloodPressureRecord.MEASUREMENT_LOCATION_LEFT_UPPER_ARM -> "left_upper_arm"
        BloodPressureRecord.MEASUREMENT_LOCATION_RIGHT_UPPER_ARM -> "right_upper_arm"
        else -> UNKNOWN
    }

    internal fun bodyPosition(value: Int): String = when (value) {
        BloodPressureRecord.BODY_POSITION_STANDING_UP -> "standing_up"
        BloodPressureRecord.BODY_POSITION_SITTING_DOWN -> "sitting_down"
        BloodPressureRecord.BODY_POSITION_LYING_DOWN -> "lying_down"
        BloodPressureRecord.BODY_POSITION_RECLINING -> "reclining"
        else -> UNKNOWN
    }

    internal fun specimenSource(value: Int): String = when (value) {
        BloodGlucoseRecord.SPECIMEN_SOURCE_INTERSTITIAL_FLUID -> "interstitial_fluid"
        BloodGlucoseRecord.SPECIMEN_SOURCE_CAPILLARY_BLOOD -> "capillary_blood"
        BloodGlucoseRecord.SPECIMEN_SOURCE_PLASMA -> "plasma"
        BloodGlucoseRecord.SPECIMEN_SOURCE_SERUM -> "serum"
        BloodGlucoseRecord.SPECIMEN_SOURCE_TEARS -> "tears"
        BloodGlucoseRecord.SPECIMEN_SOURCE_WHOLE_BLOOD -> "whole_blood"
        else -> UNKNOWN
    }

    internal fun relationToMeal(value: Int): String = when (value) {
        BloodGlucoseRecord.RELATION_TO_MEAL_GENERAL -> "general"
        BloodGlucoseRecord.RELATION_TO_MEAL_FASTING -> "fasting"
        BloodGlucoseRecord.RELATION_TO_MEAL_BEFORE_MEAL -> "before_meal"
        BloodGlucoseRecord.RELATION_TO_MEAL_AFTER_MEAL -> "after_meal"
        else -> UNKNOWN
    }

    internal fun mealType(value: Int): String = when (value) {
        MealType.MEAL_TYPE_BREAKFAST -> "breakfast"
        MealType.MEAL_TYPE_LUNCH -> "lunch"
        MealType.MEAL_TYPE_DINNER -> "dinner"
        MealType.MEAL_TYPE_SNACK -> "snack"
        else -> UNKNOWN
    }

    internal fun sleepStage(value: Int): String = when (value) {
        SleepSessionRecord.STAGE_TYPE_AWAKE -> "awake"
        SleepSessionRecord.STAGE_TYPE_SLEEPING -> "sleeping"
        SleepSessionRecord.STAGE_TYPE_OUT_OF_BED -> "out_of_bed"
        SleepSessionRecord.STAGE_TYPE_LIGHT -> "light"
        SleepSessionRecord.STAGE_TYPE_DEEP -> "deep"
        SleepSessionRecord.STAGE_TYPE_REM -> "rem"
        SleepSessionRecord.STAGE_TYPE_AWAKE_IN_BED -> "awake_in_bed"
        else -> UNKNOWN
    }

    internal fun menstruationFlow(value: Int): String = when (value) {
        MenstruationFlowRecord.FLOW_LIGHT -> "light"
        MenstruationFlowRecord.FLOW_MEDIUM -> "medium"
        MenstruationFlowRecord.FLOW_HEAVY -> "heavy"
        else -> UNKNOWN
    }

    internal fun exerciseType(value: Int): String = when (value) {
        ExerciseSessionRecord.EXERCISE_TYPE_BADMINTON -> "badminton"
        ExerciseSessionRecord.EXERCISE_TYPE_BASEBALL -> "baseball"
        ExerciseSessionRecord.EXERCISE_TYPE_BASKETBALL -> "basketball"
        ExerciseSessionRecord.EXERCISE_TYPE_BIKING -> "biking"
        ExerciseSessionRecord.EXERCISE_TYPE_BIKING_STATIONARY -> "biking_stationary"
        ExerciseSessionRecord.EXERCISE_TYPE_BOOT_CAMP -> "boot_camp"
        ExerciseSessionRecord.EXERCISE_TYPE_BOXING -> "boxing"
        ExerciseSessionRecord.EXERCISE_TYPE_CALISTHENICS -> "calisthenics"
        ExerciseSessionRecord.EXERCISE_TYPE_CRICKET -> "cricket"
        ExerciseSessionRecord.EXERCISE_TYPE_DANCING -> "dancing"
        ExerciseSessionRecord.EXERCISE_TYPE_ELLIPTICAL -> "elliptical"
        ExerciseSessionRecord.EXERCISE_TYPE_EXERCISE_CLASS -> "exercise_class"
        ExerciseSessionRecord.EXERCISE_TYPE_FENCING -> "fencing"
        ExerciseSessionRecord.EXERCISE_TYPE_FOOTBALL_AMERICAN -> "football_american"
        ExerciseSessionRecord.EXERCISE_TYPE_FOOTBALL_AUSTRALIAN -> "football_australian"
        ExerciseSessionRecord.EXERCISE_TYPE_FRISBEE_DISC -> "frisbee_disc"
        ExerciseSessionRecord.EXERCISE_TYPE_GOLF -> "golf"
        ExerciseSessionRecord.EXERCISE_TYPE_GUIDED_BREATHING -> "guided_breathing"
        ExerciseSessionRecord.EXERCISE_TYPE_GYMNASTICS -> "gymnastics"
        ExerciseSessionRecord.EXERCISE_TYPE_HANDBALL -> "handball"
        ExerciseSessionRecord.EXERCISE_TYPE_HIGH_INTENSITY_INTERVAL_TRAINING -> "high_intensity_interval_training"
        ExerciseSessionRecord.EXERCISE_TYPE_HIKING -> "hiking"
        ExerciseSessionRecord.EXERCISE_TYPE_ICE_HOCKEY -> "ice_hockey"
        ExerciseSessionRecord.EXERCISE_TYPE_ICE_SKATING -> "ice_skating"
        ExerciseSessionRecord.EXERCISE_TYPE_MARTIAL_ARTS -> "martial_arts"
        ExerciseSessionRecord.EXERCISE_TYPE_PADDLING -> "paddling"
        ExerciseSessionRecord.EXERCISE_TYPE_PARAGLIDING -> "paragliding"
        ExerciseSessionRecord.EXERCISE_TYPE_PILATES -> "pilates"
        ExerciseSessionRecord.EXERCISE_TYPE_RACQUETBALL -> "racquetball"
        ExerciseSessionRecord.EXERCISE_TYPE_ROCK_CLIMBING -> "rock_climbing"
        ExerciseSessionRecord.EXERCISE_TYPE_ROLLER_HOCKEY -> "roller_hockey"
        ExerciseSessionRecord.EXERCISE_TYPE_ROWING -> "rowing"
        ExerciseSessionRecord.EXERCISE_TYPE_ROWING_MACHINE -> "rowing_machine"
        ExerciseSessionRecord.EXERCISE_TYPE_RUGBY -> "rugby"
        ExerciseSessionRecord.EXERCISE_TYPE_RUNNING -> "running"
        ExerciseSessionRecord.EXERCISE_TYPE_RUNNING_TREADMILL -> "running_treadmill"
        ExerciseSessionRecord.EXERCISE_TYPE_SAILING -> "sailing"
        ExerciseSessionRecord.EXERCISE_TYPE_SCUBA_DIVING -> "scuba_diving"
        ExerciseSessionRecord.EXERCISE_TYPE_SKATING -> "skating"
        ExerciseSessionRecord.EXERCISE_TYPE_SKIING -> "skiing"
        ExerciseSessionRecord.EXERCISE_TYPE_SNOWBOARDING -> "snowboarding"
        ExerciseSessionRecord.EXERCISE_TYPE_SNOWSHOEING -> "snowshoeing"
        ExerciseSessionRecord.EXERCISE_TYPE_SOCCER -> "soccer"
        ExerciseSessionRecord.EXERCISE_TYPE_SOFTBALL -> "softball"
        ExerciseSessionRecord.EXERCISE_TYPE_SQUASH -> "squash"
        ExerciseSessionRecord.EXERCISE_TYPE_STAIR_CLIMBING -> "stair_climbing"
        ExerciseSessionRecord.EXERCISE_TYPE_STAIR_CLIMBING_MACHINE -> "stair_climbing_machine"
        ExerciseSessionRecord.EXERCISE_TYPE_STRENGTH_TRAINING -> "strength_training"
        ExerciseSessionRecord.EXERCISE_TYPE_STRETCHING -> "stretching"
        ExerciseSessionRecord.EXERCISE_TYPE_SURFING -> "surfing"
        ExerciseSessionRecord.EXERCISE_TYPE_SWIMMING_OPEN_WATER -> "swimming_open_water"
        ExerciseSessionRecord.EXERCISE_TYPE_SWIMMING_POOL -> "swimming_pool"
        ExerciseSessionRecord.EXERCISE_TYPE_TABLE_TENNIS -> "table_tennis"
        ExerciseSessionRecord.EXERCISE_TYPE_TENNIS -> "tennis"
        ExerciseSessionRecord.EXERCISE_TYPE_VOLLEYBALL -> "volleyball"
        ExerciseSessionRecord.EXERCISE_TYPE_WALKING -> "walking"
        ExerciseSessionRecord.EXERCISE_TYPE_WATER_POLO -> "water_polo"
        ExerciseSessionRecord.EXERCISE_TYPE_WEIGHTLIFTING -> "weightlifting"
        ExerciseSessionRecord.EXERCISE_TYPE_WHEELCHAIR -> "wheelchair"
        ExerciseSessionRecord.EXERCISE_TYPE_YOGA -> "yoga"
        else -> "other_workout"
    }

    /**
     * Null for "unknown". The server keeps a body position and a measurement location as it
     * gets them, so an unknown one is left out; the other enums are sent as they are, because
     * the server reads `unknown` itself there.
     */
    private fun known(value: String): String? = value.takeIf { it != UNKNOWN }

    private const val UNKNOWN = "unknown"
}
