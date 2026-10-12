// SPDX-License-Identifier: AGPL-3.0-only

//go:build live

package whoop

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// A smoke test against the real WHOOP API, for a maintainer with a WHOOP
// account and a registered app. It is never run by CI: it needs the live
// build tag and an access token, see docs/connectors/whoop.md.
//
//	OHS_WHOOP_LIVE_ACCESS_TOKEN=... go test -tags live -count=1 -run TestLive ./connectors/whoop/
//
// It reads OHS_WHOOP_LIVE_DAYS days (3 by default) of every data type, checks
// each record against the documented schema and maps it. It only reads: it
// never refreshes (WHOOP rotates refresh tokens, so a refresh here would
// invalidate the one an instance stores) and never revokes. It logs counts
// and field names, never a value.

type liveSink struct {
	mu    sync.Mutex
	lines map[string][][]byte
}

func (s *liveSink) Emit(_ context.Context, dataType string, _ time.Time, lines [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range lines {
		s.lines[dataType] = append(s.lines[dataType], append([]byte(nil), l...))
	}
	return nil
}

// liveItemSchemas names the schema of one record of each data type.
var liveItemSchemas = map[string]string{
	DataTypeCycle:    "Cycle",
	DataTypeRecovery: "Recovery",
	DataTypeSleep:    "Sleep",
	DataTypeWorkout:  "WorkoutV2",
	DataTypeBody:     "UserBodyMeasurement",
}

func TestLive_FetchValidateMap(t *testing.T) {
	token := os.Getenv("OHS_WHOOP_LIVE_ACCESS_TOKEN")
	if token == "" {
		t.Skip("set OHS_WHOOP_LIVE_ACCESS_TOKEN to run the live WHOOP smoke test")
	}
	days := 3
	if s := os.Getenv("OHS_WHOOP_LIVE_DAYS"); s != "" {
		n, err := strconv.Atoi(s)
		require.NoError(t, err, "OHS_WHOOP_LIVE_DAYS")
		require.True(t, n > 0 && n <= 30, "OHS_WHOOP_LIVE_DAYS must be 1 to 30")
		days = n
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := New(Options{})
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -days)
	sink := &liveSink{lines: map[string][][]byte{}}
	err := c.Fetch(ctx, connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: token}}, from, to, sink)
	require.NoError(t, err, "the pull (an expired token shows as needs reauth: take a fresh one)")

	for _, dt := range AllDataTypes {
		lines := sink.lines[dt]
		t.Logf("%s: %d records", dt, len(lines))
		unknown := map[string]bool{}
		var mapped int
		for i, l := range lines {
			if name, ok := liveItemSchemas[dt]; ok {
				for _, e := range validate(decodeNumbers(t, l), whoopSchemas[name], dt) {
					// A field WHOOP added since is worth knowing, not a failure.
					if strings.HasSuffix(e, "is not in the schema") {
						unknown[e] = true
						continue
					}
					// The message names a path and a rule, not the value.
					t.Errorf("%s record %d: %s", dt, i, e)
				}
			}
			var fields map[string]any
			require.NoError(t, json.Unmarshal(l, &fields))
			res, err := c.Map(connector.RawRecord{UserID: "live", Provider: Slug, DataType: dt, Fields: fields})
			if err != nil {
				t.Errorf("%s record %d does not map", dt, i)
				continue
			}
			if errs := model.CanonicalizeUnits(&res); len(errs) > 0 {
				t.Errorf("%s record %d: %d unit errors", dt, i, len(errs))
			}
			mapped += len(res.QuantitySamples) + len(res.SleepSessions) + len(res.Workouts) + len(res.DailySummaries)
		}
		for e := range unknown {
			t.Logf("new field: %s", e)
		}
		t.Logf("%s: %d mapped rows", dt, mapped)
	}
}
