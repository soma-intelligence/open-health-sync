-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Time tags: intervals the user or the tenant's app records to give context to the data
-- (an illness, a trip, jet lag, a vaccination). end_at NULL is a tag still ongoing. label
-- and note are free text written by a person: health data, never logged.
--
-- A plain table, not a hypertable: a user has a few tags a month, each is addressed by its
-- id and changed in place (closing an ongoing tag moves its end, a correction its start), and
-- a hypertable would need start_at in the primary key and move a row between chunks on such
-- a change. Lists read the user's tags by (tenant_id, user_id, start_at).
CREATE TABLE ohs_data.time_tags (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid        NOT NULL,
    user_id    uuid        NOT NULL,
    kind       text        NOT NULL,
    label      text,
    start_at   timestamptz NOT NULL,
    end_at     timestamptz,
    timezone   text,
    note       text,
    source     text        NOT NULL DEFAULT 'user',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT time_tags_kind_check CHECK (kind IN
        ('illness', 'injury', 'travel', 'jet_lag', 'vaccination', 'medication', 'alcohol', 'other')),
    CONSTRAINT time_tags_end_check CHECK (end_at IS NULL OR end_at > start_at),
    CONSTRAINT time_tags_label_check CHECK (char_length(label) BETWEEN 1 AND 100),
    CONSTRAINT time_tags_timezone_check CHECK (char_length(timezone) BETWEEN 1 AND 64),
    CONSTRAINT time_tags_note_check CHECK (char_length(note) BETWEEN 1 AND 2000),
    CONSTRAINT time_tags_source_check CHECK (char_length(source) BETWEEN 1 AND 64)
);
CREATE INDEX time_tags_user_start ON ohs_data.time_tags (tenant_id, user_id, start_at, id);
