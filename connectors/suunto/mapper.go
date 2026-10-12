// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// recordHandler maps one raw line to canonical records. A nil result with a
// nil error means the line carries nothing this connector maps.
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
	m := &mapper{
		handlers: map[string]recordHandler{
			DataTypeWorkout: mapWorkout,
		},
		unknownTypes: map[string]int{},
	}
	for _, dt := range rawOnlyTypes {
		m.handlers[dt] = rawOnly
	}
	return m
}

// rawOnly is the handler of a stored but not yet mapped data type.
func rawOnly([]byte, string) (*model.MapResult, error) { return nil, nil }

// mapRecord re-encodes fields (one parsed raw line) and hands it to the
// handler of dataType, which decodes it into the Suunto wire struct.
func (m *mapper) mapRecord(dataType string, fields map[string]any, userID string) (*model.MapResult, error) {
	h, ok := m.handlers[dataType]
	if !ok {
		m.trackUnknown(dataType)
		return nil, nil
	}
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("suunto: %s: re-encode line: %w", dataType, err)
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
		slog.Warn("suunto: unknown data type skipped", "provider", Slug, "data_type", dataType)
	}
}

// decode unmarshals one line into v, naming the data type on failure.
func decode(dataType string, line []byte, v any) error {
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("suunto: %s: %w", dataType, err)
	}
	return nil
}
