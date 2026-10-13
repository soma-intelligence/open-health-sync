// SPDX-License-Identifier: AGPL-3.0-only

package fhir

// CapabilityDate is the date the CapabilityStatement last changed: change it
// with the statement.
const CapabilityDate = "2026-10-13"

// CapabilityStatement is a FHIR R4 CapabilityStatement, limited to the
// elements the statement fills. Members are in the order of the
// specification.
type CapabilityStatement struct {
	ResourceType   string           `json:"resourceType"`
	Status         string           `json:"status"`
	Date           string           `json:"date"`
	Kind           string           `json:"kind"`
	Implementation CapabilityImpl   `json:"implementation"`
	FHIRVersion    string           `json:"fhirVersion"`
	Format         []string         `json:"format"`
	Rest           []CapabilityRest `json:"rest"`
}

// CapabilityImpl is CapabilityStatement.implementation.
type CapabilityImpl struct {
	Description string `json:"description"`
	URL         string `json:"url"`
}

// CapabilityRest is CapabilityStatement.rest.
type CapabilityRest struct {
	Mode     string               `json:"mode"`
	Security *CapabilitySecurity  `json:"security,omitempty"`
	Resource []CapabilityResource `json:"resource"`
}

// CapabilitySecurity is CapabilityStatement.rest.security.
type CapabilitySecurity struct {
	Description string `json:"description"`
}

// CapabilityResource is CapabilityStatement.rest.resource.
type CapabilityResource struct {
	Type        string                `json:"type"`
	Interaction []CapabilityCode      `json:"interaction"`
	SearchParam []CapabilityParam     `json:"searchParam,omitempty"`
	Operation   []CapabilityOperation `json:"operation,omitempty"`
}

// CapabilityCode is one interaction of a resource.
type CapabilityCode struct {
	Code string `json:"code"`
}

// CapabilityParam is one search parameter of a resource.
type CapabilityParam struct {
	Name          string `json:"name"`
	Definition    string `json:"definition,omitempty"`
	Type          string `json:"type"`
	Documentation string `json:"documentation,omitempty"`
}

// CapabilityOperation is one operation of a resource.
type CapabilityOperation struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

var (
	readOnly     = []CapabilityCode{{Code: "read"}}
	readSearch   = []CapabilityCode{{Code: "read"}, {Code: "search-type"}}
	windowParams = []CapabilityParam{
		{Name: "start", Type: "date", Documentation: "start <= the record's time (RFC 3339 or YYYY-MM-DD)"},
		{Name: "end", Type: "date", Documentation: "the record's time < end"},
	}
	pageParams = []CapabilityParam{
		{Name: "limit", Type: "number", Documentation: "page size, 1 to 1000 (default 100)"},
		{Name: "cursor", Type: "string", Documentation: "the cursor of the next link"},
	}
)

// Capability is the CapabilityStatement of the FHIR base base: what the
// routes of docs/fhir.md serve, read only.
func Capability(base string) CapabilityStatement {
	obsParams := append([]CapabilityParam{
		{Name: "code", Definition: "http://hl7.org/fhir/SearchParameter/clinical-code", Type: "token",
			Documentation: "one system|code token: a LOINC code, a quantity type or a record type"},
		{Name: "category", Definition: "http://hl7.org/fhir/SearchParameter/Observation-category", Type: "token",
			Documentation: "one observation category code"},
		{Name: "type", Type: "string", Documentation: "one canonical quantity type, for example heart_rate"},
	}, append(append([]CapabilityParam{}, windowParams...), pageParams...)...)
	return CapabilityStatement{
		ResourceType:   "CapabilityStatement",
		Status:         "active",
		Date:           CapabilityDate,
		Kind:           "instance",
		Implementation: CapabilityImpl{Description: "Open Health Sync, the records of one user", URL: base},
		FHIRVersion:    "4.0.1",
		Format:         []string{"application/fhir+json"},
		Rest: []CapabilityRest{{
			Mode:     "server",
			Security: &CapabilitySecurity{Description: "A tenant API key as a bearer token: Authorization: Bearer ohs_sk_..."},
			Resource: []CapabilityResource{
				{Type: "Patient", Interaction: readOnly, Operation: []CapabilityOperation{{
					Name: "everything", Definition: "http://hl7.org/fhir/OperationDefinition/Patient-everything",
				}}},
				{Type: "Observation", Interaction: readSearch, SearchParam: obsParams},
				{Type: "Device", Interaction: readSearch, SearchParam: windowParams},
				{Type: "Provenance", Interaction: readSearch, SearchParam: []CapabilityParam{{
					Name: "target", Definition: "http://hl7.org/fhir/SearchParameter/Provenance-target", Type: "reference",
					Documentation: "required: Observation/{id}",
				}}},
			},
		}},
	}
}
