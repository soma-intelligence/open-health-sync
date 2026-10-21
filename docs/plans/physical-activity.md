# Plan: activity intensity minutes and the HL7 Physical Activity IG

Spec: [specs/physical-activity.md](../specs/physical-activity.md).

1. Tests first: the Oura and Withings mappings (values, seconds to minutes, zero kept, missing
   figure, day identity, replaceable), the range rules, the consent category, the FHIR mapping
   of the two types and of the workout and daily summary codes (golden files).
2. Catalog: the two types in `internal/model` (name, storage unit `min`, seconds conversion),
   migration 0039 seeding `quantity_codes` with LOINC 77592-4 and 77593-2, range rules in
   `internal/etl`, the activity consent category in `internal/consent`.
3. Connectors: `connectors/oura/daily.go` and `connectors/withings/activity.go` emit the two
   samples next to the existing ones.
4. FHIR: the two types in the `activity` category, the `PhysicalActivity` category on them and
   on workouts, the LOINC codes on the workout heart rate components and on `total_kcal`;
   golden files regenerated and reviewed.
5. Validation: `scripts/fhir-validate.sh` runs a second pass on the Physical Activity golden
   files with `-ig hl7.fhir.us.physical-activity#1.0.0 -profile` the activity measure profile.
6. Docs: `docs/fhir.md`, the Oura and Withings connector guides, regenerated
   `docs/data-model.md`, CHANGELOG.
