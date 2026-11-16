// SPDX-License-Identifier: AGPL-3.0-only

package labs

import (
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// TypeConcept is the provenance of every lab result.
const TypeConcept = "ehr"

// Metadata keys of a stored lab result (inside the sealed metadata column).
const (
	MetaSourceCode         = "source_code"
	MetaSourceSystem       = "source_system"
	MetaDisplay            = "display"
	MetaComparator         = "comparator"
	MetaValueSystem        = "value_system"
	MetaNormalizedUnit     = "normalized_unit"
	MetaReferenceLow       = "reference_low"
	MetaReferenceHigh      = "reference_high"
	MetaReferenceText      = "reference_text"
	MetaInterpretation     = "interpretation"
	MetaInterpretationText = "interpretation_text"
	MetaCritical           = "critical"
	MetaStatus             = "status"
	MetaSpecimen           = "specimen"
	MetaIssuedAt           = "issued_at"
	MetaAbsent             = "absent"
	MetaIssuedAsCollected  = "issued_as_collected"
	MetaReportCode         = "report_code"
	MetaReportLOINC        = "report_loinc"
	MetaReportSystem       = "report_system"
	MetaReportDisplay      = "report_display"
	MetaReportStatus       = "report_status"
	MetaReportIssuedAt     = "report_issued_at"
	MetaReportCollectedAt  = "report_collected_at"
	MetaOrderer            = "orderer"
)

// ContentHash is the identity of a stored lab result: its owner and its
// source id. The collection time and the value are left out, so an amended
// copy of the result is the same record.
func ContentHash(user, externalID string) string {
	return model.ComputeContentHash(map[string]string{
		"user_id":     user,
		"provider":    Slug,
		"kind":        "lab_result",
		"external_id": externalID,
	})
}

// Observations maps checked reports to one observation per result.
func Observations(user string, reports []Report) []model.Observation {
	var out []model.Observation
	for _, rep := range reports {
		for _, r := range rep.Results {
			out = append(out, observation(user, rep, r))
		}
	}
	return out
}

func observation(user string, rep Report, r Result) model.Observation {
	meta := map[string]any{MetaStatus: r.Status}
	put := func(k, v string) {
		if v != "" {
			meta[k] = v
		}
	}
	putNum := func(k string, v *float64) {
		if v != nil {
			meta[k] = *v
		}
	}
	putTime := func(k string, t *time.Time) {
		if t != nil {
			meta[k] = t.UTC().Format(time.RFC3339Nano)
		}
	}
	put(MetaSourceCode, r.Code)
	put(MetaSourceSystem, r.System)
	put(MetaDisplay, r.Display)
	put(MetaComparator, r.Comparator)
	put(MetaValueSystem, r.ValueSystem)
	putNum(MetaReferenceLow, r.ReferenceLow)
	putNum(MetaReferenceHigh, r.ReferenceHigh)
	put(MetaReferenceText, r.ReferenceText)
	put(MetaInterpretation, r.Interpretation)
	put(MetaInterpretationText, r.InterpretationText)
	if Critical(r.Interpretation) {
		meta[MetaCritical] = true
	}
	put(MetaSpecimen, r.Specimen)
	putTime(MetaIssuedAt, r.IssuedAt)
	put(MetaAbsent, r.Absent)
	if r.IssuedAsCollected {
		meta[MetaIssuedAsCollected] = true
	}
	put(MetaReportCode, rep.Code)
	put(MetaReportLOINC, rep.LOINC)
	put(MetaReportSystem, rep.System)
	put(MetaReportDisplay, rep.Display)
	if rep.ID != "" {
		put(MetaReportStatus, rep.Status)
	}
	putTime(MetaReportIssuedAt, rep.IssuedAt)
	putTime(MetaReportCollectedAt, rep.CollectedAt)
	put(MetaOrderer, rep.Orderer)

	o := model.Observation{
		UserID:          user,
		TS:              r.CollectedAt.UTC(),
		CanonicalCode:   CanonicalCode(r),
		ValueNum:        r.Value,
		ValueCode:       r.ValueCode,
		ValueText:       r.ValueString,
		UnitUCUM:        r.Unit,
		Provider:        Slug,
		SourceName:      r.Performer,
		IsPrimarySource: true,
		TypeConcept:     TypeConcept,
		SourceValue:     r.Code,
		Metadata:        meta,
		ExternalID:      r.ID,
		ReportID:        rep.ID,
		Replaceable:     true,
		ContentHash:     ContentHash(user, r.ID),
	}
	if o.SourceValue == "" {
		o.SourceValue = r.LOINC
	}
	if r.CollectedEnd != nil {
		o.EndTS = r.CollectedEnd.UTC()
	}
	if r.Value != nil && r.LOINC != "" {
		if v, unit, ok := Normalize(r.LOINC, *r.Value, r.Unit); ok {
			o.ValueCanonical = &v
			meta[MetaNormalizedUnit] = unit
		}
	}
	return o
}
