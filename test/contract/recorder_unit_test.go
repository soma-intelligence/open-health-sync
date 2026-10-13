// SPDX-License-Identifier: AGPL-3.0-only

package contract

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeServer func(*http.Request) (int, string, string)

func (f fakeServer) RoundTrip(r *http.Request) (*http.Response, error) {
	status, ct, body := f(r)
	h := http.Header{}
	h.Set("Content-Type", ct)
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

const user = `{"id":"3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11","external_ref":null,"status":"active","created_at":"2026-01-10T10:00:00Z"}`

func record(t *testing.T, srv fakeServer, method, path, auth string) Call {
	t.Helper()
	rec, err := NewRecorder(context.Background(), "../../api/openapi.yaml", "http://ohs.test")
	require.NoError(t, err)
	rec.Next = srv
	req, err := http.NewRequest(method, "http://ohs.test"+path, nil)
	require.NoError(t, err)
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	res, err := (&http.Client{Transport: rec}).Do(req)
	require.NoError(t, err)
	res.Body.Close()
	calls := rec.Calls()
	require.Len(t, calls, 1)
	return calls[0]
}

func TestRecorderAcceptsDocumentedExchange(t *testing.T) {
	c := record(t, func(*http.Request) (int, string, string) {
		return 200, "application/json", user
	}, "GET", "/v1/users/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11", "k")
	assert.Empty(t, c.Problems)
	assert.Equal(t, "getUser", c.Operation)
}

func TestRecorderFlagsUndocumentedField(t *testing.T) {
	c := record(t, func(*http.Request) (int, string, string) {
		return 200, "application/json", `{"data":[` + strings.TrimSuffix(user, "}") + `,"secret":1},` + strings.TrimSuffix(user, "}") + `,"secret":2}],"next_cursor":null,"total":3}`
	}, "GET", "/v1/users", "k")
	assert.Contains(t, c.Problems, "response: undocumented field $.data[].secret")
	assert.Contains(t, c.Problems, "response: undocumented field $.total")
	assert.Len(t, c.Problems, 2, "one line per path, not per element")
}

func TestRecorderAllowsFreeFormMetadata(t *testing.T) {
	sample := `{"data":[{"id":1,"type":"heart_rate","value":60,"unit":"bpm","start_date":"2026-01-10T10:00:00Z","end_date":null,` +
		`"provider":"apple_health","source":null,"device":null,"ingested_at":"2026-01-10T10:00:00Z","metadata":{"timezone":"Europe/Paris","anything":{"x":1}}}],"next_cursor":null}`
	c := record(t, func(*http.Request) (int, string, string) { return 200, "application/json", sample },
		"GET", "/v1/users/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11/samples?type=heart_rate", "k")
	assert.Empty(t, c.Problems)
}

func TestRecorderFlagsUndocumentedStatusAndRoute(t *testing.T) {
	c := record(t, func(*http.Request) (int, string, string) {
		return 418, "application/problem+json", `{"type":"about:blank","title":"Teapot","status":418}`
	}, "GET", "/v1/users/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11", "k")
	require.NotEmpty(t, c.Problems)
	assert.Contains(t, strings.Join(c.Problems, " "), "status is not supported")

	c = record(t, func(*http.Request) (int, string, string) { return 200, "application/json", `{}` },
		"GET", "/v1/nowhere", "k")
	require.NotEmpty(t, c.Problems)
	assert.Contains(t, c.Problems[0], "undocumented route")
}

func TestRecorderFlagsWrongType(t *testing.T) {
	c := record(t, func(*http.Request) (int, string, string) {
		return 200, "application/json", strings.Replace(user, `"active"`, `"gone"`, 1)
	}, "GET", "/v1/users/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11", "k")
	require.NotEmpty(t, c.Problems)
	assert.Contains(t, c.Problems[0], "response:")
}

// The FHIR golden files are what the FHIR routes answer: they match the
// document, and a member it does not name is flagged.
func TestRecorderFHIRGoldenFiles(t *testing.T) {
	const (
		fhirBase = "/v1/users/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11/fhir"
		fhirPath = fhirBase + "/Observation"
	)
	// Each golden file is the answer of one operation, by its name.
	routes := []struct{ prefix, path, op string }{
		{"observation-", fhirPath + "/qs-1001", "readFHIRObservation"},
		{"bundle-searchset-", fhirPath, "searchFHIRObservations"},
		{"patient", fhirBase + "/Patient/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11", "readFHIRPatient"},
		{"bundle-everything-", fhirBase + "/Patient/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11/$everything", "everythingFHIRPatient"},
		{"device-", fhirBase + "/Device/dev-4cd59d3e7b09cd28", "readFHIRDevice"},
		{"bundle-device-", fhirBase + "/Device", "searchFHIRDevices"},
		{"provenance-", fhirBase + "/Provenance/prov-qs-1001", "readFHIRProvenance"},
		{"bundle-provenance-", fhirBase + "/Provenance?target=Observation/qs-1001", "searchFHIRProvenance"},
		{"capabilitystatement", fhirBase + "/metadata", "readFHIRCapabilityStatement"},
	}
	files, err := filepath.Glob("../../internal/fhir/testdata/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		path, op := "", ""
		for _, r := range routes {
			if strings.HasPrefix(filepath.Base(f), r.prefix) {
				path, op = r.path, r.op
				break
			}
		}
		require.NotEmpty(t, path, "%s: no route for this golden file; add its prefix here", f)
		c := record(t, func(*http.Request) (int, string, string) { return 200, "application/fhir+json", string(raw) },
			"GET", path, "k")
		assert.Empty(t, c.Problems, f)
		assert.Equal(t, op, c.Operation, f)
	}

	// The entries of $everything are checked against the schema of their
	// resource type, members included.
	c := record(t, func(*http.Request) (int, string, string) {
		return 200, "application/fhir+json", `{"resourceType":"Bundle","type":"searchset","link":[{"relation":"self","url":"http://h/x"}],` +
			`"entry":[{"fullUrl":"http://h/Device/dev-0123456789abcdef","resource":{"resourceType":"Device","id":"dev-0123456789abcdef","serialNumber":"x"},"search":{"mode":"include"}}]}`
	}, "GET", fhirBase+"/Patient/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11/$everything", "k")
	assert.Equal(t, []string{"response: undocumented field $.entry[].resource.serialNumber"}, c.Problems)

	c = record(t, func(*http.Request) (int, string, string) {
		return 200, "application/fhir+json", `{"resourceType":"Bundle","type":"searchset","link":[{"relation":"self","url":"http://h/x"}],"total":3}`
	}, "GET", fhirPath+"?code=http://loinc.org|8867-4", "k")
	assert.Contains(t, c.Problems, "response: undocumented field $.total")
	assert.Equal(t, "searchFHIRObservations", c.Operation)

	c = record(t, func(*http.Request) (int, string, string) {
		return 200, "application/fhir+json", `{"resourceType":"Observation","id":"qs-1","status":"preliminary","code":{"text":"x"},"subject":{"reference":"Patient/x"}}`
	}, "GET", fhirPath+"/qs-1", "k")
	require.NotEmpty(t, c.Problems, "the body is checked against the schema")
	assert.Contains(t, c.Problems[0], "response:")
	assert.Equal(t, "readFHIRObservation", c.Operation)
}
