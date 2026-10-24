// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"log/slog"
	"sync"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// recordHandler maps one raw line to canonical records. A nil result with a
// nil error means the line carries nothing this connector stores.
type recordHandler func(record map[string]any, userID string) (*model.MapResult, error)

// mapper dispatches raw Apple Health lines to their handler by the type
// discriminator fields each line carries.
type mapper struct {
	quantityHandlers  map[string]recordHandler
	categoryHandlers  map[string]recordHandler
	workoutHandler    recordHandler
	sleepHandler      recordHandler
	ecgHandler        recordHandler
	ecgVoltageHandler recordHandler
	heartbeatHandler  recordHandler
	routeHandler      recordHandler

	// unknownTypes counts identifiers seen without a handler, so each is
	// logged once rather than once per line.
	unknownTypes    map[string]int
	unknownOverflow int // unknown lines seen after the map reached its cap
	mu              sync.Mutex
}

func newMapper() *mapper {
	m := &mapper{
		quantityHandlers: make(map[string]recordHandler),
		categoryHandlers: make(map[string]recordHandler),
		unknownTypes:     make(map[string]int),
	}
	m.registerQuantityHandlers()
	m.registerCategoryHandlers()
	m.workoutHandler = m.mapWorkout
	m.sleepHandler = m.mapSleep
	m.ecgHandler = m.mapECG
	m.ecgVoltageHandler = m.mapECGVoltage
	m.heartbeatHandler = m.mapHeartbeat
	m.routeHandler = m.mapRoute
	return m
}

// mapRecord dispatches a raw JSON record to the appropriate handler based on
// type discriminator fields in the record.
func (m *mapper) mapRecord(record map[string]any, userID string) (*model.MapResult, error) {
	// A deletion line names only the UUID of a sample HealthKit deleted.
	if _, ok := record[deletedField]; ok {
		return m.mapDeletion(record)
	}

	// Audiogram records arrive with quantity_type = "HKDataTypeIdentifierAudiogramSample".
	if record["quantity_type"] == "HKDataTypeIdentifierAudiogramSample" {
		return m.mapAudiogram(record, userID)
	}
	// State-of-mind records arrive with quantity_type = "HKStateOfMindType".
	if record["quantity_type"] == "HKStateOfMindType" {
		return m.mapStateOfMind(record, userID)
	}

	if qt, ok := record["quantityType"].(string); ok {
		// Dietary quantity types dispatch to the nutrition handler.
		if _, isDietary := dietaryNutrient(qt); isDietary {
			return m.mapDietaryQuantity(record, userID)
		}
		if handler, found := m.quantityHandlers[qt]; found {
			return handler(record, userID)
		}
		m.trackUnknown(qt)
		return nil, nil
	}

	if ct, ok := record["categoryType"].(string); ok {
		if ct == "HKCategoryTypeIdentifierSleepAnalysis" {
			return m.sleepHandler(record, userID)
		}
		if handler, found := m.categoryHandlers[ct]; found {
			return handler(record, userID)
		}
		m.trackUnknown(ct)
		return nil, nil
	}

	if _, ok := record["workoutActivityType"]; ok {
		return m.workoutHandler(record, userID)
	}

	// Correlations: blood pressure, food.
	if _, ok := record["correlationType"]; ok {
		return m.handleCorrelation(record, userID)
	}

	// Clinical records (FHIR resources) are not stored: Open Health Sync has no
	// canonical table for them. They are dropped on purpose, without an error,
	// so a client that still uploads them does not fill the failure log.
	if _, ok := record["clinicalType"]; ok {
		return nil, nil
	}
	if _, ok := record["fhirResource"]; ok {
		return nil, nil
	}

	// ECG metadata has a "classification" field.
	if _, ok := record["classification"]; ok {
		return m.ecgHandler(record, userID)
	}

	// ECG voltage sample: "timeSinceSampleStart" and "value", no "quantityType".
	if _, ok := record["timeSinceSampleStart"]; ok {
		if _, hasValue := record["value"]; hasValue {
			return m.ecgVoltageHandler(record, userID)
		}
	}

	// Heartbeat series: a "heartbeats" array, or the legacy one-beat shape
	// with a top-level "timeSinceSeriesStart".
	if _, ok := record["heartbeats"]; ok {
		return m.heartbeatHandler(record, userID)
	}
	if _, ok := record["timeSinceSeriesStart"]; ok {
		return m.heartbeatHandler(record, userID)
	}

	// Route point: "latitude" and "longitude".
	if _, ok := record["latitude"]; ok {
		if _, hasLng := record["longitude"]; hasLng {
			return m.routeHandler(record, userID)
		}
	}

	m.trackUnknown(unrecognizedLine)
	return nil, nil
}

// unrecognizedLine is the label trackUnknown counts a line under when it
// carries none of the fields the mapper dispatches on.
const unrecognizedLine = "(line without a known type field)"

// supportedTypes returns every type identifier this mapper handles.
func (m *mapper) supportedTypes() []string {
	types := make([]string, 0,
		len(m.quantityHandlers)+len(m.categoryHandlers)+len(dietaryNutrients)+10)
	for k := range m.quantityHandlers {
		types = append(types, k)
	}
	for k := range m.categoryHandlers {
		types = append(types, k)
	}
	for k := range dietaryNutrients {
		types = append(types, dietaryPrefix+k)
	}
	return append(types,
		"HKCategoryTypeIdentifierSleepAnalysis",
		"workout",
		"ecg_metadata",
		"ecg_voltage",
		"heartbeat",
		"route",
		"HKCorrelationTypeIdentifierFood",
	)
}

// trackUnknown counts an unknown type identifier and logs it the first time.
// The identifier is a HealthKit type name, never a health value.
func (m *mapper) trackUnknown(typeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, seen := m.unknownTypes[typeID]; !seen && len(m.unknownTypes) >= maxUnknownTypes {
		// The map is bounded: a client sending endless distinct identifiers
		// must not grow it. Past the cap, unknown lines are only counted.
		m.unknownOverflow++
		if m.unknownOverflow == 1 {
			slog.Warn("apple_health: unknown type tracking full, further new types are only counted",
				"tracked", maxUnknownTypes)
		}
		return
	}
	m.unknownTypes[typeID]++
	if m.unknownTypes[typeID] == 1 {
		slog.Warn("apple_health: unknown type skipped", "type_id", typeID)
	}
}

// maxUnknownTypes caps how many distinct unknown identifiers are tracked
// (and logged once each).
const maxUnknownTypes = 1000

// unknownTypeCounts returns a copy of the unknown types counter.
func (m *mapper) unknownTypeCounts() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()

	counts := make(map[string]int, len(m.unknownTypes))
	for k, v := range m.unknownTypes {
		counts[k] = v
	}
	return counts
}
