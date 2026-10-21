// SPDX-License-Identifier: AGPL-3.0-only

package bluetooth

import (
	"maps"
	"math"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapMeasurement turns a decoded value into canonical records.
func mapMeasurement(userID string, p parsed, m *measurement) model.MapResult {
	res := model.MapResult{HighFrequency: m.stream}
	if u := p.DeviceUser; u != nil && m.userIndex >= 0 && m.userIndex != unknownUser && m.userIndex != *u {
		return model.MapResult{} // another person on a shared device
	}
	at, clock := when(p.at, m)
	var dev device
	if p.Device != nil {
		dev = *p.Device
	}
	src := sourceOf(dev)

	_, offset := p.at.Zone()
	minutes := offset / 60
	base := map[string]any{
		"gatt_characteristic": p.char,
		"session":             p.Session,
		"clock":               clock,
		"utc_offset_minutes":  minutes,
	}
	if z := offsetZone(minutes); z != "" {
		base["timezone"] = z
	}
	if dev.Firmware != "" {
		base["firmware"] = dev.Firmware
	}
	if m.userIndex >= 0 && m.userIndex != unknownUser {
		base["device_user"] = m.userIndex
	}
	maps.Copy(base, m.md)

	for _, rd := range m.readings {
		// One map per sample: the ETL validator writes its quality flag into it.
		md := maps.Clone(base)
		maps.Copy(md, rd.md)
		measured := false
		res.QuantitySamples = append(res.QuantitySamples, model.QuantitySample{
			UserID:         userID,
			Provider:       Slug,
			SourceSlug:     src.slug,
			SourceName:     src.name,
			DeviceID:       src.deviceID,
			DeviceModel:    src.deviceModel,
			WasUserEntered: &measured,
			QuantityType:   rd.qt,
			Value:          rd.value,
			Unit:           rd.unit,
			StartDate:      at.UTC(),
			EndDate:        at.UTC(),
			Metadata:       md,
			// Hashed on the reading, never on the arrival: a cuff re-sending its
			// memory on the next connection gives the same rows.
			ContentHash: model.ComputeContentHash(map[string]string{
				"user_id":       userID,
				"provider":      Slug,
				"source_slug":   src.slug,
				"quantity_type": rd.qt,
				"start_date":    at.UTC().Format(time.RFC3339Nano),
				"value":         model.CanonicalFloat(rd.value),
			}),
		})
	}

	// The RR intervals of one notification end at its arrival: the last beat
	// is stamped then, each earlier one at the following beat minus the
	// following interval.
	if len(m.rr) > 0 {
		res.HeartbeatSamples = make([]model.HeartbeatSample, len(m.rr))
		beat := p.at
		for i := len(m.rr) - 1; i >= 0; i-- {
			res.HeartbeatSamples[i] = model.HeartbeatSample{
				SeriesID:  p.Session,
				UserID:    userID,
				Timestamp: beat.UTC(),
				RRMS:      m.rr[i],
			}
			beat = beat.Add(-time.Duration(math.Round(m.rr[i]*1e3)) * time.Microsecond)
		}
	}
	return res
}

// when is the time of a measurement and where it comes from: the device's
// clock when the value carries a time stamp that can be a reading time
// (a real date, not before 2000, not more than deviceAhead after the
// arrival, and not flagged as unset), read in the arrival's UTC offset;
// otherwise the arrival.
func when(arrived time.Time, m *measurement) (time.Time, string) {
	if m.clock != nil && !m.clockNotSet {
		if t, ok := m.clock.in(arrived.Location(), m.offset); ok &&
			!t.Before(earliestDeviceTime) && !t.After(arrived.Add(deviceAhead)) {
			return t, "device"
		}
	}
	return arrived, "received"
}

// offsetZone is the Etc/GMT zone of a whole hour offset ("Etc/GMT-2" is
// UTC+2), "Etc/UTC" for 0, and "" otherwise.
func offsetZone(minutes int) string {
	if minutes == 0 {
		return "Etc/UTC"
	}
	if minutes%60 != 0 {
		return ""
	}
	h := minutes / 60
	if h > 0 {
		return "Etc/GMT-" + strconv.Itoa(h)
	}
	return "Etc/GMT+" + strconv.Itoa(-h)
}
