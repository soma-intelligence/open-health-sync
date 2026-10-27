// SPDX-License-Identifier: AGPL-3.0-only

package omron

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const (
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret"
	testUser         = "00000000-0000-0000-0000-000000000001"
)

var fakeNow = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

// fakeOmron is an httptest OMRON API: the token endpoint and the measurement
// endpoint, which pages its records by id the way the API documents.
type fakeOmron struct {
	srv *httptest.Server

	mu        sync.Mutex
	forms     []url.Values
	tokenBody string
	tokenCode int
	status    int
	bp        []map[string]any
	weights   []map[string]any
	pageSize  int
}

func newFake(t *testing.T) *fakeOmron {
	t.Helper()
	f := &fakeOmron{tokenCode: 200, pageSize: 2}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /connect/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		code, body := f.tokenCode, f.tokenBody
		f.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("POST /api/measurement", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		f.mu.Lock()
		defer f.mu.Unlock()
		f.forms = append(f.forms, r.PostForm)
		if r.Header.Get("Authorization") != "Bearer acc-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.status != 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": f.status})
			return
		}
		typ := r.PostForm.Get("type")
		recs, arr, keyName := f.bp, "bloodPressure", "bloodpressurePaginationKey"
		if typ == "weight" {
			recs, arr, keyName = f.weights, "weight", "weightPaginationKey"
		}
		after := int64(-1)
		if k := r.PostForm.Get(keyName); k != "" {
			after, _ = strconv.ParseInt(k, 10, 64)
		}
		var page []map[string]any
		for _, rec := range recs {
			if id := int64(rec["id"].(float64)); id > after && len(page) < f.pageSize {
				page = append(page, rec)
			}
		}
		result := map[string]any{arr: page, "measurementCount": len(page), "truncated": false}
		if len(page) == f.pageSize {
			result["paginationKey"] = map[string]any{keyName: page[len(page)-1]["id"]}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 0, "result": result})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOmron) connector() *Connector {
	return New(Options{
		ClientID: testClientID, ClientSecret: testClientSecret,
		Endpoints:  Endpoints{Authorize: f.srv.URL + "/connect/authorize", Token: f.srv.URL + "/connect/token", Measurement: f.srv.URL + "/api/measurement"},
		HTTPClient: f.srv.Client(),
		Now:        func() time.Time { return fakeNow },
		Sleep:      func(context.Context, time.Duration) error { return nil },
	})
}

func idToken(sub string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(`{"sub":"`+sub+`","email":"a@b.c"}`)) + ".sig"
}

type sink struct {
	types []string
	days  []time.Time
	lines [][]byte
}

func (s *sink) Emit(_ context.Context, dt string, day time.Time, lines [][]byte) error {
	s.types = append(s.types, dt)
	s.days = append(s.days, day)
	s.lines = append(s.lines, lines...)
	return nil
}

func bpRecord(id float64, dt string, sys, dia, pulse any) map[string]any {
	return map[string]any{
		"id": id, "dateTime": dt, "dateTimeLocal": dt, "dateTimeUtcOffset": "+02:00",
		"systolic": sys, "diastolic": dia, "bloodPressureUnits": "mmHg",
		"pulse": pulse, "pulseUnits": "bpm", "deviceType": "M700 Intelli IT",
	}
}

func TestAuthorizeURLAndEnvironments(t *testing.T) {
	c := New(Options{ClientID: testClientID})
	u, err := c.AuthorizeURL("st", "https://ohs.example/cb")
	require.NoError(t, err)
	p, err := url.Parse(u)
	require.NoError(t, err)
	require.Equal(t, "prd-oauth-website.ohiomron.com", p.Host)
	require.Equal(t, "bloodpressure weight openid offline_access", p.Query().Get("scope"))
	require.Equal(t, "code", p.Query().Get("response_type"))
	require.Equal(t, "st", p.Query().Get("state"))

	require.NoError(t, c.Configure(connector.Config{KeyClientID: "a", KeyClientSecret: "b", KeyEnvironment: "staging"}))
	require.Equal(t, Environments["staging"], c.endpoints())
	require.Error(t, c.Configure(connector.Config{KeyClientID: "a", KeyClientSecret: "b", KeyEnvironment: "dev"}))
	require.Error(t, c.Configure(connector.Config{KeyClientSecret: "b"}))

	_, err = New(Options{}).AuthorizeURL("st", "x")
	require.ErrorIs(t, err, errNotConfigured)
}

