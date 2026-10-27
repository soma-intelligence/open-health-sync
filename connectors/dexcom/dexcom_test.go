// SPDX-License-Identifier: AGPL-3.0-only

package dexcom

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

const (
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret"
	testUser         = "00000000-0000-0000-0000-000000000001"
)

var fakeNow = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

// fakeDexcom is an httptest Dexcom API: the token endpoint and the EGV
// endpoint, with the answers a test sets.
type fakeDexcom struct {
	srv *httptest.Server

	mu         sync.Mutex
	tokenForms []url.Values
	egvQueries []url.Values
	tokenCode  int
	tokenBody  string
	egvCode    int
	records    []map[string]any
}

func newFake(t *testing.T) *fakeDexcom {
	t.Helper()
	f := &fakeDexcom{tokenCode: 200, egvCode: 200,
		tokenBody: `{"access_token":"acc-2","expires_in":7200,"token_type":"Bearer","refresh_token":"ref-2","refresh_expires_in":0}`}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		f.mu.Lock()
		f.tokenForms = append(f.tokenForms, r.PostForm)
		code, body := f.tokenCode, f.tokenBody
		f.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("GET /v3/users/self/egvs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.egvQueries = append(f.egvQueries, r.URL.Query())
		if r.Header.Get("Authorization") != "Bearer acc-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.egvCode != 200 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(f.egvCode)
			return
		}
		start, _ := time.Parse(queryLayout, r.URL.Query().Get("startDate"))
		end, _ := time.Parse(queryLayout, r.URL.Query().Get("endDate"))
		var recs []map[string]any
		for _, rec := range f.records {
			at, _, _ := parseTime(rec["systemTime"].(string))
			if !at.Before(start) && at.Before(end) {
				recs = append(recs, rec)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"recordType": "egv", "recordVersion": "3.0", "userId": "u-1", "records": recs})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDexcom) connector() *Connector {
	return New(Options{
		ClientID: testClientID, ClientSecret: testClientSecret,
		APIBase: f.srv.URL, HTTPClient: f.srv.Client(), ChunkDelay: -1,
		Now:   func() time.Time { return fakeNow },
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
}

type sink struct {
	calls map[time.Time][][]byte
	order []time.Time
}

func (s *sink) Emit(_ context.Context, dt string, day time.Time, lines [][]byte) error {
	if dt != DataTypeEGV {
		return errors.New("unexpected data type " + dt)
	}
	if s.calls == nil {
		s.calls = map[time.Time][][]byte{}
	}
	s.calls[day] = append(s.calls[day], lines...)
	s.order = append(s.order, day)
	return nil
}

func egvRecord(id, system, display string, value any) map[string]any {
	return map[string]any{
		"recordId": id, "systemTime": system, "displayTime": display,
		"transmitterId": "hashed-tx", "transmitterTicks": 1000, "value": value,
		"status": nil, "trend": "flat", "trendRate": 0.5, "unit": "mg/dL", "rateUnit": "mg/dL/min",
		"displayDevice": "iOS", "transmitterGeneration": "g7", "displayApp": "G7",
	}
}

func TestAuthorizeURL(t *testing.T) {
	c := New(Options{ClientID: testClientID, ClientSecret: testClientSecret})
	u, err := c.AuthorizeURL("st", "https://ohs.example/callback")
	require.NoError(t, err)
	p, err := url.Parse(u)
	require.NoError(t, err)
	require.Equal(t, "api.dexcom.com", p.Host)
	require.Equal(t, "/v3/oauth2/login", p.Path)
	q := p.Query()
	require.Equal(t, testClientID, q.Get("client_id"))
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, "offline_access", q.Get("scope"))
	require.Equal(t, "st", q.Get("state"))
	require.Equal(t, "https://ohs.example/callback", q.Get("redirect_uri"))

	_, err = New(Options{}).AuthorizeURL("st", "x")
	require.ErrorIs(t, err, errNotConfigured)
}

func TestConfigureRegion(t *testing.T) {
	c := New(Options{})
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "a", KeyClientSecret: "b", KeyRegion: "eu"}))
	require.Equal(t, "https://api.dexcom.eu", c.base())
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "a", KeyClientSecret: "b", KeyRegion: "sandbox"}))
	require.Equal(t, "https://sandbox-api.dexcom.com", c.base())
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "a", KeyClientSecret: "b"}))
	require.Equal(t, "https://api.dexcom.com", c.base())
	require.Error(t, c.Configure(connector.Config{KeyClientID: "a", KeyClientSecret: "b", KeyRegion: "mars"}))
	require.Error(t, c.Configure(connector.Config{KeyClientID: "a"}))
	require.True(t, c.Enabled(connector.Config{KeyClientID: "a", KeyClientSecret: "b"}))
	require.False(t, c.Enabled(connector.Config{KeyClientID: "a"}))
}

