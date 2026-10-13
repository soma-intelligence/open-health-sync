-- SPDX-License-Identifier: AGPL-3.0-only
UPDATE ohs_sync.pull_jobs SET priority = 1 WHERE priority = 2;
DROP TABLE ohs_core.provider_limits;
