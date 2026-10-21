# Plan: nutrition from Health Connect, and nutrition in FHIR

Spec: [specs/nutrition-health-connect-fhir.md](../specs/nutrition-health-connect-fhir.md).

1. Nutrient units: the nutrient table in `internal/model` (name, unit, LOINC where one fits) and
   its conversion; the Apple Health single sample and food entry mappers go through it. Tests
   first: kg, g, mg and mcg masses, L and fluid ounce volumes, kJ, an unknown unit refused, a
   food entry in kilograms.
2. Catalog: `total_energy_burned` and `basal_metabolic_rate` in `internal/model` (storage units
   `kcal` and `kcal/d`), migration 0040 seeding `quantity_codes`, range rules, consent
   categories, the FHIR activity table.
3. Health Connect: the four record types in `connectors/healthconnect` (record fields, nutrition
   log builder, the two samples), with tests for each line, a missing value, a meal type, a
   re-sent record and a moved start.
4. Writer: nutrition logs supersede their stored copy under another start (as samples do), and
   the derived daily summary takes `total_kcal` from `total_energy_burned`; integration tests.
5. FHIR: `read.KindNutrition` in the merged observation list, the record route and its id,
   `fhir.FromNutritionLog`, the record type search; golden files and the HL7 validator in CI.
6. Docs: `docs/connectors/health-connect.md`, `docs/connectors/apple-health.md` (units),
   `docs/fhir.md`, `docs/api.md`, `api/openapi.yaml` and the server SDK comments it generates,
   regenerated `docs/data-model.md`, CHANGELOG.
