-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Codes of the activity intensity types declared in internal/model: the minutes of a day a
-- provider classed as moderate or vigorous activity (Oura daily_activity, Withings
-- getactivity), stored in minutes. LOINC 77592-4 and 77593-2 are the codes the HL7 Physical
-- Activity IG uses for these measures.
INSERT INTO ohs_data.quantity_codes
    (canonical_code, loinc_code, snomed_code, ucum_unit, storage_unit, value_type, hot_table, promoted)
VALUES
    ('moderate_activity_time', '77592-4', NULL, 'min', 'min', 'num', 'ohs_data.quantity_samples', true),
    ('vigorous_activity_time', '77593-2', NULL, 'min', 'min', 'num', 'ohs_data.quantity_samples', true)
ON CONFLICT (canonical_code) DO UPDATE SET
    loinc_code   = EXCLUDED.loinc_code,
    ucum_unit    = EXCLUDED.ucum_unit,
    storage_unit = EXCLUDED.storage_unit,
    value_type   = EXCLUDED.value_type,
    hot_table    = EXCLUDED.hot_table,
    promoted     = EXCLUDED.promoted,
    updated_at   = now();
