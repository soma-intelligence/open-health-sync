-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Outgoing webhooks. An endpoint is a tenant's URL with the event types it wants
-- (empty: all of them) and a signing secret sealed with OHS_ENCRYPTION_KEY. A
-- delivery is one event for one endpoint: written in the transaction that
-- produced the event, then posted by the etl role with retries.
CREATE TABLE ohs_core.webhook_endpoints (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid        NOT NULL REFERENCES ohs_core.tenants (id) ON DELETE CASCADE,
    url             text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    event_types     text[]      NOT NULL DEFAULT '{}',
    secret_sealed   bytea       NOT NULL,
    status          text        NOT NULL DEFAULT 'active',
    disabled_reason text,
    failing_since   timestamptz,
    last_success_at timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT webhook_endpoints_status_check CHECK (status IN ('active', 'disabled'))
);
CREATE INDEX webhook_endpoints_tenant ON ohs_core.webhook_endpoints (tenant_id) WHERE status = 'active';

-- Tenant-level like the endpoints (a test event names no user), so it lives in
-- ohs_core; the deletion worker removes a deleted user's rows by user_id.
CREATE TABLE ohs_core.webhook_deliveries (
    id           bigserial   PRIMARY KEY,
    tenant_id    uuid        NOT NULL,
    endpoint_id  uuid        NOT NULL REFERENCES ohs_core.webhook_endpoints (id) ON DELETE CASCADE,
    user_id      uuid,
    event_id     uuid        NOT NULL,
    event_type   text        NOT NULL,
    payload      jsonb       NOT NULL,
    status       text        NOT NULL DEFAULT 'pending',
    attempts     integer     NOT NULL DEFAULT 0,
    not_before   timestamptz NOT NULL DEFAULT now(),
    claimed_at   timestamptz,
    last_error   text,
    last_status  integer,
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    CONSTRAINT webhook_deliveries_status_check CHECK (status IN ('pending', 'running', 'done', 'parked'))
);
CREATE INDEX webhook_deliveries_due ON ohs_core.webhook_deliveries (not_before, id)
    WHERE status IN ('pending', 'running');
CREATE INDEX webhook_deliveries_endpoint ON ohs_core.webhook_deliveries (endpoint_id, id DESC);
CREATE INDEX webhook_deliveries_user ON ohs_core.webhook_deliveries (tenant_id, user_id);
