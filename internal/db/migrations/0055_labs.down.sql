-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Dropping the lab columns loses the ids that let an amended result replace the stored one;
-- the lab rows themselves stay in ohs_data.observations.
DROP INDEX IF EXISTS ohs_data.observations_user_report;
DROP INDEX IF EXISTS ohs_data.observations_user_external;
ALTER TABLE ohs_data.observations
    DROP COLUMN IF EXISTS captured_at,
    DROP COLUMN IF EXISTS report_id,
    DROP COLUMN IF EXISTS external_id;
