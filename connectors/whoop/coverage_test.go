// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"context"
	"encoding/json"
	"errors"
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

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 3))
	assert.Equal(t, "ab", truncate("abc", 2))
	assert.Empty(t, truncate("abc", 0))
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestReadBody(t *testing.T) {
	b, err := readBody(strings.NewReader("{}"), 2)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(b))
	_, err = readBody(strings.NewReader("{ }"), 2)
	require.EqualError(t, err, "whoop: response exceeds 2 bytes")
	_, err = readBody(failingReader{}, 2)
	require.EqualError(t, err, "connection reset")
}

func TestFlexString(t *testing.T) {
	for in, want := range map[string]string{
		`"ab-12"`: "ab-12", `93845`: "93845", `null`: "", `1.5`: "1.5",
	} {
		var f FlexString
		require.NoError(t, json.Unmarshal([]byte(in), &f), in)
		assert.Equal(t, FlexString(want), f, in)
	}
	for _, in := range []string{`true`, `{}`, `[1]`} {
		var f FlexString
		assert.Error(t, f.UnmarshalJSON([]byte(in)), in)
	}
	var f FlexString
	assert.Error(t, f.UnmarshalJSON([]byte(`"unterminated`)))
}

func TestRecordKey(t *testing.T) {
	for in, want := range map[string]string{
		`{"id":"sleep-1","sleep_id":"x"}`:     "sleep-1",
		`{"id":42}`:                           "42",
		`{"sleep_id":"s-1","cycle_id":93845}`: "s-1/93845",
		`{"cycle_id":93845}`:                  "/93845",
		`{"score":{}}`:                        "",
		`[1]`:                                 "",
		`{"id":true}`:                         "",
	} {
		assert.Equal(t, want, recordKey([]byte(in)), in)
	}
}

func TestPageDay(t *testing.T) {
	from := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		line string
		want time.Time
	}{
		{`{"start":"2026-03-02T23:30:00-05:00"}`, time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)},
		{`{"cycle":{"start":"2026-02-27T08:00:00Z"},"created_at":"2026-02-28T08:00:00Z"}`, time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC)},
		{`{"created_at":"2026-02-28T08:00:00Z"}`, time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)},
		{`{"start":"not a time"}`, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
		{`[]`, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, pageDay([][]byte{[]byte(tc.line)}, from), tc.line)
	}
}

func TestMapWebhook_DeletionWithoutID(t *testing.T) {
	_, err := mapWebhook([]byte(`{"user_id":1,"type":"`+EventWorkoutDeleted+`"}`), testUserID)
	require.EqualError(t, err, "whoop: webhook: "+EventWorkoutDeleted+" without id")
	_, err = mapWebhook([]byte(`{"user_id":true}`), testUserID)
	require.Error(t, err, "a line of the wrong shape")

	res, err := mapWebhook([]byte(`{"user_id":1,"id":"w-1","type":"`+EventWorkoutDeleted+`"}`), testUserID)
	require.NoError(t, err)
	require.Len(t, res.Deletions, 1)
	assert.Equal(t, "w-1", res.Deletions[0].ExternalID)
	assert.Empty(t, res.Deletions[0].ClearSummaryFields, "a workout leaves the day's summary alone")
}

// cycleAPI serves GET /v2/cycle/{id} and /v2/user/measurement/body with the
// status and body set per path.
type cycleAPI struct {
	mu      sync.Mutex
	answers map[string]func(w http.ResponseWriter)
	paths   []string
}

func (a *cycleAPI) start(t *testing.T) *fetch {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.paths = append(a.paths, r.URL.Path)
		answer := a.answers[r.URL.Path]
		a.mu.Unlock()
		if answer == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		answer(w)
	}))
	t.Cleanup(srv.Close)
	c := New(Options{APIBase: srv.URL, HTTPClient: srv.Client(), PageDelay: -1,
		Now:   func() time.Time { return fakeNow },
		Sleep: func(context.Context, time.Duration) error { return nil }})
	return &fetch{c: c, token: "tok", emit: &recordingSink{}, cycles: map[string]cycleRef{}}
}

func (a *cycleAPI) asked() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.paths...)
}

