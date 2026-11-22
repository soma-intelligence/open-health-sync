// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// "Local" makes time.LoadLocation return the server's own zone, so the day a
// sample counts toward would move with the host TZ. It names no zone of the
// user's: read it as UTC, as an unknown zone already is.
func TestSampleLocationRefusesLocal(t *testing.T) {
	local := time.Local
	time.Local = time.FixedZone("server", -7*3600)
	t.Cleanup(func() { time.Local = local })

	at := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	assert.Equal(t, time.UTC, sampleLocation(map[string]any{MetadataTimezone: "Local"}))
	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), localDate(at, map[string]any{MetadataTimezone: "Local"}))
}

// A zone is loaded once and served from the cache after; a name that does not
// load reads as UTC and is not kept.
func TestSampleLocationCachesZones(t *testing.T) {
	md := map[string]any{MetadataTimezone: "Europe/Paris"}
	first := sampleLocation(md)
	assert.Equal(t, "Europe/Paris", first.String())
	assert.Same(t, first, sampleLocation(md))

	bad := map[string]any{MetadataTimezone: "Not/AZone"}
	assert.Equal(t, time.UTC, sampleLocation(bad))
	_, kept := zoneCache.Load("Not/AZone")
	assert.False(t, kept)
}

// The local day of a record is its own zone's, else the user's, else UTC, and
// the tz column names the zone that was used.
func TestRecordZoneOrder(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)
	own := map[string]any{MetadataTimezone: "America/Los_Angeles"}
	bad := map[string]any{MetadataTimezone: "Not/AZone"}

	none := scope{}
	assert.Equal(t, "America/Los_Angeles", none.location(own).String())
	assert.Equal(t, time.UTC, none.location(nil))
	assert.Equal(t, time.UTC, none.location(bad))
	assert.Equal(t, "America/Los_Angeles", none.tzColumn(own, nil))
	assert.Nil(t, none.tzColumn(map[string]any{}, nil), "no zone anywhere: NULL, the day is UTC")
	assert.Equal(t, "Not/AZone", none.tzColumn(bad, nil), "without a user zone the column is what the metadata says, as before")

	user := scope{zone: paris}
	assert.Equal(t, "America/Los_Angeles", user.location(own).String(), "the record's own zone first")
	assert.Same(t, paris, user.location(nil))
	assert.Same(t, paris, user.location(bad), "a name that is no zone falls to the user's")
	assert.Same(t, paris, user.location(map[string]any{MetadataTimezone: "Local"}))
	assert.Equal(t, "America/Los_Angeles", user.tzColumn(own, nil))
	assert.Equal(t, "Europe/Paris", user.tzColumn(map[string]any{}, nil))
	assert.Equal(t, "Europe/Paris", user.tzColumn(bad, nil))
	assert.Equal(t, "Asia/Tokyo", user.tzColumn(map[string]any{}, "Asia/Tokyo"), "a stored row keeps the zone it was written with")
	assert.Equal(t, "America/Los_Angeles", user.tzColumn(own, "Asia/Tokyo"))

	// 23:30 UTC on March 1 is March 2 in Paris.
	at := time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC)
	acc := &model.MapResult{QuantitySamples: []model.QuantitySample{{QuantityType: model.QTStepCount, StartDate: at}}}
	utcDays := touchedDays(scope{provider: "dev"}, acc)
	assert.Contains(t, utcDays, dayKey{provider: "dev", day: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)})
	parisDays := touchedDays(scope{provider: "dev", zone: paris}, acc)
	require.Len(t, parisDays, 1)
	assert.Same(t, paris, parisDays[dayKey{provider: "dev", day: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)}])

	cov := newCoverageBuilder(at)
	cov.zone = paris
	cov.add(acc)
	assert.Equal(t, []coverageEntry{{Metric: model.QTStepCount, Date: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), Count: 1}}, cov.entries())
}