func TestExchangeAndRefreshRotate(t *testing.T) {
	f := newFake(t)
	c := f.connector()
	tok, err := c.Exchange(context.Background(), "the-code", "https://ohs.example/callback")
	require.NoError(t, err)
	require.Equal(t, "acc-2", tok.Access)
	require.Equal(t, "ref-2", tok.Refresh)
	require.Equal(t, fakeNow.Add(2*time.Hour), tok.Expiry)
	form := f.tokenForms[0]
	require.Equal(t, "authorization_code", form.Get("grant_type"))
	require.Equal(t, "the-code", form.Get("code"))
	require.Equal(t, testClientID, form.Get("client_id"))
	require.Equal(t, testClientSecret, form.Get("client_secret"))

	f.tokenBody = `{"access_token":"acc-3","expires_in":7200,"refresh_token":"ref-3"}`
	nt, err := c.Refresh(context.Background(), connector.Tokens{Access: "acc-2", Refresh: "ref-2", ProviderUserID: "u-1"})
	require.NoError(t, err)
	require.Equal(t, "acc-3", nt.Access)
	require.Equal(t, "ref-3", nt.Refresh, "the rotated refresh token replaces the old one")
	require.Equal(t, "u-1", nt.ProviderUserID)
	require.Equal(t, "refresh_token", f.tokenForms[1].Get("grant_type"))
	require.Equal(t, "ref-2", f.tokenForms[1].Get("refresh_token"))
}

func TestRefreshRefused(t *testing.T) {
	f := newFake(t)
	c := f.connector()
	f.tokenCode, f.tokenBody = 400, `{"error":"invalid_grant"}`
	_, err := c.Refresh(context.Background(), connector.Tokens{Refresh: "used-once"})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	_, err = c.Refresh(context.Background(), connector.Tokens{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	f.tokenCode, f.tokenBody = 429, `{}`
	_, err = c.Refresh(context.Background(), connector.Tokens{Refresh: "r"})
	var rl *connector.RateLimitError
	require.ErrorAs(t, err, &rl)
}

func TestFetchChunksAndGroupsByDay(t *testing.T) {
	f := newFake(t)
	f.records = []map[string]any{
		egvRecord("r1", "2026-01-20T23:55:00+00:00", "2026-01-21T00:55:00+01:00", 110),
		egvRecord("r2", "2026-01-21T00:00:00+00:00", "2026-01-21T01:00:00+01:00", 112),
		egvRecord("r3", "2026-02-25T08:00:00", "2026-02-25T09:00:00", 39),
	}
	c := f.connector()
	from := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	var s sink
	conn := connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "acc-1"}}
	require.NoError(t, c.Fetch(context.Background(), conn, from, to, &s))

	require.Len(t, f.egvQueries, 2, "45 days are two windows of at most 30 days")
	require.Equal(t, "2026-01-15T00:00:00", f.egvQueries[0].Get("startDate"))
	require.Equal(t, "2026-02-14T00:00:00", f.egvQueries[0].Get("endDate"))
	require.Equal(t, "2026-02-14T00:00:00", f.egvQueries[1].Get("startDate"))
	require.Equal(t, "2026-03-01T00:00:00", f.egvQueries[1].Get("endDate"))

	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	require.Equal(t, []time.Time{day(1, 20), day(1, 21), day(2, 25)}, s.order)
	require.Len(t, s.calls[day(1, 21)], 1)
	require.True(t, json.Valid(s.calls[day(1, 21)][0]))
	require.False(t, strings.Contains(string(s.calls[day(1, 21)][0]), "\n"))
}

func TestFetchClampsToNow(t *testing.T) {
	f := newFake(t)
	c := f.connector()
	var s sink
	conn := connector.Connection{Tokens: connector.Tokens{Access: "acc-1"}}
	require.NoError(t, c.Fetch(context.Background(), conn, fakeNow.Add(-time.Hour), fakeNow.Add(48*time.Hour), &s))
	require.Len(t, f.egvQueries, 1)
	require.Equal(t, "2026-03-10T12:00:00", f.egvQueries[0].Get("endDate"))
}

func TestFetchErrors(t *testing.T) {
	f := newFake(t)
	c := f.connector()
	var s sink
	from, to := fakeNow.Add(-24*time.Hour), fakeNow

	err := c.Fetch(context.Background(), connector.Connection{Tokens: connector.Tokens{Access: "revoked"}}, from, to, &s)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	err = c.Fetch(context.Background(), connector.Connection{}, from, to, &s)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	f.egvCode = http.StatusTooManyRequests
	err = c.Fetch(context.Background(), connector.Connection{Tokens: connector.Tokens{Access: "acc-1"}}, from, to, &s)
	var rl *connector.RateLimitError
	require.ErrorAs(t, err, &rl)
	require.Equal(t, 2*time.Minute, rl.RetryAfter)
}

