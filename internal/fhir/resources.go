// SPDX-License-Identifier: AGPL-3.0-only

package fhir

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

// The identifiers and code systems of the Patient, Device and Provenance
// resources.
const (
	// ProviderSystem is the identifier system of a provider integration
	// (apple_health, withings...): the assembler agent of a Provenance.
	ProviderSystem = Namespace + "/provider"
	// ParticipantType is the HL7 provenance participant type code system.
	ParticipantType = "http://terminology.hl7.org/CodeSystem/provenance-participant-type"

	// DeviceIDPrefix starts the id of a Device; the rest is a hash of its
	// identifying fields.
	DeviceIDPrefix = "dev-"
	// ProvenanceIDPrefix starts the id of a Provenance; the rest is the id of
	// the Observation it is about.
	ProvenanceIDPrefix = "prov-"
)

// ExternalRefSystem is the identifier system of the tenant's own references
// to its users (external_ref, unique within one tenant).
func ExternalRefSystem(tenantID uuid.UUID) string {
	return Namespace + "/tenant/" + tenantID.String() + "/external-ref"
}

// Patient is a FHIR R4 Patient: the user, with nothing Open Health Sync does
// not store (no name, no birth date).
type Patient struct {
	ResourceType string       `json:"resourceType"`
	ID           string       `json:"id"`
	Identifier   []Identifier `json:"identifier,omitempty"`
	Active       bool         `json:"active"`
}

// FromUser maps the user of tenant to a Patient; externalRef is the tenant's
// own reference to the user, "" when it set none. Only an active user is
// served, so the Patient is always active.
func FromUser(tenantID, user uuid.UUID, externalRef string) Patient {
	p := Patient{ResourceType: "Patient", ID: user.String(), Active: true}
	if externalRef != "" {
		p.Identifier = []Identifier{{System: ExternalRefSystem(tenantID), Value: externalRef}}
	}
	return p
}

// DeviceName is a FHIR Device.deviceName.
type DeviceName struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Device is a FHIR R4 Device, limited to the elements the mapping fills.
// Members are in the order of the specification.
type Device struct {
	ResourceType string       `json:"resourceType"`
	ID           string       `json:"id"`
	Meta         *Meta        `json:"meta,omitempty"`
	Identifier   []Identifier `json:"identifier,omitempty"`
	DeviceName   []DeviceName `json:"deviceName,omitempty"`
	ModelNumber  string       `json:"modelNumber,omitempty"`
	Patient      *Reference   `json:"patient,omitempty"`
}

// DeviceID is the stable id of the device d of user: a hash of the user and
// of the five fields of d, so the same device gets the same id on every read
// and no query is needed to reference it.
func DeviceID(user uuid.UUID, d read.Device) string {
	h := sha256.New()
	// NUL cannot appear in a stored text value: the fields cannot run into
	// each other.
	h.Write([]byte(strings.Join([]string{user.String(), d.Provider, d.Source, d.DeviceID, d.DeviceModel, d.ProductType}, "\x00")))
	return DeviceIDPrefix + hex.EncodeToString(h.Sum(nil))[:16]
}

// deviceDisplay is the name an Observation shows for its device: the device
// id, else the model, else the product type.
func deviceDisplay(d read.Device) string {
	switch {
	case d.DeviceID != "":
		return d.DeviceID
	case d.DeviceModel != "":
		return d.DeviceModel
	}
	return d.ProductType
}

// deviceRef is the reference to the Device of d, nil when d names none.
func deviceRef(user uuid.UUID, d read.Device) *Reference {
	if !d.Known() {
		return nil
	}
	return &Reference{Reference: "Device/" + DeviceID(user, d), Display: deviceDisplay(d)}
}

// FromDevice maps one device of user's records to a Device.
func FromDevice(user uuid.UUID, d read.Device) Device {
	out := Device{
		ResourceType: "Device",
		ID:           DeviceID(user, d),
		Meta:         &Meta{Source: ProviderSystem + "/" + d.Provider},
		ModelNumber:  d.ProductType,
		Patient:      &Reference{Reference: "Patient/" + user.String()},
	}
	if d.Source != "" {
		out.Meta.Tag = []Coding{{System: SourceSystem, Code: d.Source}}
	}
	if d.DeviceID != "" {
		out.Identifier = []Identifier{{System: deviceIDSystem(d.Provider), Value: d.DeviceID}}
	}
	if d.DeviceModel != "" {
		out.DeviceName = []DeviceName{{Name: d.DeviceModel, Type: "model-name"}}
	}
	return out
}

func deviceIDSystem(provider string) string {
	return ProviderSystem + "/" + provider + "/device-id"
}

// RecordDevice is the device of r (for a blood pressure panel, the systolic
// sample's); it names none (Known is false) for a daily summary or a record
// without device fields.
func RecordDevice(r read.ObservationRecord) read.Device {
	switch r.Kind {
	case read.KindQuantity:
		return read.SampleDevice(r.Sample.Sample)
	case read.KindSleep:
		return read.ProvenanceDevice(r.Sleep.Provenance)
	case read.KindWorkout:
		return read.ProvenanceDevice(r.Workout.Provenance)
	case read.KindDaily:
		return read.Device{Provider: r.Daily.Provider, Source: deref(r.Daily.Source)}
	case read.KindECG:
		return read.ProvenanceDevice(r.ECG.Provenance)
	case read.KindNutrition:
		return read.ProvenanceDevice(r.Nutrition.Provenance)
	}
	return read.Device{}
}

