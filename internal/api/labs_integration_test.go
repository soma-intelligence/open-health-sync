// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	labconn "github.com/soma-intelligence/open-health-sync/connectors/labs"
	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// labEnv is an env whose router takes lab uploads, with the raw store the
// ETL reads them back from, sealed under a vault or clear.
type labEnv struct {
	*env
	raw rawstore.Store
	v   *vault.Vault
}

func newLabEnv(t *testing.T, sealed bool) *labEnv {
	t.Helper()
	le := &labEnv{}
	le.env = newEnv(t, func(d *api.Deps) {
		d.Labs = labconn.New()
		d.PublicURL = publicURL + "/"
		le.raw = d.Raw
		if sealed {
			p, err := vault.OpenProvider(context.Background(), config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{9}, 32)})
			require.NoError(t, err)
			le.v = vault.New(d.Pool, p, vault.Options{CanOpen: true})
			d.Vault = le.v
		}
	})
	return le
}

func (e *labEnv) newUser(ref string) uuid.UUID {
	e.t.Helper()
	u := e.user(e.tenantA, ref)
	if e.v != nil {
		ctx := context.Background()
		tx, err := e.pool.Begin(ctx)
		require.NoError(e.t, err)
		require.NoError(e.t, e.v.EnsureUser(ctx, tx, e.tenantA, u))
		require.NoError(e.t, tx.Commit(ctx))
	}
	return u
}

// process runs the ETL over every queued job, as the worker would, in the
// order given by order (identity when nil).
func (e *labEnv) process(order func([]queue.ETLJob) []queue.ETLJob) int {
	e.t.Helper()
	ctx := context.Background()
	q := queue.NewETL(e.pool)
	var jobs []queue.ETLJob
	for _, tier := range []string{queue.TierHighFreq, queue.TierStandard} {
		js, err := q.Claim(ctx, tier, 50)
		require.NoError(e.t, err)
		jobs = append(jobs, js...)
	}
	if order != nil {
		jobs = order(jobs)
	}
	for _, j := range jobs {
		_, err := etl.ProcessWith(ctx, e.pool, e.raw, j, etl.Options{Vault: e.v})
		require.NoError(e.t, err)
		require.NoError(e.t, q.Complete(ctx, j))
	}
	return len(jobs)
}

type labResultJSON struct {
	ID              int64     `json:"id"`
	ReportID        *string   `json:"report_id"`
	Code            string    `json:"code"`
	LOINC           *string   `json:"loinc"`
	Display         *string   `json:"display"`
	Value           *float64  `json:"value"`
	Unit            *string   `json:"unit"`
	ValueString     *string   `json:"value_string"`
	NormalizedValue *float64  `json:"normalized_value"`
	NormalizedUnit  *string   `json:"normalized_unit"`
	ReferenceLow    *float64  `json:"reference_low"`
	ReferenceHigh   *float64  `json:"reference_high"`
	Interpretation  *string   `json:"interpretation"`
	Critical        bool      `json:"critical"`
	Status          string    `json:"status"`
	CollectedAt     time.Time `json:"collected_at"`
	IssuedAt        *time.Time
	Performer       *string `json:"performer"`
	Orderer         *string `json:"orderer"`
	ExternalID      string  `json:"external_id"`
	Provider        string  `json:"provider"`
}

type labReportJSON struct {
	ID      string          `json:"id"`
	LOINC   *string         `json:"loinc"`
	Display *string         `json:"display"`
	Status  *string         `json:"status"`
	Orderer *string         `json:"orderer"`
	Results []labResultJSON `json:"results"`
}

type labsPostJSON struct {
	Reports []struct {
		ID      *string `json:"id"`
		Results []struct {
			ID     string `json:"id"`
			Code   string `json:"code"`
			Status string `json:"status"`
		} `json:"results"`
	} `json:"reports"`
	Duplicate bool `json:"duplicate"`
}

