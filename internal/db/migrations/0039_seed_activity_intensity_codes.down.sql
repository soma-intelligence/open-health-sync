-- SPDX-License-Identifier: AGPL-3.0-only
DELETE FROM ohs_data.quantity_codes WHERE canonical_code IN (
    'moderate_activity_time',
    'vigorous_activity_time'
);
