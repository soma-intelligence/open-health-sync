// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Plausibility bounds of daily figures; a value outside is not stored.
const (
	maxDailySteps    = 1e6
	maxDailyDistance = 1e6 // m
	maxDailyKcal     = 5e4
	maxDailyFloors   = 1e4
)

// mapDaily turns one daily summary into a DailySummary, day total samples
// (step_count, distance_walking_running, active_energy_burned,
// basal_energy_burned, flights_climbed, resting_heart_rate) and heart_rate
// samples from timeOffsetHeartRateSamples.
//
// Garmin re-sends a day's summary with growing totals while the day is open,
// so the summary and the totals are Replaceable and hashed on the calendar
// date, not the value: the newest copy replaces the stored one.
func mapDaily(line []byte, userID string) (*model.MapResult, error) {
	var d Daily
	if err := decode(DataTypeDailies, line, &d); err != nil {
		return nil, err
	}
	start, err := unixTime("startTimeInSeconds", d.StartTimeInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: dailies: %w", err)
	}
	date := localDate(start, d.StartTimeOffsetInSeconds)
	if d.CalendarDate != "" {
		if _, err := parseDate(d.CalendarDate); err != nil {
			return nil, fmt.Errorf("garmin: dailies: %w", err)
		}
		date = d.CalendarDate
	}
	day, _ := parseDate(date)
	end := start.Add(time.Duration(seconds(d.DurationInSeconds, 86400)) * time.Second)

	md := dayMeta(d.SummaryID, d.StartTimeOffsetInSeconds)
	md["date"] = date
	setIf(md, "average_stress_level", d.AverageStressLevel)
	setIf(md, "max_stress_level", d.MaxStressLevel)
	setIf(md, "min_heart_rate", d.MinHeartRate)
	setIf(md, "average_heart_rate", d.AverageHeartRate)
	setIf(md, "max_heart_rate", d.MaxHeartRate)
	setIf(md, "active_time_s", d.ActiveTimeInSeconds)
	setIf(md, "moderate_intensity_s", d.ModerateIntensityDuration)
	setIf(md, "vigorous_intensity_s", d.VigorousIntensityDuration)
	setIf(md, "steps_goal", d.StepsGoal)

	res := &model.MapResult{}
	sum := model.DailySummary{
		UserID:      userID,
		Provider:    Slug,
		SummaryDate: day,
		Metadata:    copyMap(md),
		Replaceable: true,
	}
	figures := 0
	if inRange(d.Steps, maxDailySteps) {
		n := int(math.Round(*d.Steps))
		sum.StepCount = &n
		figures++
	}
	if inRange(d.DistanceInMeters, maxDailyDistance) {
		sum.DistanceM = d.DistanceInMeters
		figures++
	}
	if inRange(d.ActiveKilocalories, maxDailyKcal) {
		sum.ActiveKcal = d.ActiveKilocalories
		figures++
		if inRange(d.BMRKilocalories, maxDailyKcal) {
			total := *d.ActiveKilocalories + *d.BMRKilocalories
			sum.TotalKcal = &total
		}
	}
	if inRange(d.FloorsClimbed, maxDailyFloors) {
		n := int(math.Round(*d.FloorsClimbed))
		sum.FloorsClimbed = &n
		figures++
	}
	if inBounds(d.RestingHeartRate, 20, 250) {
		sum.RestingHR = d.RestingHeartRate
		figures++
	}
	if figures > 0 {
		sum.ContentHash = hash(userID, DataTypeDailies, "date", date)
		res.DailySummaries = append(res.DailySummaries, sum)
	}

	for _, m := range []struct {
		qt, unit string
		v        *float64
		ok       bool
	}{
		{model.QTStepCount, "count", d.Steps, inRange(d.Steps, maxDailySteps)},
		{model.QTDistanceWalkingRunning, "m", d.DistanceInMeters, inRange(d.DistanceInMeters, maxDailyDistance)},
		{model.QTActiveEnergyBurned, "kcal", d.ActiveKilocalories, inRange(d.ActiveKilocalories, maxDailyKcal)},
		{model.QTBasalEnergyBurned, "kcal", d.BMRKilocalories, inRange(d.BMRKilocalories, maxDailyKcal)},
		{model.QTFlightsClimbed, "count", d.FloorsClimbed, inRange(d.FloorsClimbed, maxDailyFloors)},
		{model.QTRestingHeartRate, "bpm", d.RestingHeartRate, inBounds(d.RestingHeartRate, 20, 250)},
	} {
		if !m.ok {
			continue
		}
		smd := copyMap(md)
		smd[model.MetadataSummaryDate] = date
		s := sample(userID, "daily_"+date+"_"+m.qt, m.qt, m.unit, *m.v, start, end, smd)
		s.Replaceable = true
		s.ContentHash = hash(userID, DataTypeDailies, "quantity_type", m.qt, "date", date)
		res.QuantitySamples = append(res.QuantitySamples, s)
	}

	hmd := withOffset(map[string]any{"context": "daily"}, d.StartTimeOffsetInSeconds)
	res.QuantitySamples = append(res.QuantitySamples,
		seriesSamples(userID, DataTypeDailies, "daily_"+date+"_hr", model.QTHeartRate, "bpm", start, d.TimeOffsetHeartRateSamples, 20, 250, hmd)...)

	if res.IsEmpty() {
		return nil, nil
	}
	return res, nil
}

