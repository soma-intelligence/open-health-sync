// SPDX-License-Identifier: AGPL-3.0-only

package fuzz

import (
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// FuzzConnectorPipeline sends one line down the worker's path for one
// connector and data type: etl.Reader, Map, source attribution, unit
// canonicalization and validation (synthtest.Run). Whatever the bytes, the
// mapper must not panic, and a record that validation would store must keep
// the canonical model's invariants (finite numbers, no time more than two
// days ahead, no end before its start).
func FuzzConnectorPipeline(f *testing.F) {
	for pi := range providers {
		c := loadCorpora()[pi]
		for ti, dt := range c.types {
			for _, l := range c.lines[dt] {
				f.Add(uint8(pi), uint8(ti), l.JSON)
			}
		}
	}
	f.Fuzz(func(t *testing.T, pi, ti uint8, line []byte) {
		p, _, dt := pick(pi, ti)
		if dt == "" {
			return
		}
		conn := mustConnector(t, p.slug)
		res := synthtest.Run(conn, []synth.Line{{DataType: dt, JSON: line}}, genNow)
		for _, r := range res {
			switch {
			case r.Outcome == synthtest.Panic:
				t.Fatalf("%s %s: mapper panicked: %s", p.slug, dt, r.Detail)
			case r.Outcome == synthtest.Stored && len(r.Broken) > 0:
				t.Fatalf("%s %s: stored a record breaking %v", p.slug, dt, r.Broken)
			}
		}
	})
}
