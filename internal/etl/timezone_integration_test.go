// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
)

// setZone sets the env user's time zone as the API does (source "explicit"),
// or clears it with "".
func (e *env) setZone(zone string) {
	e.t.Helper()
	if zone == "" {
		e.exec(`UPDATE ohs_core.end_users SET timezone = NULL, timezone_source = NULL, timezone_seen_at = NULL WHERE id = $1`, e.user)
		return
	}
	e.exec(`UPDATE ohs_core.end_users SET timezone = $2, timezone_source = 'explicit', timezone_seen_at = NULL WHERE id = $1`, e.user, zone)
}

// userZone is the env user's time zone and where it came from, "" for NULL.
func (e *env) userZone() (zone, source string) {
	e.t.Helper()
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`SELECT coalesce(timezone, ''), coalesce(timezone_source, '') FROM ohs_core.end_users WHERE id = $1`, e.user).Scan(&zone, &source))
	return zone, source
}

// sampleZones is the tz column of every stored sample of the env's user, by
// its value.
func (e *env) sampleZones() map[float64]string {
	e.t.Helper()
	rows, err := e.read().Query(context.Background(), `
		SELECT value, coalesce(tz, '') FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user)
	require.NoError(e.t, err)
	defer rows.Close()
	out := map[float64]string{}
	for rows.Next() {
		var v float64
		var tz string
		require.NoError(e.t, rows.Scan(&v, &tz))
		out[v] = tz
	}
	require.NoError(e.t, rows.Err())
	return out
}

// derivedSteps is the step count of every derived daily summary of the env's
// user, by day ("2026-03-01"), with the zone the day was summed in.
func (e *env) derivedSteps() map[string]string {
	e.t.Helper()
	rows, err := e.read().Query(context.Background(), `
		SELECT to_char(summary_date, 'YYYY-MM-DD'), step_count, metadata->>'timezone' FROM ohs_data.daily_summaries
		WHERE tenant_id = $1 AND user_id = $2 AND derived`, e.tenant, e.user)
	require.NoError(e.t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var day, zone string
		var steps int
		require.NoError(e.t, rows.Scan(&day, &steps, &zone))
		out[day] = fmt.Sprintf("%d %s", steps, zone)
	}
	require.NoError(e.t, rows.Err())
	return out
}

// coverageDays are the days of the env user's step count coverage.
func (e *env) coverageDays() []string {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT to_char(date, 'YYYY-MM-DD') FROM ohs_sync.pull_coverage
		WHERE tenant_id = $1 AND user_id = $2 AND metric = 'step_count' ORDER BY date`, e.tenant, e.user)
	require.NoError(e.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		require.NoError(e.t, rows.Scan(&d))
		out = append(out, d)
	}
	require.NoError(e.t, rows.Err())
	return out
}

