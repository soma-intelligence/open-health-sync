-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Consent per end user, per source (provider) and per data category. A user
-- with rows for a source gets only the granted categories stored from it; a
-- source without rows stores everything unless OHS_CONSENT_REQUIRED is set.
-- consent_events is the history (who changed what, when, with what evidence);
-- consent_purges queues the deletion of a revoked category's stored records,
-- carried out by the etl role. All three go with the user on erasure.
CREATE TABLE ohs_sync.consents (
    tenant_id  uuid        NOT NULL,
    user_id    uuid        NOT NULL,
    provider   text        NOT NULL,
    category   text        NOT NULL,
    granted    boolean     NOT NULL,
    granted_at timestamptz,
    revoked_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, provider, category)
);

CREATE TABLE ohs_sync.consent_events (
    id        bigserial   PRIMARY KEY,
    tenant_id uuid        NOT NULL,
    user_id   uuid        NOT NULL,
    provider  text        NOT NULL,
    category  text        NOT NULL,
    action    text        NOT NULL,
    at        timestamptz NOT NULL DEFAULT now(),
    actor_id  text        NOT NULL DEFAULT '',
    evidence  text        NOT NULL DEFAULT '',
    CONSTRAINT consent_events_action_check CHECK (action IN ('granted', 'revoked'))
);
CREATE INDEX consent_events_user ON ohs_sync.consent_events (tenant_id, user_id, id DESC);

CREATE TABLE ohs_sync.consent_purges (
    id           bigserial   PRIMARY KEY,
    tenant_id    uuid        NOT NULL,
    user_id      uuid        NOT NULL,
    provider     text        NOT NULL,
    category     text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz,
    rows_deleted bigint
);
CREATE INDEX consent_purges_pending ON ohs_sync.consent_purges (id) WHERE finished_at IS NULL;
CREATE INDEX consent_purges_user ON ohs_sync.consent_purges (tenant_id, user_id);
