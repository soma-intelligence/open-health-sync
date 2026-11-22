-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Server sessions of the web console. A person signs in once with a tenant
-- API key; the server keeps a session row and the browser holds a cookie
-- with the session token and a CSRF token. Only the SHA-256 hex of each
-- token is stored, never the token and never the API key. A session ends
-- after 12 hours without a request, 7 days after it was created, when its
-- API key is revoked or expires, or when its tenant (or its acting tenant)
-- is deleted. acting_tenant_id is the tenant the console currently shows: it
-- starts as the key's tenant, and an instance admin may point it at another
-- one. read_only sessions cannot write. Ended rows are removed by a daily job.
-- audit_events.actor_tenant_id records who acted when that differs from the
-- tenant the event belongs to.
CREATE TABLE ohs_core.console_sessions (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash       text        NOT NULL,
    tenant_id        uuid        NOT NULL REFERENCES ohs_core.tenants (id) ON DELETE CASCADE,
    api_key_id       uuid        NOT NULL REFERENCES ohs_core.api_keys (id) ON DELETE CASCADE,
    acting_tenant_id uuid        NOT NULL REFERENCES ohs_core.tenants (id) ON DELETE CASCADE,
    read_only        boolean     NOT NULL,
    csrf_hash        text        NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_seen_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT console_sessions_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX console_sessions_api_key ON ohs_core.console_sessions (api_key_id);
CREATE INDEX console_sessions_last_seen ON ohs_core.console_sessions (last_seen_at);

ALTER TABLE ohs_core.audit_events ADD COLUMN actor_tenant_id uuid;
