// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/labs"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// Lab results (docs/labs.md) are rows of ohs_data.observations whose
// canonical code starts with "lab:". The range below keeps them with the
// btree index on canonical_code: every code from "lab:" up to "lab;" (the
// character after ':').
const labCond = "canonical_code >= 'lab:' AND canonical_code < 'lab;'"

// labCondOn is labCond on alias a.
func labCondOn(a string) string {
	return a + ".canonical_code >= 'lab:' AND " + a + ".canonical_code < 'lab;'"
}

// LabResult is one lab result. Its fields are flat, so the CSV and Parquet
// exports carry each one as a column.
type LabResult struct {
	ID int64 `json:"id"`
	// ReportID is the source's id of its report; nil for a result sent alone.
	ReportID *string `json:"report_id"`
	// Code is the stored code: lab:loinc:<code> or lab:local:<code>.
	Code         string  `json:"code"`
	LOINC        *string `json:"loinc"`
	SourceCode   *string `json:"source_code"`
	SourceSystem *string `json:"source_system"`
	Display      *string `json:"display"`
	// The value as sent: a number with its unit, a string or a code.
	Value       *float64 `json:"value"`
	Unit        *string  `json:"unit"`
	Comparator  *string  `json:"comparator"`
	ValueString *string  `json:"value_string"`
	ValueCode   *string  `json:"value_code"`
	ValueSystem *string  `json:"value_system"`
	// NormalizedValue is the value in NormalizedUnit, set only for the
	// analytes and units of the normalization table.
	NormalizedValue    *float64   `json:"normalized_value"`
	NormalizedUnit     *string    `json:"normalized_unit"`
	ReferenceLow       *float64   `json:"reference_low"`
	ReferenceHigh      *float64   `json:"reference_high"`
	ReferenceText      *string    `json:"reference_text"`
	Interpretation     *string    `json:"interpretation"`
	InterpretationText *string    `json:"interpretation_text"`
	Critical           bool       `json:"critical"`
	Status             string     `json:"status"`
	Specimen           *string    `json:"specimen"`
	CollectedAt        time.Time  `json:"collected_at"`
	CollectedEnd       *time.Time `json:"collected_end"`
	IssuedAt           *time.Time `json:"issued_at"`
	// IssuedAsCollected says the source gave no collection time:
	// CollectedAt is the issued time.
	IssuedAsCollected bool    `json:"issued_as_collected"`
	Absent            *string `json:"data_absent_reason"`
	Performer         *string `json:"performer"`
	Orderer           *string `json:"orderer"`
	ExternalID        string  `json:"external_id"`
	Provenance

	report     labReportMeta
	capturedAt *time.Time
}

// labReportMeta is what a result row carries of its report.
type labReportMeta struct {
	LOINC, Code, System, Display, Status *string
	IssuedAt, CollectedAt                *time.Time
}

// LabReport is one lab report with its results. Its fields come from its
// latest stored result (the one from the latest upload).
type LabReport struct {
	ID          string      `json:"id"`
	LOINC       *string     `json:"loinc"`
	Code        *string     `json:"code"`
	System      *string     `json:"system"`
	Display     *string     `json:"display"`
	Status      *string     `json:"status"`
	CollectedAt time.Time   `json:"collected_at"`
	IssuedAt    *time.Time  `json:"issued_at"`
	Performer   *string     `json:"performer"`
	Orderer     *string     `json:"orderer"`
	Results     []LabResult `json:"results"`
}

const labCols = `id, ts, end_ts, canonical_code, value_num, value_code, value_text, unit_ucum, value_canonical,
	source_name, external_id, report_id, metadata, provider, source_slug, device_id, ingested_at, captured_at`

var labSel = vault.Select("observations", labCols)

// labRow holds the scanned columns of one result.
type labRow struct {
	x        LabResult
	end      *time.Time
	code     string
	text     *string
	external *string
	meta     json.RawMessage
}

