// SPDX-License-Identifier: AGPL-3.0-only

package synth

import (
	"fmt"
	"time"
	_ "time/tzdata" // zone names must resolve on a host without a zone database
)

// Leg is a stretch of time spent in one zone, from From until the next leg.
type Leg struct {
	From time.Time
	Zone *time.Location
}

// travelZones is the itinerary of the Travel scenario, chosen for their
// offsets: half and three-quarter hours, a 45 minute zone with its own DST,
// both sides of the date line and the largest offsets in use.
var travelZones = []string{
	"America/New_York",    // -05:00 / -04:00
	"Asia/Kolkata",        // +05:30
	"Asia/Kathmandu",      // +05:45
	"Pacific/Chatham",     // +12:45 / +13:45
	"Pacific/Kiritimati",  // +14:00
	"Pacific/Honolulu",    // -10:00, the day after Kiritimati is the day before
	"Australia/Lord_Howe", // +10:30 / +11:00, a 30 minute DST change
}

func mustZone(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(fmt.Sprintf("synth: zone %s: %v", name, err))
	}
	return l
}

// ZoneAt returns the zone the person is in at t.
func (tl *Timeline) ZoneAt(t time.Time) *time.Location {
	z := tl.Legs[0].Zone
	for _, l := range tl.Legs {
		if l.From.After(t) {
			break
		}
		z = l.Zone
	}
	return z
}

// Local returns t in the zone the person is in at t.
func (tl *Timeline) Local(t time.Time) time.Time { return t.In(tl.ZoneAt(t)) }

// transitions returns the instants in [from, to) where loc changes its UTC
// offset, to the minute.
func transitions(loc *time.Location, from, to time.Time) []time.Time {
	var out []time.Time
	_, prev := from.In(loc).Zone()
	for t := from.Truncate(time.Hour); t.Before(to); t = t.Add(time.Hour) {
		_, off := t.In(loc).Zone()
		if off == prev {
			continue
		}
		// Narrow down to the minute.
		lo := t.Add(-time.Hour)
		for m := lo; !m.After(t); m = m.Add(time.Minute) {
			if _, o := m.In(loc).Zone(); o == off {
				out = append(out, m)
				break
			}
		}
		prev = off
	}
	return out
}

// Transitions returns the DST changes of the home zone inside the days of the
// timeline.
func (tl *Timeline) Transitions() []time.Time {
	var out []time.Time
	for _, d := range tl.Days {
		out = append(out, transitions(d.Loc, d.Start, d.End)...)
	}
	return out
}