// inRange reports whether v is set, finite and in [0, max].
func inRange(v *float64, max float64) bool { return valid(v) && *v <= max }

// inBounds reports whether v is set, finite and in [lo, hi].
func inBounds(v *float64, lo, hi float64) bool { return valid(v) && *v >= lo && *v <= hi }

// mapEpoch turns one 15 minute epoch into the intraday variants of steps,
// distance and active energy. Garmin sends one epoch per activity type of a
// slice (walking and sedentary minutes of the same quarter hour are two
// epochs), so the activity type is part of the identity, and the slice's
// figures are their sum. An epoch re-sent with new figures replaces the
// stored ones.
func mapEpoch(line []byte, userID string) (*model.MapResult, error) {
	var e Epoch
	if err := decode(DataTypeEpochs, line, &e); err != nil {
		return nil, err
	}
	start, err := unixTime("startTimeInSeconds", e.StartTimeInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: epochs: %w", err)
	}
	end := start.Add(time.Duration(seconds(e.DurationInSeconds, 900)) * time.Second)
	md := dayMeta(e.SummaryID, e.StartTimeOffsetInSeconds)
	if e.ActivityType != "" {
		md["activity_type"] = e.ActivityType
	}
	if e.Intensity != "" {
		md["intensity"] = e.Intensity
	}
	setIf(md, "met", e.MET)
	setIf(md, "active_time_s", e.ActiveTimeInSeconds)

	var out []model.QuantitySample
	for _, m := range []struct {
		qt, unit string
		v        *float64
		max      float64
	}{
		{model.QTStepCountIntraday, "count", e.Steps, 1e5},
		{model.QTDistanceWalkRunIntday, "m", e.DistanceInMeters, 1e5},
		{model.QTActiveEnergyIntraday, "kcal", e.ActiveKilocalories, 1e4},
	} {
		if !inRange(m.v, m.max) {
			continue
		}
		ts := strconv.FormatInt(start.Unix(), 10)
		s := sample(userID, "epoch_"+ts+"_"+e.ActivityType+"_"+m.qt, m.qt, m.unit, *m.v, start, end, copyMap(md))
		s.Replaceable = true
		s.ContentHash = hash(userID, DataTypeEpochs, "quantity_type", m.qt, "start_date", start.Format(time.RFC3339), "activity_type", e.ActivityType)
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: out}, nil
}
