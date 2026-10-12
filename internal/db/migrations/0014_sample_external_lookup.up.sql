-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A provider deletion (ohs_data.deleted_records) removes the quantity and category samples
-- stored under the record's id or "<id>_<suffix>", whenever they start, and a Replaceable
-- quantity sample whose start moved supersedes the stored copy of the same id. Both look the
-- samples up by provider and external_id, with no time bound: these partial indexes keep that
-- off a scan of every chunk. external_id is indexed in the "C" collation so that the prefix
-- match ("<id>_" up to, not including, "<id>`") is a plain byte-order range scan whatever the
-- database's collation.
CREATE INDEX quantity_samples_external ON ohs_data.quantity_samples
    (tenant_id, user_id, provider, (external_id COLLATE "C"))
    WHERE external_id IS NOT NULL;
CREATE INDEX category_samples_external ON ohs_data.category_samples
    (tenant_id, user_id, provider, (external_id COLLATE "C"))
    WHERE external_id IS NOT NULL;
