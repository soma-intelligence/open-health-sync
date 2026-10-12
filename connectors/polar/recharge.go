// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapNightlyRecharge turns one Nightly Recharge result into quantity
// samples: hrv_rmssd (ms) and respiratory_rate (breaths per minute, stored
// under "bpm" like every respiratory rate). AccessLink documents
// heart_rate_variability_avg as RMSSD, a different statistic from SDNN: it is
// never written to hrv_sdnn.
//
// AccessLink dates a result but gives no time: the samples span the date
// (midnight UTC to one second before the next). The averages Polar computes
// over the first hours of sleep (heart_rate_avg, beat_to_beat_avg), the ANS
// charge (a -10 to +10 index relative to the user's last 28 days) and the
// status codes are kept in the metadata. Polar may recompute a night, so the
// samples are Replaceable and hashed on the date, not the value.
func mapNightlyRecharge(line []byte, userID string) (*model.MapResult, error) {
	var n NightlyRecharge
	if err := decode(DataTypeNightlyRecharge, line, &n); err != nil {
		return nil, err
	}
	date, err := parseDate(n.Date)
	if err != nil {
		return nil, fmt.Errorf("polar: nightly recharge: %w", err)
	}
	start := date
	end := date.AddDate(0, 0, 1).Add(-time.Second)

	md := map[string]any{"date": n.Date, "source": "nightly_recharge"}
	setIf(md, "heart_rate_avg", n.HeartRateAvg)
	setIf(md, "beat_to_beat_avg_ms", n.BeatToBeatAvg)
	setIf(md, "nightly_recharge_status", n.NightlyRechargeStatus)
	setIf(md, "ans_charge", n.ANSCharge)
	setIf(md, "ans_charge_status", n.ANSChargeStatus)

	var samples []model.QuantitySample
	for _, m := range []struct {
		quantityType, unit string
		v                  *float64
	}{
		{quantityType: model.QTHRVRMSSD, unit: "ms", v: n.HeartRateVariabilityA},
		{quantityType: model.QTRespiratoryRate, unit: "bpm", v: n.BreathingRateAvg},
	} {
		if m.v == nil {
			continue
		}
		s := sample(userID, "", "nightly_recharge_"+n.Date, m.quantityType, m.unit, *m.v, start, end, copyMap(md))
		s.Replaceable = true
		s.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id":       userID,
			"provider":      Slug,
			"data_type":     DataTypeNightlyRecharge,
			"quantity_type": m.quantityType,
			"date":          n.Date,
		})
		samples = append(samples, s)
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}