func answer(code int, body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func TestKeepCyclesThenAddCycle(t *testing.T) {
	a := &cycleAPI{}
	f := a.start(t)
	line := []byte(`{"id":93845,"start":"2026-03-01T22:00:00Z","end":"2026-03-02T21:00:00Z","timezone_offset":"-05:00"}`)
	out, err := f.keepCycles(context.Background(), line)
	require.NoError(t, err)
	assert.Equal(t, line, out, "a cycle line is emitted as it is")
	_, err = f.keepCycles(context.Background(), []byte(`{"id":{}}`))
	require.ErrorContains(t, err, "cycle: ")
	_, err = f.keepCycles(context.Background(), []byte(`{"id":7}`))
	require.NoError(t, err)
	assert.NotContains(t, f.cycles, "7", "a cycle without a start is not kept")

	got, err := f.addCycle(context.Background(), []byte(`{"cycle_id":93845,"sleep_id":"s-1"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"cycle":{"start":"2026-03-01T22:00:00Z","end":"2026-03-02T21:00:00Z","timezone_offset":"-05:00"},
		"cycle_id":93845,"sleep_id":"s-1"}`, string(got))
	assert.Empty(t, a.asked(), "a cycle seen in the pull is not asked for")

	got, err = f.addCycle(context.Background(), []byte(`{"sleep_id":"s-2"}`))
	require.NoError(t, err)
	assert.Equal(t, `{"sleep_id":"s-2"}`, string(got), "no cycle id: unchanged")

	_, err = f.addCycle(context.Background(), []byte(`{"cycle_id":[]}`))
	require.ErrorContains(t, err, "recovery: ")
}

func TestAddCycle_AsksForAnUnseenCycle(t *testing.T) {
	a := &cycleAPI{answers: map[string]func(http.ResponseWriter){
		"/v2/cycle/111": answer(200, `{"id":111,"start":"2026-02-27T23:00:00Z","timezone_offset":"+01:00"}`),
		"/v2/cycle/222": answer(200, `{"id":222}`),
		"/v2/cycle/333": answer(http.StatusUnauthorized, ``),
		"/v2/cycle/444": answer(http.StatusBadRequest, ``),
	}}
	f := a.start(t)

	got, err := f.addCycle(context.Background(), []byte(`{"cycle_id":111}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"cycle":{"start":"2026-02-27T23:00:00Z","timezone_offset":"+01:00"},"cycle_id":111}`, string(got))
	_, err = f.addCycle(context.Background(), []byte(`{"cycle_id":111,"sleep_id":"again"}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"/v2/cycle/111"}, a.asked(), "an answer is remembered")

	for _, id := range []string{"222", "404", "444"} {
		line := `{"cycle_id":` + id + `}`
		got, err := f.addCycle(context.Background(), []byte(line))
		require.NoError(t, err, id)
		assert.Equal(t, line, string(got), "cycle %s: the recovery is kept without it", id)
	}

	_, err = f.addCycle(context.Background(), []byte(`{"cycle_id":333}`))
	require.ErrorIs(t, err, errUnauthorized, "a rejected token stops the pull")
}

type failingSink struct{}

func (failingSink) Emit(context.Context, string, time.Time, [][]byte) error {
	return errors.New("disk full")
}

func TestBodySnapshot(t *testing.T) {
	a := &cycleAPI{answers: map[string]func(http.ResponseWriter){
		pathBody: answer(200, `{"height_meter":1.8,"weight_kilogram":80.5,"max_heart_rate":190}`),
	}}
	f := a.start(t)
	sink := &recordingSink{}
	f.emit = sink

	require.NoError(t, f.body(context.Background(), fakeNow.Add(-25*time.Hour)))
	assert.Empty(t, a.asked(), "an old window does not ask for today's figures")

	require.NoError(t, f.body(context.Background(), fakeNow))
	lines := sink.linesOf(DataTypeBody)
	require.Len(t, lines, 1)
	assert.JSONEq(t, `{"snapshot_date":"2026-03-03","height_meter":1.8,"weight_kilogram":80.5,"max_heart_rate":190}`, string(lines[0]))
	assert.Equal(t, fakeNow, sink.calls[0].Day)

	f.emit = failingSink{}
	require.ErrorContains(t, f.body(context.Background(), fakeNow), "whoop: emit body 2026-03-03: disk full")

	cases := map[string]func(http.ResponseWriter){
		"not an object": answer(200, `[1]`),
		"unauthorized":  answer(http.StatusUnauthorized, ``),
	}
	for name, ans := range cases {
		a := &cycleAPI{answers: map[string]func(http.ResponseWriter){pathBody: ans}}
		f := a.start(t)
		assert.Error(t, f.body(context.Background(), fakeNow), name)
	}

	a = &cycleAPI{}
	f = a.start(t)
	require.NoError(t, f.body(context.Background(), fakeNow), "no body figures yet")
	assert.Empty(t, f.emit.(*recordingSink).calls)
}

func TestFatal(t *testing.T) {
	assert.True(t, fatal(errUnauthorized))
	assert.True(t, fatal(context.Canceled))
	assert.True(t, fatal(&connector.RateLimitError{RetryAfter: time.Minute}))
	assert.False(t, fatal(errNotFound))
	assert.False(t, fatal(errors.New("other")))
}
