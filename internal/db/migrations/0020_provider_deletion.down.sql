-- SPDX-License-Identifier: AGPL-3.0-only
-- A provider job left without its provider would read as the erasure of the whole user.
DELETE FROM ohs_core.deletion_jobs WHERE provider IS NOT NULL;
ALTER TABLE ohs_core.deletion_jobs DROP COLUMN provider;