func TestExchangeReadsSubject(t *testing.T) {
	f := newFake(t)
	f.tokenBody = `{"access_token":"acc-1","refresh_token":"ref-1","expiresIn":3600,"tokenType":"Bearer","id_token":"` + idToken("omron-user-7") + `"}`
	c := f.connector()
	tok, err := c.Exchange(context.Background(), "code-1", "https://ohs.example/cb")
	require.NoError(t, err)
	require.Equal(t, "acc-1", tok.Access)
	require.Equal(t, "ref-1", tok.Refresh)
	require.Equal(t, "omron-user-7", tok.ProviderUserID)
	require.Equal(t, fakeNow.Add(time.Hour), tok.Expiry)
	require.Equal(t, "authorization_code", f.forms[0].Get("grant_type"))
	require.Equal(t, testClientSecret, f.forms[0].Get("client_secret"))

	f.tokenBody = `{"access_token":"acc-2","refresh_token":"ref-2","expires_in":3600}`
	nt, err := c.Refresh(context.Background(), tok)
	require.NoError(t, err)
	require.Equal(t, "ref-2", nt.Refresh)
	require.Equal(t, "omron-user-7", nt.ProviderUserID, "kept when the refresh answer has no id_token")

	f.tokenCode, f.tokenBody = 400, `{"error":"invalid_grant"}`
	_, err = c.Refresh(context.Background(), tok)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestSubject(t *testing.T) {
	require.Equal(t, "x", subject(idToken("x")))
	require.Empty(t, subject("not-a-jwt"))
	require.Empty(t, subject("a.!!!.c"))
}

func TestFetchPagesAndFilters(t *testing.T) {
	f := newFake(t)
	f.bp = []map[string]any{
		bpRecord(1, "2026-03-01T06:00:00", 120, 80, 60), // before from
		bpRecord(2, "2026-03-02T07:00:00", 121, 81, 61),
		bpRecord(3, "2026-03-02T21:00:00", 122, 82, 62),
		bpRecord(4, "2026-03-03T07:00:00", 123, 83, 63),
		bpRecord(5, "2026-03-09T07:00:00", 124, 84, 64), // at or after to
	}
	f.weights = []map[string]any{{"id": float64(9), "dateTime": "2026-03-02T08:00:00", "weight": "71.6", "weightPref": "kg"}}
	c := f.connector()
	from := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	var s sink
	require.NoError(t, c.Fetch(context.Background(), connector.Connection{Tokens: connector.Tokens{Access: "acc-1"}}, from, to, &s))

	require.Equal(t, []string{DataTypeBloodPressure, DataTypeBloodPressure, DataTypeWeight}, s.types)
	require.Equal(t, []time.Time{from, from.AddDate(0, 0, 1), from}, s.days)
	require.Len(t, s.lines, 4)

	first := f.forms[0]
	require.Equal(t, "bloodpressure", first.Get("type"))
	require.Equal(t, "2026-03-01", first.Get("since"))
	require.Equal(t, "asc", first.Get("sortOrder"))
	require.Empty(t, first.Get("bloodpressurePaginationKey"))
	require.Equal(t, "2", f.forms[1].Get("bloodpressurePaginationKey"))
	require.Equal(t, "4", f.forms[2].Get("bloodpressurePaginationKey"))
	require.Len(t, f.forms, 4, "three bp pages (the third reaches to), one weight page")
}

func TestFetchErrors(t *testing.T) {
	f := newFake(t)
	c := f.connector()
	var s sink
	err := c.Fetch(context.Background(), connector.Connection{Tokens: connector.Tokens{Access: "gone"}}, fakeNow.Add(-time.Hour), fakeNow, &s)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
	err = c.Fetch(context.Background(), connector.Connection{}, fakeNow.Add(-time.Hour), fakeNow, &s)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
	f.status = 4001
	err = c.Fetch(context.Background(), connector.Connection{Tokens: connector.Tokens{Access: "acc-1"}}, fakeNow.Add(-time.Hour), fakeNow, &s)
	require.ErrorContains(t, err, "status 4001")
}

func fieldsOf(t *testing.T, rec map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func TestMapBloodPressurePairs(t *testing.T) {
	c := New(Options{})
	res, err := c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeBloodPressure,
		Fields: fieldsOf(t, bpRecord(1644043106941, "2022-02-03T16:44:13", 92, "71", 78))})
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 3)
	sys, dia, pulse := res.QuantitySamples[0], res.QuantitySamples[1], res.QuantitySamples[2]
	require.Equal(t, "blood_pressure_systolic", sys.QuantityType)
	require.Equal(t, 92.0, sys.Value)
	require.Equal(t, "blood_pressure_diastolic", dia.QuantityType)
	require.Equal(t, 71.0, dia.Value, "a value sent as a string is read")
	require.Equal(t, model.QTHeartRate, pulse.QuantityType)
	want := time.Date(2022, 2, 3, 16, 44, 13, 0, time.UTC)
	for _, s := range res.QuantitySamples {
		require.Equal(t, want, s.StartDate)
		require.Equal(t, want, s.EndDate)
		require.Equal(t, "M700 Intelli IT", s.DeviceID)
		require.Equal(t, Slug, s.SourceSlug)
		require.Equal(t, 120, s.Metadata["utc_offset_minutes"])
		require.Equal(t, "Etc/GMT-2", s.Metadata["timezone"])
	}
	require.Equal(t, "1644043106941_systolic", sys.ExternalID)
	require.NotEqual(t, sys.ContentHash, dia.ContentHash)
}