func (r *labRow) dest() []any {
	return []any{&r.x.ID, &r.x.CollectedAt, &r.end, &r.code, &r.x.Value, &r.x.ValueCode, &r.text, &r.x.Unit,
		&r.x.NormalizedValue, &r.x.Performer, &r.external, &r.x.ReportID, &r.meta,
		&r.x.Provider, &r.x.Source, &r.x.Device, &r.x.IngestedAt, &r.x.capturedAt}
}

// finish fills the result from the scanned columns and its metadata.
func (r *labRow) finish() (LabResult, error) {
	x := r.x
	x.Code = r.code
	for _, p := range []**string{&r.text, &x.ValueCode, &x.Unit, &x.Performer, &x.Source, &x.Device} {
		if *p != nil && **p == "" {
			*p = nil
		}
	}
	x.ValueString = r.text
	x.CollectedEnd = r.end
	if r.external != nil {
		x.ExternalID = *r.external
	}
	if strings.HasPrefix(r.code, labs.LOINCPrefix) {
		l := strings.TrimPrefix(r.code, labs.LOINCPrefix)
		x.LOINC = &l
	}
	var md map[string]any
	if len(r.meta) > 0 {
		if err := json.Unmarshal(r.meta, &md); err != nil {
			return LabResult{}, fmt.Errorf("lab result metadata: %w", err)
		}
	}
	str := func(k string) *string {
		if s, ok := md[k].(string); ok && s != "" {
			return &s
		}
		return nil
	}
	num := func(k string) *float64 {
		if f, ok := md[k].(float64); ok {
			return &f
		}
		return nil
	}
	tm := func(k string) *time.Time {
		if s, ok := md[k].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				t = t.UTC()
				return &t
			}
		}
		return nil
	}
	flag := func(k string) bool { b, _ := md[k].(bool); return b }
	x.SourceCode = str(labs.MetaSourceCode)
	x.SourceSystem = str(labs.MetaSourceSystem)
	x.Display = str(labs.MetaDisplay)
	x.Comparator = str(labs.MetaComparator)
	x.ValueSystem = str(labs.MetaValueSystem)
	x.NormalizedUnit = str(labs.MetaNormalizedUnit)
	if x.NormalizedUnit == nil {
		x.NormalizedValue = nil
	}
	x.ReferenceLow = num(labs.MetaReferenceLow)
	x.ReferenceHigh = num(labs.MetaReferenceHigh)
	x.ReferenceText = str(labs.MetaReferenceText)
	x.Interpretation = str(labs.MetaInterpretation)
	x.InterpretationText = str(labs.MetaInterpretationText)
	x.Critical = flag(labs.MetaCritical)
	if s := str(labs.MetaStatus); s != nil {
		x.Status = *s
	}
	x.Specimen = str(labs.MetaSpecimen)
	x.IssuedAt = tm(labs.MetaIssuedAt)
	x.IssuedAsCollected = flag(labs.MetaIssuedAsCollected)
	x.Absent = str(labs.MetaAbsent)
	x.Orderer = str(labs.MetaOrderer)
	x.report = labReportMeta{
		LOINC: str(labs.MetaReportLOINC), Code: str(labs.MetaReportCode), System: str(labs.MetaReportSystem),
		Display: str(labs.MetaReportDisplay), Status: str(labs.MetaReportStatus),
		IssuedAt: tm(labs.MetaReportIssuedAt), CollectedAt: tm(labs.MetaReportCollectedAt),
	}
	utc(&x.CollectedAt)
	utc(x.CollectedEnd)
	utc(&x.IngestedAt)
	return x, nil
}

// scanLab scans one row selected with labSel.
func scanLab(ctx context.Context, keys *vault.Reader, r pgx.Rows) (LabResult, error) {
	var row labRow
	if err := labSel.Scan(ctx, keys, r, row.dest()); err != nil {
		return LabResult{}, err
	}
	return row.finish()
}

// LabFilter narrows a list of lab results.
type LabFilter struct {
	// Code keeps the results of this LOINC code or source code.
	Code string
	// Report keeps the results of this report id.
	Report string
}

