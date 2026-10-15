// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestFloatField(t *testing.T) {
	m := map[string]any{
		"f": 1.5, "i": 7, "i64": int64(-3), "num": json.Number("2.25"),
		"badnum": json.Number("x"), "s": "4", "nil": nil,
	}
	cases := []struct {
		key  string
		want float64
		ok   bool
	}{
		{"f", 1.5, true},
		{"i", 7, true},
		{"i64", -3, true},
		{"num", 2.25, true},
		{"badnum", 0, false},
		{"s", 0, false},
		{"nil", 0, false},
		{"missing", 0, false},
	}
	for _, tc := range cases {
		got, ok := floatField(m, tc.key)
		assert.Equal(t, tc.ok, ok, tc.key)
		assert.InDelta(t, tc.want, got, 1e-9, tc.key)
	}
	assert.Nil(t, optFloat(m, "s"))
	require.NotNil(t, optFloat(m, "i"))
	assert.InDelta(t, 7.0, *optFloat(m, "i"), 1e-9)
}

func TestDecodeNamesTheDataType(t *testing.T) {
	var a Activity
	err := decode(DataTypeActivity, []byte(`{"date":5}`), &a)
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "withings: activity: "), err.Error())
	require.NoError(t, decode(DataTypeActivity, []byte(`{"date":"2026-03-01"}`), &a))
	assert.Equal(t, "2026-03-01", a.Date)

	for _, dt := range []string{DataTypeActivity, DataTypeIntraday, DataTypeECG, DataTypeSleep, DataTypeWorkout, DataTypeMeasure} {
		_, err := mapLine(t, dt, `{"date":{"nested":true},"timestamp":"soon","startdate":[1],"enddate":"x","measures":7}`)
		assert.ErrorContains(t, err, "withings: "+dt, dt)
	}
}

func TestPager_TruncatesAtMaxPages(t *testing.T) {
	c := New(Options{PageDelay: -1})
	calls := 0
	var offsets []int
	err := c.pager(context.Background(), "getactivity", "a", "b", func(offset int) (int, bool, int, error) {
		calls++
		if calls <= 2 {
			offsets = append(offsets, offset)
		}
		return 1, true, calls * 10, nil
	})
	require.NoError(t, err, "a truncated window is not an error")
	assert.Equal(t, maxPagesPerChunk, calls)
	assert.Equal(t, []int{0, 10}, offsets, "each page asks for the offset the last one gave")
}

func TestPager_StopsOnCancel(t *testing.T) {
	c := New(Options{PageDelay: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := c.pager(ctx, "getactivity", "a", "b", func(int) (int, bool, int, error) {
		calls++
		cancel()
		return 1, true, 1, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls)
}

func TestPause(t *testing.T) {
	require.NoError(t, pause(context.Background(), -1))
	require.NoError(t, pause(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, pause(ctx, 0), context.Canceled)
	start := time.Now()
	require.ErrorIs(t, pause(ctx, time.Hour), context.Canceled)
	assert.Less(t, time.Since(start), time.Second)
}

// measureServer answers /v2/measure with answer(action, form offset) and
// records the forms it got.
type measureServer struct {
	mu    sync.Mutex
	forms []map[string]string
}

func (s *measureServer) start(t *testing.T, answer func(form map[string]string) string) *Connector {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		s.mu.Lock()
		s.forms = append(s.forms, form)
		s.mu.Unlock()
		_, _ = w.Write([]byte(answer(form)))
	}))
	t.Cleanup(srv.Close)
	return New(Options{APIBase: srv.URL, HTTPClient: srv.Client(), ChunkDelay: -1, PageDelay: -1,
		Now: func() time.Time { return fakeNow }})
}

func (s *measureServer) requests() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]string(nil), s.forms...)
}

func TestFetchActivity_PagesAndChunks(t *testing.T) {
	s := &measureServer{}
	c := s.start(t, func(form map[string]string) string {
		if form["offset"] == "" {
			return `{"status":0,"body":{"activities":[{"date":"` + form["startdateymd"] + `","steps":100}],"more":true,"offset":1}}`
		}
		return `{"status":0,"body":{"activities":[{"date":"` + form["enddateymd"] + `","steps":200}],"more":false}}`
	})
	// 40 days: two windows of at most 30 days, two pages each.
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out, err := c.fetchActivity(context.Background(), "tok", from, from.AddDate(0, 0, 40))
	require.NoError(t, err)
	reqs := s.requests()
	require.Len(t, reqs, 4)
	assert.Equal(t, "2026-01-01", reqs[0]["startdateymd"])
	assert.Equal(t, "2026-01-30", reqs[0]["enddateymd"])
	assert.Equal(t, "1", reqs[1]["offset"])
	assert.Equal(t, "2026-01-31", reqs[2]["startdateymd"])
	assert.Equal(t, "2026-02-09", reqs[2]["enddateymd"], "the exclusive end does not pull the next day")
	assert.Equal(t, "getactivity", reqs[0]["action"])
	assert.Contains(t, reqs[0]["data_fields"], "steps")

	require.Len(t, out, 4)
	wantDays := []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 9, 0, 0, 0, 0, time.UTC),
	}
	for i, dl := range out {
		assert.Equal(t, wantDays[i], dl.day, i)
	}
	assert.JSONEq(t, `{"date":"2026-01-01","steps":100}`, string(out[0].line))
}

