-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Webhook sandbox. One row per delivery attempt, written by the worker in the
-- transaction that settles the delivery, so a developer can see every try
-- (status code, error, timing, the start of the receiver's answer) and not
-- only the last one. The worker keeps the last 20 per delivery. Rows go with
-- their delivery: an endpoint's deletion, the 30-day prune and a user's
-- erasure (which deletes the user's deliveries) cascade here.
CREATE TABLE ohs_core.webhook_attempts (
    id                    bigserial   PRIMARY KEY,
    tenant_id             uuid        NOT NULL,
    delivery_id           bigint      NOT NULL REFERENCES ohs_core.webhook_deliveries (id) ON DELETE CASCADE,
    endpoint_id           uuid        NOT NULL,
    attempt               integer     NOT NULL,
    started_at            timestamptz NOT NULL,
    duration_ms           integer     NOT NULL,
    status_code           integer,
    error                 text,
    response_body_excerpt text,
    created_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_attempts_delivery ON ohs_core.webhook_attempts (delivery_id, id);

-- A replay is a new delivery of the same event (same event_id and payload);
-- replay_of names the delivery it copied, and caps the replays queued per
-- endpoint.
ALTER TABLE ohs_core.webhook_deliveries ADD COLUMN replay_of bigint;
CREATE INDEX webhook_deliveries_replays ON ohs_core.webhook_deliveries (endpoint_id)
    WHERE replay_of IS NOT NULL AND status IN ('pending', 'running');
