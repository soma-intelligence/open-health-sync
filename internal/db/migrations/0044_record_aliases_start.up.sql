-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Retention prunes record_aliases by start_time per user (internal/etl/automation/retention.go).
-- The table is a plain table, so no chunk exclusion bounds that delete, and neither the primary
-- key nor record_aliases_row has start_time right after (tenant_id, user_id): without this
-- index every run reads all of a user's aliases, about one per stored HealthKit sample.
CREATE INDEX IF NOT EXISTS record_aliases_start ON ohs_data.record_aliases
    (tenant_id, user_id, start_time);
