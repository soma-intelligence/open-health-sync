// SPDX-License-Identifier: AGPL-3.0-only

package omron

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record: the vendor.
const sourceName = "OMRON"

// num is a measurement value the API sends as a JSON number or as a string
// ("71.6"). Missing, empty and unreadable values are not set.
type num struct {
	v   float64
	set bool
}

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if u, err := strconv.Unquote(s); err == nil {
		s = strings.TrimSpace(u)
	}
	if s == "" || s == "null" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	n.v, n.set = f, true
	return nil
}

// positive reports a value that is a real reading: OMRON sends -1 (and 0 for
// a blood pressure reading flagged as possible AFib) when it has none.
func (n num) positive() bool { return n.set && n.v > 0 }

// measurement holds the fields every measurement has.
type measurement struct {
	ID                json.RawMessage `json:"id"`
	DateTime          string          `json:"dateTime"`
	DateTimeUTCOffset json.RawMessage `json:"dateTimeUtcOffset"`
	DeviceType        string          `json:"deviceType"`
}

// head reads the id, the instant and the metadata shared by every sample of a
// measurement.
func (m measurement) head(dataType string) (id string, at time.Time, md map[string]any, err error) {
	id = strings.Trim(strings.TrimSpace(string(m.ID)), `"`)
	if id == "" || id == "null" {
		return "", time.Time{}, nil, fmt.Errorf("omron: %s: measurement without id", dataType)
	}
	at, err = parseUTC(m.DateTime)
	if err != nil {
		return "", time.Time{}, nil, fmt.Errorf("omron: %s: %w", dataType, err)
	}
	md = map[string]any{}
	if m.DeviceType != "" {
		md["device_type"] = m.DeviceType
	}
	if off, ok := utcOffset(m.DateTimeUTCOffset); ok {
		md["utc_offset_minutes"] = off
		if z := offsetZone(off); z != "" {
			md["timezone"] = z
		}
	}
	return id, at, md, nil
}

type bloodPressure struct {
	measurement
	Systolic     num   `json:"systolic"`
	Diastolic    num   `json:"diastolic"`
	Pulse        num   `json:"pulse"`
	PossibleAfib *bool `json:"possibleAfib"`
	IrregularHB  *bool `json:"irregularHB"`
	Movement     *bool `json:"movementDetect"`
	CuffWrap     *bool `json:"cuffWrapDetect"`
}

// mapBloodPressure turns one blood pressure measurement into a systolic and
// a diastolic sample (which the read API and FHIR pair into one panel, since
// they share time, source and device) and a heart_rate sample for the pulse.
// When OMRON flags a possible AFib it zeroes the three values: the reading
// then gives an irregular_heart_rhythm_event category sample and no
// pressure.
func mapBloodPressure(fields map[string]any, userID string) (*model.MapResult, error) {
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("omron: bloodpressure: re-encode line: %w", err)
	}
	var r bloodPressure
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("omron: bloodpressure: %w", err)
	}
	id, at, md, err := r.head(DataTypeBloodPressure)
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]*bool{"possible_afib": r.PossibleAfib, "irregular_heartbeat": r.IrregularHB, "movement_detected": r.Movement, "cuff_wrap_ok": r.CuffWrap} {
		if v != nil {
			md[k] = *v
		}
	}
	res := &model.MapResult{}
	if r.Systolic.positive() && r.Diastolic.positive() {
		res.QuantitySamples = append(res.QuantitySamples,
			sample(userID, r.DeviceType, id+"_systolic", "blood_pressure_systolic", "mmHg", r.Systolic.v, at, md),
			sample(userID, r.DeviceType, id+"_diastolic", "blood_pressure_diastolic", "mmHg", r.Diastolic.v, at, md))
	}
	if r.Pulse.positive() {
		res.QuantitySamples = append(res.QuantitySamples,
			sample(userID, r.DeviceType, id+"_pulse", model.QTHeartRate, "bpm", r.Pulse.v, at, md))
	}
	if r.PossibleAfib != nil && *r.PossibleAfib {
		cs := model.CategorySample{
			UserID:       userID,
			Provider:     Slug,
			ExternalID:   id + "_afib",
			CategoryType: "irregular_heart_rhythm_event",
			ValueInt:     0,
			ValueLabel:   "possible_afib",
			StartDate:    at,
			EndDate:      at,
			SourceName:   sourceName,
			Metadata:     md,
		}
		cs.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id": userID, "provider": Slug, "category_type": cs.CategoryType,
			"start_date": at.Format(time.RFC3339), "external_id": cs.ExternalID,
		})
		res.CategorySamples = append(res.CategorySamples, cs)
	}
	if len(res.QuantitySamples) == 0 && len(res.CategorySamples) == 0 {
		return nil, nil
	}
	return res, nil
}

