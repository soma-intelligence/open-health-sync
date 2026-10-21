// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/fitbit/fitbitfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestMapMoreSamples(t *testing.T) {
	vo2 := single(t, DataTypeVO2Max, []byte(`{"vo2Max":{"sampleTime":{"physicalTime":"2026-03-09T08:00:00Z","utcOffset":"7200s"},
		"vo2Max":47.5,"measurementMethod":"FITBIT_RUN"}}`))
	assert.Equal(t, "vo2_max", vo2.QuantityType)
	assert.Equal(t, "mL/kg/min", vo2.Unit)
	assert.InDelta(t, 47.5, vo2.Value, 1e-9)
	assert.Equal(t, ts("2026-03-09T08:00:00Z"), vo2.StartDate)
	assert.Equal(t, vo2.StartDate, vo2.EndDate)
	assert.False(t, vo2.Replaceable)
	assert.Equal(t, "FITBIT_RUN", vo2.Metadata["measurement_method"])
	assert.Equal(t, "+02:00", vo2.Metadata["utc_offset"])
	assert.Equal(t, "Etc/GMT-2", vo2.Metadata["timezone"])

	bg := single(t, DataTypeBloodGlucose, []byte(`{"bloodGlucose":{"sampleTime":{"physicalTime":"2026-03-09T12:15:00Z","utcOffset":"-18000s"},
		"bloodGlucoseMilligramsPerDeciliter":"98","specimen":"CAPILLARY_BLOOD","mealType":"LUNCH","measurementTiming":"BEFORE_MEAL"}}`))
	assert.Equal(t, "blood_glucose", bg.QuantityType)
	assert.Equal(t, "mg/dL", bg.Unit)
	assert.InDelta(t, 98.0, bg.Value, 1e-9)
	assert.Equal(t, ts("2026-03-09T12:15:00Z"), bg.StartDate)
	assert.Equal(t, "CAPILLARY_BLOOD", bg.Metadata["specimen"])
	assert.Equal(t, "LUNCH", bg.Metadata["meal_type"])
	assert.Equal(t, "BEFORE_MEAL", bg.Metadata["measurement_timing"])
	assert.NotContains(t, bg.Metadata, "measurement_source", "an empty attribute is left out")
	assert.Equal(t, "Etc/GMT+5", bg.Metadata["timezone"])

	core := single(t, DataTypeCoreBodyTemp, []byte(`{"coreBodyTemperature":{"sampleTime":{"physicalTime":"2026-03-09T23:00:00Z","utcOffset":"0s"},
		"temperatureCelsius":37.1,"measurementLocation":"ORAL"}}`))
	assert.Equal(t, "body_temperature", core.QuantityType)
	assert.Equal(t, "celsius", core.Unit)
	assert.InDelta(t, 37.1, core.Value, 1e-9)
	assert.Equal(t, "ORAL", core.Metadata["measurement_location"])
	assert.Equal(t, "UTC", core.Metadata["timezone"])

	for dt, line := range map[string]string{
		DataTypeVO2Max:       `{"vo2Max":{"sampleTime":{"physicalTime":"2026-03-09T08:00:00Z"},"vo2Max":0}}`,
		DataTypeBloodGlucose: `{"bloodGlucose":{"sampleTime":{"physicalTime":"2026-03-09T08:00:00Z"}}}`,
		DataTypeCoreBodyTemp: `{"coreBodyTemperature":{"sampleTime":{"physicalTime":"2026-03-09T08:00:00Z"},"temperatureCelsius":-1}}`,
	} {
		assert.Empty(t, mapLine(t, dt, []byte(line)).QuantitySamples, "%s: a missing or non-positive value is skipped", dt)
	}
}

