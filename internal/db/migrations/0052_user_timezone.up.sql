-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A user has a time zone. It is the zone of the local day of a sample that carries no zone of
-- its own (a cloud source that reports instants only): the ETL takes the sample's zone, then
-- this one, then UTC.
--
-- timezone is an IANA name ("Europe/Paris"), NULL when none is known. timezone_source says
-- where it came from: 'explicit' when the developer set it through the API, 'learnt' when the
-- ETL took it from a device upload (Apple Health, Health Connect) that carried one. A learnt
-- zone follows the latest record that named a zone (timezone_seen_at is that record's time); an
-- explicit zone is never replaced by a learnt one.
ALTER TABLE ohs_core.end_users
    ADD COLUMN timezone text,
    ADD COLUMN timezone_source text,
    ADD COLUMN timezone_seen_at timestamptz,
    ADD CONSTRAINT end_users_timezone_source_check
        CHECK (timezone_source IN ('explicit', 'learnt')),
    ADD CONSTRAINT end_users_timezone_pair_check
        CHECK ((timezone IS NULL) = (timezone_source IS NULL));
