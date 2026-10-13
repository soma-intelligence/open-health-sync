-- SPDX-License-Identifier: AGPL-3.0-only
-- A user's own order of sources for a metric, a group of metrics or every
-- metric ('*'). The merged read view ranks overlapping records by it; with
-- no row, the built-in defaults of internal/fusion apply. Raw rows are never
-- touched: the order only decides which source a merged read shows.
CREATE TABLE ohs_sync.source_priorities (
    tenant_id  uuid        NOT NULL,
    user_id    uuid        NOT NULL,
    metric     text        NOT NULL,
    sources    text[]      NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, metric),
    CONSTRAINT source_priorities_sources_check CHECK (cardinality(sources) BETWEEN 1 AND 50)
);
