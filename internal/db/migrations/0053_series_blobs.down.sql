-- SPDX-License-Identifier: AGPL-3.0-only
-- Back to per-point rows only. A blob cannot be turned back into rows in SQL
-- (it is compressed, and sealed with keys), so this refuses while any series
-- is stored as a blob: downgrade only an instance that never wrote one, or
-- accept losing them by emptying ohs_data.series_blobs first.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ohs_data.series_blobs) THEN
        RAISE EXCEPTION 'ohs_data.series_blobs holds series that cannot be turned back into per-point rows; empty it first to accept losing them';
    END IF;
END $$;

DROP TABLE ohs_data.series_blobs;
