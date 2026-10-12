-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Storage units of the quantity names the Oura connector adds, declared in
-- internal/model/units.go: the daily sleep score (an index, like readiness_score) and the
-- minutes of a day classed as high stress or high recovery.
INSERT INTO ohs_data.quantity_codes
    (canonical_code, loinc_code, snomed_code, ucum_unit, storage_unit, value_type, hot_table, promoted)
VALUES
    ('sleep_score', NULL, NULL, NULL, 'score', 'num', 'ohs_data.quantity_samples', true),
    ('stress_high_time', NULL, NULL, 'min', 'min', 'num', 'ohs_data.quantity_samples', true),
    ('recovery_high_time', NULL, NULL, 'min', 'min', 'num', 'ohs_data.quantity_samples', true)
ON CONFLICT (canonical_code) DO UPDATE SET
    ucum_unit    = EXCLUDED.ucum_unit,
    storage_unit = EXCLUDED.storage_unit,
    value_type   = EXCLUDED.value_type,
    hot_table    = EXCLUDED.hot_table,
    promoted     = EXCLUDED.promoted,
    updated_at   = now();
