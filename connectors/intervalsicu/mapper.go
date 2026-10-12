// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// recordHandler maps one raw line to canonical records. A nil result with a
// nil error means the line carries nothing this connector stores.
type recordHandler func(line []byte, userID string) (*model.MapResult, error)

var handlers = map[string]recordHandler{
	DataTypeActivity: mapActivity,
	DataTypeWellness: mapWellness,
}

// unknownTypes counts data types seen without a handler, so each is logged
// once rather than once per line.
var (
	unknownMu    sync.Mutex
	unknownTypes = map[string]int{}
)

// maxUnknownTypes bounds the unknown type counter.
const maxUnknownTypes = 64

// mapRecord re-encodes fields (one parsed raw line) and hands it to the
// handler of dataType, which decodes it into the wire struct.
func mapRecord(dataType string, fields map[string]any, userID string) (*model.MapResult, error) {
	h, ok := handlers[dataType]
	if !ok {
		trackUnknown(dataType)
		return nil, nil
	}
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("intervals_icu: %s: re-encode line: %w", dataType, err)
	}
	return h(line, userID)
}

func trackUnknown(dataType string) {
	unknownMu.Lock()
	defer unknownMu.Unlock()
	if _, seen := unknownTypes[dataType]; !seen && len(unknownTypes) >= maxUnknownTypes {
		return
	}
	unknownTypes[dataType]++
	if unknownTypes[dataType] == 1 {
		slog.Warn("intervals_icu: unknown data type skipped", "provider", Slug, "data_type", dataType)
	}
}

// decode unmarshals one line into v, naming the data type on failure.
func decode(dataType string, line []byte, v any) error {
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("intervals_icu: %s: %w", dataType, err)
	}
	return nil
}
