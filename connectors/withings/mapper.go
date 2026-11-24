// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record: the vendor.
const sourceName = "Withings"

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
			DataTypeMeasure:  mapMeasure,
			DataTypeActivity: mapActivity,
			DataTypeIntraday: mapIntraday,
			DataTypeSleep:    mapSleep,
			DataTypeWorkout:  mapWorkout,
			DataTypeECG:      mapECG,
		},
		unknownTypes: map[string]int{},
	}
}

// mapRecord re-encodes fields (one parsed raw line) and hands it to the
// handler of dataType, which decodes it into the Withings wire struct.
func (m *mapper) mapRecord(dataType string, fields map[string]any, userID string) (*model.MapResult, error) {
	h, ok := m.handlers[dataType]
	if !ok {
		m.trackUnknown(dataType)
		return nil, nil
	}
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("withings: %s: re-encode line: %w", dataType, err)
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
		modlog().Warn().Str("data_type", dataType).Msg("unknown_data_type_skipped")
	}
}

// decode unmarshals one line into v, naming the data type on failure.
func decode(dataType string, line []byte, v any) error {
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("withings: %s: %w", dataType, err)
	}
	return nil
}

func ptrFloat(v float64) *float64 { return &v }

func ptrInt(v int) *int { return &v }

// floatField reads a numeric field of a decoded map.
func floatField(m map[string]any, key string) (float64, bool) {
	switch v := m[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func optFloat(m map[string]any, key string) *float64 {
	if v, ok := floatField(m, key); ok {
		return &v
	}
	return nil
}

func optInt(m map[string]any, key string) *int {
	if v, ok := floatField(m, key); ok {
		return ptrInt(int(v))
	}
	return nil
}
