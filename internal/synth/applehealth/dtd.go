// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

// dtd is the document type declaration an export.xml starts with, HealthKit
// Export Version 17 as the Health app writes it: the format the export
// follows, and what an importer of a newer export may meet.
const dtd = `<!DOCTYPE HealthData [
<!-- HealthKit Export Version: 17 -->
<!ELEMENT HealthData (ExportDate,Me,(Record|Correlation|Workout|ActivitySummary|ClinicalRecord|Audiogram|VisionPrescription)*)>
<!ATTLIST HealthData
  locale CDATA #REQUIRED
>
<!ELEMENT ExportDate EMPTY>
<!ATTLIST ExportDate
  value CDATA #REQUIRED
>
<!ELEMENT Me EMPTY>
<!ATTLIST Me
  HKCharacteristicTypeIdentifierDateOfBirth                   CDATA #REQUIRED
  HKCharacteristicTypeIdentifierBiologicalSex                 CDATA #REQUIRED
  HKCharacteristicTypeIdentifierBloodType                     CDATA #REQUIRED
  HKCharacteristicTypeIdentifierFitzpatrickSkinType           CDATA #REQUIRED
  HKCharacteristicTypeIdentifierCardioFitnessMedicationsUse   CDATA #REQUIRED
>
<!ELEMENT Record ((MetadataEntry|HeartRateVariabilityMetadataList)*)>
<!ATTLIST Record
  type          CDATA #REQUIRED
  unit          CDATA #IMPLIED
  value         CDATA #IMPLIED
  sourceName    CDATA #REQUIRED
  sourceVersion CDATA #IMPLIED
  device        CDATA #IMPLIED
  creationDate  CDATA #IMPLIED
  startDate     CDATA #REQUIRED
  endDate       CDATA #REQUIRED
>
<!-- Note: Any Records that appear as children of a correlation also appear as top-level records in this document. -->
<!ELEMENT Correlation ((MetadataEntry|Record)*)>
<!ATTLIST Correlation
  type          CDATA #REQUIRED
  sourceName    CDATA #REQUIRED
  sourceVersion CDATA #IMPLIED
  device        CDATA #IMPLIED
  creationDate  CDATA #IMPLIED
  startDate     CDATA #REQUIRED
  endDate       CDATA #REQUIRED
>
<!-- Note: Any Records that appear as children of a Workout also appear as top-level records in this document. -->
<!ELEMENT Workout ((MetadataEntry|WorkoutEvent|WorkoutRoute|WorkoutStatistics|WorkoutActivity|WorkoutZoneGroup|Record)*)>
<!ATTLIST Workout
  workoutActivityType   CDATA #REQUIRED
  duration              CDATA #IMPLIED
  durationUnit          CDATA #IMPLIED
  totalDistance         CDATA #IMPLIED
  totalDistanceUnit     CDATA #IMPLIED
  totalEnergyBurned     CDATA #IMPLIED
  totalEnergyBurnedUnit CDATA #IMPLIED
  sourceName            CDATA #REQUIRED
  sourceVersion         CDATA #IMPLIED
  device                CDATA #IMPLIED
  creationDate          CDATA #IMPLIED
  startDate             CDATA #REQUIRED
  endDate               CDATA #REQUIRED
>
<!ELEMENT WorkoutActivity ((MetadataEntry|WorkoutEvent|WorkoutStatistics|WorkoutZoneGroup)*)>
<!ATTLIST WorkoutActivity
  uuid                 CDATA #REQUIRED
  startDate            CDATA #REQUIRED
  endDate              CDATA #IMPLIED
  duration             CDATA #IMPLIED
  durationUnit         CDATA #IMPLIED
>
<!ELEMENT WorkoutEvent ((MetadataEntry)*)>
<!ATTLIST WorkoutEvent
  type                 CDATA #REQUIRED
  date                 CDATA #REQUIRED
  duration             CDATA #IMPLIED
  durationUnit         CDATA #IMPLIED
>
<!ELEMENT WorkoutStatistics EMPTY>
<!ATTLIST WorkoutStatistics
  type                 CDATA #REQUIRED
  startDate            CDATA #REQUIRED
  endDate              CDATA #REQUIRED
  average              CDATA #IMPLIED
  minimum              CDATA #IMPLIED
  maximum              CDATA #IMPLIED
  sum                  CDATA #IMPLIED
  unit                 CDATA #IMPLIED
>
<!ELEMENT WorkoutRoute ((MetadataEntry|FileReference)*)>
<!ATTLIST WorkoutRoute
  sourceName    CDATA #REQUIRED
  sourceVersion CDATA #IMPLIED
  device        CDATA #IMPLIED
  creationDate  CDATA #IMPLIED
  startDate     CDATA #REQUIRED
  endDate       CDATA #REQUIRED
>
<!ELEMENT WorkoutZoneGroup ((WorkoutZone)*)>
<!ATTLIST WorkoutZoneGroup
  type              CDATA #REQUIRED
  source            CDATA #REQUIRED
  unit              CDATA #REQUIRED
>
<!ELEMENT WorkoutZone EMPTY>
<!ATTLIST WorkoutZone
  minimum      CDATA #IMPLIED
  maximum      CDATA #IMPLIED
  duration     CDATA #REQUIRED
  durationUnit CDATA #REQUIRED
>
<!ELEMENT FileReference EMPTY>
<!ATTLIST FileReference
  path CDATA #REQUIRED
>
<!ELEMENT ActivitySummary EMPTY>
<!ATTLIST ActivitySummary
  dateComponents           CDATA #IMPLIED
  activeEnergyBurned       CDATA #IMPLIED
  activeEnergyBurnedGoal   CDATA #IMPLIED
  activeEnergyBurnedUnit   CDATA #IMPLIED
  appleMoveTime            CDATA #IMPLIED
  appleMoveTimeGoal        CDATA #IMPLIED
  appleExerciseTime        CDATA #IMPLIED
  appleExerciseTimeGoal    CDATA #IMPLIED
  appleStandHours          CDATA #IMPLIED
  appleStandHoursGoal      CDATA #IMPLIED
>
<!ELEMENT MetadataEntry EMPTY>
<!ATTLIST MetadataEntry
  key   CDATA #REQUIRED
  value CDATA #REQUIRED
>
<!-- Note: Heart Rate Variability records captured by Apple Watch may include an associated list of instantaneous beats-per-minute readings. -->
<!ELEMENT HeartRateVariabilityMetadataList (InstantaneousBeatsPerMinute*)>
<!ELEMENT InstantaneousBeatsPerMinute EMPTY>
<!ATTLIST InstantaneousBeatsPerMinute
  bpm  CDATA #REQUIRED
  time CDATA #REQUIRED
>
<!ELEMENT ClinicalRecord EMPTY>
<!ATTLIST ClinicalRecord
  type              CDATA #REQUIRED
  identifier        CDATA #REQUIRED
  sourceName        CDATA #REQUIRED
  sourceURL         CDATA #REQUIRED
  fhirVersion       CDATA #REQUIRED
  receivedDate      CDATA #REQUIRED
  resourceFilePath  CDATA #REQUIRED
>
<!ELEMENT Audiogram ((MetadataEntry|SensitivityPoint)*)>
<!ATTLIST Audiogram
  type          CDATA #REQUIRED
  sourceName    CDATA #REQUIRED
  sourceVersion CDATA #IMPLIED
  device        CDATA #IMPLIED
  creationDate  CDATA #IMPLIED
  startDate     CDATA #REQUIRED
  endDate       CDATA #REQUIRED
>
<!ELEMENT SensitivityPoint EMPTY>
<!ATTLIST SensitivityPoint
  frequencyValue   CDATA #REQUIRED
  frequencyUnit    CDATA #REQUIRED
  leftEarValue     CDATA #IMPLIED
  leftEarUnit      CDATA #IMPLIED
  leftEarMasked      CDATA #IMPLIED
  leftEarClampingRangeLowerBound   CDATA #IMPLIED
  leftEarClampingRangeUpperBound   CDATA #IMPLIED
  rightEarValue    CDATA #IMPLIED
  rightEarUnit     CDATA #IMPLIED
  rightEarMasked      CDATA #IMPLIED
  rightEarClampingRangeLowerBound  CDATA #IMPLIED
  rightEarClampingRangeUpperBound  CDATA #IMPLIED
>
<!ELEMENT VisionPrescription ((RightEye|LeftEye|Attachment|MetadataEntry)*)>
<!ATTLIST VisionPrescription
  type             CDATA #REQUIRED
  dateIssued       CDATA #REQUIRED
  expirationDate   CDATA #IMPLIED
  brand            CDATA #IMPLIED
>
<!ELEMENT RightEye EMPTY>
<!ATTLIST RightEye
  sphere           CDATA #IMPLIED
  sphereUnit       CDATA #IMPLIED
  cylinder         CDATA #IMPLIED
  cylinderUnit     CDATA #IMPLIED
  axis             CDATA #IMPLIED
  axisUnit         CDATA #IMPLIED
  add              CDATA #IMPLIED
  addUnit          CDATA #IMPLIED
  vertex           CDATA #IMPLIED
  vertexUnit       CDATA #IMPLIED
  prismAmount      CDATA #IMPLIED
  prismAmountUnit  CDATA #IMPLIED
  prismAngle       CDATA #IMPLIED
  prismAngleUnit   CDATA #IMPLIED
  farPD            CDATA #IMPLIED
  farPDUnit        CDATA #IMPLIED
  nearPD           CDATA #IMPLIED
  nearPDUnit       CDATA #IMPLIED
  baseCurve        CDATA #IMPLIED
  baseCurveUnit    CDATA #IMPLIED
  diameter         CDATA #IMPLIED
  diameterUnit     CDATA #IMPLIED
>
<!ELEMENT LeftEye EMPTY>
<!ATTLIST LeftEye
  sphere           CDATA #IMPLIED
  sphereUnit       CDATA #IMPLIED
  cylinder         CDATA #IMPLIED
  cylinderUnit     CDATA #IMPLIED
  axis             CDATA #IMPLIED
  axisUnit         CDATA #IMPLIED
  add              CDATA #IMPLIED
  addUnit          CDATA #IMPLIED
  vertex           CDATA #IMPLIED
  vertexUnit       CDATA #IMPLIED
  prismAmount      CDATA #IMPLIED
  prismAmountUnit  CDATA #IMPLIED
  prismAngle       CDATA #IMPLIED
  prismAngleUnit   CDATA #IMPLIED
  farPD            CDATA #IMPLIED
  farPDUnit        CDATA #IMPLIED
  nearPD           CDATA #IMPLIED
  nearPDUnit       CDATA #IMPLIED
  baseCurve        CDATA #IMPLIED
  baseCurveUnit    CDATA #IMPLIED
  diameter         CDATA #IMPLIED
  diameterUnit     CDATA #IMPLIED
>
<!ELEMENT Attachment EMPTY>
<!ATTLIST Attachment
  identifier       CDATA #IMPLIED
>
]>
`
