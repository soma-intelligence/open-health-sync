-- SPDX-License-Identifier: AGPL-3.0-only
DROP INDEX ohs_core.webhook_deliveries_endpoint_running;
DROP INDEX ohs_core.webhook_deliveries_endpoint_due;
ALTER TABLE ohs_core.webhook_endpoints
    DROP COLUMN secret_previous_expires_at,
    DROP COLUMN secret_previous_sealed,
    DROP COLUMN paused_until,
    DROP COLUMN consecutive_failures;
