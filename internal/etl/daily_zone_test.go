// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
