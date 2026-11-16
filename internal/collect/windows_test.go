// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

func TestBackfillWindows(t *testing.T) {
	now := time.Date(2026, 10, 10, 13, 5, 7, 500, time.UTC)
	w := backfillWindows(now, connector.DefaultSyncPolicy())
	require.Len(t, w, 5)
	end := now.Truncate(time.Second)
	assert.Equal(t, end.Add(-30*24*time.Hour), w[0][0])
	assert.Equal(t, end, w[4][1])
	assert.Equal(t, 2*24*time.Hour, w[4][1].Sub(w[4][0]))
	for i := 1; i < len(w); i++ {
		assert.Equal(t, w[i-1][1], w[i][0])
	}

	// A provider keeping 28 days, in 14 day chunks.
	w = backfillWindows(now, connector.SyncPolicy{Backfill: 28 * 24 * time.Hour, BackfillChunk: 14 * 24 * time.Hour}.Normalize())
	require.Len(t, w, 2)
	assert.Equal(t, end.Add(-28*24*time.Hour), w[0][0])
	assert.Equal(t, end, w[1][1])
}

func TestRegularWindow(t *testing.T) {
	def := connector.DefaultSyncPolicy()
	slot := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	from, to := regularWindow(slot, PullEvery, def, nil)
	assert.Equal(t, slot, to)
	assert.Equal(t, slot.Add(-7*time.Hour), from)

	recent := slot.Add(-2 * time.Hour)
	from, _ = regularWindow(slot, PullEvery, def, &recent)
	assert.Equal(t, slot.Add(-7*time.Hour), from, "a recent sync does not shrink the window")

	old := slot.Add(-30 * time.Hour)
	from, _ = regularWindow(slot, PullEvery, def, &old)
	assert.Equal(t, old.Add(-time.Hour), from, "after an outage the window reaches back")

	ancient := slot.Add(-60 * 24 * time.Hour)
	from, _ = regularWindow(slot, PullEvery, def, &ancient)
	assert.Equal(t, slot.Add(-7*24*time.Hour), from, "catch up is bounded")
}

func TestRegularWindowFollowsThePolicy(t *testing.T) {
	late := connector.SyncPolicy{Lookback: 6 * time.Hour, Reconcile: 3 * 24 * time.Hour}.Normalize()
	slot := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	from, _ := regularWindow(slot, time.Hour, late, nil)
	assert.Equal(t, slot.Add(-7*time.Hour), from, "hourly pull re-reads the provider's 6 hour lookback")

	synced := slot.Add(-3 * time.Hour)
	from, _ = regularWindow(slot, time.Hour, late, &synced)
	assert.Equal(t, slot.Add(-9*time.Hour), from, "catch-up starts a lookback before the last complete pull")

	midnight := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	from, _ = regularWindow(midnight, time.Hour, late, nil)
	assert.Equal(t, midnight.Add(-3*24*time.Hour), from, "the first slot of the day reconciles")

	from, _ = regularWindow(midnight, time.Hour, connector.DefaultSyncPolicy(), nil)
	assert.Equal(t, midnight.Add(-2*time.Hour), from, "no reconciliation by default")
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

func TestBackfillJobsRunNewestFirst(t *testing.T) {
	now := time.Date(2026, 10, 10, 13, 5, 7, 0, time.UTC)
	tenant, user := uuid.New(), uuid.New()
	jobs := backfillJobs(tenant, user, "oura", connector.DefaultSyncPolicy(), now)
	require.Len(t, jobs, 5)
	assert.Equal(t, now, jobs[0].To, "the most recent chunk is queued first")
	assert.Equal(t, now.Add(-30*24*time.Hour), jobs[4].From)
	for i, j := range jobs {
		assert.Equal(t, queue.TriggerBackfill, j.Trigger)
		assert.Equal(t, user, j.UserID)
		if i > 0 {
			assert.Equal(t, jobs[i-1].From, j.To, "chunks meet without a gap")
		}
	}
}

// policied is a connector with a sync policy of its own.
type policied struct {
	pageOAuth
	p connector.SyncPolicy
}

func (c policied) SyncPolicy() connector.SyncPolicy { return c.p }

func TestBackfillWindowOption(t *testing.T) {
	day := 24 * time.Hour
	registry := map[string]connector.Connector{
		"plain":   pageOAuth{"plain"},
		"long":    policied{pageOAuth{"long"}, connector.SyncPolicy{Backfill: 90 * day}},
		"limited": policied{pageOAuth{"limited"}, connector.SyncPolicy{Backfill: 28 * day, BackfillChunk: 14 * day}},
	}
	d := Deps{Lookup: func(slug string) (connector.Connector, bool) {
		c, ok := registry[slug]
		return c, ok
	}}
	// Unset: each provider's own value, 30 days by default.
	assert.Equal(t, 30*day, d.backfillPolicy("plain").Backfill)
	assert.Equal(t, 90*day, d.backfillPolicy("long").Backfill)
	assert.Equal(t, 28*day, d.backfillPolicy("limited").Backfill)

	// Two years, in chunks that stay the provider's.
	d.BackfillWindow = 730 * day
	for _, slug := range []string{"plain", "long"} {
		p := d.backfillPolicy(slug)
		assert.Equal(t, 730*day, p.Backfill, slug)
		assert.Equal(t, 7*day, p.BackfillChunk, slug)
	}
	assert.Equal(t, 28*day, d.backfillPolicy("limited").Backfill, "a provider that keeps less history keeps its value")
	now := time.Date(2026, 10, 10, 13, 5, 7, 0, time.UTC)
	jobs := backfillJobs(uuid.New(), uuid.New(), "plain", d.backfillPolicy("plain"), now)
	require.Len(t, jobs, 105)
	assert.Equal(t, now.Add(-730*day), jobs[104].From)
	assert.Equal(t, 7*day, jobs[104].To.Sub(jobs[104].From))
	assert.Equal(t, 2*day, jobs[0].To.Sub(jobs[0].From), "the most recent chunk is the partial one")

	// Shorter than the default, and past the bound.
	d.BackfillWindow = 3 * day
	assert.Equal(t, 3*day, d.backfillPolicy("long").Backfill)
	assert.Equal(t, 3*day, d.backfillPolicy("long").BackfillChunk)
	assert.Equal(t, 3*day, d.backfillPolicy("limited").Backfill)
	d.BackfillWindow = 100 * 365 * day
	assert.Equal(t, MaxBackfillWindow, d.backfillPolicy("plain").Backfill)
}

func TestScheduleAfter(t *testing.T) {
	slot := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, slot.Add(time.Hour), scheduleAfter(slot, time.Hour, false, nil))
	assert.Equal(t, slot.Add(6*time.Hour), scheduleAfter(slot, 6*time.Hour, true, nil))
	// Notifications that will count as silent before the slot ends: the
	// connection moves to its short slots then.
	last := slot.Add(2*time.Hour - PushSilence)
	assert.Equal(t, slot.Add(2*time.Hour), scheduleAfter(slot, 6*time.Hour, true, &last))
	recent := slot.Add(-time.Minute)
	assert.Equal(t, slot.Add(6*time.Hour), scheduleAfter(slot, 6*time.Hour, true, &recent))
}

