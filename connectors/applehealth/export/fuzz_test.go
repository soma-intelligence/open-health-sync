// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// FuzzConvertXML runs an arbitrary export.xml through Convert, the parser
// behind `ohs import apple-health`. No input may panic it, and every part it
// produces must pass the server's own check of an upload
// (applehealth.ParseUpload): a part the importer builds and the server
// refuses fails the whole import.
func FuzzConvertXML(f *testing.F) {
	if b, err := os.ReadFile(filepath.Join("testdata", "apple_health_export", "export.xml")); err == nil {
		f.Add(b)
	}
	f.Add([]byte(`<?xml version="1.0"?><HealthData><Record type="HKQuantityTypeIdentifierHeartRate" unit="count/min" value="61" sourceName="W" startDate="2026-01-10 08:00:00 -0800" endDate="2026-01-10 08:00:00 -0800"/></HealthData>`))
	f.Add([]byte(`<HealthData><Workout workoutActivityType="HKWorkoutActivityTypeRunning" duration="30" durationUnit="min" startDate="2026-01-10 08:00:00 +0000" endDate="2026-01-10 08:30:00 +0000"><WorkoutRoute><FileReference path="/workout-routes/../../../../etc/passwd"/></WorkoutRoute></Workout></HealthData>`))
	f.Add([]byte(`<!DOCTYPE HealthData [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;">]><HealthData>&b;</HealthData>`))
	server := applehealth.New()
	f.Fuzz(func(t *testing.T, xml []byte) {
		dir := t.TempDir()
		p := filepath.Join(dir, "export.xml")
		if err := os.WriteFile(p, xml, 0o600); err != nil {
			t.Fatal(err)
		}
		e, err := Open(p)
		if err != nil {
			return
		}
		defer func() { _ = e.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = Convert(ctx, e, Options{MaxPartBytes: 64 << 10, MaxBufferedBytes: 256 << 10}, func(part Part) error {
			day, err := time.Parse(time.DateOnly, part.Day)
			if err != nil {
				t.Fatalf("part %d has day %q", part.Number, part.Day)
			}
			meta := connector.UploadMeta{Type: part.Type, Day: day, Batch: "fuzz", Part: part.Number}
			if err := server.ParseUpload(meta, bytes.NewReader(part.NDJSON), nil); err != nil {
				t.Fatalf("part %d (%s %s) refused by the server: %v\n%s", part.Number, part.Type, part.Day, err, clipBytes(part.NDJSON, 600))
			}
			return nil
		})
	})
}

func clipBytes(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
