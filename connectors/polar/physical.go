// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"errors"
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// physicalInfoTime is when the physical information was last set: modified,
// else created, else the pull time Fetch added.
func physicalInfoTime(p PhysicalInfo) (time.Time, error) {
	for _, s := range []string{p.Modified, p.Created, p.FetchedAt} {
		if s == "" {
			continue
		}
		t, _, err := parseTime(s)
		if err != nil {
			return time.Time{}, err
		}
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("polar: physical info: no modified, created or fetched_at time")
}

// mapPhysicalInfo turns the user's physical information into body_mass,
// height, resting_heart_rate and vo2_max samples, stamped at the time it was
// last set. The maximum heart rate is a setting rather than a measurement: it
// stays in the metadata.
//
// Fetch sends the same information on every pull. A sample is hashed on the
// quantity and the day of its time, not the value, and is Replaceable: a
// line without a modified or created time is stamped at midnight UTC of the
// pull day and gives one sample a day, the newest value of the day winning.
func mapPhysicalInfo(line []byte, userID string) (*model.MapResult, error) {
	var p PhysicalInfo
	if err := decode(DataTypePhysicalInfo, line, &p); err != nil {
		return nil, err
	}
	at, err := physicalInfoTime(p)
	if err != nil {
		return nil, fmt.Errorf("polar: physical info: %w", err)
	}
	if p.Modified == "" && p.Created == "" {
		// Only the pull time: stamp the day, so every pull of the day lands
		// on the same row (the unique key includes the time).
		at = utcDay(at)
	}
	day := at.Format("2006-01-02")
	md := map[string]any{"source": "physical_info"}
	setIf(md, "maximum_heart_rate", p.MaximumHeartRate)

	var samples []model.QuantitySample
	for _, m := range []struct {
		quantityType, unit string
		v                  *float64
	}{
		{quantityType: "body_mass", unit: "kg", v: p.Weight},
		{quantityType: "height", unit: "cm", v: p.Height},
		{quantityType: model.QTRestingHeartRate, unit: "bpm", v: p.RestingHeartRate},
		{quantityType: "vo2_max", unit: "mL/kg/min", v: p.VO2Max},
	} {
		if m.v == nil || *m.v <= 0 {
			continue
		}
		s := sample(userID, "", "physical_info_"+day, m.quantityType, m.unit, *m.v, at, at, copyMap(md))
		s.Replaceable = true
		s.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id":       userID,
			"provider":      Slug,
			"data_type":     DataTypePhysicalInfo,
			"quantity_type": m.quantityType,
			"date":          day,
		})
		samples = append(samples, s)
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}
