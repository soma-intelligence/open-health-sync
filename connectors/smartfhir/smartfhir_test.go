// SPDX-License-Identifier: AGPL-3.0-only

package smartfhir

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Invented patient and values; the shapes follow Epic's R4 sandbox answers.
const patientID = "eTest.Patient-1"

var now = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func obsBP(id, at string, sys, dia float64) string {
	return fmt.Sprintf(`{"resourceType":"Observation","id":%q,"status":"final",
"category":[{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/observation-category","code":"vital-signs"}]}],
"code":{"coding":[{"system":"http://loinc.org","code":"85354-9","display":"Blood pressure panel"}],"text":"Blood Pressure"},
"effectiveDateTime":%q,
"component":[
 {"code":{"coding":[{"system":"http://loinc.org","code":"8480-6"}]},"valueQuantity":{"value":%v,"unit":"mm[Hg]","system":"http://unitsofmeasure.org","code":"mm[Hg]"}},
 {"code":{"coding":[{"system":"http://loinc.org","code":"8462-4"}]},"valueQuantity":{"value":%v,"unit":"mm[Hg]","system":"http://unitsofmeasure.org","code":"mm[Hg]"}}]}`, id, at, sys, dia)
}

func obsQty(id, cat, loinc, label, at string, v float64, ucum string) string {
	return fmt.Sprintf(`{"resourceType":"Observation","id":%q,"status":"final",
"category":[{"coding":[{"code":%q}]}],
"code":{"coding":[{"system":"http://loinc.org","code":%q,"display":%q}]},
"effectiveDateTime":%q,
"valueQuantity":{"value":%v,"unit":%q,"system":"http://unitsofmeasure.org","code":%q},
"interpretation":[{"coding":[{"code":"H"}]}],
"referenceRange":[{"low":{"value":70},"high":{"value":99}}]}`, id, cat, loinc, label, at, v, ucum, ucum)
}

func searchset(next string, resources ...string) string {
	links := `[]`
	if next != "" {
		links = fmt.Sprintf(`[{"relation":"next","url":%q}]`, next)
	}
	entries := make([]string, 0, len(resources)+1)
	for _, r := range resources {
		entries = append(entries, `{"resource":`+r+`}`)
	}
	// Epic adds OperationOutcome warnings to searches.
	entries = append(entries, `{"resource":{"resourceType":"OperationOutcome","issue":[{"severity":"warning","code":"processing"}]},"search":{"mode":"outcome"}}`)
	return `{"resourceType":"Bundle","type":"searchset","link":` + links + `,"entry":[` + strings.Join(entries, ",") + `]}`
}

// fakeFHIR is an httptest SMART on FHIR server: discovery, token endpoint and
// a small record. It records the token requests and the searches it served.
type fakeFHIR struct {
	*httptest.Server
	mu       sync.Mutex
	tokens   []url.Values
	auth     []string
	searches []string
	token    string // the access token it accepts
}

