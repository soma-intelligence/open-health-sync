// SPDX-License-Identifier: AGPL-3.0-only

package demo_test

import (
	"reflect"
	"testing"
	"time"

	_ "github.com/soma-intelligence/open-health-sync/connectors/all"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/demo"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

var paris = func() *time.Location {
	loc, err := time.LoadLocation(demo.DefaultZone)
	if err != nil {
		panic(err)
	}
	return loc
}()

// today is a fixed run day, so the tests do not depend on the clock.
var today = time.Date(2026, 3, 15, 14, 0, 0, 0, paris)

func generate(t *testing.T, o demo.Options) map[string]*model.MapResult {
	t.Helper()
	if o.Today.IsZero() {
		o.Today = today
	}
	batches, err := demo.Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*model.MapResult{}
	for _, b := range batches {
		out[b.Provider] = b.Records
	}
	return out
}

func TestProvidersAreConnectors(t *testing.T) {
	if len(demo.Providers) != 6 {
		t.Fatalf("%d providers, want 6", len(demo.Providers))
	}
	for _, p := range demo.Providers {
		if _, ok := connector.Get(p); !ok {
			t.Errorf("%s is not a registered connector", p)
		}
	}
}

func TestDeterministic(t *testing.T) {
	a := generate(t, demo.Options{Days: 14, Seed: 7})
	b := generate(t, demo.Options{Days: 14, Seed: 7})
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the same seed gave different records")
	}
	c := generate(t, demo.Options{Days: 14, Seed: 8})
	if reflect.DeepEqual(a[demo.Whoop].QuantitySamples, c[demo.Whoop].QuantitySamples) {
		t.Fatal("another seed gave the same records")
	}
}

// A run on a later day must give the same records for the days both runs
// cover, or a second run would add near duplicates instead of nothing.
func TestStableAcrossRunDays(t *testing.T) {
	a := generate(t, demo.Options{Days: 10, Seed: 3})
	b := generate(t, demo.Options{Days: 10, Seed: 3, Today: today.AddDate(0, 0, 2)})
	for _, p := range demo.Providers {
		hashes := map[string]bool{}
		for _, s := range b[p].QuantitySamples {
			hashes[s.ContentHash] = true
		}
		missing := 0
		for _, s := range a[p].QuantitySamples {
			if s.StartDate.After(today.AddDate(0, 0, -8)) && !hashes[s.ContentHash] {
				missing++
			}
		}
		if missing > 0 {
			t.Errorf("%s: %d samples of the shared days differ between run days", p, missing)
		}
	}
}

func TestRecordsPassTheValidator(t *testing.T) {
	recs := generate(t, demo.Options{Days: demo.DefaultDays, Seed: 1})
	now := today
	for _, p := range demo.Providers {
		mr := recs[p]
		if mr.IsEmpty() {
			t.Fatalf("%s: no records", p)
		}
		if errs := model.CanonicalizeUnits(mr); len(errs) > 0 {
			t.Fatalf("%s: unit errors: %v", p, errs)
		}
		errs := etl.ValidateMapResultAt(mr, now)
		if r := etl.Rejections(errs); len(r) > 0 {
			t.Errorf("%s: %d rejections, first %s.%s: %s", p, len(r), r[0].RecordType, r[0].Field, r[0].Message)
		}
		if s := etl.Suspicions(errs); len(s) > 0 {
			t.Errorf("%s: %d suspect values, first %s: %s", p, len(s), s[0].Field, s[0].Message)
		}
		for _, s := range mr.QuantitySamples {
			if s.Metadata[demo.MetadataSynthetic] != true {
				t.Fatalf("%s: a sample is not marked synthetic", p)
			}
			if s.StartDate.After(now) || s.EndDate.After(now) {
				t.Fatalf("%s: a sample is in the future", p)
			}
			if s.ContentHash == "" || s.Provider != p {
				t.Fatalf("%s: sample without hash or with provider %q", p, s.Provider)
			}
		}
		for _, s := range mr.SleepSessions {
			if s.Metadata[demo.MetadataSynthetic] != true {
				t.Fatalf("%s: a sleep session is not marked synthetic", p)
			}
		}
		for _, w := range mr.Workouts {
			if w.Metadata[demo.MetadataSynthetic] != true {
				t.Fatalf("%s: a workout is not marked synthetic", p)
			}
		}
	}
}