// Labs lists the user's lab results, ordered by collection time then id.
func Labs(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query, f LabFilter) (Page[LabResult], error) {
	n, err := q.normalize(false)
	if err != nil {
		return Page[LabResult]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[LabResult]{}, err
	}
	l := list{table: "ohs_data.observations", timeCol: "ts", cols: labSel.List(""),
		where: func(arg func(any) string, _ normalized) []string {
			w := []string{labCond}
			if f.Code != "" {
				w = append(w, "canonical_code = ANY("+arg(labs.CodesFor(f.Code))+"::text[])")
			}
			if f.Report != "" {
				w = append(w, "report_id = "+arg(f.Report))
			}
			return w
		}}
	keys := keysFor(ctx, tenantID, userID)
	return runOnce(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (LabResult, time.Time, int64, error) {
		x, err := scanLab(ctx, keys, r)
		return x, x.CollectedAt, x.ID, err
	})
}

// AllLabs lists every lab result of the user (the export and the archive).
func AllLabs(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[LabResult], error) {
	return Labs(ctx, db, tenantID, userID, q, LabFilter{})
}

// labReportResults is the most results a report read returns.
const labReportResults = labs.MaxResultsPerReport * 2

// LabReportByID reads one report of the user with its results, ErrNotFound
// when it has none.
func LabReportByID(ctx context.Context, db Querier, tenantID, userID uuid.UUID, reportID string) (LabReport, error) {
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return LabReport{}, err
	}
	reps, err := labReports(ctx, db, tenantID, userID, []string{reportID})
	if err != nil {
		return LabReport{}, err
	}
	if len(reps) == 0 {
		return LabReport{}, ErrNotFound
	}
	return reps[0], nil
}

// LabReportsByIDs reads the user's reports of ids with their results, in
// the order of ids; an id with no result is left out.
func LabReportsByIDs(ctx context.Context, db Querier, tenantID, userID uuid.UUID, ids []string) ([]LabReport, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return labReports(ctx, db, tenantID, userID, ids)
}

