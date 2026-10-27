// SPDX-License-Identifier: AGPL-3.0-only

package connector

import "time"

// SyncPolicy is how the collect role pulls one provider: how often, how far
// back each pull re-reads for late or revised data, how far a new connection
// is backfilled, and how far a daily reconciliation pull reaches. Providers
// differ a lot here (Dexcom readings reach the API hours late, Polar keeps
// 28 days, Suunto allows a few hundred calls a week), so each connector can
// declare its own with SyncPolicied. A zero field takes the default.
type SyncPolicy struct {
	// PullEvery is the regular pull interval while the provider's
	// notifications arrive: the pull is only a backstop for a lost one.
	PullEvery time.Duration
	// PullEveryWithoutPush is the interval when the provider sends no
	// notifications, or none arrived lately.
	PullEveryWithoutPush time.Duration
	// Lookback is how far before the previous window each regular pull
	// starts again, for data that reaches the provider late (a ring synced
	// the next morning, a cuff uploaded days later) or is revised (a sleep
	// re-scored).
	Lookback time.Duration
	// MaxCatchUp bounds how far back a regular pull reaches after an outage.
	MaxCatchUp time.Duration
	// Backfill is how far back a new connection is pulled, in jobs of
	// BackfillChunk.
	Backfill, BackfillChunk time.Duration
	// Reconcile, when set, widens the first pull of each UTC day to the last
	// Reconcile, so data revised or delivered later than Lookback, or a
	// window whose pull failed, is fetched again. Zero turns it off.
	Reconcile time.Duration
}

// SyncPolicied is optionally implemented by an OAuthConnector whose provider
// needs another sync policy than DefaultSyncPolicy. See SyncPolicyOf.
type SyncPolicied interface {
	SyncPolicy() SyncPolicy
}

// Policy bounds. An interval must divide a day so that slots line up on UTC
// midnight, where reconciliation runs.
const (
	minPullEvery    = 15 * time.Minute
	maxLookback     = 7 * 24 * time.Hour
	maxCatchUpLimit = 30 * 24 * time.Hour
	maxBackfill     = 365 * 24 * time.Hour
	maxReconcile    = 14 * 24 * time.Hour
	maxBackfillJobs = 100
)

// DefaultSyncPolicy is the policy of a connector that declares none.
func DefaultSyncPolicy() SyncPolicy {
	return SyncPolicy{
		PullEvery:            6 * time.Hour,
		PullEveryWithoutPush: time.Hour,
		Lookback:             time.Hour,
		MaxCatchUp:           7 * 24 * time.Hour,
		Backfill:             30 * 24 * time.Hour,
		BackfillChunk:        7 * 24 * time.Hour,
	}
}

// SyncPolicyOf is c's sync policy: the one it declares, with every zero or
// out of bounds field replaced by its default (Reconcile stays off when
// zero), so the scheduler can rely on it.
func SyncPolicyOf(c Connector) SyncPolicy {
	p, ok := c.(SyncPolicied)
	if !ok {
		return DefaultSyncPolicy()
	}
	return p.SyncPolicy().Normalize()
}

// Normalize returns p with every zero or out of bounds field replaced by its
// default value.
func (p SyncPolicy) Normalize() SyncPolicy {
	d := DefaultSyncPolicy()
	interval := func(v, def time.Duration) time.Duration {
		if v < minPullEvery || v > 24*time.Hour || (24*time.Hour)%v != 0 {
			return def
		}
		return v
	}
	bounded := func(v, def, lo, hi time.Duration) time.Duration {
		if v < lo || v > hi {
			return def
		}
		return v
	}
	p.PullEvery = interval(p.PullEvery, d.PullEvery)
	p.PullEveryWithoutPush = interval(p.PullEveryWithoutPush, d.PullEveryWithoutPush)
	p.Lookback = bounded(p.Lookback, d.Lookback, time.Minute, maxLookback)
	p.MaxCatchUp = bounded(p.MaxCatchUp, d.MaxCatchUp, time.Hour, maxCatchUpLimit)
	p.Backfill = bounded(p.Backfill, d.Backfill, time.Hour, maxBackfill)
	p.BackfillChunk = bounded(p.BackfillChunk, d.BackfillChunk, time.Hour, p.Backfill)
	if p.Backfill/p.BackfillChunk > maxBackfillJobs {
		p.BackfillChunk = p.Backfill / maxBackfillJobs
	}
	// Catch-up must at least cover a regular window, else its floor would
	// cut every pull short.
	if base := max(p.PullEvery, p.PullEveryWithoutPush) + p.Lookback; p.MaxCatchUp < base {
		p.MaxCatchUp = base
	}
	if p.Reconcile != 0 {
		p.Reconcile = bounded(p.Reconcile, 0, time.Hour, maxReconcile)
	}
	return p
}