func newFakeFHIR(t *testing.T) *fakeFHIR {
	f := &fakeFHIR{token: "access-1"}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	base := f.URL + "/fhir"
	mux.HandleFunc("/fhir/.well-known/smart-configuration", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"authorization_endpoint":%q,"token_endpoint":%q,"code_challenge_methods_supported":["S256"]}`,
			f.URL+"/oauth2/authorize", f.URL+"/oauth2/token")
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		f.mu.Lock()
		f.tokens = append(f.tokens, r.PostForm)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.PostForm.Get("code") == "bad" || r.PostForm.Get("refresh_token") == "revoked" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600,"scope":"launch/patient patient/Observation.read","patient":%q,"refresh_token":"refresh-2"}`, f.token, patientID)
	})
	mux.HandleFunc("/fhir/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.searches = append(f.searches, r.URL.Path+"?"+r.URL.RawQuery)
		f.mu.Unlock()
		q := r.URL.Query()
		switch strings.TrimPrefix(r.URL.Path, "/fhir/") {
		case "Patient/" + patientID:
			fmt.Fprintf(w, `{"resourceType":"Patient","id":%q,"name":[{"family":"Test","given":["Pat"]}],"birthDate":"1987-09-12"}`, patientID)
		case "Observation":
			require.Equal(t, patientID, q.Get("patient"))
			switch {
			case q.Get("category") == "vital-signs" && q.Get("page") == "":
				fmt.Fprint(w, searchset(base+"/Observation?patient="+patientID+"&category=vital-signs&page=2",
					obsBP("bp-1", "2026-10-20T09:30:00Z", 128, 82),
					obsQty("hr-1", "vital-signs", "8867-4", "Heart rate", "2026-10-20T09:31:00Z", 64, "/min")))
			case q.Get("category") == "vital-signs":
				fmt.Fprint(w, searchset("",
					obsQty("wt-1", "vital-signs", "29463-7", "Body weight", "2026-10-21T08:00:00-07:00", 165, "[lb_av]")))
			case q.Get("category") == "laboratory":
				fmt.Fprint(w, searchset("",
					obsQty("glu-1", "laboratory", "2345-7", "Glucose [Mass/volume] in Serum or Plasma", "2026-10-22T07:00:00Z", 104, "mg/dL"),
					obsQty("a1c-1", "laboratory", "4548-4", "Hemoglobin A1c", "2026-10-22T07:00:00Z", 5.9, "%")))
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "Condition":
			fmt.Fprint(w, searchset("", `{"resourceType":"Condition","id":"c-2","code":{"text":"Asthma"}}`,
				`{"resourceType":"Condition","id":"c-1","code":{"text":"Hypertension"}}`))
		case "Procedure":
			w.WriteHeader(http.StatusForbidden) // scope not granted
		default:
			fmt.Fprint(w, searchset(""))
		}
	})
	return f
}

func newConnector(t *testing.T, f *fakeFHIR, extra connector.Config) *Connector {
	c := New(Options{HTTPClient: f.Client(), PageDelay: -1, Now: func() time.Time { return now }})
	cfg := connector.Config{KeyClientID: "client-1", KeyFHIRBase: f.URL + "/fhir/"}
	for k, v := range extra {
		cfg[k] = v
	}
	require.NoError(t, c.Configure(cfg))
	return c
}

// sink collects emitted raw objects.
type sink struct {
	objects []emitted
}

type emitted struct {
	dataType string
	day      time.Time
	lines    [][]byte
}

func (s *sink) Emit(_ context.Context, dataType string, day time.Time, lines [][]byte) error {
	s.objects = append(s.objects, emitted{dataType, day, lines})
	return nil
}

func (s *sink) of(dataType string) []emitted {
	var out []emitted
	for _, o := range s.objects {
		if o.dataType == dataType {
			out = append(out, o)
		}
	}
	return out
}

func TestConfigure(t *testing.T) {
	c := New(Options{})
	assert.Error(t, c.Configure(connector.Config{KeyClientID: "x"}), "base missing")
	assert.Error(t, c.Configure(connector.Config{KeyClientID: "x", KeyFHIRBase: "http://ehr.example.org/fhir"}), "plain http off loopback")
	assert.Error(t, c.Configure(connector.Config{KeyClientID: "x", KeyFHIRBase: "https://ehr.example.org/fhir", KeyAuthorizeURL: "https://ehr.example.org/a"}), "authorize without token")
	assert.NoError(t, c.Configure(connector.Config{KeyClientID: "x", KeyFHIRBase: "https://fhir.epic.com/interconnect-fhir-oauth/api/FHIR/R4"}))
	assert.True(t, c.Enabled(connector.Config{KeyClientID: "x", KeyFHIRBase: "y"}))
	assert.False(t, c.Enabled(connector.Config{KeyClientID: "x"}))
}

func TestAuthorizeURL(t *testing.T) {
	f := newFakeFHIR(t)
	c := newConnector(t, f, nil)
	raw, err := c.AuthorizeURL("state-1", "https://ohs.example.org/oauth/smart_fhir/callback")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, f.URL+"/oauth2/authorize", u.Scheme+"://"+u.Host+u.Path, "discovered endpoint")
	q := u.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "client-1", q.Get("client_id"))
	assert.Equal(t, f.URL+"/fhir", q.Get("aud"), "aud is the base without its trailing slash")
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, CodeChallenge(CodeVerifier(c.verifierKey(), "state-1")), q.Get("code_challenge"))
	assert.Contains(t, q.Get("scope"), "launch/patient")
	assert.Contains(t, q.Get("scope"), "offline_access")

	_, err = c.AuthorizeURL("", "x")
	assert.Error(t, err, "PKCE needs a state")
}

