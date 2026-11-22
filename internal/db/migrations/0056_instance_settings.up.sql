-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Runtime settings the admin console saves (docs/specs/console-v2.md).
--
-- instance_settings holds one row per OHS_* setting an admin changed. name is
-- the variable name; it is checked against the settings registry in Go, not
-- here, so a release can add or drop a setting without a migration. A clear
-- setting keeps its text in value; a secret keeps it in sealed, sealed with
-- OHS_ENCRYPTION_KEY and bound to name, so a sealed value copied to another
-- row does not open. Exactly one of the two is set. updated_by is the id of
-- the admin tenant that saved the row.
--
-- instance_settings_revision is a single row. revision rises by one in the
-- transaction of every write to instance_settings; a running process compares
-- it with the revision it loaded and reloads the settings that can change live.
-- restart_revision is the revision of the last write to a setting that needs a
-- restart, and changed_at the time of the last write.
CREATE TABLE ohs_core.instance_settings (
    name       text        PRIMARY KEY,
    value      text,
    sealed     bytea,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid        NOT NULL,
    CONSTRAINT instance_settings_value_or_sealed CHECK ((value IS NULL) <> (sealed IS NULL))
);

CREATE TABLE ohs_core.instance_settings_revision (
    id               boolean     PRIMARY KEY DEFAULT true CHECK (id),
    revision         bigint      NOT NULL DEFAULT 0,
    restart_revision bigint      NOT NULL DEFAULT 0,
    changed_at       timestamptz NOT NULL DEFAULT now()
);

INSERT INTO ohs_core.instance_settings_revision (id, revision, restart_revision, changed_at)
VALUES (true, 0, 0, now());
