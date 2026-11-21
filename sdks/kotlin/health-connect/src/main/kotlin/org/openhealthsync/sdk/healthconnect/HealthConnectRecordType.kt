// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk.healthconnect

import androidx.health.connect.client.permission.HealthPermission
import androidx.health.connect.client.records.ActiveCaloriesBurnedRecord
import androidx.health.connect.client.records.BasalMetabolicRateRecord
import androidx.health.connect.client.records.BloodGlucoseRecord
import androidx.health.connect.client.records.BloodPressureRecord
import androidx.health.connect.client.records.BodyFatRecord
import androidx.health.connect.client.records.BodyTemperatureRecord
import androidx.health.connect.client.records.DistanceRecord
import androidx.health.connect.client.records.ExerciseSessionRecord
import androidx.health.connect.client.records.HeartRateRecord
import androidx.health.connect.client.records.HeartRateVariabilityRmssdRecord
import androidx.health.connect.client.records.HeightRecord
import androidx.health.connect.client.records.HydrationRecord
import androidx.health.connect.client.records.IntermenstrualBleedingRecord
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
import kotlin.reflect.KClass

/**
 * The Health Connect record types the server stores: every `recordType` of
 * connectors/healthconnect/mapper.go, in the order of the table in
 * docs/connectors/health-connect.md. `Deletion` lines are not a record type: they are sent for
 * the records of these types that Health Connect reports deleted.
 */
public enum class HealthConnectRecordType(
    /** The `recordType` of the upload line: the record class name without `Record`. */
    public val wireName: String,
    /** The androidx record class. */
    public val recordClass: KClass<out Record>,
) {
    STEPS("Steps", StepsRecord::class),
    HEART_RATE("HeartRate", HeartRateRecord::class),
    RESTING_HEART_RATE("RestingHeartRate", RestingHeartRateRecord::class),
    HEART_RATE_VARIABILITY_RMSSD("HeartRateVariabilityRmssd", HeartRateVariabilityRmssdRecord::class),
    OXYGEN_SATURATION("OxygenSaturation", OxygenSaturationRecord::class),
    RESPIRATORY_RATE("RespiratoryRate", RespiratoryRateRecord::class),
    BODY_TEMPERATURE("BodyTemperature", BodyTemperatureRecord::class),
    BLOOD_PRESSURE("BloodPressure", BloodPressureRecord::class),
    BLOOD_GLUCOSE("BloodGlucose", BloodGlucoseRecord::class),
    WEIGHT("Weight", WeightRecord::class),
    HEIGHT("Height", HeightRecord::class),
    BODY_FAT("BodyFat", BodyFatRecord::class),
    ACTIVE_CALORIES_BURNED("ActiveCaloriesBurned", ActiveCaloriesBurnedRecord::class),
    TOTAL_CALORIES_BURNED("TotalCaloriesBurned", TotalCaloriesBurnedRecord::class),
    BASAL_METABOLIC_RATE("BasalMetabolicRate", BasalMetabolicRateRecord::class),
    DISTANCE("Distance", DistanceRecord::class),
    SLEEP_SESSION("SleepSession", SleepSessionRecord::class),
    EXERCISE_SESSION("ExerciseSession", ExerciseSessionRecord::class),
    MENSTRUATION_FLOW("MenstruationFlow", MenstruationFlowRecord::class),
    INTERMENSTRUAL_BLEEDING("IntermenstrualBleeding", IntermenstrualBleedingRecord::class),
    NUTRITION("Nutrition", NutritionRecord::class),
    HYDRATION("Hydration", HydrationRecord::class),
    ;

    /** The Health Connect permission that lets an app read this type. */
    public val readPermission: String get() = HealthPermission.getReadPermission(recordClass)

    public companion object {
        private val byWireName = entries.associateBy { it.wireName }
        private val byClass = entries.associateBy { it.recordClass }

        /** The type of a `recordType`; null when the server does not store it. */
        public fun fromWireName(wireName: String): HealthConnectRecordType? = byWireName[wireName]

        /** The type of [record]; null when the server does not store its class. */
        public fun of(record: Record): HealthConnectRecordType? = byClass[record::class]
    }
}
