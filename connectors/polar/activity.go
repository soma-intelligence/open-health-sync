// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapActivity turns one daily activity summary into step_count,
// active_energy_burned and distance_walking_running samples.
//
// AccessLink gives the day's start as a local wall clock time without an
// offset; its date is the user's local day. The samples span that date from
// midnight UTC to one second before the next, so the ETL's local day (UTC
// when no zone is given) is the date Polar named. A day's total grows while
// the day is open and Polar re-sends it, so the samples are Replaceable and
// hashed on the date, not the value: the newest total replaces the stored
// one.
func mapActivity(line []byte, userID string) (*model.MapResult, error) {
	var a Activity
	if err := decode(DataTypeActivity, line, &a); err != nil {
		return nil, err
	}
	if a.StartTime == "" {
		return nil, fmt.Errorf("polar: activity: missing start_time")
	}
	st, _, err := parseTime(a.StartTime)
	if err != nil {
		return nil, fmt.Errorf("polar: activity: %w", err)
	}
	date := st.Format("2006-01-02")
	start := time.Date(st.Year(), st.Month(), st.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1).Add(-time.Second)

	md := map[string]any{"date": date}
	setIf(md, "total_calories", a.Calories)
	setIf(md, "daily_activity", a.DailyActivity)
	setIf(md, "inactivity_alert_count", a.InactivityAlertCount)
	for k, v := range map[string]string{"active_duration": a.ActiveDuration, "inactive_duration": a.InactiveDuration} {
		if v == "" {
			continue
		}
		d, err := parseISODuration(v)
		if err != nil {
			return nil, fmt.Errorf("polar: activity: %w", err)
		}
		md[k+"_s"] = int(d.Round(time.Second) / time.Second)
	}

	var samples []model.QuantitySample
	for _, m := range []struct {
		quantityType, unit string
		v                  *float64
	}{
		{quantityType: model.QTStepCount, unit: "count", v: a.Steps},
		{quantityType: model.QTActiveEnergyBurned, unit: "kcal", v: a.ActiveCalories},
		{quantityType: model.QTDistanceWalkingRunning, unit: "m", v: a.DistanceFromSteps},
	} {
		if m.v == nil {
			continue
		}
		s := sample(userID, "", fmt.Sprintf("%s_%s", date, m.quantityType), m.quantityType, m.unit, *m.v, start, end, copyMap(md))
		s.Replaceable = true
		s.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id":       userID,
			"provider":      Slug,
			"data_type":     DataTypeActivity,
			"quantity_type": m.quantityType,
			"date":          date,
		})
		samples = append(samples, s)
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}
