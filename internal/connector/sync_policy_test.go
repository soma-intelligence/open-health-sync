// SPDX-License-Identifier: AGPL-3.0-only

package connector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type policied struct {
	Connector
	p SyncPolicy
}

func (c policied) SyncPolicy() SyncPolicy { return c.p }

func TestSyncPolicyOfDefaults(t *testing.T) {
	assert.Equal(t, DefaultSyncPolicy(), SyncPolicyOf(nil))
	assert.Equal(t, DefaultSyncPolicy(), SyncPolicyOf(policied{}), "every zero field takes the default")
}

func TestSyncPolicyNormalize(t *testing.T) {
	d := DefaultSyncPolicy()
	p := SyncPolicyOf(policied{p: SyncPolicy{
		PullEvery:            7 * time.Hour, // does not divide a day
		PullEveryWithoutPush: 5 * time.Minute,
		Lookback:             72 * time.Hour,
		MaxCatchUp:           90 * 24 * time.Hour,
		Backfill:             90 * 24 * time.Hour,
		BackfillChunk:        100 * 24 * time.Hour,
		Reconcile:            30 * 24 * time.Hour,
	}})
	assert.Equal(t, d.PullEvery, p.PullEvery)
	assert.Equal(t, d.PullEveryWithoutPush, p.PullEveryWithoutPush)
	assert.Equal(t, 72*time.Hour, p.Lookback)
	assert.Equal(t, d.MaxCatchUp, p.MaxCatchUp)
	assert.Equal(t, 90*24*time.Hour, p.Backfill)
	assert.Equal(t, d.BackfillChunk, p.BackfillChunk, "a chunk longer than the backfill")
	assert.Zero(t, p.Reconcile, "an out of bounds reconciliation is turned off")

	p = SyncPolicy{PullEvery: 12 * time.Hour, PullEveryWithoutPush: 3 * time.Hour, Reconcile: 7 * 24 * time.Hour}.Normalize()
	assert.Equal(t, 12*time.Hour, p.PullEvery)
	assert.Equal(t, 3*time.Hour, p.PullEveryWithoutPush)
	assert.Equal(t, 7*24*time.Hour, p.Reconcile)
}
