// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record: the vendor.
const sourceName = "WHOOP"

// kcalPerKilojoule converts WHOOP's kilojoules to the kilocalories the
// canonical records carry (one thermochemical calorie is 4.184 J).
const kcalPerKilojoule = 1 / 4.184

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
			DataTypeCycle:    mapCycle,
			DataTypeRecovery: mapRecovery,
			DataTypeSleep:    mapSleep,
			DataTypeWorkout:  mapWorkout,
			DataTypeBody:     mapBody,
			DataTypeWebhook:  mapWebhook,
		},
		unknownTypes: map[string]int{},
	}
}

// mapRecord re-encodes fields (one parsed raw line) and hands it to the
// handler of dataType, which decodes it into the WHOOP wire struct.
func (m *mapper) mapRecord(dataType string, fields map[string]any, userID string) (*model.MapResult, error) {
	h, ok := m.handlers[dataType]
	if !ok {
		m.trackUnknown(dataType)
		return nil, nil
	}
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("whoop: %s: re-encode line: %w", dataType, err)
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
		slog.Warn("whoop: unknown data type skipped", "provider", Slug, "data_type", dataType)
	}
}

// decode unmarshals one line into v, naming the data type on failure.
func decode(dataType string, line []byte, v any) error {
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("whoop: %s: %w", dataType, err)
	}
	return nil
}

// requireTime parses a mandatory timestamp field of a line.
func requireTime(dataType, field, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("whoop: %s: missing %s", dataType, field)
	}
	t, err := parseTime(value)
	if err != nil {
		return time.Time{}, fmt.Errorf("whoop: %s: %s: %w", dataType, field, err)
	}
	return t, nil
}

// quantity builds one quantity sample of a WHOOP record. Every WHOOP figure
// can be re-scored after it was first sent (a recovery recomputed, the
// current cycle's strain still growing), so the sample is Replaceable and its
// hash names the record and the quantity, never the value: the newest score
// replaces the stored one.
func quantity(userID, recordID, qt string, value float64, unit string, start, end time.Time, md map[string]any) model.QuantitySample {
	s := model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		ExternalID:      recordID + "_" + qt,
		QuantityType:    qt,
		Value:           value,
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
		"external_id":   recordID,
	})
	return s
}

// dailySummary builds the WHOOP summary of one local day. Cycles (strain,
// energy) and recoveries (recovery score, resting heart rate) each fill their
// own figures of the same row: the hash names only the day, the summary is
// Replaceable, and the writer keeps a stored figure the other record type
// leaves out.
func dailySummary(userID string, day time.Time, offset string) model.DailySummary {
	s := model.DailySummary{
		UserID:      userID,
		Provider:    Slug,
		SummaryDate: day,
		Metadata:    withTimezone(map[string]any{}, offset),
		Replaceable: true,
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":      userID,
		"provider":     Slug,
		"summary_date": day.Format("2006-01-02"),
		"kind":         "whoop_day",
	})
	return s
}

// msToSeconds converts an optional millisecond figure to whole seconds.
func msToSeconds(ms *int64) *int {
	if ms == nil {
		return nil
	}
	v := int(*ms / 1000)
	return &v
}

func ptrFloat(v float64) *float64 { return &v }

// setIf copies an optional figure into metadata.
func setIf[T any](md map[string]any, key string, v *T) {
	if v != nil {
		md[key] = *v
	}
}