func TestMapSamplesRejectBadInput(t *testing.T) {
	c := New(Options{})
	for dt, fields := range map[string]map[string]any{
		DataTypeVO2Max:       {"vo2Max": "not an object"},
		DataTypeBloodGlucose: {"bloodGlucose": map[string]any{"sampleTime": map[string]any{"physicalTime": "soon"}, "bloodGlucoseMilligramsPerDeciliter": 90.0}},
		DataTypeCoreBodyTemp: {"coreBodyTemperature": []any{1.0}},
		DataTypeBodyFat:      {"bodyFat": map[string]any{"sampleTime": map[string]any{"physicalTime": "2026-03-09T06:30:00Z"}, "percentage": 120.0}},
		DataTypeWeight:       {"weight": 3.0},
		DataTypeDistance: {"distance": map[string]any{"millimeters": "5",
			"interval": map[string]any{"startTime": "2026-03-09T07:01:00Z", "endTime": "2026-03-09T07:00:00Z"}}},
		DataTypeActiveEnergy: {"activeEnergyBurned": map[string]any{"kcal": 1.0,
			"interval": map[string]any{"startTime": "", "endTime": "2026-03-09T07:00:00Z"}}},
		DataTypeDailyRestingHR: {"dailyRestingHeartRate": map[string]any{"date": map[string]any{"year": 2026, "month": 2, "day": 30}, "beatsPerMinute": 50.0}},
	} {
		_, err := c.Map(connector.RawRecord{DataType: dt, Fields: fields})
		require.Error(t, err, dt)
	}
}

func TestMapIntervalsSkipEmptyValues(t *testing.T) {
	const iv = `"interval":{"startTime":"2026-03-09T07:00:00Z","endTime":"2026-03-09T07:01:00Z"}`
	for dt, line := range map[string]string{
		DataTypeDistance:       `{"distance":{` + iv + `,"millimeters":"-10"}}`,
		DataTypeActiveEnergy:   `{"activeEnergyBurned":{` + iv + `}}`,
		DataTypeBodyFat:        `{"bodyFat":{"sampleTime":{"physicalTime":"2026-03-09T06:30:00Z"}}}`,
		DataTypeDailyRestingHR: `{"dailyRestingHeartRate":{"date":{"year":2026,"month":3,"day":9},"beatsPerMinute":0}}`,
	} {
		assert.Empty(t, mapLine(t, dt, []byte(line)).QuantitySamples, dt)
	}
	// No offset: no zone is claimed.
	d := single(t, DataTypeDistance, []byte(`{"distance":{`+iv+`,"millimeters":"1500"}}`))
	assert.InDelta(t, 1.5, d.Value, 1e-9, "mm to m")
	assert.NotContains(t, d.Metadata, "utc_offset")
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"3600s": time.Hour, "-25200s": -7 * time.Hour, "1.5s": 1500 * time.Millisecond, " 0s ": 0,
	} {
		got, err := parseDuration(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "3600", "abcs", "2e9s", "-2e9s"} {
		_, err := parseDuration(in)
		assert.Error(t, err, in)
	}
}

func TestZoneMetadata(t *testing.T) {
	at := ts("2026-03-09T12:00:00Z")
	cases := []struct {
		offset, utc, zone string
	}{
		{"", "", ""},
		{"bogus", "", ""},
		{"54000s", "", ""}, // +15:00 is past any real offset
		{"-43200s", "-12:00", "Etc/GMT+12"},
		{"50400s", "+14:00", "Etc/GMT-14"},
		{"20700s", "+05:45", "Asia/Kathmandu"},
		{"-34200s", "-09:30", "Pacific/Marquesas"},
		{"4500s", "+01:15", ""}, // no zone uses it
	}
	for _, tc := range cases {
		md := map[string]any{}
		zoneMetadata(md, at, tc.offset)
		if tc.utc == "" {
			assert.NotContains(t, md, "utc_offset", tc.offset)
		} else {
			assert.Equal(t, tc.utc, md["utc_offset"], tc.offset)
		}
		if tc.zone == "" {
			assert.NotContains(t, md, "timezone", tc.offset)
		} else {
			assert.Equal(t, tc.zone, md["timezone"], tc.offset)
		}
	}
}

