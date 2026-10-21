# Spec: nutrition from Health Connect, and nutrition in FHIR

Status: draft for review (2026-10-17).

## Problem

Nutrition logs (`GET /v1/users/{id}/nutrition`, the exports, the MCP tool `get_nutrition`) are
fed by Apple Health only. Health Connect carries the same data (`NutritionRecord`,
`HydrationRecord`) and the energy figures Android apps write (`TotalCaloriesBurnedRecord`,
`BasalMetabolicRateRecord`), but the server does not map them, so the Kotlin reader cannot read
them either (its spec defers them "because the server does not map them yet").

FHIR serves none of the nutrition logs: a FHIR client sees what a person burned but never what
they ate or drank.

The extended nutrients (`nutrients` in the REST output, every nutrient other than the seven
columns) have no declared unit. The Apple Health mapper stores each one in the unit the line
gives (a value from an export in milligrams, from an older app build in kilograms), and the
mapper of food entries (`HKCorrelationTypeIdentifierFood`) does not convert the core masses to
the unit of their column, as the mapper of single dietary samples does.

## Scope

1. **One unit per nutrient.** `internal/model` declares the unit of every nutrient: kilocalories
   for energy, grams for the macronutrients, milligrams or micrograms for the others (the units
   the iOS SDK already sends), millilitres for water. Every mapper converts to it (mass between
   kg, g, mg and mcg; volume between L, mL and US and imperial fluid ounces; energy between kcal
   and kJ) and refuses a unit it cannot convert, as the core columns do today. The Apple Health
   food entry mapper goes through the same conversion as the single samples. The table is
   documented, so `nutrients.caffeine` means milligrams for every row written from now on.
2. **Health Connect**, four record types:

   | `recordType` | Value fields | Canonical records |
   |---|---|---|
   | `Nutrition` | `energyKilocalories`, `energyFromFatKilocalories`, every mass as `<field>Grams` (`proteinGrams`, `totalCarbohydrateGrams`, `sodiumGrams`, `vitaminDGrams`...), `name`, `mealType` | one nutrition log |
   | `Hydration` | `volumeLiters` | one nutrition log with `nutrients.water` in mL |
   | `TotalCaloriesBurned` | `energyKilocalories` | `total_energy_burned` (kcal) |
   | `BasalMetabolicRate` | `basalMetabolicRateKilocaloriesPerDay` | `basal_metabolic_rate` (kcal/d) |

   Masses go up in grams (`Mass.inGrams`) whatever the nutrient, and the server converts them to
   the nutrient's unit. `mealType` is `breakfast`, `lunch`, `dinner`, `snack` or `unknown` (no
   meal type); `name` goes to `metadata.food_name`. A log is identified by its record id, like
   the samples: a re-sent record replaces the stored figures, a record whose start moved
   replaces its older copy, and a `Deletion` line removes it.
3. **Two quantity types** (migration 0040): `total_energy_burned` (kcal, summed per day) and
   `basal_metabolic_rate` (kcal/d, a rate, not summed). Neither has a LOINC code that fits:
   41979-6 is a 24 hour total (it codes the daily summary's `total_kcal`), and the resting
   metabolic rate codes (69429-9, 82286-6) name another measure. The derived daily summary takes
   `total_kcal` from the day's `total_energy_burned` when a source sends it, and from basal plus
   active energy otherwise, as today.
4. **FHIR**: each nutrition log becomes an Observation `nutrition-<id>`:
   - `code`: the record type `nutrition_log`, text `food and drink`. LOINC has no code for one
     food entry; its intake panels (75284-0, 81895-5) group 24 hour totals.
   - `effectivePeriod` (or `effectiveDateTime` when start and end are equal).
   - one component per figure, coded by its REST field (`energy_kcal`, `protein_g`,
     `nutrients.caffeine`...) and, where LOINC has a point in time code, by LOINC: energy 9052-2
     (Calorie intake total), protein 9079-5, carbohydrates 9059-7 and fat 9066-2 (intake,
     estimated), water 8999-5 (Fluid intake oral, estimated). Quantities in UCUM (`kcal`, `g`,
     `mg`, `ug`, `mL`).
   - the meal type as a component with a `valueCodeableConcept`.
   - no category: none of the HL7 observation categories describes intake.
   Nutrition Observations are in the Observation search, `$everything` and the record route;
   `code=.../CodeSystem/record-type|nutrition_log` finds them. Like ECG recordings they have no
   merged view: two apps logging one meal are two entries.

## Out of scope

- Rewriting the extended nutrients of rows stored before this change. Their unit is the one the
  line gave; the docs say so.
- Daily intake totals (the LOINC 24 hour intake codes) and an Open mHealth mapping.
- Google Health API nutrition (a restricted scope) and other providers.
- The Kotlin reader reading these four types: a one line change per type once this ships
  (#132).

## Standards

- LOINC 9052-2, 9079-5, 9059-7, 9066-2 and 8999-5 are point in time codes ("Pt") with mass,
  energy or volume properties, the shape of one entry; the Finnish PHR profiles code
  self-reported intake with the same codes.
- UCUM `kcal`, `g`, `mg`, `ug`, `mL`, `kcal/d`.

## Decisions to confirm

- No HL7 category on the nutrition Observations rather than a project code system category.
- `basal_metabolic_rate` filed under the body consent category (smart scales estimate it),
  `total_energy_burned` under activity.
- The type names `total_energy_burned` and `basal_metabolic_rate`.