func TestExchangeState_PublicClient(t *testing.T) {
	f := newFakeFHIR(t)
	c := newConnector(t, f, connector.Config{KeyPKCESecret: "pkce-key"})
	tok, err := c.ExchangeState(context.Background(), "code-1", "state-1", "https://ohs.example.org/cb")
	require.NoError(t, err)
	assert.Equal(t, "access-1", tok.Access)
	assert.Equal(t, "refresh-2", tok.Refresh)
	assert.Equal(t, patientID, tok.ProviderUserID, "the patient in context")
	assert.Equal(t, now.Add(time.Hour), tok.Expiry)
	assert.Equal(t, []string{"launch/patient", "patient/Observation.read"}, tok.Scopes)

	form := f.tokens[0]
	assert.Equal(t, "client-1", form.Get("client_id"), "a public client names itself in the body")
	assert.Equal(t, CodeVerifier("pkce-key", "state-1"), form.Get("code_verifier"))
	assert.Empty(t, f.auth[0])

	_, err = c.Exchange(context.Background(), "code-1", "x")
	assert.ErrorIs(t, err, ErrPKCEState)
	_, err = c.ExchangeState(context.Background(), "bad", "state-1", "x")
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestExchangeState_ConfidentialClient(t *testing.T) {
	f := newFakeFHIR(t)
	c := newConnector(t, f, connector.Config{KeyClientSecret: "s3cret"})
	_, err := c.ExchangeState(context.Background(), "code-1", "state-1", "x")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(f.auth[0], "Basic "), "client_secret_basic")
	assert.Empty(t, f.tokens[0].Get("client_id"))
	assert.Equal(t, CodeVerifier("s3cret", "state-1"), f.tokens[0].Get("code_verifier"))
}

func TestRefresh(t *testing.T) {
	f := newFakeFHIR(t)
	c := newConnector(t, f, nil)
	_, err := c.Refresh(context.Background(), connector.Tokens{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "no refresh token: connect again")
	_, err = c.Refresh(context.Background(), connector.Tokens{Refresh: "revoked"})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
	nt, err := c.Refresh(context.Background(), connector.Tokens{Refresh: "refresh-1", ProviderUserID: patientID})
	require.NoError(t, err)
	assert.Equal(t, "refresh-2", nt.Refresh)
	assert.Equal(t, patientID, nt.ProviderUserID)
}

func conn(token string) connector.Connection {
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: token, ProviderUserID: patientID}}
}

func TestFetch(t *testing.T) {
	f := newFakeFHIR(t)
	c := newConnector(t, f, nil)
	var s sink
	require.NoError(t, c.Fetch(context.Background(), conn("access-1"), now.Add(-14*24*time.Hour), now, &s))

	obs := s.of(DataTypeObservation)
	byDay := map[string]int{}
	for _, o := range obs {
		byDay[o.day.Format(time.DateOnly)] += len(o.lines)
	}
	assert.Equal(t, map[string]int{"2026-10-20": 2, "2026-10-21": 1, "2026-10-22": 2}, byDay,
		"observations of both categories and both pages, by UTC day; OperationOutcome dropped")

	require.Len(t, s.of(DataTypePatient), 1)
	cond := s.of(DataTypeCondition)
	require.Len(t, cond, 1)
	assert.Equal(t, "c-1", idOf(cond[0].lines[0]), "sorted by id, so a re-pull gives the same bytes")
	assert.Empty(t, s.of(DataTypeProcedure), "403 skips the resource, not the pull")

	assert.NotContains(t, f.searches[0], "category", "every category at once first; the fake answers 400 like Epic")

	var dates []string
	for _, q := range f.searches {
		if strings.Contains(q, "category=laboratory") {
			u, _ := url.ParseQuery(strings.SplitN(q, "?", 2)[1])
			dates = u["date"]
		}
	}
	assert.Equal(t, []string{"ge2026-10-13T12:00:00Z", "lt2026-10-27T12:00:00Z"}, dates)
}

