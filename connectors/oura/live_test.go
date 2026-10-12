// SPDX-License-Identifier: AGPL-3.0-only

//go:build live

package oura

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Live smoke check against the real Oura API. Never run in CI: it needs the
// live build tag and an access token of a real account,
//
//	OHS_OURA_LIVE_TOKEN=... go test -tags live -run TestLive -v ./connectors/oura/
//
// OHS_OURA_LIVE_DAYS (default 7, at most 30) sets how far back it reads. It
// only reads. It checks every answer against Oura's response schemas
// (testdata/responses.schema.json), then pulls through the connector and maps
// every line. It prints counts and schema paths, never a value or the token.

const (
	envLiveToken = "OHS_OURA_LIVE_TOKEN"
	envLiveDays  = "OHS_OURA_LIVE_DAYS"
)

func liveToken(t *testing.T) string {
	tok := strings.TrimSpace(os.Getenv(envLiveToken))
	if tok == "" {
		t.Skipf("%s is not set", envLiveToken)
	}
	return tok
}

func liveDays(t *testing.T) int {
	days := 7
	if s := os.Getenv(envLiveDays); s != "" {
		n, err := strconv.Atoi(s)
		require.NoError(t, err, envLiveDays)
		days = min(max(n, 1), 30)
	}
	return days
}

// redact keeps the path and the kind of each schema mismatch, and drops the
// value the walker quotes.
func redact(errs []string) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		path, msg, _ := strings.Cut(e, ": ")
		switch {
		case strings.HasPrefix(msg, "missing required"), strings.HasPrefix(msg, "unknown property"),
			strings.HasPrefix(msg, "property "), strings.HasPrefix(msg, "unknown schema"):
			out = append(out, path+": "+msg)
		default:
			out = append(out, path+": value does not match the schema")
		}
	}
	return out
}

// Each endpoint's raw answer, page by page, matches the schema.
func TestLive_AnswersMatchTheSpec(t *testing.T) {
	tok := liveToken(t)
	d := loadSchemaDoc(t)
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -liveDays(t))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	get := func(path string, q url.Values) (int, []byte) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultAPIBase+path+"?"+q.Encode(), nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		require.NoError(t, err)
		return res.StatusCode, b
	}

	status, b := get(d.Endpoints["personal_info"].Path, url.Values{})
	require.Equal(t, http.StatusOK, status, "personal_info: is the token valid?")
	require.Empty(t, redact(validate(d, d.Endpoints["personal_info"].Schema, decodeNumbers(t, b))), "personal_info")

	for _, dt := range AllDataTypes {
		ep := d.Endpoints[dt]
		q := url.Values{"start_date": {from.Format(time.DateOnly)}, "end_date": {to.Format(time.DateOnly)}}
		if dt == DataTypeHeartRate {
			q = url.Values{"start_datetime": {to.Add(-24 * time.Hour).Format(time.RFC3339)}, "end_datetime": {to.Format(time.RFC3339)}}
		}
		docs := 0
		for page := 0; page < 20; page++ {
			status, b := get(ep.Path, q)
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				t.Logf("%s: HTTP %d (scope not granted?)", dt, status)
				break
			}
			require.Equal(t, http.StatusOK, status, dt)
			val := decodeNumbers(t, b)
			if errs := redact(validate(d, ep.Schema, val)); len(errs) > 0 {
				t.Errorf("%s page %d does not match the spec:\n  %s", dt, page, strings.Join(errs, "\n  "))
			}
			m, _ := val.(map[string]any)
			arr, _ := m["data"].([]any)
			docs += len(arr)
			next, _ := m["next_token"].(string)
			if next == "" {
				break
			}
			q.Set("next_token", next)
		}
		t.Logf("%s: %d documents", dt, docs)
	}
}

type liveSink struct {
	mu    sync.Mutex
	lines map[string][][]byte
}

func (s *liveSink) Emit(_ context.Context, dataType string, _ time.Time, lines [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines[dataType] = append(s.lines[dataType], lines...)
	return nil
}

// The connector pulls the same span and maps every line without an error.
func TestLive_FetchAndMap(t *testing.T) {
	tok := liveToken(t)
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -liveDays(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := New(Options{})
	sink := &liveSink{lines: map[string][][]byte{}}
	err := c.Fetch(ctx, connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: tok}}, from, to, sink)
	require.NoError(t, err)

	for dt, lines := range sink.lines {
		records := 0
		for i, line := range lines {
			var fields map[string]any
			require.NoError(t, json.Unmarshal(line, &fields), "%s line %d", dt, i)
			res, err := c.Map(connector.RawRecord{UserID: "live-check", Provider: Slug, DataType: dt, Fields: fields})
			if err != nil {
				t.Errorf("%s line %d does not map: %s", dt, i, strings.SplitN(err.Error(), "\"", 2)[0])
				continue
			}
			records += len(res.QuantitySamples) + len(res.CategorySamples) + len(res.SleepSessions) + len(res.Workouts)
		}
		t.Logf("%s: %d lines, %d records", dt, len(lines), records)
	}
}
