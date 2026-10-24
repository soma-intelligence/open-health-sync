// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"fmt"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// deletedField is the discriminator of a deletion line: the UUID of a sample
// HealthKit reports deleted (HKDeletedObject), as the SDK sends it:
//
//	{"deletedUuid": "6F1A9D2B-0C4E-4B8A-9E61-3D2B7A5C1F00"}
//
// HealthKit samples are immutable, so an edit arrives as the deletion of the
// old sample and a new sample under a new UUID.
const deletedField = "deletedUuid"

// maxUUIDLength bounds the id a deletion line names. A HealthKit UUID string
// is 36 characters; the bound only keeps a malformed line out of the
// tombstone table.
const maxUUIDLength = 128

// mapDeletion turns a deletion line into a model.Deletion of the record
// stored under that UUID (sampleExternalID).
func (m *mapper) mapDeletion(record map[string]any) (*model.MapResult, error) {
	id, ok := record[deletedField].(string)
	if !ok || id == "" {
		return nil, fmt.Errorf("deletion: %s must be a non-empty string", deletedField)
	}
	if len(id) > maxUUIDLength {
		return nil, fmt.Errorf("deletion: %s is longer than %d bytes", deletedField, maxUUIDLength)
	}
	return &model.MapResult{Deletions: []model.Deletion{{ExternalID: id}}}, nil
}

// sampleExternalID is the external id of the records a line gives: the
// HealthKit UUID the SDK sends as "uuid", so that a deletion line of that
// UUID removes them. A line without one (a Health app export) gives "".
func sampleExternalID(record map[string]any) string {
	return stringField(record, "uuid")
}

// memberExternalID is the external id of one member of a correlation line:
// "<correlation uuid>_<canonical type>", so that the deletion of the
// correlation removes every member (the "<id>_<suffix>" convention of
// model.Deletion). It is "" when the line has no UUID.
func memberExternalID(record map[string]any, canonicalType string) string {
	id := sampleExternalID(record)
	if id == "" {
		return ""
	}
	return id + "_" + canonicalType
}
