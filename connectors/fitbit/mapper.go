// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record.
const sourceName = "Fitbit"

// point is one decoded raw line: the envelope and the union member of its
// data type, still encoded.
type point struct {
	dataPoint
	dataType dataType
	body     json.RawMessage
}

// handler maps one data point. A nil result with a nil error means the point
// carries nothing to store.
type handler func(p point, userID string) (*model.MapResult, error)

var handlers = map[string]handler{
	DataTypeSteps:            mapSteps,
	DataTypeDistance:         mapDistance,
	DataTypeFloors:           mapFloors,
	DataTypeActiveEnergy:     mapActiveEnergy,
	DataTypeExercise:         mapExercise,
	DataTypeVO2Max:           mapVO2Max,
	DataTypeDailyVO2Max:      mapDailyVO2Max,
	DataTypeHeartRate:        mapHeartRate,
	DataTypeDailyRestingHR:   mapDailyRestingHR,
	DataTypeDailyHRV:         mapDailyHRV,
	DataTypeHRV:              mapHRV,
	DataTypeOxygenSaturation: mapOxygenSaturation,
	DataTypeDailyOxygen:      mapDailyOxygen,
	DataTypeDailyRespiratory: mapDailyRespiratory,
	DataTypeWeight:           mapWeight,
	DataTypeBodyFat:          mapBodyFat,
	DataTypeHeight:           mapHeight,
	DataTypeBloodGlucose:     mapBloodGlucose,
	DataTypeCoreBodyTemp:     mapCoreBodyTemperature,
	DataTypeSleepTemperature: mapSleepTemperature,
	DataTypeSleep:            mapSleep,
}

// unknownTypes counts data types seen without a handler, so each is logged
// once.
var (
	unknownMu    sync.Mutex
	unknownTypes = map[string]bool{}
)

// mapRecord re-encodes fields (one parsed raw line) and maps it by data type.
func mapRecord(dt string, fields map[string]any, userID string) (*model.MapResult, error) {
	h, ok := handlers[dt]
	if !ok {
		unknownMu.Lock()
		if !unknownTypes[dt] && len(unknownTypes) < 64 {
			unknownTypes[dt] = true
			slog.Warn("fitbit: unknown data type skipped", "provider", Slug, "data_type", clip(dt, 64))
		}
		unknownMu.Unlock()
		return nil, nil
	}
	spec, _ := lookupDataType(dt)
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("re-encode line: %w", err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, err
	}
	p := point{dataType: spec, body: env[spec.Field]}
	if err := json.Unmarshal(line, &p.dataPoint); err != nil {
		return nil, err
	}
	if len(p.body) == 0 || string(p.body) == "null" {
		return nil, fmt.Errorf("data point has no %q member", spec.Field)
	}
	return h(p, userID)
}

// decode unmarshals the union member into v.
func (p point) decode(v any) error {
	if err := json.Unmarshal(p.body, v); err != nil {
		return fmt.Errorf("%s: %w", p.dataType.Field, err)
	}
	return nil
}

// metadata is the metadata every record of p carries: the Google data type
// and where the point came from.
func (p point) metadata() map[string]any {
	md := map[string]any{"google_data_type": p.dataType.ID}
	if s := p.DataSource; s != nil {
		if s.RecordingMethod != "" {
			md["recording_method"] = s.RecordingMethod
		}
		if s.Platform != "" {
			md["platform"] = s.Platform
		}
		if s.Device != nil {
			if s.Device.FormFactor != "" {
				md["device_form_factor"] = s.Device.FormFactor
			}
			if s.Device.DisplayName != "" {
				md["device_name"] = s.Device.DisplayName
			}
			if s.Device.Manufacturer != "" {
				md["device_manufacturer"] = s.Device.Manufacturer
			}
		}
		if s.Application != nil && s.Application.PackageName != "" {
			md["application"] = s.Application.PackageName
		}
	}
	return md
}

// Platforms whose points Google relays from another health store: the user
// linked Apple Health or Health Connect in the Google Health app.
const (
	platformHealthKit     = "HEALTH_KIT"
	platformHealthConnect = "HEALTH_CONNECT"
)

// fromHealthKit reports whether p is Apple Health data relayed by Google.
func (p point) fromHealthKit() bool {
	return p.DataSource != nil && strings.EqualFold(p.DataSource.Platform, platformHealthKit)
}

