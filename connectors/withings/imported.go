// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"encoding/json"
	"strconv"
)

// Withings returns, next to what its devices and app recorded, what the
// Withings app imported from the phone's health store or another app. With
// OHS_WITHINGS_SKIP_IMPORTED a pull leaves those records out, so data a user
// already brings through Apple Health or Health Connect is not stored twice.
//
// The rules follow the Withings API reference:
//   - getactivity "brand": "Identifies if data comes from Withings (device or
//     mobile application tracker) or an external way (Value is 1 for Withings
//     and 18 for external)".
//   - the device model table, where the ids below are the "HealthKit Apple",
//     "HealthKit Google" and "HealthKit Huawei" categories. Partner devices
//     sold through Withings (iGlucose, iPulse OX, Lingo, WPA02) are not imports
//     and stay.

// brandExternal is the getactivity brand of data from "an external way".
const brandExternal = 18

// importedModels are the model ids of data Withings imported.
var importedModels = map[int64]bool{
	1051: true, // iOS step tracker
	1052: true, // iOS step tracker
	1053: true, // Android step tracker
	1054: true, // Android step tracker
	1055: true, // GoogleFit tracker
	1056: true, // Samsung Health tracker
	1057: true, // HealthKit step iPhone tracker
	1058: true, // HealthKit step Apple Watch tracker
	1059: true, // HealthKit other step tracker
	1060: true, // Google Health Connect
	1062: true, // Huawei tracker
}

// modelField names the field holding the device model id of each data type.
// A sleep summary's "model" is a device family, its "model_id" the model; a
// workout's "model" is the model id. ECG recordings come from Withings
// devices only.
var modelField = map[string]string{
	DataTypeMeasure:  "modelid",
	DataTypeIntraday: "model_id",
	DataTypeSleep:    "model_id",
	DataTypeWorkout:  "model",
}

// imported reports whether a line of dataType is a record Withings imported.
// A line it cannot read is kept.
func imported(dataType string, line []byte) bool {
	if dataType == DataTypeActivity {
		var head struct {
			Brand json.Number `json:"brand"`
		}
		if json.Unmarshal(line, &head) != nil {
			return false
		}
		b, err := head.Brand.Int64()
		return err == nil && b == brandExternal
	}
	field, ok := modelField[dataType]
	if !ok {
		return false
	}
	var head map[string]json.RawMessage
	if json.Unmarshal(line, &head) != nil {
		return false
	}
	return importedModel(head[field])
}

// importedModel reports whether raw, a model id as Withings sends it (a
// number, sometimes a numeric string), is one of importedModels.
func importedModel(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		id, err := n.Int64()
		return err == nil && importedModels[id]
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		id, err := strconv.ParseInt(s, 10, 64)
		return err == nil && importedModels[id]
	}
	return false
}

// dropImported returns lines without the imported ones, and how many it
// dropped.
func dropImported(dataType string, lines []dayLine) ([]dayLine, int) {
	kept := lines[:0]
	dropped := 0
	for _, l := range lines {
		if imported(dataType, l.line) {
			dropped++
			continue
		}
		kept = append(kept, l)
	}
	return kept, dropped
}