func TestMapBloodPressureAfib(t *testing.T) {
	c := New(Options{})
	rec := bpRecord(5, "2026-03-02T07:00:00", 0, 0, 0)
	rec["possibleAfib"] = true
	res, err := c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeBloodPressure, Fields: fieldsOf(t, rec)})
	require.NoError(t, err)
	require.Empty(t, res.QuantitySamples, "zeroed values are not readings")
	require.Len(t, res.CategorySamples, 1)
	require.Equal(t, "irregular_heart_rhythm_event", res.CategorySamples[0].CategoryType)
	require.Equal(t, "5_afib", res.CategorySamples[0].ExternalID)
}

func TestMapWeight(t *testing.T) {
	c := New(Options{})
	rec := map[string]any{
		"id": 77, "idExt": "x", "dateTime": "2026-03-02T08:00:00", "dateTimeLocal": "2026-03-02T02:00:00",
		"dateTimeUtcOffset": "-21600", "weight": "158.0", "weightPref": "lbs", "bmiValue": "25.52",
		"bodyFatPercentage": "-1.0", "visceralFatLevel": "-1.0", "restingMetabolism": "-1.0",
		"deviceType": "HN-300T2 Intelli IT",
	}
	res, err := c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeWeight, Fields: fieldsOf(t, rec)})
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 2, "-1 means not measured")
	require.Equal(t, "body_mass", res.QuantitySamples[0].QuantityType)
	require.Equal(t, "lb", res.QuantitySamples[0].Unit)
	require.Equal(t, 158.0, res.QuantitySamples[0].Value)
	require.Equal(t, "body_mass_index", res.QuantitySamples[1].QuantityType)
	require.Equal(t, -360, res.QuantitySamples[0].Metadata["utc_offset_minutes"])
	require.Equal(t, "Etc/GMT+6", res.QuantitySamples[0].Metadata["timezone"])

	rec["weightPref"] = "stone"
	_, err = c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeWeight, Fields: fieldsOf(t, rec)})
	require.Error(t, err)
}

func TestMapRejectsAndSkips(t *testing.T) {
	c := New(Options{})
	_, err := c.Map(connector.RawRecord{DataType: DataTypeBloodPressure, Fields: map[string]any{"dateTime": "2026-03-02T08:00:00"}})
	require.ErrorContains(t, err, "without id")
	_, err = c.Map(connector.RawRecord{DataType: DataTypeBloodPressure, Fields: map[string]any{"id": 1, "dateTime": "soon"}})
	require.Error(t, err)
	res, err := c.Map(connector.RawRecord{DataType: "activity", Fields: map[string]any{"id": 1}})
	require.NoError(t, err)
	require.Empty(t, res.QuantitySamples)
}

func TestMapRefusesNonFiniteValues(t *testing.T) {
	c := New(Options{})
	res, err := c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeWeight, Fields: map[string]any{
		"id": 2, "dateTime": "2026-03-02T08:00:00", "weight": "Infinity", "weightPref": "kg", "bmiValue": "inf", "bodyFatPercentage": "NaN",
	}})
	require.NoError(t, err)
	require.Empty(t, res.QuantitySamples, "a value that is not a finite number is not a reading")

	res, err = c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeBloodPressure,
		Fields: fieldsOf(t, bpRecord(5, "2026-03-02T08:00:00", "+Inf", "Inf", "Inf"))})
	require.NoError(t, err)
	require.Empty(t, res.QuantitySamples)
}

func TestUTCOffset(t *testing.T) {
	for in, want := range map[string]int{`"+00:00"`: 0, `"-05:30"`: -330, `"+14:00"`: 840, `-21600`: -360, `"3600"`: 60} {
		got, ok := utcOffset(json.RawMessage(in))
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{``, `null`, `"+15:00"`, `"abc"`, `"61"`} {
		_, ok := utcOffset(json.RawMessage(in))
		require.False(t, ok, in)
	}
}
