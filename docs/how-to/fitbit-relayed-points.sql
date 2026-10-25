-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Repairs what the Fitbit (Google Health) connector stored before it told
-- relayed Apple Health and Health Connect points apart. See
-- fitbit-relayed-points.md next to this file before running it.
--
--   psql "$OHS_DATABASE_URL" -v ON_ERROR_STOP=1 -f docs/how-to/fitbit-relayed-points.sql
--
-- It is idempotent: a second run changes nothing. It touches provider fitbit
-- rows of ohs_data.quantity_samples only.

BEGIN;

-- 1. Apple's HRV is SDNN, stored as hrv_rmssd. A copy the fixed connector has
--    already stored again as hrv_sdnn (same user, same time) is removed, so the
--    conversion below does not leave two.
DELETE FROM ohs_data.quantity_samples r
WHERE r.provider = 'fitbit'
  AND r.quantity_type = 'hrv_rmssd'
  AND upper(r.metadata->>'platform') = 'HEALTH_KIT'
  AND EXISTS (
    SELECT 1 FROM ohs_data.quantity_samples s
    WHERE s.tenant_id = r.tenant_id AND s.user_id = r.user_id
      AND s.provider = 'fitbit' AND s.quantity_type = 'hrv_sdnn'
      AND s.start_date = r.start_date
      AND upper(s.metadata->>'platform') = 'HEALTH_KIT');

-- 2. The other copies become hrv_sdnn. A sample that carried a standard
--    deviation takes it as its value, as the connector now reads it.
UPDATE ohs_data.quantity_samples
SET quantity_type = 'hrv_sdnn',
    value = CASE
      WHEN (metadata->>'standard_deviation_ms')::double precision > 0
        THEN (metadata->>'standard_deviation_ms')::double precision
      ELSE value END,
    metadata = (metadata - 'standard_deviation_ms') || '{"hrv_method": "sdnn"}'::jsonb
WHERE provider = 'fitbit'
  AND quantity_type = 'hrv_rmssd'
  AND upper(metadata->>'platform') = 'HEALTH_KIT';

-- 3. A deep sleep RMSSD of 0 is no measurement (HEALTH_KIT points carry it).
UPDATE ohs_data.quantity_samples
SET metadata = metadata - 'deep_sleep_rmssd_ms'
WHERE provider = 'fitbit'
  AND metadata ? 'deep_sleep_rmssd_ms'
  AND (metadata->>'deep_sleep_rmssd_ms')::double precision <= 0;

-- 4. Relayed points are attributed to the store they came from, so they are a
--    stream of their own and fusion ranks them against that store's data.
UPDATE ohs_data.quantity_samples
SET source_slug = 'apple_health', source_name = 'Apple Health'
WHERE provider = 'fitbit'
  AND upper(metadata->>'platform') = 'HEALTH_KIT'
  AND (source_slug IS DISTINCT FROM 'apple_health' OR source_name IS DISTINCT FROM 'Apple Health');

UPDATE ohs_data.quantity_samples
SET source_slug = 'health_connect', source_name = 'Health Connect'
WHERE provider = 'fitbit'
  AND upper(metadata->>'platform') = 'HEALTH_CONNECT'
  AND (source_slug IS DISTINCT FROM 'health_connect' OR source_name IS DISTINCT FROM 'Health Connect');

COMMIT;
