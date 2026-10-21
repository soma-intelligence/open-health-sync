// SPDX-License-Identifier: AGPL-3.0-only

// Command gendocs writes docs/data-model.md from the canonical model: every
// exported struct of internal/model with its fields, and every quantity
// name with its storage unit. The file is generated so it cannot drift; a test
// fails when the committed copy differs from this output.
//
// Run it from the repository root:
//
//	go run ./internal/model/cmd/gendocs
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// tables names the ohs_data table each canonical type is stored in. A type
// absent from the map has no table of its own.
var tables = map[string]string{
	"QuantitySample":   "quantity_samples",
	"CategorySample":   "category_samples",
	"SleepSession":     "sleep_sessions",
	"SleepPhase":       "sleep_phases",
	"Workout":          "workouts",
	"RoutePoint":       "route_points",
	"NutritionLog":     "nutrition_logs",
	"ECGRecording":     "ecg_recordings",
	"ECGVoltageSample": "ecg_samples",
	"HeartbeatSample":  "heartbeat_samples",
	"DailySummary":     "daily_summaries",
	"Observation":      "observations",
}

// notServed names the stored types that no REST route or MCP tool reads yet.
var notServed = map[string]string{
	"Observation": "No connector writes it yet, and no read route serves it (planned).",
}

// notes explain the fields whose meaning is not obvious from the name. The key
// is Type.Field, or Field alone for a name shared by several types.
var notes = map[string]string{
	"TenantID":                           "Owning tenant. Set by the pipeline.",
	"UserID":                             "Owning end user. Set by the pipeline.",
	"ContentHash":                        "De-duplication identity of the source record. Part of the unique key with the time column.",
	"Metadata":                           "Provider fields kept as sent, plus `timezone` (IANA name) when the provider gives one.",
	"QuantitySample.Provider":            "The integration the sample came through (`apple_health`, `withings`).",
	"QuantitySample.SourceSlug":          "The vendor that produced the sample. Equals `Provider` for a direct integration.",
	"QuantitySample.QuantityType":        "Canonical name from the table below, for example `heart_rate`.",
	"QuantitySample.Unit":                "Storage unit of the quantity name, after conversion.",
	"QuantitySample.ProductType":         "Exact hardware model that ran the writing app, for example `Watch7,2`.",
	"QuantitySample.DeviceModel":         "Model of the device that took the reading.",
	"QuantitySample.WasUserEntered":      "Null when the source does not say.",
	"QuantitySample.Replaceable":         "Writer instruction, not a column: a later value for a still open window replaces the stored one.",
	"QuantitySample.SupersedeMoved":      "Writer instruction, not a column: the external id names one figure of one record, so a stored copy under another start is deleted.",
	"DailySummary.SummaryDate":           "The user's local calendar day.",
	"DailySummary.Replaceable":           "Writer instruction, not a column: the latest summary for the day wins.",
	"DailySummary.RecordID":              "Writer instruction, not a column: the provider record whose figures the summary carries; not written once that record is deleted.",
	"NutritionLog.Replaceable":           "Writer instruction, not a column.",
	"NutritionLog.SupersedeMoved":        "Writer instruction, not a column: a stored copy of the log under another start is deleted.",
	"Workout.Replaceable":                "Writer instruction, not a column: an edited workout replaces the stored figures.",
	"Workout.SourceSlug":                 "The vendor that recorded the workout. Empty means the provider; the column is never null.",
	"Workout.DeviceID":                   "The device that recorded the workout (`Garmin edge_530`). Null when the provider does not say.",
	"SleepSession.Replaceable":           "Writer instruction, not a column: a re-scored session replaces the stored figures and phases.",
	"NutritionLog.NutrientsJSON":         "Extended nutrients by name.",
	"SleepPhase.Phase":                   "`deep`, `rem`, `light`, `awake`, `in_bed` or `asleep_core`, after label normalization.",
	"SleepPhase.ParentContentHash":       "Content hash of the parent session. Empty for an orphan phase.",
	"RoutePoint.ParentContentHash":       "Content hash of the parent workout. Empty for an orphan point.",
	"ECGVoltageSample.VoltageUV":         "Microvolts, converted from volts.",
	"ECGVoltageSample.ParentContentHash": "Content hash of the parent recording. Empty for an orphan sample.",
	"HeartbeatSample.RRMS":               "RR interval in milliseconds.",
	"Observation.CanonicalCode":          "Controlled code, not free text.",
	"Observation.UnitUCUM":               "UCUM unit of the value.",
	"Observation.TypeConcept":            "`device`, `survey`, `ehr` or `derived`.",
	"MapResult.HighFrequency":            "The result holds high volume data and is written with COPY.",
	"MapResult.SourceKey":                "Raw object the batch was mapped from. Child records use it to find a parent that arrived in another object.",
	"MapResult.Deletions":                "Provider records the provider deleted: their stored rows are removed and a tombstone in `ohs_data.deleted_records` keeps them from being written again.",
}

