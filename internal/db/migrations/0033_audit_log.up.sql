-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Audit log: one row per authenticated API request and per administrative or
-- system action on a tenant's data (who, with which credential, what, about
-- which user, when, with what outcome). Never a health value, a token or a
-- query string. Append only: a trigger refuses UPDATE, DELETE and TRUNCATE,
-- except for the retention job, which sets ohs.audit_prune for its own
-- transaction. Tenant level like the API keys, so it lives in ohs_core and
-- outlives the users it names (by id only): the deletion worker leaves it.
CREATE TABLE ohs_core.audit_events (
    id         bigserial   PRIMARY KEY,
    tenant_id  uuid        NOT NULL,
    user_id    uuid,
    at         timestamptz NOT NULL DEFAULT now(),
    actor_type text        NOT NULL,
    actor_id   text        NOT NULL DEFAULT '',
    action     text        NOT NULL,
    method     text        NOT NULL DEFAULT '',
    route      text        NOT NULL DEFAULT '',
    status     integer,
    request_id text        NOT NULL DEFAULT '',
    detail     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT audit_events_actor_type_check CHECK (actor_type IN ('api_key', 'user_token', 'admin', 'system'))
);
CREATE INDEX audit_events_tenant ON ohs_core.audit_events (tenant_id, id DESC);
CREATE INDEX audit_events_tenant_user ON ohs_core.audit_events (tenant_id, user_id, id DESC);
CREATE INDEX audit_events_at ON ohs_core.audit_events (at);

CREATE FUNCTION ohs_core.audit_events_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('ohs.audit_prune', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'ohs_core.audit_events is append only (% refused)', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;

CREATE TRIGGER audit_events_no_change
    BEFORE UPDATE OR DELETE ON ohs_core.audit_events
    FOR EACH ROW EXECUTE FUNCTION ohs_core.audit_events_append_only();
CREATE TRIGGER audit_events_no_truncate
    BEFORE TRUNCATE ON ohs_core.audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION ohs_core.audit_events_append_only();
