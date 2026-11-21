-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Dropping the tables loses every setting saved in the console; the
-- environment values keep applying.
DROP TABLE IF EXISTS ohs_core.instance_settings_revision;
DROP TABLE IF EXISTS ohs_core.instance_settings;
