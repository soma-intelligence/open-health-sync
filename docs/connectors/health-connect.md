# Health Connect connector

Slug `health_connect`. [Health Connect](https://developer.android.com/health-and-fitness/guides/health-connect)
is Android's on-device health store: apps such as Samsung Health, Google Fit, Fitbit, Oura,
OMRON connect and CGM apps write to it, and an app with the user's permission reads it back.
It has no server API, so an Android app reads it on the phone and uploads the records to
`POST /v1/ingest/health-connect`. No keys are needed; the connector is always on.

Status: available on the server. The Kotlin SDK that reads Health Connect and uploads in this
format is being built; until it ships, your Android app implements the contract below.

## Upload contract

The route works exactly like the [Apple Health upload](apple-health.md): a user token with the
`upload` scope, the headers `X-OHS-Type`, `X-OHS-Day`, `X-OHS-Batch` and `X-OHS-Part`, and a
gzip NDJSON body of at most 64 MiB. Re-sending a part is safe. Use the record type of the lines
as `X-OHS-Type`, and keep `(batch, part)` unique across the types of one day.

```sh
gzip -c records.ndjson | curl -s -X POST $OHS/v1/ingest/health-connect \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H 'X-OHS-Type: BloodGlucose' -H 'X-OHS-Day: 2026-03-02' \
  -H 'X-OHS-Batch: 7f3c' -H 'X-OHS-Part: 0' \
  --data-binary @-
```

## Line format

One Health Connect record per line, as a JSON object. Field names follow the androidx
`androidx.health.connect.client.records` classes, with the unit in the name.

- `recordType`: the record class name without `Record` (`BloodGlucoseRecord` is
  `BloodGlucose`). Required.
- `metadata`: `id` (required: the Health Connect record id), `dataOrigin` (the package name of
  the app that wrote it), `lastModifiedTime`, `clientRecordId`, `recordingMethod`
  (`unknown`, `active`, `automatic` or `manual`) and `device` (`type`, `manufacturer`,
  `model`).
- Instant records: `time` (an RFC 3339 instant, as `Instant.toString()` writes it) and an
  optional `zoneOffset` (`+02:00`).
- Interval records: `startTime`, `endTime`, `startZoneOffset`, `endZoneOffset`.
- Enum values are lowercase strings: the constant name without its prefix
  (`SPECIMEN_SOURCE_INTERSTITIAL_FLUID` is `interstitial_fluid`, `STAGE_TYPE_DEEP` is `deep`,
  `EXERCISE_TYPE_BIKING` is `biking`).

| `recordType` | Value fields | Canonical records |
|---|---|---|
| `Steps` | `count` | `step_count` |
| `HeartRate` | `samples`: `[{time, beatsPerMinute}]` | one `heart_rate` sample per point |
| `RestingHeartRate` | `beatsPerMinute` | `resting_heart_rate` |
| `HeartRateVariabilityRmssd` | `heartRateVariabilityMillis` | `hrv_rmssd` |
| `OxygenSaturation` | `percentage` (0 to 100) | `oxygen_saturation` |
| `RespiratoryRate` | `rate` | `respiratory_rate` |
| `BodyTemperature` | `temperatureCelsius`, `measurementLocation` | `body_temperature` |
| `BloodPressure` | `systolicMillimetersOfMercury`, `diastolicMillimetersOfMercury`, `bodyPosition`, `measurementLocation` | `blood_pressure_systolic` and `blood_pressure_diastolic`, paired into one panel |
| `BloodGlucose` | `levelMillimolesPerLiter`, `specimenSource`, `mealType`, `relationToMeal` | `blood_glucose` (stored in mg/dL) |
| `Weight` | `weightKilograms` | `body_mass` |
| `Height` | `heightMeters` | `height` (stored in cm) |
| `BodyFat` | `percentage` | `body_fat_percentage` |
| `ActiveCaloriesBurned` | `energyKilocalories` | `active_energy_burned` |
| `TotalCaloriesBurned` | `energyKilocalories` | `total_energy_burned` |
| `BasalMetabolicRate` | `basalMetabolicRateKilocaloriesPerDay` | `basal_metabolic_rate` (kcal/d) |
| `Distance` | `distanceMeters` | `distance_walking_running` |
| `SleepSession` | `title`, `stages`: `[{startTime, endTime, stage}]` | a sleep session with its phases |
| `ExerciseSession` | `exerciseType`, `title` | a workout |
| `MenstruationFlow` | `flow` | a `menstrual_flow` category sample |
| `IntermenstrualBleeding` | none | an `intermenstrual_bleeding` category sample |
| `Nutrition` | `energyKilocalories`, `energyFromFatKilocalories`, every mass as `<field>Grams` (`proteinGrams`, `totalCarbohydrateGrams`, `sodiumGrams`, `vitaminDGrams`...), `name`, `mealType` | a nutrition log |
| `Hydration` | `volumeLiters` | a nutrition log with `nutrients.water` (mL) |
| `Deletion` | `recordId` | removes the stored records of that id |

Example lines:

```json
{"recordType":"BloodGlucose","metadata":{"id":"0b1e...","dataOrigin":"com.freestylelibre.app.de","recordingMethod":"automatic"},"time":"2026-03-02T07:30:00Z","zoneOffset":"+01:00","levelMillimolesPerLiter":5.6,"specimenSource":"interstitial_fluid","relationToMeal":"fasting"}
{"recordType":"BloodPressure","metadata":{"id":"9a2c...","dataOrigin":"jp.co.omron.healthcare.omron_connect","device":{"type":"unknown","manufacturer":"OMRON","model":"M7"}},"time":"2026-03-02T07:00:00Z","zoneOffset":"+01:00","systolicMillimetersOfMercury":121,"diastolicMillimetersOfMercury":79,"bodyPosition":"sitting_down"}
{"recordType":"Nutrition","metadata":{"id":"5d07...","dataOrigin":"com.myfitnesspal.android"},"startTime":"2026-03-02T11:30:00Z","endTime":"2026-03-02T11:50:00Z","startZoneOffset":"+01:00","mealType":"lunch","name":"Lentil soup","energyKilocalories":420,"proteinGrams":24,"sodiumGrams":0.8,"dietaryFiberGrams":13}
{"recordType":"Deletion","recordId":"0b1e..."}
```

A line of another record type is stored and skipped, so an app can send more than this list;
unknown fields are ignored. A line that is not a JSON object, or has no `recordType`, refuses
the whole part with 400. A supported record without `metadata.id` or with unreadable times
fails at processing.

## Mapping notes

- **Source**: `provider` is `health_connect`; `source_slug` names the app that wrote the record
  (`metadata.dataOrigin`). Known apps get their usual slug (`com.dexcom.*` is `dexcom`,
  `com.freestylelibre*` is `freestyle_libre`, `com.ouraring.*` is `oura`, Mi Fitness, Mi Health
  and Zepp Life are [`xiaomi`](xiaomi.md), Zepp (Amazfit) is `zepp`, RingConn is `ringconn`, ...); any other app
  gets its package name as a slug. Source priorities can rank them like any other source.
- **Edits**: Health Connect records can be edited in place. A sample is identified by its record
  id and start, not its value, so a re-sent record replaces the stored value, and a record whose
  start moved replaces its older copy. Heart rate series points are identified by time and value.
- **Deletions**: send the `DeletionChange` record ids from the changes API as `Deletion` lines.
  The stored records of that id are removed and never written again.
- **Glucose**: `specimenSource` goes to `metadata.specimen`. When it is `interstitial_fluid` (a
  CGM), the [FHIR output](../fhir.md) codes the Observation with LOINC `99504-3` instead of
  `2339-0`.
- **Blood pressure**: both halves share time, source and device, so the read API and FHIR pair
  them into one panel.
- **Sleep**: stages `awake` and `awake_in_bed` are awake, `sleeping` counts as light, `light`,
  `deep` and `rem` keep their name; `out_of_bed` and `unknown` give no phase.
- **Nutrition**: masses go up in grams (`Mass.inGrams`) whatever the nutrient; the server
  converts each to [its nutrient's unit](../api.md#nutrition-logs) (`sodiumGrams` 0.8 is
  `sodium_mg` 800, `vitaminDGrams` goes to micrograms). The mass fields are those of
  `NutritionRecord`: `biotin`, `caffeine`, `calcium`, `chloride`, `cholesterol`, `chromium`,
  `copper`, `dietaryFiber`, `folate`, `folicAcid`, `iodine`, `iron`, `magnesium`, `manganese`,
  `molybdenum`, `monounsaturatedFat`, `niacin`, `pantothenicAcid`, `phosphorus`,
  `polyunsaturatedFat`, `potassium`, `protein`, `riboflavin`, `saturatedFat`, `selenium`,
  `sodium`, `sugar`, `thiamin`, `totalCarbohydrate`, `totalFat`, `transFat`, `unsaturatedFat`,
  `vitaminA`, `vitaminB12`, `vitaminB6`, `vitaminC`, `vitaminD`, `vitaminE`, `vitaminK`, `zinc`,
  each with `Grams`. `mealType` `breakfast`, `lunch`, `dinner` or `snack` is kept, `unknown` is
  none; `name` goes to `metadata.food_name`. A record with no figure is kept: the person logged
  a meal. Like the samples, a log is identified by its record id: a re-sent record replaces its
  figures, a moved start replaces the older copy, and a `Deletion` removes it.
- **Energy**: `total_energy_burned` is summed per local day and becomes the daily summary's
  `total_kcal` of the Health Connect summary; without it, `total_kcal` stays basal plus active energy.
  `basal_metabolic_rate` is a rate, not summed. Neither has a LOINC code.
- **Menstruation**: `flow` `light`, `medium` and `heavy` keep their name as the label, with the
  values Apple Health uses (2, 3, 4); `unknown` or no flow is `unspecified` (1). The samples feed
  the [menstrual cycles](../menstrual-cycles.md). They are identified by record id and time: a
  re-sent record is a no-op, and an edited flow keeps the stored value until a `Deletion` of the
  record. `MenstruationPeriod` (a span with no flow per day) is not stored.
- **Time zone**: `zoneOffset` (or `startZoneOffset`) gives `metadata.utc_offset_minutes` and,
  for whole hours, `metadata.timezone`, which sets the local day of daily summaries.
- **Manual entries**: `recordingMethod` `manual` sets `was_user_entered`.

## Reading permissions

An app reads only the record types the user granted. By default it reads back data from 30
days before its first permission grant; older data needs the history read permission
(`PERMISSION_READ_HEALTH_DATA_HISTORY`).