// ProvenanceAgent is a FHIR Provenance.agent.
type ProvenanceAgent struct {
	Type *CodeableConcept `json:"type,omitempty"`
	Who  Reference        `json:"who"`
}

// ProvenanceEntity is a FHIR Provenance.entity.
type ProvenanceEntity struct {
	Role string    `json:"role"`
	What Reference `json:"what"`
}

// Provenance is a FHIR R4 Provenance, limited to the elements the mapping
// fills. Members are in the order of the specification.
type Provenance struct {
	ResourceType string             `json:"resourceType"`
	ID           string             `json:"id"`
	Target       []Reference        `json:"target"`
	Recorded     string             `json:"recorded"`
	Agent        []ProvenanceAgent  `json:"agent"`
	Entity       []ProvenanceEntity `json:"entity,omitempty"`
}

// ProvenanceID is the stable id of the Provenance of the Observation with id
// observationID.
func ProvenanceID(observationID string) string { return ProvenanceIDPrefix + observationID }

// ParseProvenanceID reverses ProvenanceID: the id of the Observation, checked
// like ParseID.
func ParseProvenanceID(s string) (string, bool) {
	rest, ok := strings.CutPrefix(s, ProvenanceIDPrefix)
	if !ok {
		return "", false
	}
	_, _, _, ok = ParseID(rest)
	return rest, ok
}

func participant(code, display string) *CodeableConcept {
	return &CodeableConcept{Coding: []Coding{{System: ParticipantType, Code: code, Display: display}}}
}

// FromRecordProvenance maps who produced the Observation of r to a
// Provenance: recorded when it was ingested, its device as the author (when
// the record names one), the provider integration it came through as the
// assembler, and the provider's own record (each external id) as the entity
// it derives from.
func FromRecordProvenance(user uuid.UUID, r read.ObservationRecord) Provenance {
	o := FromRecord(user, r)
	d := RecordDevice(r)
	p := Provenance{
		ResourceType: "Provenance",
		ID:           ProvenanceID(o.ID),
		Target:       []Reference{{Reference: "Observation/" + o.ID}},
		Recorded:     o.Issued,
	}
	if ref := deviceRef(user, d); ref != nil {
		p.Agent = append(p.Agent, ProvenanceAgent{Type: participant("author", "Author"), Who: *ref})
	}
	p.Agent = append(p.Agent, ProvenanceAgent{
		Type: participant("assembler", "Assembler"),
		Who:  Reference{Identifier: &Identifier{System: ProviderSystem, Value: d.Provider}, Display: d.Provider},
	})
	for _, id := range o.Identifier {
		p.Entity = append(p.Entity, ProvenanceEntity{Role: "derivation", What: Reference{Identifier: &id}})
	}
	return p
}

// DeviceSet is the searchset Bundle of the user's devices: one page, no next
// link (a user has a handful of devices).
func DeviceSet(user uuid.UUID, base, self string, devices []read.Device) SearchBundle[Device] {
	b := newSearchSet[Device](self, "")
	for _, d := range devices {
		dev := FromDevice(user, d)
		b.add(base+"/Device/"+dev.ID, dev, ModeMatch)
	}
	return b
}

// ProvenanceSet is the searchset Bundle of the given Provenance resources.
func ProvenanceSet(base, self string, provs []Provenance) SearchBundle[Provenance] {
	b := newSearchSet[Provenance](self, "")
	for _, p := range provs {
		b.add(base+"/Provenance/"+p.ID, p, ModeMatch)
	}
	return b
}

// Everything is one page of a Patient's $everything: the Patient (on the
// first page only, patient nil on the others) and the page's Observations as
// matches, then, as includes, the Devices those Observations reference (each
// once per page) and the Provenance of each Observation. The page is the
// Observations' page: next is empty on the last one.
func Everything(user uuid.UUID, base, self, next string, patient *Patient, recs []read.ObservationRecord) SearchBundle[any] {
	b := newSearchSet[any](self, next)
	if patient != nil {
		b.add(base+"/Patient/"+patient.ID, *patient, ModeMatch)
	}
	var devices []Device
	seen := map[string]bool{}
	var provs []Provenance
	for _, r := range recs {
		o := FromRecord(user, r)
		b.add(base+"/Observation/"+o.ID, o, ModeMatch)
		if d := RecordDevice(r); d.Known() {
			if dev := FromDevice(user, d); !seen[dev.ID] {
				seen[dev.ID] = true
				devices = append(devices, dev)
			}
		}
		provs = append(provs, FromRecordProvenance(user, r))
	}
	for _, d := range devices {
		b.add(base+"/Device/"+d.ID, d, ModeInclude)
	}
	for _, p := range provs {
		b.add(base+"/Provenance/"+p.ID, p, ModeInclude)
	}
	return b
}
