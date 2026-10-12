-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Storage unit of hrv_rmssd, declared in internal/model/units.go with the WHOOP connector:
-- WHOOP reports HRV as RMSSD, a different statistic from the SDNN hrv_sdnn holds.
INSERT INTO ohs_data.quantity_codes
    (canonical_code, loinc_code, snomed_code, ucum_unit, storage_unit, value_type, hot_table, promoted)
VALUES
    ('hrv_rmssd', NULL, NULL, 'ms', 'ms', 'num', 'ohs_data.quantity_samples', true)
ON CONFLICT (canonical_code) DO UPDATE SET
    ucum_unit    = EXCLUDED.ucum_unit,
    storage_unit = EXCLUDED.storage_unit,
    value_type   = EXCLUDED.value_type,
    hot_table    = EXCLUDED.hot_table,
    promoted     = EXCLUDED.promoted,
    updated_at   = now();