func fieldsOf(t *testing.T, rec map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func TestMapEGV(t *testing.T) {
	c := New(Options{})
	rec := egvRecord("rec-1", "2026-01-21T07:00:00+00:00", "2026-01-21T08:00:00+01:00", 112)
	res, err := c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: fieldsOf(t, rec)})
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 1)
	s := res.QuantitySamples[0]
	require.Equal(t, "blood_glucose", s.QuantityType)
	require.Equal(t, 112.0, s.Value)
	require.Equal(t, "mg/dL", s.Unit)
	require.Equal(t, "rec-1", s.ExternalID)
	require.Equal(t, Slug, s.Provider)
	require.Equal(t, "g7", s.DeviceModel)
	require.Equal(t, time.Date(2026, 1, 21, 7, 0, 0, 0, time.UTC), s.StartDate)
	require.Equal(t, s.StartDate, s.EndDate)
	require.Equal(t, SpecimenInterstitial, s.Metadata[MetadataSpecimen])
	require.Equal(t, 60, s.Metadata["utc_offset_minutes"])
	require.Equal(t, "Etc/GMT-1", s.Metadata["timezone"])
	require.Equal(t, "flat", s.Metadata["trend"])
	require.NotEmpty(t, s.ContentHash)

	again, err := c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: fieldsOf(t, rec)})
	require.NoError(t, err)
	require.Equal(t, s.ContentHash, again.QuantitySamples[0].ContentHash, "mapping is deterministic")
}

func TestMapEGVEdges(t *testing.T) {
	c := New(Options{})

	low := egvRecord("rec-low", "2026-01-21T07:00:00", "2026-01-20T23:00:00", 39)
	low["status"] = "low"
	res, err := c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: fieldsOf(t, low)})
	require.NoError(t, err)
	s := res.QuantitySamples[0]
	require.Equal(t, 39.0, s.Value)
	require.Equal(t, "low", s.Metadata["out_of_range"])
	require.Equal(t, -480, s.Metadata["utc_offset_minutes"], "a receiver record: offset from the display clock")

	mmol := egvRecord("rec-mmol", "2026-01-21T07:00:00Z", "2026-01-21T07:00:00Z", 6.2)
	mmol["unit"] = "mmol/L"
	res, err = c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: fieldsOf(t, mmol)})
	require.NoError(t, err)
	require.Equal(t, "mmol/L", res.QuantitySamples[0].Unit, "the pipeline converts mmol/L to mg/dL")

	none := egvRecord("rec-none", "2026-01-21T07:00:00Z", "2026-01-21T07:00:00Z", nil)
	res, err = c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: fieldsOf(t, none)})
	require.NoError(t, err)
	require.Empty(t, res.QuantitySamples)

	bad := egvRecord("rec-bad", "yesterday", "", 100)
	_, err = c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: fieldsOf(t, bad)})
	require.Error(t, err)

	res, err = c.Map(connector.RawRecord{UserID: testUser, DataType: "alerts", Fields: map[string]any{}})
	require.NoError(t, err)
	require.Empty(t, res.QuantitySamples)
}

func TestDisplayOffsetRounds(t *testing.T) {
	off, ok := displayOffset("2026-01-21T07:00:00", "2026-01-21T12:33:20")
	require.True(t, ok)
	require.Equal(t, 330, off, "5 h 33 min of drifted clock rounds to +5:30")
	_, ok = displayOffset("2026-01-21T07:00:00", "2026-01-23T07:00:00")
	require.False(t, ok)
}

func TestMapEGVRefusesZeroSystemTime(t *testing.T) {
	rec := egvRecord("rec-zero", "0001-01-01T00:00:00", "0001-01-01T00:00:00", 100)
	_, err := New(Options{}).Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: fieldsOf(t, rec)})
	require.Error(t, err)
}

func TestDisplayOffsetRefusesWrittenOffsetsOutOfRange(t *testing.T) {
	for _, display := range []string{"2026-03-01T09:00:00+23:59", "2026-03-01T09:00:00-23:00", "2026-03-01T09:00:00+14:15", "2026-03-01T09:00:00-12:30"} {
		_, ok := displayOffset("2026-03-01T08:00:00", display)
		require.False(t, ok, display)
	}
	off, ok := displayOffset("2026-03-01T08:00:00", "2026-03-01T09:00:00+14:00")
	require.True(t, ok)
	require.Equal(t, 14*60, off)

	rec := egvRecord("rec-far", "2026-03-01T08:00:00", "2026-03-01T09:00:00+23:59", 100)
	res, err := New(Options{}).Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: fieldsOf(t, rec)})
	require.NoError(t, err)
	require.NotContains(t, res.QuantitySamples[0].Metadata, "utc_offset_minutes")
}
