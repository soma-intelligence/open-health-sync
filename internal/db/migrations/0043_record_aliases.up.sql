-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Source object ids of a stored row. Apple Health de-duplicates by content, so one stored
-- row can stand for several HealthKit samples (two apps writing the same reading, an app
-- that saves a new copy before deleting the old one): each sample UUID that mapped to the
-- row, stored or de-duplicated, keeps one alias here. A deletion of a UUID removes its
-- alias, and the row only when no alias of it remains. start_time bounds the delete on
-- the hypertable. No health values in clear, but the content hash and start of the row:
-- erased with the user and the provider like every table with user_id and provider
-- columns, and with the rows they name by a consent purge.
CREATE TABLE ohs_data.record_aliases (
    tenant_id    uuid        NOT NULL,
    user_id      uuid        NOT NULL,
    provider     text        NOT NULL,
    external_id  text        NOT NULL,
    record_table text        NOT NULL,
    content_hash text        NOT NULL,
    start_time   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, provider, external_id, record_table, content_hash)
);

-- "Does any alias still hold this row?"
CREATE INDEX record_aliases_row ON ohs_data.record_aliases
    (tenant_id, user_id, record_table, content_hash, start_time);
