// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullEveryFollowsNotifications(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	recent, old := now.Add(-time.Hour), now.Add(-25*time.Hour)
	assert.True(t, pushHealthy(true, &recent, now))
	assert.False(t, pushHealthy(true, &old, now), "silent for over PushSilence")
	assert.False(t, pushHealthy(true, nil, now), "never notified")
	assert.False(t, pushHealthy(false, &recent, now), "provider sends none")
	assert.Equal(t, PullEvery, pullEvery(true))
	assert.Equal(t, PullEveryWithoutPush, pullEvery(false))
}

func TestOAuthFreshnessStates(t *testing.T) {
	now := time.Date(2026, 10, 10, 15, 5, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }

	f := oauthFreshness(freshRow{provider: "p", status: StatusActive, syncedThrough: at(5 * time.Minute)}, false, now)
	assert.Equal(t, FreshnessFresh, f.State)
	assert.Equal(t, int64(300), *f.LagSeconds)
	assert.Equal(t, int64(3600), f.PullEverySeconds)
	assert.Equal(t, time.Date(2026, 10, 10, 16, 0, 0, 0, time.UTC), *f.NextPullAt)

	f = oauthFreshness(freshRow{provider: "p", status: StatusActive, syncedThrough: at(3*time.Hour + time.Second)}, false, now)
	assert.Equal(t, FreshnessStale, f.State, "hourly: stale past two intervals plus the overlap")

	f = oauthFreshness(freshRow{provider: "p", status: StatusActive, syncedThrough: at(10 * time.Hour), lastEvent: at(time.Hour)}, true, now)
	assert.Equal(t, FreshnessFresh, f.State, "6 hourly while notifications arrive")
	assert.True(t, f.PushHealthy)
	assert.Equal(t, time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC), *f.NextPullAt)

	f = oauthFreshness(freshRow{provider: "p", status: StatusActive, syncedThrough: at(time.Minute), failures: FailingAfter}, false, now)
	assert.Equal(t, FreshnessFailing, f.State)

	f = oauthFreshness(freshRow{provider: "p", status: StatusActive}, false, now)
	assert.Equal(t, FreshnessSyncing, f.State)
	assert.Nil(t, f.LagSeconds)

	f = oauthFreshness(freshRow{provider: "p", status: StatusNeedsReauth, syncedThrough: at(time.Minute)}, false, now)
	assert.Equal(t, FreshnessNeedsReauth, f.State)
	assert.Nil(t, f.NextPullAt, "not pulled any more")
}

func TestDeviceFreshness(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	f := deviceFreshness("apple_health", now.Add(-2*time.Hour), now)
	assert.Equal(t, FreshnessFresh, f.State)
	assert.Equal(t, KindDevice, f.Kind)
	assert.Equal(t, int64(7200), *f.LagSeconds)
	assert.Equal(t, FreshnessStale, deviceFreshness("apple_health", now.Add(-DeviceStaleAfter-time.Second), now).State)
}

func TestWriteSyncMetrics(t *testing.T) {
	var b bytes.Buffer
	WriteSyncMetrics(&b, []ProviderSync{{
		Provider: "withings", States: map[string]int64{FreshnessFresh: 3, FreshnessFailing: 1},
		MaxLagSeconds: 4200, PushSilent: 2,
	}})
	out := b.String()
	require.Contains(t, out, "# TYPE ohs_sync_connections gauge\n")
	assert.Contains(t, out, `ohs_sync_connections{provider="withings",state="fresh"} 3`)
	assert.Contains(t, out, `ohs_sync_connections{provider="withings",state="stale"} 0`)
	assert.Contains(t, out, `ohs_sync_connections{provider="withings",state="failing"} 1`)
	assert.Contains(t, out, `ohs_sync_max_lag_seconds{provider="withings"} 4200`)
	assert.Contains(t, out, `ohs_sync_push_silent_connections{provider="withings"} 2`)
}
