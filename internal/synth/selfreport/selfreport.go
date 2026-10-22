// SPDX-License-Identifier: AGPL-3.0-only

// Package selfreport renders a synthetic timeline as self reports, the
// ratings an app posts to POST /v1/users/{id}/self-reports and the server
// stores one per line.
//
// Source: this project's own API (docs/self-reports.md): no vendor
// documents it. The kinds, their scales and the report members (id, kind,
// value, start_at, end_at, timezone) are taken from there.
package selfreport

import (
	"math"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "self_report"

// DataType is the X-OHS-Type of a self report part.
const DataType = "self_report"

var reportShape = synth.Shape{
	ID:       "id",
	Required: []string{"id", "kind", "value", "start_at"},
	Optional: []string{"end_at", "timezone"},
	Numbers:  map[string][2]float64{"value": {0, 10}},
	Times:    []string{"start_at", "end_at"},
	Start:    "start_at",
	End:      "end_at",
	Enums:    []string{"kind", "timezone"},
	// The server writes the stored line from the checked report.
	Closed: true,
}

func clampInt(v float64, lo, hi int) int {
	return int(math.Max(float64(lo), math.Min(float64(hi), math.Round(v))))
}

// Lines renders tl as self report lines: a morning check-in every day, an
// effort rating after each workout and the odd pain report.
func Lines(tl *synth.Timeline) []synth.Line {
	e := synth.NewEmitter(tl, Slug)
	r := synth.NewRand(tl.Opt.Seed, "self_report")
	e.UnknownType = "self_report_v2"
	report := func(kind string, v int, at time.Time, end *time.Time) {
		d := synth.Doc{"id": r.UUID(), "kind": kind, "value": v, "start_at": tl.LocalISO(at), "timezone": tl.ZoneAt(at).String()}
		if end != nil {
			d["end_at"] = end.UTC().Format(time.RFC3339Nano)
		} else {
			d["end_at"] = nil
		}
		e.Add(DataType, at, d, reportShape)
	}
	for i, d := range tl.Days {
		at := synth.AtLocal(d, 8.2)
		if d.Missing || at.After(tl.Opt.Now) {
			continue
		}
		score := 3.0
		if s, ok := tl.NightOf(i); ok {
			score = 1 + float64(s.Score)/25
			report("sleep_quality", clampInt(score, 1, 5), at, nil)
		}
		report("feeling", clampInt(score+r.Norm(0, 0.6), 1, 5), at, nil)
		report("mood", clampInt(score+r.Norm(0, 0.8), 1, 5), at.Add(time.Second), nil)
		report("energy", clampInt(score+r.Norm(0, 0.7), 1, 5), at.Add(2*time.Second), nil)
		report("stress", clampInt(5-score+r.Norm(0, 0.8), 1, 5), at.Add(3*time.Second), nil)
		if r.Chance(0.15) {
			report("pain", r.Intn(6), at.Add(4*time.Second), nil)
		}
	}
	for _, w := range tl.Recorded() {
		if w.End.After(tl.Opt.Now) {
			continue
		}
		end := w.End
		report("exertion", clampInt(2+7*w.Sport.Intensity(), 0, 10), w.Start, &end)
		report("muscle_soreness", clampInt(1+3*w.Sport.Intensity(), 1, 5), w.End.Add(20*time.Hour), nil)
	}
	return e.Lines()
}