func TestParseNotifications(t *testing.T) {
	msgs, err := parseNotifications([]byte(`  {"healthUserId":"u1","dataType":"steps"}  `))
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "u1", msgs[0].HealthUserID, "an unwrapped message")
	assert.Equal(t, "steps", msgs[0].DataType)

	msgs, err = parseNotifications([]byte(`[{"data":{"healthUserId":"u2","dataType":"sleep","operation":"DELETE"}},
		"junk", 7, {"healthUserId":"u3","dataType":"weight"}, {"data":"not an object"}]`))
	require.NoError(t, err)
	require.Len(t, msgs, 2, "unreadable messages are skipped")
	assert.Equal(t, "u2", msgs[0].HealthUserID)
	assert.Equal(t, "DELETE", msgs[0].Operation)
	assert.Equal(t, "u3", msgs[1].HealthUserID)

	for _, body := range []string{``, `   `, `"text"`, `[{"data":`} {
		_, err := parseNotifications([]byte(body))
		assert.Error(t, err, body)
	}
}

func TestParseKeysetRejects(t *testing.T) {
	s := string(fitbitfake.Keyset(fitbitfake.NewSigner(t, 9)))
	for name, body := range map[string]string{
		"not json":   `{`,
		"other type": strings.Replace(s, ecdsaPublicKeyType, "type.googleapis.com/google.crypto.tink.Ed25519PublicKey", 1),
		"bad base64": `{"key":[{"keyData":{"typeUrl":"` + ecdsaPublicKeyType + `","value":"!!!"},"status":"ENABLED","keyId":5}]}`,
		"bad key":    `{"key":[{"keyData":{"typeUrl":"` + ecdsaPublicKeyType + `","value":"AAAA"},"status":"ENABLED","keyId":5}]}`,
	} {
		_, err := parseKeyset([]byte(body))
		assert.Error(t, err, name)
	}
	// No status and no prefix type: enabled, Tink prefix.
	noStatus := strings.Replace(s, `"status":"ENABLED"`, `"status":""`, 1)
	noStatus = strings.Replace(noStatus, `"outputPrefixType":"TINK"`, `"outputPrefixType":""`, 1)
	keys, err := parseKeyset([]byte(noStatus))
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, prefixTink, keys[0].prefix)
}

// apiServer answers each GET with the next of answers and counts calls.
type apiServer struct {
	mu      sync.Mutex
	calls   int
	answers []func(w http.ResponseWriter)
}

func (s *apiServer) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	i := s.calls
	s.calls++
	s.mu.Unlock()
	if i >= len(s.answers) {
		i = len(s.answers) - 1
	}
	s.answers[i](w)
}

func (s *apiServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func status(code int, hdr ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}
		w.WriteHeader(code)
	}
}

func body(code int, b string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(b))
	}
}

// apiConnector points a connector at answers and records its waits.
func apiConnector(t *testing.T, answers ...func(w http.ResponseWriter)) (*Connector, *apiServer, *[]time.Duration) {
	t.Helper()
	s := &apiServer{answers: answers}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	var waits []time.Duration
	c := New(Options{
		ClientID: testClientID, ClientSecret: testClientSecret,
		APIBase: srv.URL, TokenURL: srv.URL + "/token", HTTPClient: srv.Client(),
		RetryBase: 100 * time.Millisecond,
		Now:       func() time.Time { return fakeNow },
		Sleep: func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
	})
	return c, s, &waits
}

func TestGetRetriesServerErrorsThenSucceeds(t *testing.T) {
	c, s, waits := apiConnector(t, status(http.StatusInternalServerError), status(http.StatusBadGateway), body(http.StatusOK, `{"n":3}`))
	var out struct{ N int }
	require.NoError(t, c.get(context.Background(), "tok", "/x", url.Values{"a": {"1"}}, &out))
	assert.Equal(t, 3, out.N)
	assert.Equal(t, 3, s.count())
	require.Len(t, *waits, 2)
	// Jittered over [d, 1.5d) on a doubling ladder.
	assert.GreaterOrEqual(t, (*waits)[0], 100*time.Millisecond)
	assert.Less(t, (*waits)[0], 150*time.Millisecond)
	assert.GreaterOrEqual(t, (*waits)[1], 200*time.Millisecond)
	assert.Less(t, (*waits)[1], 300*time.Millisecond)
}