func TestFetch_OldWindowSkipsChart(t *testing.T) {
	f := newFakeFHIR(t)
	c := newConnector(t, f, nil)
	var s sink
	require.NoError(t, c.Fetch(context.Background(), conn("access-1"), now.Add(-90*24*time.Hour), now.Add(-60*24*time.Hour), &s))
	assert.Empty(t, s.of(DataTypePatient))
	assert.Empty(t, s.of(DataTypeCondition))
}

func TestFetch_Errors(t *testing.T) {
	f := newFakeFHIR(t)
	c := newConnector(t, f, nil)
	err := c.Fetch(context.Background(), conn("expired"), now.Add(-time.Hour), now, &sink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)

	bad := conn("access-1")
	bad.Tokens.ProviderUserID = "../Patient/other"
	assert.ErrorIs(t, c.Fetch(context.Background(), bad, now.Add(-time.Hour), now, &sink{}), connector.ErrNeedsReauth)
}

func TestSearch_RefusesForeignNextLink(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the token must never reach another server")
	}))
	defer foreign.Close()
	f := newFakeFHIR(t)
	c := newConnector(t, f, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/fhir/Condition", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, searchset(foreign.URL+"/steal"))
	})
	evil := httptest.NewServer(mux)
	defer evil.Close()
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "x", KeyFHIRBase: evil.URL + "/fhir",
		KeyAuthorizeURL: evil.URL + "/a", KeyTokenURL: evil.URL + "/t"}))
	_, err := c.search(context.Background(), "tok", DataTypeCondition, url.Values{})
	assert.ErrorContains(t, err, "another server")
}

// mapLine maps one fixture line.
func mapLine(t *testing.T, line string) (res struct {
	q map[string]float64
	o map[string]float64
	n int
}) {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &fields))
	c := New(Options{})
	r, err := c.Map(connector.RawRecord{UserID: "u-1", Provider: Slug, DataType: DataTypeObservation, Fields: fields})
	require.NoError(t, err)
	r2, err := c.Map(connector.RawRecord{UserID: "u-1", Provider: Slug, DataType: DataTypeObservation, Fields: fields})
	require.NoError(t, err)
	res.q, res.o = map[string]float64{}, map[string]float64{}
	for i, s := range r.QuantitySamples {
		assert.Equal(t, s.ContentHash, r2.QuantitySamples[i].ContentHash, "stable hash")
		assert.NotEmpty(t, s.ContentHash)
		res.q[s.QuantityType+"|"+s.Unit] = s.Value
	}
	for i, o := range r.Observations {
		assert.Equal(t, o.ContentHash, r2.Observations[i].ContentHash, "stable hash")
		assert.Equal(t, "ehr", o.TypeConcept)
		v := -1.0
		if o.ValueNum != nil {
			v = *o.ValueNum
		}
		res.o[o.CanonicalCode+"|"+o.UnitUCUM] = v
	}
	res.n = len(r.Deletions)
	return res
}

