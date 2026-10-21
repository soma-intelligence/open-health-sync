-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Codes of the two energy types Health Connect sends that no other source had: a source's
-- own total of the energy burned (TotalCaloriesBurned), in kilocalories, and an estimated
-- basal metabolic rate (BasalMetabolicRate), in kilocalories per day. No LOINC code fits
-- either: 41979-6 is a 24 hour total, and the resting metabolic rate codes name another
-- measure.
INSERT INTO ohs_data.quantity_codes
    (canonical_code, loinc_code, snomed_code, ucum_unit, storage_unit, value_type, hot_table, promoted)
VALUES
    ('total_energy_burned', NULL, NULL, 'kcal', 'kcal', 'num', 'ohs_data.quantity_samples', true),
    ('basal_metabolic_rate', NULL, NULL, 'kcal/d', 'kcal/d', 'num', 'ohs_data.quantity_samples', true)
ON CONFLICT (canonical_code) DO UPDATE SET
    loinc_code   = EXCLUDED.loinc_code,
    ucum_unit    = EXCLUDED.ucum_unit,
    storage_unit = EXCLUDED.storage_unit,
    value_type   = EXCLUDED.value_type,
    hot_table    = EXCLUDED.hot_table,
    promoted     = EXCLUDED.promoted,
    updated_at   = now();