func TestGetServerErrorWithRetryAfterIsRateLimit(t *testing.T) {
	c, s, _ := apiConnector(t, status(http.StatusServiceUnavailable, "Retry-After", "30"))
	err := c.get(context.Background(), "tok", "/x", nil, &struct{}{})
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok)
	assert.Equal(t, 30*time.Second, rl.RetryAfter)
	assert.Equal(t, maxServerAttempts, s.count())
}

func TestGetRateLimitRetryAfterDate(t *testing.T) {
	at := fakeNow.Add(90 * time.Second).Format(http.TimeFormat)
	c, s, _ := apiConnector(t, status(http.StatusTooManyRequests, "Retry-After", at))
	err := c.get(context.Background(), "tok", "/x", nil, &struct{}{})
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok)
	assert.Equal(t, 90*time.Second, rl.RetryAfter)
	assert.Equal(t, 1, s.count(), "a rate limit is not retried")
}

func TestGetErrorAnswers(t *testing.T) {
	cases := []struct {
		name   string
		answer func(w http.ResponseWriter)
		want   error
	}{
		{"not linked", body(http.StatusBadRequest, `{"error":{"code":400,"status":"FAILED_PRECONDITION","details":[{"reason":""},{"reason":"ACCOUNT_NOT_LINKED"}]}}`), ErrAccountNotLinked},
		{"forbidden", body(http.StatusForbidden, `{"error":{"status":"PERMISSION_DENIED"}}`), errForbidden},
		{"not found", body(http.StatusNotFound, `not json`), errNotFound},
		{"unauthorized", status(http.StatusUnauthorized), errUnauthorized},
	}
	for _, tc := range cases {
		c, _, _ := apiConnector(t, tc.answer)
		err := c.get(context.Background(), "tok", "/x", nil, &struct{}{})
		require.ErrorIs(t, err, tc.want, tc.name)
	}

	c, _, _ := apiConnector(t, body(http.StatusBadRequest, `{"error":{"status":"INVALID_ARGUMENT","message":"bad filter","details":[{"reason":"BAD_FILTER"}]}}`))
	err := c.get(context.Background(), "tok", "/x", nil, &struct{}{})
	var ae *apiError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "fitbit: HTTP 400 INVALID_ARGUMENT (BAD_FILTER)", ae.Error())
	assert.Equal(t, "bad filter", ae.Message)

	c, _, _ = apiConnector(t, status(http.StatusNotModified))
	err = c.get(context.Background(), "tok", "/x", nil, &struct{}{})
	require.ErrorContains(t, err, "unexpected HTTP 304")

	c, _, _ = apiConnector(t, body(http.StatusOK, `{"n":`))
	err = c.get(context.Background(), "tok", "/x", nil, &struct{}{})
	require.ErrorContains(t, err, "decode answer")
}

func TestGetTransportErrorsRetried(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // nothing listens any more
	var waits int
	c := New(Options{APIBase: base, Sleep: func(context.Context, time.Duration) error { waits++; return nil }})
	err := c.get(context.Background(), "tok", "/x", nil, &struct{}{})
	require.ErrorContains(t, err, "fitbit http")
	assert.Equal(t, maxAttempts-1, waits)
}

func TestGetStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, s, _ := apiConnector(t, body(http.StatusOK, `{}`))
	require.ErrorIs(t, c.get(ctx, "tok", "/x", nil, &struct{}{}), context.Canceled)
	assert.Zero(t, s.count())

	// A wait that is cancelled ends the retries.
	c, s, _ = apiConnector(t, status(http.StatusServiceUnavailable))
	c.sleep = func(context.Context, time.Duration) error { return context.DeadlineExceeded }
	require.ErrorIs(t, c.get(context.Background(), "tok", "/x", nil, &struct{}{}), context.DeadlineExceeded)
	assert.Equal(t, 1, s.count())
}