func TestMap(t *testing.T) {
	bp := mapLine(t, obsBP("bp-1", "2026-10-20T09:30:00Z", 128, 82))
	assert.Equal(t, map[string]float64{"blood_pressure_systolic|mmHg": 128, "blood_pressure_diastolic|mmHg": 82}, bp.q)

	wt := mapLine(t, obsQty("wt-1", "vital-signs", "29463-7", "Body weight", "2026-10-21T08:00:00-07:00", 165, "[lb_av]"))
	assert.InDelta(t, 74.84, wt.q["body_mass|kg"], 0.01, "pounds converted to kg")

	https := mapLine(t, strings.Replace(obsQty("wt-2", "vital-signs", "29463-7", "Weight", "2026-10-21T08:00:00Z", 70, "kg"), "http://loinc.org", "https://loinc.org", 1))
	assert.Equal(t, map[string]float64{"body_mass|kg": 70}, https.q, "the https spelling of LOINC some servers use")

	temp := mapLine(t, obsQty("t-1", "vital-signs", "8310-5", "Body temperature", "2026-10-21", 98.6, "[degF]"))
	assert.InDelta(t, 37.0, temp.q["body_temperature|celsius"], 0.01, "a date-only time is read as midnight UTC")

	lab := mapLine(t, obsQty("glu-1", "laboratory", "2345-7", "Glucose", "2026-10-22T07:00:00Z", 104, "mg/dL"))
	assert.Empty(t, lab.q, "a serum glucose is a lab, not a blood_glucose reading")
	assert.Equal(t, map[string]float64{"loinc:2345-7|mg/dL": 104}, lab.o)

	odd := mapLine(t, obsQty("hr-2", "vital-signs", "8867-4", "Heart rate", "2026-10-22T07:00:00Z", 1.1, "Hz"))
	assert.Empty(t, odd.q, "a unit that does not convert")
	assert.Equal(t, map[string]float64{"loinc:8867-4|Hz": 1.1}, odd.o, "is kept as a generic observation")

	coded := mapLine(t, `{"resourceType":"Observation","id":"s-1","status":"final","code":{"coding":[{"system":"urn:oid:1.2.840","code":"SMOKE"}],"text":"Smoking status"},"effectiveDateTime":"2026-10-01T00:00:00Z","valueCodeableConcept":{"coding":[{"system":"http://snomed.info/sct","code":"8517006"}],"text":"Former smoker"}}`)
	assert.Equal(t, map[string]float64{"urn:oid:1.2.840|SMOKE|": -1}, coded.o)

	gone := mapLine(t, `{"resourceType":"Observation","id":"hr-1","status":"entered-in-error","code":{"text":"x"}}`)
	assert.Equal(t, 1, gone.n, "entered in error deletes what was stored")

	none := mapLine(t, `{"resourceType":"Observation","id":"x-1","status":"final","code":{"coding":[{"system":"http://loinc.org","code":"8867-4"}]},"effectiveDateTime":"2026-10-01T00:00:00Z"}`)
	assert.Empty(t, none.q)
	assert.Empty(t, none.o, "no value, nothing stored")

	r, err := New(Options{}).Map(connector.RawRecord{DataType: DataTypeCondition, Fields: map[string]any{"resourceType": "Condition"}})
	assert.NoError(t, err)
	assert.True(t, r.IsEmpty(), "other resources are raw only")

	_, err = New(Options{}).Map(connector.RawRecord{DataType: DataTypeObservation, Fields: map[string]any{"resourceType": "Patient"}})
	assert.Error(t, err)
}

func TestSyncPolicyInBounds(t *testing.T) {
	c := New(Options{})
	assert.Equal(t, c.SyncPolicy(), c.SyncPolicy().Normalize())
}

// A server that searches every category at once is asked once, which also
// brings back Observations filed without a category.
func TestFetch_AllCategoriesAtOnce(t *testing.T) {
	var queries []string
	mux := http.NewServeMux()
	mux.HandleFunc("/fhir/Observation", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		fmt.Fprint(w, searchset("", `{"resourceType":"Observation","id":"u-1","status":"final","code":{"coding":[{"system":"http://loinc.org","code":"59018-2"}]},"effectiveDateTime":"2026-10-12","valueString":"x"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(Options{HTTPClient: srv.Client(), PageDelay: -1, Now: func() time.Time { return now }})
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "x", KeyFHIRBase: srv.URL + "/fhir",
		KeyAuthorizeURL: srv.URL + "/a", KeyTokenURL: srv.URL + "/t"}))
	var s sink
	require.NoError(t, c.Fetch(context.Background(), conn("tok"), now.Add(-30*24*time.Hour), now.Add(-5*24*time.Hour), &s))
	require.Len(t, queries, 1)
	assert.NotContains(t, queries[0], "category")
	require.Len(t, s.of(DataTypeObservation), 1)
	assert.Equal(t, "2026-10-12", s.of(DataTypeObservation)[0].day.Format(time.DateOnly))
}
