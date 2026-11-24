// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// recordHandler maps one raw line to canonical records. A nil result with a
// nil error means the line carries nothing this connector stores.
type recordHandler func(line []byte, userID string) (*model.MapResult, error)

var handlers = map[string]recordHandler{
	DataTypeWorkout: mapWorkoutLine,
	DataTypeWebhook: mapWebhook,
	DataTypeUser:    mapUser,
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
		return nil, fmt.Errorf("wahoo: %s: re-encode line: %w", dataType, err)
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
		modlog().Warn().Str("data_type", dataType).Msg("unknown_data_type_skipped")
	}
}

// decode unmarshals one line into v, naming the data type on failure.
func decode(dataType string, line []byte, v any) error {
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("wahoo: %s: %w", dataType, err)
	}
	return nil
}

// mapUser turns the body figures of the user record into a body_mass and a
// height sample, dated when the user last changed the profile. The same
// figures read again hash the same and are stored once.
func mapUser(line []byte, userID string) (*model.MapResult, error) {
	var u User
	if err := decode(DataTypeUser, line, &u); err != nil {
		return nil, err
	}
	at, err := time.Parse(time.RFC3339, u.UpdatedAt)
	if err != nil {
		// Without a date the figures cannot be placed in time.
		return nil, nil
	}
	at = at.UTC()
	res := &model.MapResult{}
	add := func(qt string, v *float64, unit string, lo, hi float64) {
		if v == nil || *v <= lo || *v > hi {
			return
		}
		res.QuantitySamples = append(res.QuantitySamples, model.QuantitySample{
			UserID:          userID,
			Provider:        Slug,
			SourceSlug:      Slug,
			SourceName:      "Wahoo",
			QuantityType:    qt,
			Value:           *v,
			Unit:            unit,
			IsPrimarySource: true,
			StartDate:       at,
			EndDate:         at,
			Metadata:        map[string]any{"wahoo_source": "user_profile"},
			ContentHash: model.ComputeContentHash(map[string]string{
				"user_id":       userID,
				"provider":      Slug,
				"quantity_type": qt,
				"start_date":    at.Format(time.RFC3339),
				"value":         model.CanonicalFloat(*v),
			}),
		})
	}
	add("body_mass", u.Weight.val(), "kg", 0, 500)
	add("height", u.Height.val(), "m", 0, 3)
	if len(res.QuantitySamples) == 0 {
		return nil, nil
	}
	return res, nil
}