func TestPause(t *testing.T) {
	require.NoError(t, pause(context.Background(), 0))
	require.NoError(t, pause(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, pause(ctx, 0), context.Canceled)
	start := time.Now()
	require.ErrorIs(t, pause(ctx, time.Hour), context.Canceled)
	assert.Less(t, time.Since(start), time.Second)
}

func TestJitterReadBodyClip(t *testing.T) {
	assert.Zero(t, jitter(0))
	assert.Zero(t, jitter(-time.Second))
	for range 20 {
		d := jitter(time.Second)
		assert.GreaterOrEqual(t, d, time.Second)
		assert.LessOrEqual(t, d, 1500*time.Millisecond)
	}
	b, err := readBody(strings.NewReader("abcd"), 4)
	require.NoError(t, err)
	assert.Equal(t, "abcd", string(b))
	_, err = readBody(strings.NewReader("abcde"), 4)
	require.ErrorIs(t, err, errTooLarge)
	_, err = readBody(failingReader{}, 4)
	require.Error(t, err)
	assert.Equal(t, "ab...", clip("abcdef", 2))
	assert.Equal(t, "ab", clip("ab", 2))
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestRequestTokenAnswers(t *testing.T) {
	cases := []struct {
		name   string
		answer func(w http.ResponseWriter)
		check  func(t *testing.T, tok connector.Tokens, err error)
	}{
		{"ok", body(http.StatusOK, `{"access_token":"a1","refresh_token":"r1","expires_in":3600,"scope":"s1 s2"}`),
			func(t *testing.T, tok connector.Tokens, err error) {
				require.NoError(t, err)
				assert.Equal(t, "a1", tok.Access)
				assert.Equal(t, "r1", tok.Refresh)
				assert.Equal(t, []string{"s1", "s2"}, tok.Scopes)
				assert.Equal(t, fakeNow.Add(time.Hour), tok.Expiry)
			}},
		{"no expiry", body(http.StatusOK, `{"access_token":"a1"}`),
			func(t *testing.T, tok connector.Tokens, err error) {
				require.NoError(t, err)
				assert.True(t, tok.Expiry.IsZero())
			}},
		{"unauthorized", status(http.StatusUnauthorized),
			func(t *testing.T, _ connector.Tokens, err error) { require.ErrorIs(t, err, connector.ErrNeedsReauth) }},
		{"unauthorized client", body(http.StatusBadRequest, `{"error":"unauthorized_client"}`),
			func(t *testing.T, _ connector.Tokens, err error) { require.ErrorIs(t, err, ErrClientRejected) }},
		{"gateway timeout", status(http.StatusGatewayTimeout, "Retry-After", "5"),
			func(t *testing.T, _ connector.Tokens, err error) {
				rl, ok := connector.AsRateLimit(err)
				require.True(t, ok)
				assert.Equal(t, 5*time.Second, rl.RetryAfter)
			}},
		{"http error with code", body(http.StatusBadRequest, `{"error":"invalid_request"}`),
			func(t *testing.T, _ connector.Tokens, err error) {
				require.EqualError(t, err, "token endpoint HTTP 400: invalid_request")
			}},
		{"http error", body(http.StatusInternalServerError, `oops`),
			func(t *testing.T, _ connector.Tokens, err error) {
				require.EqualError(t, err, "token endpoint HTTP 500")
			}},
		{"error on 200", body(http.StatusOK, `{"error":"temporarily_unavailable"}`),
			func(t *testing.T, _ connector.Tokens, err error) {
				require.EqualError(t, err, "token endpoint: temporarily_unavailable")
			}},
		{"not json", body(http.StatusOK, `<html>`),
			func(t *testing.T, _ connector.Tokens, err error) { require.ErrorContains(t, err, "not JSON") }},
		{"no access token", body(http.StatusOK, `{"refresh_token":"r1"}`),
			func(t *testing.T, _ connector.Tokens, err error) { require.ErrorContains(t, err, "no access token") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := apiConnector(t, tc.answer)
			tok, err := c.requestToken(context.Background(), url.Values{"grant_type": {"refresh_token"}})
			tc.check(t, tok, err)
		})
	}

	_, err := New(Options{}).requestToken(context.Background(), url.Values{})
	require.ErrorIs(t, err, errNotConfigured)

	c, _, _ := apiConnector(t, body(http.StatusOK, `{}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.requestToken(ctx, url.Values{})
	require.ErrorIs(t, err, context.Canceled)
}