func TestTransientPullError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("oura: GET /v2/usercollection/sleep: HTTP 503"), true},
		{fmt.Errorf("fitbit: %w: /1/user/-/activities: HTTP 500", errors.New("server error")), true},
		{errors.New("token endpoint HTTP 502"), true},
		{errors.New("polar: GET /v3/exercises: HTTP 400"), false},
		{errors.New("strava: /athlete/activities: HTTP 404"), false},
		{errors.New("garmin: decode: unexpected token at offset 5000"), false},
		{errors.New("withings: HTTP 5031"), false},
		{&url.Error{Op: "Get", URL: "https://api.example.test", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}, true},
		{&net.DNSError{Err: "no such host", Name: "api.example.test"}, true},
		{fmt.Errorf("read body: %w", io.ErrUnexpectedEOF), true},
		{context.DeadlineExceeded, true},
		{&url.Error{Op: "Get", URL: "https://api.example.test", Err: context.Canceled}, false},
		{connector.ErrNeedsReauth, false},
		// A response cut off by the transport is the provider's; an empty
		// body a decoder reads as EOF is a response that does not parse.
		{&url.Error{Op: "Get", URL: "https://api.example.test", Err: io.EOF}, true},
		{&net.OpError{Op: "read", Err: syscall.ECONNRESET}, true},
		{io.EOF, false},
		{fmt.Errorf("garmin: decode: %w", io.EOF), false},
		// Only the status the connector itself reports counts, not one the
		// provider's answer happens to quote after it.
		{errors.New("token endpoint HTTP 400: upstream answered HTTP 500"), false},
		{errors.New(`fitbit: HTTP 400: {"errors":[{"message":"HTTP 503 from backend"}]}`), false},
		{errors.New("token endpoint HTTP 502: bad gateway"), true},
		{errors.New("garmin: not granted (HTTP 403)"), false},
	} {
		assert.Equal(t, tc.want, transientPullError(tc.err), "%v", tc.err)
	}
}

// A pull that runs into its own time limit is retried on the long schedule
// a few times only: one that always does is not the provider being down.
func TestJobTimeoutIsTransientOnlyAFewTimes(t *testing.T) {
	timeout := jobTimeoutError{&url.Error{Op: "Get", URL: "https://api.example.test", Err: context.DeadlineExceeded}}
	for attempts := range queue.MaxAttempts {
		want := attempts < jobTimeoutTransientAttempts
		assert.Equal(t, want, retryTransient(queue.PullJob{Attempts: attempts}, timeout), "attempts %d", attempts)
	}
	assert.ErrorIs(t, timeout, context.DeadlineExceeded)
	// Anything else follows transientPullError, whatever the attempts.
	assert.True(t, retryTransient(queue.PullJob{Attempts: 12}, errors.New("oura: GET /sleep: HTTP 503")))
	assert.False(t, retryTransient(queue.PullJob{}, errors.New("oura: GET /sleep: HTTP 400")))
}
