-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Webhook delivery fairness and secret rotation overlap.
--
-- consecutive_failures counts an endpoint's failed attempts since its last
-- success. Past the worker's threshold the endpoint is paused: nothing is
-- claimed for it before paused_until, then one delivery at a time probes it
-- until one succeeds.
--
-- secret_previous_sealed is the signing secret a rotation replaced, sealed as
-- secret_sealed is (same key, same associated data). Deliveries are signed
-- with both secrets until secret_previous_expires_at.
ALTER TABLE ohs_core.webhook_endpoints
    ADD COLUMN consecutive_failures       integer NOT NULL DEFAULT 0,
    ADD COLUMN paused_until               timestamptz,
    ADD COLUMN secret_previous_sealed     bytea,
    ADD COLUMN secret_previous_expires_at timestamptz;

-- The claim takes each endpoint's due deliveries oldest first and counts the
-- ones in flight, so one endpoint's backlog is never scanned for another.
CREATE INDEX webhook_deliveries_endpoint_due ON ohs_core.webhook_deliveries (endpoint_id, not_before, id)
    WHERE status IN ('pending', 'running');
CREATE INDEX webhook_deliveries_endpoint_running ON ohs_core.webhook_deliveries (endpoint_id)
    WHERE status = 'running';
