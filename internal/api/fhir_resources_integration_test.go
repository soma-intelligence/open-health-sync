// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/fhir"
)

// rawBundle reads a searchset Bundle whose entries may be of any type.
func (e *env) rawBundle(path, key string) fhir.SearchBundle[json.RawMessage] {
	e.t.Helper()
	r := e.do("GET", path, key, nil)
	require.Equal(e.t, http.StatusOK, r.code, "%s: %s", path, r.body)
	assert.Equal(e.t, "application/fhir+json", r.header.Get("Content-Type"))
	var b fhir.SearchBundle[json.RawMessage]
	r.json(e.t, &b)
	assert.Equal(e.t, "searchset", b.Type)
	require.NotEmpty(e.t, b.Link)
	assert.Equal(e.t, publicURL+path, b.Link[0].URL)
	return b
}

func relative(t *testing.T, link string) string {
	t.Helper()
	require.True(t, strings.HasPrefix(link, publicURL+"/"), link)
	u, err := url.Parse(link)
	require.NoError(t, err)
	return u.RequestURI()
}

type typed struct {
	ResourceType string `json:"resourceType"`
	ID           string `json:"id"`
}

// seedDevices adds records of three more devices to e.seed's: a workout of
// a Garmin watch through Strava, a weight from a scale known by its model
// only, and a heart rate of the same watch name with a model and product
// type (another device).
func (e *env) seedDevices(tn, u uuid.UUID) {
	e.t.Helper()
	t0 := time.Date(2026, 3, 1, 13, 0, 0, 0, time.UTC)
	e.exec(`INSERT INTO ohs_data.workouts
		(tenant_id, user_id, provider, source_slug, device_id, activity_type, start_date, end_date, duration_s, content_hash)
		VALUES ($1, $2, 'strava', 'garmin', 'garmin-1', 'cycling', $3, $4, 3600, 'wo-garmin')`, tn, u, t0, t0.Add(time.Hour))
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, device_model, quantity_type, value, unit, start_date, content_hash, external_id)
		VALUES ($1, $2, 'withings', 'Scale 2', 'body_mass', 72.5, 'kg', $3, 'w-1', 'wm-77')`, tn, u, t0.Add(2*time.Hour))
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, source_slug, device_id, device_model, product_type, quantity_type, value, unit, start_date, content_hash)
		VALUES ($1, $2, 'apple_health', 'apple', 'watch-1', 'Watch', 'Watch7,2', 'heart_rate', 70, 'bpm', $3, 'hr-x')`,
		tn, u, t0.Add(3*time.Hour))
}

