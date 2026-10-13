// SPDX-License-Identifier: AGPL-3.0-only

package sdkgen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const root = "../.."

// TestCommittedFilesAreCurrent fails when a generated file in the repository
// is not what the generator writes from api/openapi.yaml: the SDKs and the
// collection drifted from the document. Fix it with: make sdk
func TestCommittedFilesAreCurrent(t *testing.T) {
	files, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	// Write into a temporary tree the way the command does, then compare.
	tmp := t.TempDir()
	for p, content := range files {
		full := filepath.Join(tmp, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for p := range files {
		want, err := os.ReadFile(filepath.Join(tmp, p))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Errorf("%s: %v (run `make sdk`)", p, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is out of date: run `make sdk`", p)
		}
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		b, err := Generate(root)
		if err != nil {
			t.Fatal(err)
		}
		for p := range a {
			if !bytes.Equal(a[p], b[p]) {
				t.Fatalf("%s differs between two runs", p)
			}
		}
	}
}

// TestEveryOperationIsGenerated checks that each operation of the document is
// a method of both SDKs and a request of the collection, unless it is listed
// in skipped, and that skipped names real operations.
func TestEveryOperationIsGenerated(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join(root, SpecPath))
	if err != nil {
		t.Fatal(err)
	}
	files, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	ts, py := string(files[TSClientPath]), string(files[PyClientPath])
	var collection pmCollection
	if err := json.Unmarshal(files[PostmanPath], &collection); err != nil {
		t.Fatal(err)
	}
	requests := map[string]bool{}
	for _, f := range collection.Item {
		for _, it := range f.Item {
			requests[it.Request.Method+" "+strings.Join(it.Request.URL.Path, "/")] = true
		}
	}

	seen := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			id := op.OperationID
			seen[id] = true
			_, skip := skipped[id]
			inTS := strings.Contains(ts, "\n  "+id+"(")
			inPy := strings.Contains(py, "\n    def "+snake(id)+"(")
			pmPath := strings.NewReplacer("{", ":", "}", "").Replace(strings.TrimPrefix(path, "/"))
			inPM := requests[method+" "+pmPath]
			if skip && (inTS || inPy || inPM) {
				t.Errorf("%s is skipped but generated", id)
			}
			if !skip && (!inTS || !inPy || !inPM) {
				t.Errorf("%s: TypeScript %v, Python %v, collection %v", id, inTS, inPy, inPM)
			}
		}
	}
	for id := range skipped {
		if !seen[id] {
			t.Errorf("skipped names %s, which is not an operation", id)
		}
	}
}

func TestPagedOperationsHaveIterators(t *testing.T) {
	api, err := Load(filepath.Join(root, SpecPath))
	if err != nil {
		t.Fatal(err)
	}
	paged := map[string]string{}
	for _, op := range api.Ops {
		if op.Paged {
			paged[op.ID] = tsTypeExpr(op.ItemType)
		}
	}
	for id, item := range map[string]string{
		"listUsers": "User", "listSamples": "Sample", "listDaily": "DailySummary",
		"getWorkoutRoute": "RoutePoint", "listWebhookDeliveries": "WebhookDelivery",
		"listOMHDataPoints": "OMHDataPoint",
	} {
		if paged[id] != item {
			t.Errorf("%s: paged item %q, want %q", id, paged[id], item)
		}
	}
	// The FHIR search pages with a Bundle link, not next_cursor.
	if _, ok := paged["searchFHIRObservations"]; ok {
		t.Error("searchFHIRObservations should not get a next_cursor iterator")
	}
}

func TestGeneratedFilesAreAGPL(t *testing.T) {
	files, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	for p, content := range files {
		if strings.HasSuffix(p, ".json") {
			continue
		}
		if !bytes.HasPrefix(content, []byte("// SPDX-License-Identifier: AGPL-3.0-only")) &&
			!bytes.HasPrefix(content, []byte("# SPDX-License-Identifier: AGPL-3.0-only")) {
			t.Errorf("%s does not start with the AGPL-3.0-only header", p)
		}
		if bytes.ContainsRune(content, 0x2014) {
			t.Errorf("%s holds an em dash", p)
		}
	}
}

func TestWebhookVectorsCoverBothOutcomes(t *testing.T) {
	raw, err := WebhookVectors()
	if err != nil {
		t.Fatal(err)
	}
	var f vectorFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	valid := 0
	for _, v := range f.Vectors {
		if v.Valid {
			valid++
		}
	}
	if valid < 3 || valid == len(f.Vectors) {
		t.Fatalf("%d valid vectors of %d", valid, len(f.Vectors))
	}
	if regexp.MustCompile(`whsec_[A-Za-z0-9+/]{8}`).Match(raw) {
		t.Error("the vectors file should hold key texts, not secrets")
	}
}

func TestNaming(t *testing.T) {
	for in, want := range map[string]string{
		"searchFHIRObservations": "search_fhir_observations",
		"listOMHDataPoints":      "list_omh_data_points",
		"X-OHS-Type":             "x_ohs_type",
		"getWorkoutRoute":        "get_workout_route",
		"healthz":                "healthz",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"user_id": "userId", "ohs_type": "ohsType", "id": "id"} {
		if got := camel(in); got != want {
			t.Errorf("camel(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"days": "day", "entries": "entry", "status": "status", "meta": "meta"} {
		if got := singular(in); got != want {
			t.Errorf("singular(%q) = %q, want %q", in, got, want)
		}
	}
	if got := pathIdent("/v1/users/{id}/samples", "id"); got != "user_id" {
		t.Errorf("pathIdent = %q", got)
	}
	if got := IterName("listOMHDataPoints"); got != "iterOMHDataPoints" {
		t.Errorf("IterName = %q", got)
	}
}
