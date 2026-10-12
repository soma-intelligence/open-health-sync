// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record: the vendor.
const sourceName = "Polar"

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
			DataTypeExercise:        mapExercise,
			DataTypeSleep:           mapSleep,
			DataTypeNightlyRecharge: mapNightlyRecharge,
			DataTypeActivity:        mapActivity,
			DataTypeContinuousHR:    mapContinuousHR,
			DataTypePhysicalInfo:    mapPhysicalInfo,
		},
		unknownTypes: map[string]int{},
	}
}

// mapRecord re-encodes fields (one parsed raw line) and hands it to the
// handler of dataType, which decodes it into the AccessLink wire struct.
func (m *mapper) mapRecord(dataType string, fields map[string]any, userID string) (*model.MapResult, error) {
	h, ok := m.handlers[dataType]
	if !ok {
		m.trackUnknown(dataType)
		return nil, nil
	}
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("polar: %s: re-encode line: %w", dataType, err)
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
		slog.Warn("polar: unknown data type skipped", "provider", Slug, "data_type", dataType)
	}
}

// decode unmarshals one line into v, naming the data type on failure.
func decode(dataType string, line []byte, v any) error {
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("polar: %s: %w", dataType, err)
	}
	return nil
}

// setIf copies *v into md under key when v is set.
func setIf[T any](md map[string]any, key string, v *T) {
	if v != nil {
		md[key] = *v
	}
}

// withOffset records a UTC offset in md, plus the matching Etc/GMT zone as
// "timezone" when the offset is a whole hour.
func withOffset(md map[string]any, minutes int) map[string]any {
	md["utc_offset_minutes"] = minutes
	if z := offsetZone(minutes); z != "" {
		md["timezone"] = z
	}
	return md
}

// sample builds one quantity sample of this connector; the caller sets the
// content hash and Replaceable.
func sample(userID, deviceID, externalID, qt, unit string, v float64, start, end time.Time, md map[string]any) model.QuantitySample {
	return model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		DeviceID:        deviceID,
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

// copyMap returns a shallow copy of md, so samples of one line do not share
// a metadata map.
func copyMap(md map[string]any) map[string]any {
	out := make(map[string]any, len(md))
	for k, v := range md {
		out[k] = v
	}
	return out
}
