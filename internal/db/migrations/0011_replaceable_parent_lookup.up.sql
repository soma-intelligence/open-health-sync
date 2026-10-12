-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A Replaceable sleep session or workout supersedes the stored copies of the same provider
-- record under another start (the ETL writer deletes them by provider and external_id).
-- These partial indexes keep that lookup off a scan of every chunk.
CREATE INDEX sleep_sessions_external ON ohs_data.sleep_sessions (tenant_id, user_id, provider, external_id)
    WHERE external_id IS NOT NULL;
CREATE INDEX workouts_external ON ohs_data.workouts (tenant_id, user_id, provider, external_id)
    WHERE external_id IS NOT NULL;