func TestPlausibleRanges(t *testing.T) {
	recs := generate(t, demo.Options{Days: demo.DefaultDays, Seed: 42})
	type band struct{ lo, hi float64 }
	bands := map[string]band{
		model.QTHeartRate:                {35, 200},
		model.QTRestingHeartRate:         {38, 75},
		model.QTHRVRMSSD:                 {15, 120},
		model.QTOxygenSaturation:         {93, 100},
		model.QTRespiratoryRate:          {11, 18},
		model.QTBodyTemperatureDeviation: {-1, 1},
		"body_mass":                      {60, 90},
		"body_fat_percentage":            {10, 30},
		"blood_pressure_systolic":        {100, 140},
		"blood_pressure_diastolic":       {60, 95},
		"vo2_max":                        {38, 58},
	}
	stepsPerDay := map[string]map[string]float64{}
	types := map[string]map[string]bool{}
	for p, mr := range recs {
		types[p] = map[string]bool{}
		for _, s := range mr.QuantitySamples {
			types[p][s.QuantityType] = true
			if b, ok := bands[s.QuantityType]; ok && (s.Value < b.lo || s.Value > b.hi) {
				t.Errorf("%s %s = %v, outside [%v, %v]", p, s.QuantityType, s.Value, b.lo, b.hi)
			}
			if s.QuantityType == model.QTStepCount {
				if stepsPerDay[p] == nil {
					stepsPerDay[p] = map[string]float64{}
				}
				stepsPerDay[p][s.StartDate.In(paris).Format(time.DateOnly)] += s.Value
			}
		}
	}
	for p, days := range stepsPerDay {
		if len(days) != demo.DefaultDays {
			t.Errorf("%s: steps on %d days, want %d", p, len(days), demo.DefaultDays)
		}
		for d, n := range days {
			if n < 1500 || n > 30000 {
				t.Errorf("%s: %v steps on %s", p, n, d)
			}
		}
	}
	// Overlaps between sources, as with real devices.
	for qt, want := range map[string][]string{
		model.QTHeartRate:        {demo.Oura, demo.Whoop, demo.Fitbit, demo.Polar, demo.Withings},
		model.QTStepCount:        {demo.Fitbit, demo.AppleHealth},
		model.QTHRVRMSSD:         {demo.Oura, demo.Whoop},
		model.QTRestingHeartRate: {demo.Oura, demo.Whoop, demo.Fitbit},
		"body_mass":              {demo.Withings, demo.AppleHealth},
	} {
		for _, p := range want {
			if !types[p][qt] {
				t.Errorf("%s gives no %s", p, qt)
			}
		}
	}

	for _, p := range []string{demo.Oura, demo.Whoop} {
		mr := recs[p]
		if len(mr.SleepSessions) != demo.DefaultDays {
			t.Errorf("%s: %d nights, want %d", p, len(mr.SleepSessions), demo.DefaultDays)
		}
		byParent := map[string]int{}
		for _, ph := range mr.SleepPhases {
			byParent[ph.ParentContentHash] += ph.DurationS
		}
		for _, s := range mr.SleepSessions {
			h := float64(*s.TotalSleepSeconds) / 3600
			if h < 5 || h > 10 {
				t.Errorf("%s: %.1f h of sleep", p, h)
			}
			if byParent[s.ContentHash] != int(s.BedtimeEnd.Sub(s.BedtimeStart).Seconds()) {
				t.Errorf("%s: phases do not cover the night", p)
			}
			local := s.BedtimeStart.In(paris)
			if local.Hour() > 1 && local.Hour() < 20 {
				t.Errorf("%s: bedtime at %s", p, local.Format("15:04"))
			}
		}
	}
	runs, routes := 0, len(recs[demo.Polar].RoutePoints)
	for _, w := range recs[demo.Polar].Workouts {
		if w.ActivityType == "running" {
			runs++
			pace := *w.DistanceM / float64(*w.DurationS)
			if pace < 2.5 || pace > 3.5 {
				t.Errorf("run at %.2f m/s", pace)
			}
		}
		if *w.AvgHeartRate < 70 || *w.MaxHeartRate > 200 || *w.MaxHeartRate < *w.AvgHeartRate {
			t.Errorf("workout heart rate avg %v max %v", *w.AvgHeartRate, *w.MaxHeartRate)
		}
	}
	if runs < 10 || routes == 0 || len(recs[demo.Whoop].Workouts) == 0 {
		t.Errorf("runs %d, route points %d, whoop workouts %d", runs, routes, len(recs[demo.Whoop].Workouts))
	}
	if n := len(recs[demo.Oura].DailySummaries); n != demo.DefaultDays {
		t.Errorf("oura: %d daily summaries", n)
	}
}

func TestDaysBounds(t *testing.T) {
	for _, n := range []int{-1, demo.MaxDays + 1} {
		if _, err := demo.Generate(demo.Options{Days: n, Today: today}); err == nil {
			t.Errorf("days %d accepted", n)
		}
	}
	recs := generate(t, demo.Options{Days: 1})
	last := today.AddDate(0, 0, -1).Format(time.DateOnly)
	for _, s := range recs[demo.Fitbit].QuantitySamples {
		if d := s.StartDate.In(paris).Format(time.DateOnly); d != last && s.QuantityType != model.QTOxygenSaturation {
			t.Fatalf("one day ending yesterday, got a sample on %s", d)
		}
	}
}
