-- SPDX-License-Identifier: AGPL-3.0-only
--
-- The event stream (GET /v1/events/stream). Every event a webhook can carry is also written
-- here, once, in the transaction that produced it, whatever the endpoints of the tenant: the
-- same payload, thin, never a health value. The transaction then sends a NOTIFY on the
-- channel ohs_events carrying only "tenant_id:seq"; each api process listening loads the row
-- for the streams of that tenant. Rows are kept 24 hours, the window a client resumes in with
-- Last-Event-ID, and are pruned by the etl role. A user's erasure removes their rows except the
-- user.deleted event that announces it.
CREATE TABLE ohs_core.stream_events (
    seq        bigserial   PRIMARY KEY,
    tenant_id  uuid        NOT NULL REFERENCES ohs_core.tenants (id) ON DELETE CASCADE,
    user_id    uuid,
    event_id   uuid        NOT NULL,
    event_type text        NOT NULL,
    payload    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX stream_events_event ON ohs_core.stream_events (tenant_id, event_id);
CREATE INDEX stream_events_tenant_seq ON ohs_core.stream_events (tenant_id, seq);
CREATE INDEX stream_events_user ON ohs_core.stream_events (tenant_id, user_id) WHERE user_id IS NOT NULL;
CREATE INDEX stream_events_created ON ohs_core.stream_events (created_at);