func (e *labEnv) labs(user uuid.UUID, query string) []labResultJSON {
	e.t.Helper()
	r := e.do("GET", "/v1/users/"+user.String()+"/labs"+query, e.keyA, nil)
	require.Equal(e.t, http.StatusOK, r.code, string(r.body))
	var p page[labResultJSON]
	r.json(e.t, &p)
	return p.Data
}

func lipidFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../labs/testdata/hl7-r4-diagnosticreport-example-lipids.json")
	require.NoError(t, err)
	return string(b)
}

type fhirBundleJSON struct {
	Entry []struct {
		FullURL  string         `json:"fullUrl"`
		Resource map[string]any `json:"resource"`
		Search   struct {
			Mode string `json:"mode"`
		} `json:"search"`
	} `json:"entry"`
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestLabsRoundTrip(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		name := map[bool]string{false: "clear", true: "sealed"}[sealed]
		t.Run(name, func(t *testing.T) { testLabsRoundTrip(t, sealed) })
	}
}

func testLabsRoundTrip(t *testing.T, sealed bool) {
	e := newLabEnv(t, sealed)
	u := e.newUser("patient")
	base := "/v1/users/" + u.String()
	body := lipidFixture(t)

	// The HL7 lipid panel example, as FHIR.
	r := e.do("POST", base+"/labs", e.keyA, body, "Content-Type", "application/fhir+json")
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	var posted labsPostJSON
	r.json(t, &posted)
	assert.False(t, posted.Duplicate)
	require.Len(t, posted.Reports, 1)
	require.NotNil(t, posted.Reports[0].ID)
	assert.Equal(t, "lipids", *posted.Reports[0].ID)
	require.Len(t, posted.Reports[0].Results, 4)
	assert.Equal(t, "lab:loinc:35200-5", posted.Reports[0].Results[0].Code)

	// Sent again: the same upload part.
	r = e.do("POST", base+"/labs", e.keyA, body)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	r.json(t, &posted)
	assert.True(t, posted.Duplicate)
	require.Equal(t, 1, e.process(nil), "one ETL job for the two identical uploads")

	all := e.labs(u, "")
	require.Len(t, all, 4)
	var chol labResultJSON
	for _, x := range all {
		if x.ExternalID == "cholesterol" {
			chol = x
		}
		assert.Equal(t, "labs", x.Provider)
		require.NotNil(t, x.ReportID)
		assert.Equal(t, "lipids", *x.ReportID)
		assert.True(t, x.CollectedAt.Equal(time.Date(2011, 3, 3, 21, 30, 0, 0, time.UTC)), "the report's collection time")
	}
	require.NotNil(t, chol.Value)
	assert.Equal(t, 6.3, *chol.Value)
	assert.Equal(t, "mmol/L", *chol.Unit)
	assert.Equal(t, "35200-5", *chol.LOINC)
	assert.Equal(t, "Cholesterol", *chol.Display)
	assert.Equal(t, 4.5, *chol.ReferenceHigh)
	assert.Equal(t, "final", chol.Status)
	assert.Equal(t, "Acme Laboratory, Inc", *chol.Performer)
	require.NotNil(t, chol.NormalizedValue)
	assert.InDelta(t, 6.3*38.665, *chol.NormalizedValue, 1e-9)
	assert.Equal(t, "mg/dL", *chol.NormalizedUnit)

	// Filters.
	assert.Len(t, e.labs(u, "?code=35200-5"), 1)
	assert.Len(t, e.labs(u, "?report=lipids"), 4)
	assert.Empty(t, e.labs(u, "?report=other"))
	assert.Empty(t, e.labs(u, "?start=2012-01-01"))
	assert.Len(t, e.labs(u, "?start=2011-03-01&end=2011-03-05"), 4)
	page1 := e.do("GET", base+"/labs?limit=3", e.keyA, nil)
	var p1 page[labResultJSON]
	page1.json(t, &p1)
	require.Len(t, p1.Data, 3)
	require.NotNil(t, p1.NextCursor)
	assert.Len(t, e.labs(u, "?limit=3&cursor="+url.QueryEscape(*p1.NextCursor)), 1)
	for _, bad := range []string{"?from=2011-01-01", "?type=x", "?code=a&code=b", "?cursor=nope"} {
		r := e.do("GET", base+"/labs"+bad, e.keyA, nil)
		assert.Equal(t, http.StatusBadRequest, r.code, bad+": "+string(r.body))
	}

	// The report.
	r = e.do("GET", base+"/labs/reports/lipids", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var rep labReportJSON
	r.json(t, &rep)
	assert.Equal(t, "lipids", rep.ID)
	assert.Equal(t, "24331-1", *rep.LOINC)
	assert.Equal(t, "Lipid Panel", *rep.Display)
	assert.Equal(t, "final", *rep.Status)
	assert.Len(t, rep.Results, 4)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/labs/reports/none", e.keyA, nil).code)

	// FHIR: the results as laboratory Observations, the report as a
	// DiagnosticReport that references them.
	fhirGet := func(path string) fhirBundleJSON {
		t.Helper()
		r := e.do("GET", base+"/fhir/"+path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, path+": "+string(r.body))
		var b fhirBundleJSON
		r.json(t, &b)
		return b
	}
	obs := fhirGet("Observation?category=laboratory")
	require.Len(t, obs.Entry, 4)
	for _, en := range obs.Entry {
		assert.True(t, strings.HasPrefix(en.Resource["id"].(string), "lab-"))
		assert.Contains(t, mustJSON(t, en.Resource["category"]), `"laboratory"`)
	}
	byCode := fhirGet("Observation?code=" + url.QueryEscape("http://loinc.org|35200-5"))
	require.Len(t, byCode.Entry, 1)
	assert.Contains(t, mustJSON(t, byCode.Entry[0].Resource), `"referenceRange"`)
	assert.Empty(t, fhirGet("Observation?category=vital-signs").Entry)
	id := byCode.Entry[0].Resource["id"].(string)
	r = e.do("GET", base+"/fhir/Observation/"+id, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Contains(t, string(r.body), `"valueQuantity"`)

	reports := fhirGet("DiagnosticReport")
	require.Len(t, reports.Entry, 1)
	dr := reports.Entry[0].Resource
	assert.Equal(t, "DiagnosticReport", dr["resourceType"])
	assert.Len(t, dr["result"], 4)
	assert.Len(t, fhirGet("DiagnosticReport?category=LAB").Entry, 1)
	assert.Empty(t, fhirGet("DiagnosticReport?category="+url.QueryEscape("http://terminology.hl7.org/CodeSystem/v2-0074|RAD")).Entry)
	r = e.do("GET", base+"/fhir/DiagnosticReport/"+dr["id"].(string), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/fhir/DiagnosticReport/labreport-"+strings.Repeat("0", 40), e.keyA, nil).code)
	assert.Equal(t, http.StatusBadRequest, e.do("GET", base+"/fhir/DiagnosticReport?code=x", e.keyA, nil).code)
	every := fhirGet("Patient/" + u.String() + "/$everything")
	kinds := map[string]int{}
	for _, en := range every.Entry {
		kinds[en.Resource["resourceType"].(string)]++
	}
	assert.Equal(t, 4, kinds["Observation"])
	assert.Equal(t, 1, kinds["DiagnosticReport"])

	// The CSV export: one column per field.
	r = e.do("GET", base+"/exports/labs", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	rows, err := csv.NewReader(bytes.NewReader(r.body)).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 1+4)
	assert.Equal(t, []string{"id", "report_id", "code", "loinc"}, rows[0][:4])
	assert.Contains(t, rows[0], "normalized_value")
	r = e.do("GET", base+"/exports/labs?format=parquet", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.True(t, bytes.HasPrefix(r.body, []byte("PAR1")))

	// The amended report: the cholesterol corrected, from a later upload.
	amended := strings.Replace(body, `"value": 6.3, "unit": "mmol/L"`, `"value": 6.1, "unit": "mmol/L"`, 1)
	amended = strings.Replace(amended, `"id": "cholesterol",
        "status": "final"`, `"id": "cholesterol",
        "status": "amended"`, 1)
	require.NotEqual(t, body, amended)
	require.Contains(t, amended, `"status": "amended"`)
	r = e.do("POST", base+"/labs", e.keyA, amended)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	require.Equal(t, 1, e.process(nil))
	all = e.labs(u, "")
	require.Len(t, all, 4, "the amended result replaces the earlier one")
	got := e.labs(u, "?code=35200-5")
	require.Len(t, got, 1)
	assert.Equal(t, 6.1, *got[0].Value)
	assert.Equal(t, "amended", got[0].Status)

	// The first upload processed again (a replayed job) brings nothing back.
	_, err = e.pool.Exec(context.Background(), `INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key)
		SELECT tenant_id, user_id, provider, key FROM ohs_sync.raw_objects
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY created_at LIMIT 1`, e.tenantA, u)
	require.NoError(t, err)
	require.Equal(t, 1, e.process(nil))
	got = e.labs(u, "?code=35200-5")
	require.Len(t, got, 1)
	assert.Equal(t, 6.1, *got[0].Value, "the older upload is held back")
}

func TestLabsRejects(t *testing.T) {
	e := newLabEnv(t, false)
	u := e.newUser("patient")
	other := e.newUser("other")
	base := "/v1/users/" + u.String()
	vital := `{"resourceType":"Observation","id":"bp","status":"final",
	  "category":[{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/observation-category","code":"vital-signs"}]}],
	  "code":{"coding":[{"system":"http://loinc.org","code":"8867-4"}]},"effectiveDateTime":"2026-01-01T00:00:00Z","valueQuantity":{"value":60}}`
	for body, want := range map[string]string{
		vital:                        "category must be laboratory",
		`{"resourceType":"Patient"}`: "resourceType must be",
		`{"reports":[]}`:             "at least one report",
		`not json`:                   "not a JSON object",
		`{"reports":[{"id":"r","collected_at":"2026-01-01T00:00:00Z","results":[{"id":"a","loinc":"2345-7","value":1,"interpretation":"X"}]}]}`: "interpretation must be",
	} {
		r := e.do("POST", base+"/labs", e.keyA, body)
		require.Equal(t, http.StatusBadRequest, r.code, string(r.body))
		var p problem
		r.json(t, &p)
		assert.Contains(t, p.Detail, want)
	}
	r := e.do("POST", base+"/labs", e.keyA, `{"reports":[{"id":"r","collected_at":"2026-01-01T00:00:00Z","results":[{"id":"a","loinc":"2345-7","value":1}]}]}`+strings.Repeat(" ", 1<<20))
	assert.Equal(t, http.StatusRequestEntityTooLarge, r.code)

	// A user token posts for its own user only.
	tok, _, err := e.store.MintUserToken(context.Background(), e.tenantA, other, []string{"upload"}, time.Hour)
	require.NoError(t, err)
	ok := `{"reports":[{"id":"r","collected_at":"2026-01-01T00:00:00Z","results":[{"id":"a","loinc":"2345-7","value":99,"unit":"mg/dL"}]}]}`
	assert.Equal(t, http.StatusNotFound, e.do("POST", base+"/labs", tok, ok).code)
	assert.Equal(t, http.StatusAccepted, e.do("POST", "/v1/users/"+other.String()+"/labs", tok, ok).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/users/"+uuid.NewString()+"/labs", e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/labs", e.keyB, nil).code, "another tenant's user")
}