// source is the source slug and name of p's samples. A relayed point is
// attributed to the store it came from, so it is a stream of its own: it
// never adds up with the tracker's minutes, and fusion ranks it against the
// same data read directly from that store.
func (p point) source() (slug, name string) {
	if s := p.DataSource; s != nil {
		switch strings.ToUpper(s.Platform) {
		case platformHealthKit:
			return "apple_health", "Apple Health"
		case platformHealthConnect:
			return "health_connect", "Health Connect"
		}
	}
	return Slug, sourceName
}

// sourceKey identifies the source of a point in content hashes: the API
// returns the points of every source (phone, tracker, scale, other apps), and
// two sources may report the same minute.
func (p point) sourceKey() string {
	s := p.DataSource
	if s == nil {
		return ""
	}
	parts := []string{s.Platform, s.RecordingMethod}
	if s.Device != nil {
		parts = append(parts, s.Device.Manufacturer, s.Device.DisplayName, s.Device.FormFactor)
	}
	if s.Application != nil {
		parts = append(parts, s.Application.PackageName)
	}
	return strings.Join(parts, "|")
}

// recordID is the id of an identifiable point (exercise, sleep): the last
// segment of its resource name, users/{user}/dataTypes/{type}/dataPoints/{id}.
func (p point) recordID() string {
	n := strings.TrimSpace(p.Name)
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		n = n[i+1:]
	}
	return n
}

// spanOf reads an interval: start, end and the start offset in metadata.
func spanOf(iv interval, md map[string]any) (time.Time, time.Time, error) {
	start, err := parseTime(iv.StartTime)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("interval.startTime: %w", err)
	}
	end, err := parseTime(iv.EndTime)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("interval.endTime: %w", err)
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, errors.New("interval ends before it starts")
	}
	zoneMetadata(md, start, iv.StartUTCOffset)
	return start, end, nil
}

// instantOf reads a sample time and puts its offset in metadata.
func instantOf(st sampleTime, md map[string]any) (time.Time, error) {
	at, err := parseTime(st.PhysicalTime)
	if err != nil {
		return time.Time{}, fmt.Errorf("sampleTime.physicalTime: %w", err)
	}
	zoneMetadata(md, at, st.UTCOffset)
	return at, nil
}

// quantity builds a sample. Fields of the content hash: user, provider, type,
// source and start, plus the end for an interval and the value for a
// measurement. An interval sample is Replaceable (hashed without its value):
// Google may revise a minute's count after a later sync.
func quantity(p point, userID, qt, unit string, v float64, start, end time.Time, md map[string]any, interval bool) model.QuantitySample {
	slug, name := p.source()
	s := model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      slug,
		SourceName:      name,
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        md,
		Replaceable:     interval,
	}
	h := map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"quantity_type": qt,
		"source":        p.sourceKey(),
		"start_date":    start.Format(time.RFC3339Nano),
	}
	if interval {
		h["end_date"] = end.Format(time.RFC3339Nano)
	} else {
		h["value"] = model.CanonicalFloat(v)
	}
	s.ContentHash = model.ComputeContentHash(h)
	return s
}

// one wraps a single sample in a result.
func one(s model.QuantitySample) *model.MapResult {
	return &model.MapResult{QuantitySamples: []model.QuantitySample{s}}
}

// dailySample builds the Replaceable sample of a daily point. A daily point
// has a date and no time or offset, so it spans that date in UTC and claims
// no time zone: the ETL files it under the date itself. The hash leaves the
// value out, so a recomputed day replaces the stored figure.
func dailySample(p point, userID string, date civilDate, qt, unit string, v float64, md map[string]any) (model.QuantitySample, error) {
	day, err := date.time()
	if err != nil {
		return model.QuantitySample{}, err
	}
	ds := day.Format(time.DateOnly)
	md["date"] = ds
	slug, name := p.source()
	s := model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      slug,
		SourceName:      name,
		ExternalID:      fmt.Sprintf("%s_%s_%s", p.dataType.ID, ds, qt),
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       day,
		EndDate:         day.AddDate(0, 0, 1).Add(-time.Second),
		Metadata:        md,
		Replaceable:     true,
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"quantity_type": qt,
		"data_type":     p.dataType.ID,
		"source":        p.sourceKey(),
		"date":          ds,
	})
	return s, nil
}