type weight struct {
	measurement
	Weight            num    `json:"weight"`
	WeightPref        string `json:"weightPref"`
	BMI               num    `json:"bmiValue"`
	BodyFatPercentage num    `json:"bodyFatPercentage"`
	VisceralFatLevel  num    `json:"visceralFatLevel"`
}

// mapWeight turns one weight measurement into body_mass and, when the scale
// measured them, body_mass_index, body_fat_percentage and
// visceral_fat_rating samples. The weight is in the unit of weightPref (kg
// or lbs); the pipeline converts pounds to kg.
func mapWeight(fields map[string]any, userID string) (*model.MapResult, error) {
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("omron: weight: re-encode line: %w", err)
	}
	var r weight
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("omron: weight: %w", err)
	}
	id, at, md, err := r.head(DataTypeWeight)
	if err != nil {
		return nil, err
	}
	unit := strings.ToLower(strings.TrimSpace(r.WeightPref))
	switch unit {
	case "", "kg", "kgs":
		unit = "kg"
	case "lb", "lbs":
		unit = "lb"
	default:
		return nil, fmt.Errorf("omron: weight: unknown weightPref %q", r.WeightPref)
	}
	res := &model.MapResult{}
	add := func(suffix, qt, u string, n num) {
		if n.positive() {
			res.QuantitySamples = append(res.QuantitySamples, sample(userID, r.DeviceType, id+"_"+suffix, qt, u, n.v, at, md))
		}
	}
	add("weight", "body_mass", unit, r.Weight)
	add("bmi", "body_mass_index", "kg/m2", r.BMI)
	add("body_fat", "body_fat_percentage", "percent", r.BodyFatPercentage)
	add("visceral_fat", "visceral_fat_rating", "index", r.VisceralFatLevel)
	if len(res.QuantitySamples) == 0 {
		return nil, nil
	}
	return res, nil
}

// sample builds one point-in-time quantity sample of this connector.
func sample(userID, device, externalID, qt, unit string, v float64, at time.Time, md map[string]any) model.QuantitySample {
	s := model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		DeviceID:        device,
		DeviceModel:     device,
		ExternalID:      externalID,
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       at,
		EndDate:         at,
		Metadata:        maps.Clone(md),
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"quantity_type": qt,
		"start_date":    at.Format(time.RFC3339),
		"value":         model.CanonicalFloat(v),
	})
	return s
}

// utcOffset reads dateTimeUtcOffset in minutes: "+02:00" or seconds
// ("-21600", as a number or a string). Offsets outside -12 h..+14 h are
// refused.
func utcOffset(raw json.RawMessage) (int, bool) {
	s := strings.TrimSpace(string(raw))
	if u, err := strconv.Unquote(s); err == nil {
		s = strings.TrimSpace(u)
	}
	if s == "" || s == "null" {
		return 0, false
	}
	var minutes int
	if strings.Contains(s, ":") {
		sign := 1
		switch s[0] {
		case '-':
			sign = -1
			s = s[1:]
		case '+':
			s = s[1:]
		}
		h, m, ok := strings.Cut(s, ":")
		hh, err1 := strconv.Atoi(h)
		mm, err2 := strconv.Atoi(m)
		if !ok || err1 != nil || err2 != nil || mm < 0 || mm >= 60 {
			return 0, false
		}
		minutes = sign * (hh*60 + mm)
	} else {
		sec, err := strconv.Atoi(s)
		if err != nil || sec%60 != 0 {
			return 0, false
		}
		minutes = sec / 60
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
	if h > 0 {
		return "Etc/GMT-" + strconv.Itoa(h)
	}
	return "Etc/GMT+" + strconv.Itoa(-h)
}