const header = `# Data model

This file is generated by ` + "`go run ./internal/model/cmd/gendocs`" + ` from
` + "`internal/model`" + `. Do not edit it by hand: a test fails when it differs from
the generator output.

Every connector maps what a provider sends into the records below. The ETL then
converts units, validates, de-duplicates and writes them to the ` + "`ohs_data`" + `
schema. Every stored row carries ` + "`tenant_id`" + ` and ` + "`user_id`" + `; the two are
set by the pipeline and are never part of a content hash. Times are UTC. A field
typed as a pointer is optional and reads as null in the API when absent.

Field names below are the Go names used inside the pipeline, not the wire names:
the JSON the API returns uses snake_case names, and some differ (for example
` + "`SummaryDate`" + ` is ` + "`date`" + `). Use [../api/openapi.yaml](../api/openapi.yaml) for the JSON shapes.

The read API returns these records with the provenance fields ` + "`provider`" + `,
` + "`source`" + `, ` + "`device`" + ` and ` + "`ingested_at`" + `; see [api.md](api.md). ECG
recordings are served with their voltage samples by ` + "`/ecg`" + `, heartbeat (RR
interval) samples by ` + "`/rr-intervals`" + ` and ` + "`/hrv`" + `.

## Canonical types
`

const quantityIntro = `
## Quantity names and storage units

A quantity sample is stored in the unit below. The ETL converts a known source
unit into it (pounds to kilograms, Fahrenheit to Celsius, mmol/L to mg/dL and so
on) and parks a sample whose unit it cannot convert. A quantity name that is not
in this table is stored as the provider sent it, unit included. Percentages are
stored as 0 to 100, not as fractions.

| Quantity name | Storage unit |
|---|---|
`

func main() {
	modelDir := flag.String("model", "internal/model", "directory of the model package")
	out := flag.String("o", "docs/data-model.md", "output file, or - for standard output")
	flag.Parse()

	doc, err := Generate(*modelDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
	if *out == "-" {
		_, err = os.Stdout.Write(doc)
	} else {
		err = os.WriteFile(*out, doc, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}

// Generate renders the data model document from the model package in modelDir.
func Generate(modelDir string) ([]byte, error) {
	fset := token.NewFileSet()
	types, err := structTypes(fset, modelDir)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(header)
	for _, t := range types {
		if !isRecord(t) {
			continue
		}
		if err := writeType(&b, fset, t.name, t.doc, t.st); err != nil {
			return nil, err
		}
	}
	b.WriteString(quantityIntro)
	units := model.StorageUnits()
	names := make([]string, 0, len(units))
	for n := range units {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "| `%s` | `%s` |\n", n, units[n])
	}
	return b.Bytes(), nil
}

type structType struct {
	name string
	doc  *ast.CommentGroup
	st   *ast.StructType
}

// structTypes returns the exported struct types of every non-test Go file of
// dir, files in name order, declarations in source order.
func structTypes(fset *token.FileSet, dir string) ([]structType, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []structType
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				st, ok := ts.Type.(*ast.StructType)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				doc := gd.Doc
				if ts.Doc != nil {
					doc = ts.Doc
				}
				out = append(out, structType{ts.Name.Name, doc, st})
			}
		}
	}
	return out, nil
}

// isRecord reports whether t is a canonical record: a struct that carries a
// TenantID (every stored record does) or MapResult, the mapper's output.
// Helper structs of the package, such as UnitError, are left out.
func isRecord(t structType) bool {
	if t.name == "MapResult" {
		return true
	}
	for _, f := range t.st.Fields.List {
		for _, n := range f.Names {
			if n.Name == "TenantID" {
				return true
			}
		}
	}
	return false
}

func writeType(w io.Writer, fset *token.FileSet, name string, doc *ast.CommentGroup, st *ast.StructType) error {
	fmt.Fprintf(w, "\n### %s\n\n", name)
	if s := firstSentence(doc, name); s != "" {
		fmt.Fprintf(w, "%s\n\n", s)
	}
	if t, ok := tables[name]; ok {
		if note, unserved := notServed[name]; unserved {
			fmt.Fprintf(w, "Stored in `ohs_data.%s`. %s\n\n", t, note)
		} else {
			fmt.Fprintf(w, "Stored in `ohs_data.%s`.\n\n", t)
		}
	}
	fmt.Fprintln(w, "| Field | Type | Note |")
	fmt.Fprintln(w, "|---|---|---|")
	for _, f := range st.Fields.List {
		typ, err := exprString(fset, f.Type)
		if err != nil {
			return err
		}
		for _, n := range f.Names {
			note, ok := notes[name+"."+n.Name]
			if !ok {
				note = notes[n.Name]
			}
			fmt.Fprintf(w, "| `%s` | `%s` | %s |\n", n.Name, typ, note)
		}
	}
	return nil
}

func exprString(fset *token.FileSet, e ast.Expr) (string, error) {
	var b bytes.Buffer
	if err := printer.Fprint(&b, fset, e); err != nil {
		return "", err
	}
	return b.String(), nil
}

// firstSentence returns the first sentence of a doc comment, with the
// leading type name kept ("QuantitySample represents ..." reads fine).
func firstSentence(doc *ast.CommentGroup, name string) string {
	if doc == nil {
		return ""
	}
	text := strings.Join(strings.Fields(doc.Text()), " ")
	if !strings.HasPrefix(text, name+" ") {
		return ""
	}
	if i := strings.Index(text, ". "); i >= 0 {
		return text[:i+1]
	}
	return text
}
