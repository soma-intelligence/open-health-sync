// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record: the vendor.
const sourceName = "Oura"

// recordHandler maps one raw line to canonical records. A nil result with a
// nil error means the line carries nothing this connector stores.
type recordHandler func(line []byte, userID string) (*model.MapResult, error)

// mapper dispatches stored raw lines by data type.
type mapper struct {
	handlers map[string]recordHandler

	// unknownTypes counts data types seen without a handler, so each is
	// logged once rather than once per line.
	mu           sync.Mutex
	unknownTypes map[string]int
}

// maxUnknownTypes bounds the unknown type counter.
const maxUnknownTypes = 64

func newMapper() *mapper {
	return &mapper{
		handlers: map[string]recordHandler{
			DataTypeDailyActivity:  mapDailyActivity,
			DataTypeSleep:          mapSleep,
			DataTypeDailyReadiness: mapDailyReadiness,
			DataTypeDailySpO2:      mapDailySpO2,
			DataTypeHeartRate:      mapHeartRate,
			DataTypeWorkout:        mapWorkout,
			DataTypeDailySleep:     mapDailySleep,
			DataTypeDailyStress:    mapDailyStress,
			DataTypeSession:        mapSession,
			DataTypeVO2Max:         mapVO2Max,
			DataTypeWebhook:        mapWebhook,
		},
		unknownTypes: map[string]int{},
	}
}

// mapRecord re-encodes fields (one parsed raw line) and hands it to the
// handler of dataType, which decodes it into the Oura wire struct.
func (m *mapper) mapRecord(dataType string, fields map[string]any, userID string) (*model.MapResult, error) {
	h, ok := m.handlers[dataType]
	if !ok {
		m.trackUnknown(dataType)
		return nil, nil
	}
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("oura: %s: re-encode line: %w", dataType, err)
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
		slog.Warn("oura: unknown data type skipped", "provider", Slug, "data_type", dataType)
	}
}

// decode unmarshals one line into v, naming the data type on failure.
func decode(dataType string, line []byte, v any) error {
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("oura: %s: %w", dataType, err)
	}
	return nil
}

// dailySample builds one Replaceable sample of a daily document. Oura
// re-sends a day's document with new values while the day is open (and after
// a late sync), so the hash leaves the value out: the newest value replaces
// the stored one.
func dailySample(userID, day, qt, unit string, v float64, start, end time.Time, md map[string]any) model.QuantitySample {
	s := model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		ExternalID:      fmt.Sprintf("%s_%s", day, qt),
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        md,
		Replaceable:     true,
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"quantity_type": qt,
		"date":          day,
	})
	return s
}

// copyMetadata returns a shallow copy of md, so samples of one document do
// not share a map.
func copyMetadata(md map[string]any) map[string]any {
	out := make(map[string]any, len(md))
	for k, v := range md {
		out[k] = v
	}
	return out
}

func ptrFloat(v float64) *float64 { return &v }
