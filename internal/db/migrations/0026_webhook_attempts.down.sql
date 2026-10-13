-- SPDX-License-Identifier: AGPL-3.0-only
DROP INDEX ohs_core.webhook_deliveries_replays;
ALTER TABLE ohs_core.webhook_deliveries DROP COLUMN replay_of;
DROP TABLE ohs_core.webhook_attempts;
