// SPDX-License-Identifier: AGPL-3.0-only

package dexcom

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record: the vendor.
const sourceName = "Dexcom"

// MetadataSpecimen is the metadata key naming what a glucose value was
// measured in. A CGM measures interstitial fluid, not blood; the FHIR output
// codes such a value with the interstitial fluid LOINC.
const MetadataSpecimen = "specimen"

// SpecimenInterstitial is the MetadataSpecimen value of a CGM reading.
const SpecimenInterstitial = "interstitial_fluid"

// egv is one record of GET /v3/users/self/egvs. Nullable fields are
// pointers.
type egv struct {
	RecordID              string   `json:"recordId"`
	SystemTime            string   `json:"systemTime"`
	DisplayTime           string   `json:"displayTime"`
	TransmitterID         *string  `json:"transmitterId"`
	Value                 *float64 `json:"value"`
	Status                *string  `json:"status"`
	Trend                 *string  `json:"trend"`
	TrendRate             *float64 `json:"trendRate"`
	Unit                  string   `json:"unit"`
	RateUnit              string   `json:"rateUnit"`
	DisplayDevice         string   `json:"displayDevice"`
	TransmitterGeneration string   `json:"transmitterGeneration"`
	DisplayApp            *string  `json:"displayApp"`
}

// mapEGV turns one EGV line into one blood_glucose sample. A record without
// a value (the sensor gave none) gives nothing.
//
// Dexcom clamps the value at the ends of its measuring range: 39 means below
// 40 mg/dL and 401 above 400, with status "low" or "high". The clamped value
// is stored as is and the status kept in metadata ("out_of_range"), so a
// reader can tell a real 40 from "under 40".
func mapEGV(fields map[string]any, userID string) (*model.MapResult, error) {
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("dexcom: egvs: re-encode line: %w", err)
	}
	var r egv
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("dexcom: egvs: %w", err)
	}
	if r.Value == nil {
		return nil, nil
	}
	if r.RecordID == "" {
		return nil, fmt.Errorf("dexcom: egvs: record without recordId")
	}
	at, _, err := parseTime(r.SystemTime)
	if err != nil {
		return nil, fmt.Errorf("dexcom: egvs: systemTime: %w", err)
	}
	if at.IsZero() {
		return nil, fmt.Errorf("dexcom: egvs: zero systemTime %q", r.SystemTime)
	}
	at = at.UTC()

	unit := r.Unit
	if unit == "" || unit == "unknown" {
		// The API documents EGVs in mg/dL.
		unit = "mg/dL"
	}
	md := map[string]any{
		MetadataSpecimen:         SpecimenInterstitial,
		"display_device":         r.DisplayDevice,
		"transmitter_generation": r.TransmitterGeneration,
	}
	if r.Status != nil && *r.Status != "" {
		md["out_of_range"] = *r.Status
	}
	if r.Trend != nil {
		md["trend"] = *r.Trend
	}
	if r.TrendRate != nil {
		md["trend_rate"] = *r.TrendRate
		md["trend_rate_unit"] = r.RateUnit
	}
	if r.DisplayApp != nil {
		md["display_app"] = *r.DisplayApp
	}
	if off, ok := displayOffset(r.SystemTime, r.DisplayTime); ok {
		md["utc_offset_minutes"] = off
		if z := offsetZone(off); z != "" {
			md["timezone"] = z
		}
	}
	device := ""
	if r.TransmitterID != nil {
		device = *r.TransmitterID
	}
	s := model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		DeviceID:        device,
		DeviceModel:     r.TransmitterGeneration,
		ExternalID:      r.RecordID,
		QuantityType:    "blood_glucose",
		Value:           *r.Value,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       at,
		EndDate:         at,
		Metadata:        md,
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"quantity_type": s.QuantityType,
		"start_date":    at.Format(time.RFC3339),
		"value":         model.CanonicalFloat(s.Value),
		"unit":          unit,
	})
	return &model.MapResult{QuantitySamples: []model.QuantitySample{s}}, nil
}

// displayOffset is the UTC offset of the device that showed the reading, in
// minutes. When displayTime carries an offset it is that offset; otherwise it
// is displayTime (local wall clock) minus systemTime (UTC), rounded to 15
// minutes since device clocks drift. Offsets outside -12 h..+14 h are
// refused.
func displayOffset(system, display string) (int, bool) {
	d, dOff, err := parseTime(display)
	if err != nil {
		return 0, false
	}
	if dOff {
		_, sec := d.Zone()
		minutes := sec / 60
		if minutes < -12*60 || minutes > 14*60 {
			return 0, false
		}
		return minutes, true
	}
	s, _, err := parseTime(system)
	if err != nil {
		return 0, false
	}
	wall := time.Date(d.Year(), d.Month(), d.Day(), d.Hour(), d.Minute(), d.Second(), 0, time.UTC)
	diff := wall.Sub(s.UTC())
	minutes := int((diff + 450*time.Second).Truncate(15*time.Minute) / time.Minute)
	if diff < 0 {
		minutes = -int((-diff + 450*time.Second).Truncate(15*time.Minute) / time.Minute)
	}
	if minutes < -12*60 || minutes > 14*60 {
		return 0, false
	}
	return minutes, true
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
	if h < -12 || h > 14 {
		return ""
	}
	if h > 0 {
		return "Etc/GMT-" + strconv.Itoa(h)
	}
	return "Etc/GMT+" + strconv.Itoa(-h)
}
