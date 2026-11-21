-- SPDX-License-Identifier: AGPL-3.0-only
--
-- The heartbeats are transient; the next start of each process writes them again.
DROP TABLE IF EXISTS ohs_core.process_heartbeats;