// A sample that carries no zone counts toward the day of the user's zone,
// not the UTC day: its tz column says so, the derived daily summary and the
// coverage are those of that day, and a deletion finds the day again. A
// sample with a zone of its own keeps it. Changing the user's zone moves no
// stored sample. Sealed and clear, in one batch and chunk by chunk.
func TestUserZoneAttributesTheDay(t *testing.T) {
	steps := func(v float64, at time.Time, extra ...any) string {
		f := map[string]any{"type": "step_count", "value": v, "unit": "count", "start": at.Format(time.RFC3339)}
		for i := 0; i+1 < len(extra); i += 2 {
			f[extra[i].(string)] = extra[i+1]
		}
		return line(f)
	}
	// 03:00 UTC on March 2 is 19:00 on March 1 in Los Angeles.
	evening := time.Date(2026, 3, 2, 3, 0, 0, 0, time.UTC)
	for _, mode := range []struct {
		name  string
		clear bool
		chunk int
	}{{"sealed", false, 0}, {"clear", true, 0}, {"sealed chunked", false, 1}, {"clear chunked", true, 1}} {
		t.Run(mode.name, func(t *testing.T) {
			e := newEnv(t)
			e.now = func() time.Time { return evening.AddDate(0, 0, 30) }
			e.register("mix", mixMap)
			run := func(lines ...string) {
				opts := e.opts()
				if mode.clear {
					opts = e.clearOpts()
				}
				if mode.chunk > 0 {
					opts.Writer.ChunkRecords = mode.chunk
				}
				_, err := ProcessWith(context.Background(), e.pool, e.rs, e.upload(e.tenant, e.user, "mix", "plain", lines...), opts)
				require.NoError(t, err)
			}

			// No zone anywhere: the UTC day, as before.
			run(steps(11, evening.AddDate(0, 0, -10)))
			assert.Equal(t, map[float64]string{11: ""}, e.sampleZones())
			assert.Equal(t, map[string]string{"2026-02-20": "11 UTC"}, e.derivedSteps())

			e.setZone("America/Los_Angeles")
			run(
				steps(100, evening, "ext", "evening"),
				steps(40, evening.Add(time.Hour)),
				// Half past midnight on March 11 in Paris, 23:30 UTC on March 10.
				steps(7, time.Date(2026, 3, 10, 23, 30, 0, 0, time.UTC), "tz", "Europe/Paris"),
			)
			assert.Equal(t, map[float64]string{11: "", 100: "America/Los_Angeles", 40: "America/Los_Angeles", 7: "Europe/Paris"}, e.sampleZones())
			assert.Equal(t, map[string]string{
				"2026-02-20": "11 UTC",
				"2026-03-01": "140 America/Los_Angeles",
				"2026-03-11": "7 Europe/Paris",
			}, e.derivedSteps())
			assert.Equal(t, []string{"2026-02-20", "2026-03-01", "2026-03-11"}, e.coverageDays())
			var withZone int
			require.NoError(t, e.read().QueryRow(context.Background(), `
				SELECT count(*) FROM ohs_data.quantity_samples
				WHERE tenant_id = $1 AND user_id = $2 AND value = 100 AND metadata ? 'timezone'`, e.tenant, e.user).Scan(&withZone))
			assert.Zero(t, withZone, "the metadata stays what the source sent")

			// The user moves: what is stored keeps its day, new samples take
			// the new zone. 20:00 UTC on March 5 is March 6 in Tokyo.
			e.setZone("Asia/Tokyo")
			run(steps(9, time.Date(2026, 3, 5, 20, 0, 0, 0, time.UTC)))
			assert.Equal(t, map[float64]string{11: "", 100: "America/Los_Angeles", 40: "America/Los_Angeles", 7: "Europe/Paris", 9: "Asia/Tokyo"},
				e.sampleZones())
			assert.Equal(t, map[string]string{
				"2026-02-20": "11 UTC",
				"2026-03-01": "140 America/Los_Angeles",
				"2026-03-06": "9 Asia/Tokyo",
				"2026-03-11": "7 Europe/Paris",
			}, e.derivedSteps())

			// A deletion reads the day back from the stored zone, not from
			// the user's zone of today.
			run(line(map[string]any{"kind": "delete", "id": "evening"}))
			assert.Equal(t, "40 America/Los_Angeles", e.derivedSteps()["2026-03-01"])
			assert.Len(t, e.derivedSteps(), 4)
		})
	}
}

// A device upload that carries a zone teaches the user's zone: the zone of
// its latest record. An older record teaches nothing, a zone the developer
// set is never replaced, and a cloud source teaches nothing either.
func TestDeviceUploadTeachesTheUserZone(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	e.register("cloud", quantityMap)
	e.now = func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) }
	hr := func(day int, zone string) string {
		at := time.Date(2026, 1, day, 14, 30, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000Z")
		md := ""
		if zone != "" {
			md = fmt.Sprintf(`"metadata":{"HKTimeZone":%q},`, zone)
		}
		return fmt.Sprintf(`{"endDate":%q,%s"quantityType":"HKQuantityTypeIdentifierHeartRate","sourceName":"Apple Watch","startDate":%q,"unit":"count/min","value":%d}`,
			at, md, at, 60+day)
	}
	upload := func(lines ...string) {
		e.process(e.upload(e.tenant, e.user, applehealth.Slug, "mixed", lines...))
	}
	zone := func() string {
		z, source := e.userZone()
		return z + " " + source
	}

	// A cloud source with a zone, and a device upload without one.
	e.process(e.upload(e.tenant, e.user, "cloud", "x",
		line(map[string]any{"type": "heart_rate", "value": 60.0, "unit": "bpm", "start": "2026-01-02T10:00:00Z", "tz": "Asia/Kolkata"})))
	upload(hr(3, ""))
	assert.Equal(t, " ", zone())

	upload(hr(10, "Europe/Paris"), hr(9, "America/New_York"))
	assert.Equal(t, "Europe/Paris learnt", zone())

	upload(hr(5, "America/New_York"))
	assert.Equal(t, "Europe/Paris learnt", zone(), "a backfill of older records does not move the zone")

	upload(hr(12, "Asia/Tokyo"), hr(13, "Not/AZone"))
	assert.Equal(t, "Asia/Tokyo learnt", zone())

	e.setZone("Europe/London")
	upload(hr(20, "America/Chicago"))
	assert.Equal(t, "Europe/London explicit", zone(), "the developer's zone is never replaced")

	// Cleared, the zone is learnt again.
	e.setZone("")
	upload(hr(21, "America/Chicago"))
	assert.Equal(t, "America/Chicago learnt", zone())

	// The learnt zone is the day of what a source without a zone sends next.
	e.process(e.upload(e.tenant, e.user, "cloud", "x",
		line(map[string]any{"type": "step_count", "value": 5.0, "unit": "count", "start": "2026-01-25T03:00:00Z"})))
	assert.Equal(t, "5 America/Chicago", e.derivedSteps()["2026-01-24"])
}
