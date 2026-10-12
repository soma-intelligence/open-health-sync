// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLongExport writes an invented export.xml of days days of heart rate
// then days days of steps, perDay records each, in the order the Health app
// writes them (by type, then by date).
func writeLongExport(t *testing.T, days, perDay int) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "export.xml")
	f, err := os.Create(name)
	require.NoError(t, err)
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprintln(w, `<HealthData locale="en_US"><ExportDate value="2026-07-15 09:00:00 +0200"/>`)
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.FixedZone("", 3600))
	for _, typ := range []string{"HKQuantityTypeIdentifierHeartRate", "HKQuantityTypeIdentifierStepCount"} {
		for d := range days {
			for i := range perDay {
				at := start.AddDate(0, 0, d).Add(time.Duration(i) * time.Minute).Format(exportLayout)
				fmt.Fprintf(w, ` <Record type="%s" sourceName="Gen" unit="count" startDate="%s" endDate="%s" value="%d"/>`+"\n", typ, at, at, 50+i)
			}
		}
	}
	fmt.Fprintln(w, `</HealthData>`)
	require.NoError(t, w.Flush())
	require.NoError(t, f.Close())
	return name
}

// TestLongExportStreams: with a buffer far smaller than the export, every
// (type, day) still comes out as one whole part, because the input is in
// date order and the group written to least recently is sent first.
func TestLongExportStreams(t *testing.T) {
	const days, perDay = 400, 20
	name := writeLongExport(t, days, perDay)
	e, err := Open(name)
	require.NoError(t, err)
	defer e.Close()

	opts := Options{MaxBufferedBytes: 32 << 10}
	seen := map[groupKey]int{}
	maxPart := 0
	stats, err := Convert(context.Background(), e, opts, func(p Part) error {
		seen[groupKey{p.Type, p.Day}]++
		maxPart = max(maxPart, len(p.NDJSON))
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2*days*perDay, stats.Records)
	assert.Equal(t, 2*days*perDay, stats.Lines)
	assert.Equal(t, 2*days, stats.Parts)
	for k, n := range seen {
		assert.Equal(t, 1, n, "%v sent in %d parts", k, n)
	}
	assert.LessOrEqual(t, maxPart, opts.MaxBufferedBytes)
}
