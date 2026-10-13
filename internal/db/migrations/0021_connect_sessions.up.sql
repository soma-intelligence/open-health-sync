-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Hosted connection page sessions. A tenant creates one for a user; the link
-- it gets back carries a random token that is the only credential of the
-- page, so only its SHA-256 is stored (like OAuth states). A session serves
-- several providers until it expires. It is a credential like a user token,
-- not provider data, so it lives in ohs_core and goes with its user.
CREATE TABLE ohs_core.connect_sessions (
    token_hash   text        PRIMARY KEY,
    tenant_id    uuid        NOT NULL,
    user_id      uuid        NOT NULL,
    providers    text[]      NOT NULL,
    redirect_uri text,
    app_name     text,
    logo_url     text,
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connect_sessions_user_fk FOREIGN KEY (tenant_id, user_id)
        REFERENCES ohs_core.end_users (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX connect_sessions_expires ON ohs_core.connect_sessions (expires_at);
CREATE INDEX connect_sessions_user ON ohs_core.connect_sessions (tenant_id, user_id);
