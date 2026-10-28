// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/connectors/dexcom"
	"github.com/soma-intelligence/open-health-sync/connectors/garmin"
)

func TestSyncJSON(t *testing.T) {
	// Dexcom declares its own policy: 6 h lookback, 90 day backfill and a
	// 2 day reconciliation; the other fields take the defaults.
	got := syncJSON(dexcom.New(dexcom.Options{}))
	require.NotNil(t, got)
	assert.Equal(t, adminSyncJSON{
		PullEverySeconds: 6 * 3600, PullEveryWithoutPushSeconds: 3600,
		LookbackSeconds: 6 * 3600, MaxCatchUpSeconds: 7 * 86400,
		BackfillSeconds: 90 * 86400, BackfillChunkSeconds: 7 * 86400,
		ReconcileSeconds: 2 * 86400,
	}, *got)

	// Garmin declares none: the defaults, reconciliation off.
	got = syncJSON(garmin.New(garmin.Options{}))
	require.NotNil(t, got)
	assert.Equal(t, int64(30*86400), got.BackfillSeconds)
	assert.Equal(t, int64(3600), got.LookbackSeconds)
	assert.Zero(t, got.ReconcileSeconds)

	// Apple Health is uploaded by an app, never pulled.
	assert.Nil(t, syncJSON(applehealth.New()))
}