func TestFHIRPatient(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.PublicURL = publicURL })
	u := e.user(e.tenantA, "a-1")
	plain := e.user(e.tenantA, "")
	base := "/v1/users/" + u.String() + "/fhir"

	r := e.do("GET", base+"/Patient/"+u.String(), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, "application/fhir+json", r.header.Get("Content-Type"))
	var p fhir.Patient
	r.json(t, &p)
	assert.Equal(t, fhir.Patient{ResourceType: "Patient", ID: u.String(), Active: true,
		Identifier: []fhir.Identifier{{System: fhir.ExternalRefSystem(e.tenantA), Value: "a-1"}}}, p)

	r = e.do("GET", "/v1/users/"+plain.String()+"/fhir/Patient/"+plain.String(), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	assert.NotContains(t, string(r.body), "identifier", "no external_ref, no identifier")

	for path, key := range map[string]string{
		base + "/Patient/" + plain.String():                  e.keyA, // another user's id under this user
		base + "/Patient/" + u.String():                      e.keyB, // another tenant
		base + "/Patient/" + u.String() + "/$everything":     e.keyB,
		base + "/Patient/" + plain.String() + "/$everything": e.keyA,
		base + "/Device": e.keyB,
		base + "/Provenance?target=Observation/qs-1": e.keyB,
		base + "/metadata":                           e.keyB,
	} {
		r := e.do("GET", path, key, nil)
		assert.Equal(t, http.StatusNotFound, r.code, path)
		assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"), path)
	}

	r = e.do("GET", base+"/metadata", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	var cs fhir.CapabilityStatement
	r.json(t, &cs)
	assert.Equal(t, "CapabilityStatement", cs.ResourceType)
	assert.Equal(t, publicURL+base, cs.Implementation.URL)

	// A user being deleted is no Patient.
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/Patient/"+u.String(), e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/Patient/"+u.String()+"/$everything", e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/Device", e.keyA, nil).code)
}

func TestFHIRDevicesAndProvenance(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.PublicURL = publicURL })
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	e.seedDevices(e.tenantA, u)
	// Tenant B's user with the same device: another user, another id.
	ub := e.user(e.tenantB, "b-1")
	e.seedDevices(e.tenantB, ub)
	base := "/v1/users/" + u.String() + "/fhir"

	devs := e.rawBundle(base+"/Device", e.keyA)
	assert.Len(t, devs.Link, 1, "one page")
	devices := map[string]fhir.Device{}
	for _, en := range devs.Entry {
		var d fhir.Device
		require.NoError(t, json.Unmarshal(en.Resource, &d))
		assert.Equal(t, publicURL+base+"/Device/"+d.ID, en.FullURL)
		assert.Equal(t, "Patient/"+u.String(), d.Patient.Reference)
		devices[d.ID] = d
	}
	watch := fhir.DeviceID(u, read.Device{Provider: "apple_health", Source: "apple", DeviceID: "watch-1"})
	watch2 := fhir.DeviceID(u, read.Device{Provider: "apple_health", Source: "apple", DeviceID: "watch-1", DeviceModel: "Watch", ProductType: "Watch7,2"})
	garmin := fhir.DeviceID(u, read.Device{Provider: "strava", Source: "garmin", DeviceID: "garmin-1"})
	scale := fhir.DeviceID(u, read.Device{Provider: "withings", DeviceModel: "Scale 2"})
	require.Len(t, devices, 4, "the samples without a device have none")
	for _, id := range []string{watch, watch2, garmin, scale} {
		require.Contains(t, devices, id)
	}
	assert.Equal(t, "Watch7,2", devices[watch2].ModelNumber)
	assert.Equal(t, []fhir.DeviceName{{Name: "Scale 2", Type: "model-name"}}, devices[scale].DeviceName)
	assert.Equal(t, "garmin-1", devices[garmin].Identifier[0].Value)

	// A window keeps the devices of its records.
	w := e.rawBundle(base+"/Device?start=2026-03-01T13:00:00Z&end=2026-03-01T16:00:00Z", e.keyA)
	assert.Len(t, w.Entry, 2, "the workout and the scale")
	assert.Empty(t, e.rawBundle(base+"/Device?start=2027-01-01", e.keyA).Entry)

	// Each Device reads back by id; another user's id for the same device
	// is not this user's.
	for id, d := range devices {
		r := e.do("GET", base+"/Device/"+id, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, id)
		var one fhir.Device
		r.json(t, &one)
		assert.Equal(t, d, one)
	}
	other := fhir.DeviceID(ub, read.Device{Provider: "strava", Source: "garmin", DeviceID: "garmin-1"})
	assert.NotEqual(t, garmin, other)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/Device/"+other, e.keyA, nil).code)
	assert.Equal(t, http.StatusOK, e.do("GET", "/v1/users/"+ub.String()+"/fhir/Device/"+other, e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/Device/"+garmin, e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/Device/dev-0000000000000000", e.keyA, nil).code)

	// Every Observation references a listed Device or none, and has one
	// Provenance that reads back by target and by id.
	obs := e.walk(base+"/Observation", e.keyA)
	require.NotEmpty(t, obs)
	for _, o := range obs {
		if o.Device != nil {
			ref, ok := strings.CutPrefix(o.Device.Reference, "Device/")
			require.True(t, ok, o.Device.Reference)
			assert.Contains(t, devices, ref, o.ID)
		}
		b := e.rawBundle(base+"/Provenance?target=Observation/"+o.ID, e.keyA)
		require.Len(t, b.Entry, 1, o.ID)
		var p fhir.Provenance
		require.NoError(t, json.Unmarshal(b.Entry[0].Resource, &p))
		assert.Equal(t, "prov-"+o.ID, p.ID)
		assert.Equal(t, publicURL+base+"/Provenance/"+p.ID, b.Entry[0].FullURL)
		assert.Equal(t, []fhir.Reference{{Reference: "Observation/" + o.ID}}, p.Target)
		assert.Equal(t, o.Issued, p.Recorded)
		last := p.Agent[len(p.Agent)-1]
		assert.Equal(t, "assembler", last.Type.Coding[0].Code)
		if o.Device != nil {
			require.Len(t, p.Agent, 2)
			assert.Equal(t, *o.Device, p.Agent[0].Who)
		} else {
			assert.Len(t, p.Agent, 1)
		}
		assert.Len(t, p.Entity, len(o.Identifier))

		r := e.do("GET", relative(t, b.Entry[0].FullURL), e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code)
		var one fhir.Provenance
		r.json(t, &one)
		assert.Equal(t, p, one)
	}
	assert.Empty(t, e.rawBundle(base+"/Provenance?target=Observation/qs-999999999", e.keyA).Entry)
	assert.Empty(t, e.rawBundle(base+"/Provenance?target=Observation/nope", e.keyA).Entry)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/Provenance/prov-qs-999999999", e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/Provenance/prov-"+obs[0].ID, e.keyB, nil).code)
	ubObs := "/v1/users/" + ub.String() + "/fhir/Provenance/prov-" + obs[0].ID
	assert.Equal(t, http.StatusNotFound, e.do("GET", ubObs, e.keyB, nil).code, "tenant B cannot reach tenant A's rows")
}

