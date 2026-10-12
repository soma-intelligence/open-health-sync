-- SPDX-License-Identifier: AGPL-3.0-only
DELETE FROM ohs_data.quantity_codes WHERE canonical_code IN (
    'hrv_rmssd'
);
