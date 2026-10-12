-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Tombstones of provider records the provider deleted (a sleep the user removed in the
-- provider's app, a deleted workout). The ETL writer deletes the stored rows of the record
-- and keeps one row here, so a later copy of the record (a re-pull, an old raw object
-- processed again, a delete that arrived before the record) is never written. A plain
-- table: it holds ids only, no health values, and is looked up by its primary key.
CREATE TABLE ohs_data.deleted_records (
    tenant_id   uuid        NOT NULL,
    user_id     uuid        NOT NULL,
    provider    text        NOT NULL,
    external_id text        NOT NULL,
    deleted_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, provider, external_id)
);