func TestFHIREverything(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.PublicURL = publicURL })
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	e.seedDevices(e.tenantA, u)
	base := "/v1/users/" + u.String() + "/fhir"
	everything := base + "/Patient/" + u.String() + "/$everything"
	want := ids(e.walk(base+"/Observation", e.keyA))

	// Every page size gives every Observation once, in the Observation
	// search's order, with the Patient once, on the first page.
	for limit := 1; limit <= len(want)+1; limit++ {
		var got []string
		patients := 0
		pages := 0
		for link := everything + "?limit=" + strconv.Itoa(limit); link != ""; {
			b := e.rawBundle(link, e.keyA)
			pages++
			page := map[string]bool{}
			var refs []string
			provs := 0
			for _, en := range b.Entry {
				var r typed
				require.NoError(t, json.Unmarshal(en.Resource, &r))
				assert.Equal(t, publicURL+base+"/"+r.ResourceType+"/"+r.ID, en.FullURL)
				assert.False(t, page[r.ResourceType+"/"+r.ID], "once per page")
				page[r.ResourceType+"/"+r.ID] = true
				switch r.ResourceType {
				case "Patient":
					patients++
					assert.Equal(t, 1, pages, "the Patient is on the first page")
					assert.Equal(t, "match", en.Search.Mode)
				case "Observation":
					got = append(got, r.ID)
					assert.Equal(t, "match", en.Search.Mode)
					var o fhir.Observation
					require.NoError(t, json.Unmarshal(en.Resource, &o))
					if o.Device != nil {
						refs = append(refs, o.Device.Reference)
					}
					refs = append(refs, "Provenance/prov-"+o.ID)
				case "Device", "Provenance":
					assert.Equal(t, "include", en.Search.Mode)
					if r.ResourceType == "Provenance" {
						provs++
					}
				default:
					t.Errorf("unexpected %s", r.ResourceType)
				}
			}
			for _, ref := range refs {
				assert.True(t, page[ref], "%s is included in its page", ref)
			}
			assert.Equal(t, provs, len(got)-countBefore(got, page), "one Provenance per Observation of the page")
			link = next(fhir.Bundle{Link: b.Link})
			if link != "" {
				link = relative(t, link)
			}
		}
		assert.Equal(t, 1, patients, "limit %d", limit)
		assert.Equal(t, want, got, "limit %d", limit)
	}

	// A window bounds the Observations; the Patient stays.
	b := e.rawBundle(everything+"?start=2026-03-01T13:00:00Z&end=2026-03-01T14:00:00Z", e.keyA)
	var kinds []string
	for _, en := range b.Entry {
		var r typed
		require.NoError(t, json.Unmarshal(en.Resource, &r))
		kinds = append(kinds, r.ResourceType)
	}
	assert.Equal(t, []string{"Patient", "Observation", "Device", "Provenance"}, kinds)
	assert.Equal(t, http.StatusBadRequest, e.do("GET", everything+"?cursor=bm9wZQ", e.keyA, nil).code)
}

// countBefore is the number of Observations of got listed before this page.
func countBefore(got []string, page map[string]bool) int {
	n := 0
	for _, id := range got {
		if !page["Observation/"+id] {
			n++
		}
	}
	return n
}
