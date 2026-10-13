// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rrJSON struct {
	Timestamp time.Time `json:"timestamp"`
	RRMs      float64   `json:"rr_ms"`
	SeriesID  string    `json:"series_id"`
	Provider  string    `json:"provider"`
}

type hrvJSON struct {
	SeriesID  string    `json:"series_id"`
	Provider  string    `json:"provider"`
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
	Intervals int       `json:"intervals"`
	Accepted  int       `json:"accepted_intervals"`
	Pairs     int       `json:"successive_pairs"`
	MeanRRMs  *float64  `json:"mean_rr_ms"`
	MeanHR    *float64  `json:"mean_heart_rate_bpm"`
	SDNNMs    *float64  `json:"sdnn_ms"`
	RMSSDMs   *float64  `json:"rmssd_ms"`
	PNN50     *float64  `json:"pnn50_percent"`
}

// beats writes one heartbeat series of n intervals alternating 790 and 810
// ms, its first beat at start, and returns the time of its last beat.
func (e *env) beats(tn, u uuid.UUID, series string, start time.Time, n int) time.Time {
	e.t.Helper()
	at := start
	for i := range n {
		rr := 790.0
		if i%2 == 1 {
			rr = 810
		}
		if i > 0 {
			at = at.Add(time.Duration(rr) * time.Millisecond)
		}
		e.exec(`INSERT INTO ohs_data.heartbeat_samples (tenant_id, user_id, provider, series_id, "timestamp", rr_ms, content_hash)
			VALUES ($1, $2, 'apple_health', $3, $4, $5, $6)`, tn, u, series, at, rr, fmt.Sprintf("%s-%d", series, i))
	}
	return at
}

func TestRRIntervalsAndHRV(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// "late-night" starts before the window and ends inside it; "morning"
	// and "evening" start inside it.
	e.beats(e.tenantA, u, "late-night", day.Add(-30*time.Second), 60)
	e.beats(e.tenantA, u, "morning", day.Add(7*time.Hour), 30)
	last := e.beats(e.tenantA, u, "evening", day.Add(23*time.Hour+59*time.Minute+50*time.Second), 30)
	require.True(t, last.After(day.Add(24*time.Hour)), "the evening series runs past the window")
	base := "/v1/users/" + u.String()

	// RR intervals page by beat time without skipping or repeating one.
	var got []rrJSON
	cursor := ""
	for pages := 0; ; pages++ {
		require.Less(t, pages, 20)
		path := base + "/rr-intervals?series_id=morning&limit=7"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		r := e.do("GET", path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		var pg page[rrJSON]
		r.json(t, &pg)
		got = append(got, pg.Data...)
		if pg.NextCursor == nil {
			break
		}
		cursor = *pg.NextCursor
	}
	require.Len(t, got, 30)
	for i := 1; i < len(got); i++ {
		assert.True(t, got[i].Timestamp.After(got[i-1].Timestamp))
		assert.Equal(t, "morning", got[i].SeriesID)
	}

	// The beats of a window, whatever their series.
	r := e.do("GET", base+"/rr-intervals?start=2026-03-01&end=2026-03-02&limit=1000", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	var win page[rrJSON]
	r.json(t, &win)
	assert.Greater(t, len(win.Data), 30, "the late series' beats after midnight are in the window")

	// HRV: a series is in the window when its first beat is, and is summarized whole.
	r = e.do("GET", base+"/hrv?start=2026-03-01&end=2026-03-02", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var hrv page[hrvJSON]
	r.json(t, &hrv)
	require.Len(t, hrv.Data, 2)
	assert.Equal(t, "morning", hrv.Data[0].SeriesID)
	assert.Equal(t, "evening", hrv.Data[1].SeriesID)
	ev := hrv.Data[1]
	assert.Equal(t, 30, ev.Intervals)
	assert.Equal(t, 29, ev.Pairs, "the first beat has no predecessor")
	assert.True(t, ev.EndDate.Equal(last))
	require.NotNil(t, ev.RMSSDMs)
	assert.InDelta(t, 20, *ev.RMSSDMs, 1e-9)
	require.NotNil(t, ev.MeanHR)

	// Paged one series at a time, the same series in the same order.
	var ids []string
	cursor = ""
	for {
		path := base + "/hrv?limit=1"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		r := e.do("GET", path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		var pg page[hrvJSON]
		r.json(t, &pg)
		for _, s := range pg.Data {
			ids = append(ids, s.SeriesID)
		}
		if pg.NextCursor == nil {
			break
		}
		cursor = *pg.NextCursor
	}
	assert.Equal(t, []string{"late-night", "morning", "evening"}, ids)

	// One series by id.
	r = e.do("GET", base+"/hrv?series_id=late-night", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	r.json(t, &hrv)
	require.Len(t, hrv.Data, 1)
	assert.Equal(t, 60, hrv.Data[0].Intervals)

	// Caller mistakes and other tenants.
	for _, p := range []string{"/rr-intervals?cursor=nope", "/hrv?cursor=nope", "/hrv?limit=0",
		"/rr-intervals?start=2026-03-02&end=2026-03-01"} {
		r := e.do("GET", base+p, e.keyA, nil)
		assert.Equal(t, http.StatusBadRequest, r.code, p)
	}
	for _, p := range []string{"/rr-intervals", "/hrv"} {
		r := e.do("GET", base+p, e.keyB, nil)
		assert.Equal(t, http.StatusNotFound, r.code, p)
		assert.NotContains(t, string(r.body), "morning")
	}
	ub := e.user(e.tenantB, "b-1")
	r = e.do("GET", "/v1/users/"+ub.String()+"/hrv", e.keyB, nil)
	require.Equal(t, http.StatusOK, r.code)
	r.json(t, &hrv)
	assert.Empty(t, hrv.Data, "tenant B's user sees none of tenant A's beats")
}
