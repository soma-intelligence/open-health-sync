# Spec: activity intensity minutes and the HL7 Physical Activity IG

Status: draft for review (2026-10-17).

## Problem

Minutes of moderate and vigorous activity are what physical activity guidelines count (the WHO
asks adults for 150 to 300 minutes of moderate activity a week, or 75 to 150 of vigorous), and
what clinicians ask about as the exercise vital sign. Several providers already send them:
Oura's `daily_activity` has `medium_activity_time` and `high_activity_time`, Withings'
`getactivity` has `moderate` and `intense`. Open Health Sync keeps those figures in the
metadata of the step sample only, so no read, export, statistic or FHIR client can use them.

On the FHIR side, HL7 publishes an implementation guide for exactly this data, the
[Physical Activity IG](https://hl7.org/fhir/us/physical-activity/) (STU1, 1.0.0, FHIR R4). Our
workout and daily summary Observations already use four of its LOINC codes (41950-7 steps,
55411-3 exercise duration, 73985-4 exercise activity, 40443-4 resting heart rate) but not the
rest, and they do not carry its category.

## Scope

1. Two quantity types, stored in minutes (UCUM `min`), one sample per provider day:

   | Type | LOINC | Meaning |
   |---|---|---|
   | `moderate_activity_time` | 77592-4 (Moderate physical activity) | minutes the provider classed as moderate intensity |
   | `vigorous_activity_time` | 77593-2 (Vigorous physical activity) | minutes the provider classed as vigorous intensity |

   Each provider's own classes are kept as they are: Open Health Sync does not reclassify
   activity from heart rate or METs. The two are kept apart, not summed, so a client can apply
   its own rule (the WHO counts one vigorous minute as two moderate ones).
2. Mappings:

   | Provider | Source | `moderate_activity_time` | `vigorous_activity_time` |
   |---|---|---|---|
   | Oura | `daily_activity` | `medium_activity_time` (seconds) | `high_activity_time` (seconds) |
   | Withings | `getactivity` | `moderate` (seconds) | `intense` (seconds) |

   Both samples cover the provider's local day, are keyed by the day (and the Withings device),
   and are replaceable like the other daily totals of these documents: a day re-sent with new
   minutes replaces the stored value. A zero is stored as zero; a figure the provider leaves
   out gives no sample. The metadata copies stay, so nothing a client reads today changes.
3. Reads need nothing new: `/samples?type=moderate_activity_time`, `/samples/stats` (weekly
   totals are a sum of daily ones), CSV and Parquet exports, the archive, the MCP tools.
4. FHIR, following the Physical Activity IG:
   - The two types are Observations coded with their LOINC code, category `activity` plus the
     IG's `PhysicalActivity` category (`http://hl7.org/fhir/us/physical-activity/CodeSystem/pa-temporary-codes`),
     `valueQuantity` in `min`, over the day.
   - Workouts also carry the `PhysicalActivity` category, and their heart rate components carry
     the IG's LOINC codes: `avg_heart_rate` 55425-3, `max_heart_rate` 55426-1.
   - The daily summary's `total_kcal` component carries LOINC 41979-6 (calories burned in 24
     hours), the IG's daily energy measure.
   - CI validates these golden files a second time against the IG's activity measure profile
     (`pa-observation-activity-measure`) with the IG package loaded, on top of the R4 pass.
   - `meta.profile` does not claim the IG profiles (see Decisions).

## Out of scope

- Providers that send no intensity split (Ultrahuman's `active_minutes`, Polar's
  `active_duration`, Apple's exercise time) and sources this version does not read yet
  (Google Health API `active-minutes` levels, Polar activity zone samples). Each is a
  connector change on its own.
- Columns in the daily summary. The figures are samples, read like steps per day.
- The IG's Activity Group panel (`hasMember` grouping a workout's measures as separate
  Observations) and the exercise vital sign (a self-reported days per week and minutes per day
  questionnaire).
- Open mHealth: `physical-activity` describes one activity, not a day's intensity total, and
  no other schema of the catalog fits.

## Standards

- LOINC 77592-4, 77593-2, 55425-3, 55426-1 and 41979-6 are the codes of the IG's activity and
  time measure value sets (`pa-observation-code-activity`, `pa-observation-code-time`).
- UCUM `min` for the minutes, as in the IG's examples.
- The `PhysicalActivity` category is a code of the IG's own temporary code system; a client that
  does not know it still finds the Observations under the HL7 `activity` category.

## Decisions to confirm

- Not claiming the IG profiles in `meta.profile`. The IG derives from US Core 3.1.1: its
  Observations must point at a US Core Patient, which requires a name and a gender, and our
  Patient carries neither by design. Claiming the profiles would also mark the output as US
  realm. The golden files pass the IG profile in CI, so a client can rely on the shape; claiming
  it later is a one-line change if a US Core Patient (name masked with a data absent reason,
  gender `unknown`) is wanted.
- The type names. Adding a type is cheap; renaming one is a breaking change.
