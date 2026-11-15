-- SPDX-License-Identifier: AGPL-3.0-only
ALTER TABLE ohs_core.audit_events DROP COLUMN actor_tenant_id;
DROP TABLE ohs_core.console_sessions;
