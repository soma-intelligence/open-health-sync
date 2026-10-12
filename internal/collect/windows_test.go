// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestBackfillWindows(t *testing.T) {
	now := time.Date(2026, 10, 10, 13, 5, 7, 500, time.UTC)
	w := backfillWindows(now)
	require.Len(t, w, 5)
	end := now.Truncate(time.Second)
	assert.Equal(t, end.Add(-30*24*time.Hour), w[0][0])
	assert.Equal(t, end, w[4][1])
	assert.Equal(t, 2*24*time.Hour, w[4][1].Sub(w[4][0]))
	for i := 1; i < len(w); i++ {
		assert.Equal(t, w[i-1][1], w[i][0])
	}
}

func TestRegularWindow(t *testing.T) {
	slot := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	from, to := regularWindow(slot, PullEvery, nil)
	assert.Equal(t, slot, to)
	assert.Equal(t, slot.Add(-7*time.Hour), from)

	recent := slot.Add(-2 * time.Hour)
	from, _ = regularWindow(slot, PullEvery, &recent)
	assert.Equal(t, slot.Add(-7*time.Hour), from, "a recent sync does not shrink the window")

	old := slot.Add(-30 * time.Hour)
	from, _ = regularWindow(slot, PullEvery, &old)
	assert.Equal(t, old.Add(-time.Hour), from, "after an outage the window reaches back")

	ancient := slot.Add(-60 * 24 * time.Hour)
	from, _ = regularWindow(slot, PullEvery, &ancient)
	assert.Equal(t, slot.Add(-7*24*time.Hour), from, "catch up is bounded")
}

func TestInboundWindow(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	from, to := inboundWindow(connector.InboundEvent{}, now)
	assert.Equal(t, now, to)
	assert.Equal(t, now.Add(-24*time.Hour), from)

	at := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	from, to = inboundWindow(connector.InboundEvent{From: at, To: at}, now)
	assert.Equal(t, at, from)
	assert.Equal(t, at.Add(time.Second), to, "an instant becomes a one second window")

	from, to = inboundWindow(connector.InboundEvent{From: at.Add(-90 * 24 * time.Hour), To: at}, now)
	assert.Equal(t, at, to)
	assert.Equal(t, at.Add(-30*24*time.Hour), from, "a window is at most 30 days")
}

func TestSafeErr(t *testing.T) {
	err := errors.New(`Get "https://api.example.test/v2/data?access_token=secret&from=1": status 500: ` + strings.Repeat("body ", 100))
	got := safeErr(err)
	assert.NotContains(t, got, "secret")
	assert.NotContains(t, got, "?")
	assert.Contains(t, got, "https://api.example.test/v2/data")
	assert.LessOrEqual(t, len(got), maxErrText)
	assert.Equal(t, "", safeErr(nil))
	assert.LessOrEqual(t, len(safeErr(errors.New(strings.Repeat("é", 300)))), maxErrText)
}
