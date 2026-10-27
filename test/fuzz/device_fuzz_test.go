// SPDX-License-Identifier: AGPL-3.0-only

package fuzz

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// devices are the /v1/ingest routes' connectors, in a fixed order: the fuzz
// target picks one by index.
var devices = []string{"apple_health", "health_connect", "bluetooth", "activity_files", "xiaomi"}

func repoRoot(tb testing.TB) string {
	tb.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// FuzzDeviceUpload hands a decompressed upload part to a device connector's
// ParseUpload, the check the upload route runs before it stores the part.
// When ParseUpload accepts the part, the worker will later map every line of
// it, so the lines then go down the worker's path too: no panic, and no
// stored record that breaks the canonical model's invariants.
func FuzzDeviceUpload(f *testing.F) {
	cs := loadCorpora()
	for pi, p := range providers {
		if !p.device {
			continue
		}
		di := -1
		for i, d := range devices {
			if d == p.slug {
				di = i
			}
		}
		for _, dt := range cs[pi].types {
			var body bytes.Buffer
			for _, l := range cs[pi].lines[dt] {
				if l.Valid() {
					body.Write(l.JSON)
					body.WriteByte('\n')
				}
			}
			f.Add(uint8(di), dt, body.Bytes())
		}
	}
	if b, err := os.ReadFile(filepath.Join(repoRoot(f), "connectors", "xiaomi", "testdata", "upload.ndjson")); err == nil {
		f.Add(uint8(4), "fitness_data", b)
	}
	f.Add(uint8(0), "HKQuantityTypeIdentifierHeartRate", []byte("{}\n\n{\"a\":1}"))

	f.Fuzz(func(t *testing.T, di uint8, typ string, body []byte) {
		slug := devices[int(di)%len(devices)]
		c := mustConnector(t, slug)
		dev, ok := c.(connector.DeviceConnector)
		if !ok {
			t.Fatalf("%s is not a device connector", slug)
		}
		meta := connector.UploadMeta{TenantID: uuid.Nil, UserID: uuid.Nil, Type: typ, Batch: "fuzz"}
		if err := dev.ParseUpload(meta, bytes.NewReader(body), nil); err != nil {
			return
		}
		var lines []synth.Line
		for _, l := range bytes.Split(body, []byte("\n")) {
			lines = append(lines, synth.Line{DataType: typ, JSON: l})
		}
		for _, r := range synthtest.Run(c, lines, genNow) {
			switch {
			case r.Outcome == synthtest.Panic:
				t.Fatalf("%s %q: accepted upload panics the mapper: %s", slug, typ, r.Detail)
			case r.Outcome == synthtest.Stored && len(r.Broken) > 0:
				t.Fatalf("%s %q: accepted upload stores a record breaking %v", slug, typ, r.Broken)
			}
		}
	})
}