// labReports reads reports by id, in the order of ids; an id with no result
// is left out.
func labReports(ctx context.Context, db Querier, tenantID, userID uuid.UUID, ids []string) ([]LabReport, error) {
	rows, err := db.Query(ctx, `SELECT `+labSel.List("o")+` FROM ohs_data.observations o
		WHERE o.tenant_id = $1 AND o.user_id = $2 AND `+labCondOn("o")+` AND o.report_id = ANY($3::text[])
		ORDER BY o.ts, o.id LIMIT $4`, tenantID, userID, ids, labReportResults*len(ids))
	if err != nil {
		return nil, fmt.Errorf("read: lab reports: %w", err)
	}
	defer rows.Close()
	keys := keysFor(ctx, tenantID, userID)
	byID := map[string]*LabReport{}
	latest := map[string]*LabResult{}
	for rows.Next() {
		x, err := scanLab(ctx, keys, rows)
		if err != nil {
			return nil, fmt.Errorf("read: lab reports: scan: %w", err)
		}
		if x.ReportID == nil {
			continue
		}
		id := *x.ReportID
		rep := byID[id]
		if rep == nil {
			rep = &LabReport{ID: id, Results: []LabResult{}}
			byID[id] = rep
		}
		rep.Results = append(rep.Results, x)
		if l := latest[id]; l == nil || laterCapture(x, *l) {
			c := x
			latest[id] = &c
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: lab reports: %w", err)
	}
	out := make([]LabReport, 0, len(byID))
	for _, id := range ids {
		rep := byID[id]
		if rep == nil {
			continue
		}
		l := latest[id]
		rep.LOINC, rep.Code, rep.System, rep.Display, rep.Status = l.report.LOINC, l.report.Code, l.report.System, l.report.Display, l.report.Status
		rep.IssuedAt, rep.Orderer = l.report.IssuedAt, l.Orderer
		rep.CollectedAt = rep.Results[0].CollectedAt
		if l.report.CollectedAt != nil {
			rep.CollectedAt = *l.report.CollectedAt
		}
		rep.Performer = l.Performer
		out = append(out, *rep)
	}
	return out, nil
}

func laterCapture(a, b LabResult) bool {
	switch {
	case a.capturedAt == nil:
		return false
	case b.capturedAt == nil:
		return true
	}
	return a.capturedAt.After(*b.capturedAt)
}

// LabReports lists the user's reports with their results, ordered by the
// collection time of their first result, then report id. Results sent
// without a report are not in any.
func LabReports(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[LabReport], error) {
	cursor := q.Cursor
	q.Cursor = ""
	n, err := q.normalize(false)
	if err != nil {
		return Page[LabReport]{}, err
	}
	var afterT time.Time
	var afterID string
	if cursor != "" {
		if afterT, afterID, err = decodeReportCursor(cursor); err != nil {
			return Page[LabReport]{}, err
		}
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[LabReport]{}, err
	}
	args := []any{tenantID, userID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	where := []string{"tenant_id = $1", "user_id = $2", labCond, "report_id IS NOT NULL"}
	if !n.Start.IsZero() {
		where = append(where, "ts >= "+arg(n.Start))
	}
	if !n.End.IsZero() {
		where = append(where, "ts < "+arg(n.End))
	}
	having := ""
	if cursor != "" {
		having = " HAVING (min(ts), report_id) > (" + arg(afterT) + "::timestamptz, " + arg(afterID) + "::text)"
	}
	rows, err := db.Query(ctx, `SELECT report_id, min(ts) FROM ohs_data.observations WHERE `+strings.Join(where, " AND ")+
		` GROUP BY report_id`+having+` ORDER BY 2, 1 LIMIT `+arg(n.Limit+1), args...)
	if err != nil {
		return Page[LabReport]{}, fmt.Errorf("read: lab reports: %w", err)
	}
	type key struct {
		id string
		t  time.Time
	}
	var ks []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.id, &k.t); err != nil {
			rows.Close()
			return Page[LabReport]{}, fmt.Errorf("read: lab reports: scan: %w", err)
		}
		ks = append(ks, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Page[LabReport]{}, fmt.Errorf("read: lab reports: %w", err)
	}
	out := Page[LabReport]{Records: []LabReport{}}
	if len(ks) > n.Limit {
		last := ks[n.Limit-1]
		out.NextCursor = encodeReportCursor(last.t, last.id)
		ks = ks[:n.Limit]
	}
	if len(ks) == 0 {
		return out, nil
	}
	ids := make([]string, len(ks))
	for i, k := range ks {
		ids[i] = k.id
	}
	reps, err := labReports(ctx, db, tenantID, userID, ids)
	if err != nil {
		return Page[LabReport]{}, err
	}
	out.Records = reps
	return out, nil
}

// LabReportIDByHash finds the report whose id hashes to hash (the FHIR
// DiagnosticReport id: the first 40 hex digits of the SHA-256 of the report
// id), ErrNotFound when the user has none.
func LabReportIDByHash(ctx context.Context, db Querier, tenantID, userID uuid.UUID, hash string) (string, error) {
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return "", err
	}
	var id string
	err := db.QueryRow(ctx, `SELECT report_id FROM ohs_data.observations
		WHERE tenant_id = $1 AND user_id = $2 AND `+labCond+` AND report_id IS NOT NULL
		  AND substr(encode(sha256(convert_to(report_id, 'UTF8')), 'hex'), 1, 40) = $3
		LIMIT 1`, tenantID, userID, hash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read: lab report: %w", err)
	}
	return id, nil
}

func encodeReportCursor(t time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeReportCursor(c string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	t, err := time.Parse(time.RFC3339Nano, ts)
	if !ok || err != nil || id == "" {
		return time.Time{}, "", ErrInvalidCursor
	}
	return t, id, nil
}

// CheckLabReportQuery reports a mistake in q for LabReports without
// running it.
func CheckLabReportQuery(q Query) error {
	c := q.Cursor
	q.Cursor = ""
	if _, err := q.normalize(false); err != nil {
		return err
	}
	if c != "" {
		if _, _, err := decodeReportCursor(c); err != nil {
			return err
		}
	}
	return nil
}