func TestFetchActivity_BadAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"bad date":      `{"status":0,"body":{"activities":[{"date":"01/03/2026"}]}}`,
		"date a number": `{"status":0,"body":{"activities":[{"date":20260301}]}}`,
		"not an object": `{"status":0,"body":{"activities":"none"}}`,
		"api status":    `{"status":503}`,
	} {
		s := &measureServer{}
		c := s.start(t, func(map[string]string) string { return body })
		_, err := c.fetchActivity(context.Background(), "tok", fetchFrom, fetchTo)
		assert.Error(t, err, name)
	}
	s := &measureServer{}
	c := s.start(t, func(map[string]string) string { return `{"status":401}` })
	_, err := c.fetchActivity(context.Background(), "tok", fetchFrom, fetchTo)
	require.ErrorIs(t, err, errUnauthorized)
}

func TestFetchIntraday_SortsSeriesAndAddsTimestamp(t *testing.T) {
	s := &measureServer{}
	c := s.start(t, func(form map[string]string) string {
		if form["startdate"] != "1772323200" { // 2026-03-01T00:00:00Z
			return `{"status":0,"body":{"series":{}}}`
		}
		return `{"status":0,"body":{"series":{
			"1772326800":{"steps":20},
			"1772323260":{"steps":10,"heart_rate":61},
			"1772409540":{"steps":5}}}}`
	})
	out, err := c.fetchIntraday(context.Background(), "tok", fetchFrom, fetchTo)
	require.NoError(t, err)
	reqs := s.requests()
	require.Len(t, reqs, 2, "one call per 24 h")
	assert.Equal(t, "getintradayactivity", reqs[0]["action"])
	assert.Equal(t, "1772409600", reqs[0]["enddate"])
	assert.Equal(t, "1772409600", reqs[1]["startdate"])

	require.Len(t, out, 3)
	var ts []int64
	for _, dl := range out {
		var p IntradayPoint
		require.NoError(t, json.Unmarshal(dl.line, &p))
		ts = append(ts, p.Timestamp)
	}
	assert.Equal(t, []int64{1772323260, 1772326800, 1772409540}, ts, "in time order")
	assert.Equal(t, day(1), out[0].day)
	assert.Equal(t, day(1), out[2].day, "23:59 UTC stays on its day")
	assert.JSONEq(t, `{"steps":10,"heart_rate":61,"timestamp":1772323260}`, string(out[0].line))

	// The mapped point keeps the timestamp the fetch added.
	res := mustMap(t, DataTypeIntraday, string(out[0].line))
	require.NotEmpty(t, res.QuantitySamples)
	assert.Equal(t, time.Unix(1772323260, 0).UTC(), res.QuantitySamples[0].StartDate)
}

func TestFetchIntraday_KeyNotAnEpoch(t *testing.T) {
	s := &measureServer{}
	c := s.start(t, func(map[string]string) string {
		return `{"status":0,"body":{"series":{"noon":{"steps":1}}}}`
	})
	_, err := c.fetchIntraday(context.Background(), "tok", fetchFrom, fetchFrom.Add(time.Hour))
	require.ErrorContains(t, err, `intraday key "noon" is not an epoch`)
}

func TestFetchChunksStopOnCancel(t *testing.T) {
	s := &measureServer{}
	c := s.start(t, func(map[string]string) string { return `{"status":0,"body":{}}` })
	c.chunkDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.fetchIntraday(ctx, "tok", fetchFrom, fetchTo)
	require.ErrorIs(t, err, context.Canceled)
	_, err = c.fetchActivity(ctx, "tok", fetchFrom, fetchFrom.AddDate(0, 0, 61))
	require.ErrorIs(t, err, context.Canceled)
}

func TestNotify_Answers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		{"ok", http.StatusOK, `{"status":0,"body":{}}`, func(t *testing.T, err error) { require.NoError(t, err) }},
		{"unauthorized", http.StatusUnauthorized, ``, func(t *testing.T, err error) {
			require.ErrorIs(t, err, connector.ErrNeedsReauth)
			require.ErrorIs(t, err, errUnauthorized)
		}},
		{"server error", http.StatusBadGateway, ``, func(t *testing.T, err error) { require.EqualError(t, err, "notify HTTP 502") }},
		{"not json", http.StatusOK, `<html>`, func(t *testing.T, err error) { require.ErrorContains(t, err, "notify: decode answer") }},
		{"api status", http.StatusOK, `{"status":293}`, func(t *testing.T, err error) { require.EqualError(t, err, "notify status 293") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *http.Request
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(Options{APIBase: srv.URL, HTTPClient: srv.Client()})
			tc.check(t, c.notify(context.Background(), "tok", testCallback, "subscribe", 44))
			require.NotNil(t, got)
			assert.Equal(t, http.MethodPost, got.Method)
			assert.Equal(t, notifyPath, got.URL.Path)
			assert.Equal(t, "44", got.URL.Query().Get("appli"))
			assert.Equal(t, testCallback, got.URL.Query().Get("callbackurl"))
		})
	}
}

func TestNotify_TransportErrorHidesTheCallback(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	c := New(Options{APIBase: base})
	err := c.notify(context.Background(), "tok", testCallback+"?sig=secret-signature", "subscribe", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "notify request")
	assert.NotContains(t, err.Error(), "secret-signature")
	assert.NotContains(t, err.Error(), "callbackurl")
}
