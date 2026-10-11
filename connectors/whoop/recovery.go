// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"fmt"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// recoverySuffix makes the record id of a recovery from its sleep id. WHOOP
// names a recovery by the sleep it was scored from (recovery.updated and
// recovery.deleted webhooks carry the sleep id), and deletes it when that
// sleep is deleted. The recovery samples are stored as
// "<sleep id>_recovery_<quantity type>", so:
//
//   - sleep.deleted (a deletion of "<sleep id>") removes them with the sleep,
//     as samples derived from it, and its tombstone keeps them out;
//   - recovery.deleted (a deletion of "<sleep id>_recovery") tombstones them
//     without touching the sleep.
const recoverySuffix = "_recovery"

// recoveryRecordID is the record id of a recovery: "<sleep id>_recovery", or,
// for a recovery without a sleep id (the API documents it as always present),
// "cycle-<cycle id>_recovery". "" when it has neither.
func recoveryRecordID(sleepID, cycleID string) string {
	switch {
	case sleepID != "":
		return sleepID + recoverySuffix
	case cycleID != "":
		return "cycle-" + cycleID + recoverySuffix
	}
	return ""
}

// mapRecovery turns one scored recovery into resting heart rate, HRV, SpO2
// and skin temperature samples, and the recovery figures of the day's
// summary.
//
// WHOOP's HRV is the RMSSD of the night (hrv_rmssd_milli), a different
// statistic from the SDNN that hrv_sdnn holds, so it lands under its own key,
// hrv_rmssd. The summary's hrv_avg_ms is left empty for the same reason.
//
// A recovery has no time of its own besides created_at, which WHOOP sets when
// it scores the night, shortly after waking: the samples are stamped with it.
// It does not change when the recovery is re-scored, so the re-scored copy
// replaces the stored one. The day of the summary is its cycle's (see
// cycleDay) when Fetch added the cycle, else the local day of created_at.
func mapRecovery(line []byte, userID string) (*model.MapResult, error) {
	var r Recovery
	if err := decode(DataTypeRecovery, line, &r); err != nil {
		return nil, err
	}
	id := recoveryRecordID(string(r.SleepID), string(r.CycleID))
	if id == "" {
		return nil, fmt.Errorf("whoop: recovery: missing sleep_id and cycle_id")
	}
	at, err := requireTime(DataTypeRecovery, "created_at", r.CreatedAt)
	if err != nil {
		return nil, err
	}
	if r.ScoreState != scoreScored || r.Score == nil {
		return nil, nil
	}
	sc := r.Score

	offset := ""
	day := dateOnly(at)
	if r.Cycle != nil {
		offset = r.Cycle.TimezoneOffset
		day = dateOnly(at.In(offsetZone(offset)))
		if start, err := parseTime(r.Cycle.Start); err == nil {
			day = cycleDay(start, offset)
		}
	}

	md := func() map[string]any {
		m := map[string]any{}
		if r.CycleID != "" {
			m["whoop_cycle_id"] = string(r.CycleID)
		}
		if r.SleepID != "" {
			m["whoop_sleep_id"] = string(r.SleepID)
		}
		setIf(m, "user_calibrating", sc.UserCalibrating)
		return withTimezone(m, offset, at)
	}

	res := &model.MapResult{}
	add := func(qt, unit string, v *float64) {
		if v == nil {
			return
		}
		res.QuantitySamples = append(res.QuantitySamples, quantity(userID, id, qt, *v, unit, at, at, md()))
	}
	add(model.QTRestingHeartRate, "count/min", sc.RestingHeartRate)
	add(model.QTHRVRMSSD, "ms", sc.HRVRMSSDMilli)
	add(model.QTOxygenSaturation, "percent", sc.SpO2Percentage)
	add(model.QTSkinTemperature, "celsius", sc.SkinTempCelsius)

	if sc.RecoveryScore != nil || sc.RestingHeartRate != nil {
		d := dailySummary(userID, day, offset, at)
		d.RecoveryScore = sc.RecoveryScore
		d.RestingHR = sc.RestingHeartRate
		res.DailySummaries = append(res.DailySummaries, d)
	}
	if res.IsEmpty() {
		return nil, nil
	}
	return res, nil
}
