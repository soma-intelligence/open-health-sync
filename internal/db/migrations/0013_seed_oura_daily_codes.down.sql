-- SPDX-License-Identifier: AGPL-3.0-only
DELETE FROM ohs_data.quantity_codes WHERE canonical_code IN (
    'sleep_score',
    'stress_high_time',
    'recovery_high_time'
);
