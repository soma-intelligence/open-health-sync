-- SPDX-License-Identifier: AGPL-3.0-only
-- Provider rate limit budget.
--
-- Most providers limit an application as a whole, not one user: when one
-- pull meets a rate limit, every pull of that provider waits. paused_until
-- is when pulls of the provider resume; consecutive counts the limits met
-- since the last successful pull, and doubles the pause each time. The table
-- is about providers, not users, so it lives in ohs_core.
CREATE TABLE ohs_core.provider_limits (
    provider     text        PRIMARY KEY,
    paused_until timestamptz NOT NULL,
    consecutive  integer     NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Backfills go after every other pull (priority 2): a hundred new users
-- backfilling 30 days each must not hold back the regular pulls of everyone
-- else.
UPDATE ohs_sync.pull_jobs SET priority = 2 WHERE trigger = 'backfill' AND status = 'pending';
