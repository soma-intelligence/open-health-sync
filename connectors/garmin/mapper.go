// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record: the vendor.
const sourceName = "Garmin"

// recordHandler maps one raw line to canonical records. A nil result with a
// nil error means the line carries nothing this connector stores.
type recordHandler func(line []byte, userID string) (*model.MapResult, error)

// mapper dispatches stored raw lines by data type.
type mapper struct {
	handlers map[string]recordHandler

	mu           sync.Mutex
	unknownTypes map[string]int
}

// maxUnknownTypes bounds the unknown type counter.
const maxUnknownTypes = 64

func newMapper() *mapper {
	return &mapper{
		handlers: map[string]recordHandler{
			DataTypeDailies:        mapDaily,
			DataTypeEpochs:         mapEpoch,
			DataTypeSleeps:         mapSleep,
			DataTypeActivities:     mapActivity,
			DataTypeBodyComps:      mapBodyComp,
			DataTypeHRV:            mapHRV,
			DataTypePulseOx:        mapPulseOx,
			DataTypeRespiration:    mapRespiration,
			DataTypeUserMetrics:    mapUserMetrics,
			DataTypeBloodPressures: mapBloodPressure,
			DataTypeSkinTemp:       mapSkinTemp,
			// Raw only: no canonical name for stress or Body Battery.
			DataTypeStressDetails: func([]byte, string) (*model.MapResult, error) { return nil, nil },
		},
		unknownTypes: map[string]int{},
	}
}

// mapRecord re-encodes fields (one parsed raw line) and hands it to the
// handler of dataType.
func (m *mapper) mapRecord(dataType string, fields map[string]any, userID string) (*model.MapResult, error) {
	if dt, ok := notificationKeys[dataType]; ok {
		dataType = dt
	}
	h, ok := m.handlers[dataType]
	if !ok {
		m.trackUnknown(dataType)
		return nil, nil
	}
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("garmin: %s: re-encode line: %w", dataType, err)
	}
	return h(line, userID)
}

func (m *mapper) trackUnknown(dataType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, seen := m.unknownTypes[dataType]; !seen && len(m.unknownTypes) >= maxUnknownTypes {
		return
	}
	m.unknownTypes[dataType]++
	if m.unknownTypes[dataType] == 1 {
		slog.Warn("garmin: unknown data type skipped", "provider", Slug, "data_type", dataType)
	}
}

// decode unmarshals one line into v, naming the data type on failure.
func decode(dataType string, line []byte, v any) error {
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("garmin: %s: %w", dataType, err)
	}
	return nil
}

// setIf copies *v into md under key when v is set.
func setIf[T any](md map[string]any, key string, v *T) {
	if v != nil {
		md[key] = *v
	}
}

// valid reports whether v is set and a finite number not below zero.
func valid(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0
}

// sample builds one quantity sample of this connector; the caller sets the
// content hash and Replaceable.
func sample(userID, externalID, qt, unit string, v float64, start, end time.Time, md map[string]any) model.QuantitySample {
	return model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		ExternalID:      externalID,
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        md,
	}
}

// hash is the content hash of a record of this connector.
func hash(userID, dataType string, kv ...string) string {
	m := map[string]string{"user_id": userID, "provider": Slug, "data_type": dataType}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return model.ComputeContentHash(m)
}

// copyMap returns a shallow copy of md.
func copyMap(md map[string]any) map[string]any {
	out := make(map[string]any, len(md))
	for k, v := range md {
		out[k] = v
	}
	return out
}

// reading is one timed value of a "seconds from start" series.
type reading struct {
	at time.Time
	v  float64
}

func sortReadings(r []reading) {
	sort.Slice(r, func(i, j int) bool { return r[i].at.Before(r[j].at) })
}

// seriesSamples turns a time offset series into point samples of qt, each
// hashed on its time (first write wins), skipping values outside [lo, hi].
func seriesSamples(userID, dataType, externalID, qt, unit string, start time.Time, series map[string]float64, lo, hi float64, md map[string]any) []model.QuantitySample {
	var out []model.QuantitySample
	for _, r := range timeOffsets(start, series) {
		if math.IsNaN(r.v) || r.v < lo || r.v > hi {
			continue
		}
		s := sample(userID, externalID, qt, unit, r.v, r.at, r.at, copyMap(md))
		s.ContentHash = hash(userID, dataType, "quantity_type", qt, "start_date", r.at.Format(time.RFC3339))
		out = append(out, s)
	}
	return out
}

// dayMeta returns base metadata of a summary: its id and the offset.
func dayMeta(summaryID string, off *int64) map[string]any {
	md := map[string]any{}
	if summaryID != "" {
		md["summary_id"] = summaryID
	}
	return withOffset(md, off)
}
