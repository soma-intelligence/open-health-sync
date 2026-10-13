-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A deletion job erases one end user (provider NULL) or, with provider set, only the data
-- the user holds from that provider: DELETE /v1/users/{id}/data/{provider}, or a disconnect
-- with delete_data=true. The user and their other providers stay.
ALTER TABLE ohs_core.deletion_jobs ADD COLUMN provider text;
