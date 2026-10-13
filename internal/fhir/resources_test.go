// SPDX-License-Identifier: AGPL-3.0-only

package fhir

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

var testTenant = uuid.MustParse("6b1d4c2a-1f0e-4a9b-8c7d-5e4f3a2b1c0d")

// testBase is the default OHS_PUBLIC_URL: the validator refuses example.org
// links.
var testBase = "http://localhost:8080/v1/users/" + testUser.String() + "/fhir"

// watch is a device with every field Apple Health gives.
var watch = read.Device{Provider: "apple_health", Source: "apple", DeviceID: "apple_watch", DeviceModel: "Watch", ProductType: "Watch7,2"}

func TestGoldenPatient(t *testing.T) {
	golden(t, "patient", FromUser(testTenant, testUser, "crm-0042"))
	golden(t, "patient-no-identifier", FromUser(testTenant, testUser, ""))
}

func TestGoldenDevices(t *testing.T) {
	golden(t, "device-apple-watch", FromDevice(testUser, watch))
	golden(t, "device-model-only", FromDevice(testUser, read.Device{Provider: "withings", DeviceModel: "Invented Meter 1"}))
	golden(t, "bundle-device-searchset", DeviceSet(testUser, testBase, testBase+"/Device", []read.Device{
		watch,
		{Provider: "oura", Source: "oura", DeviceID: "oura_ring"},
	}))
}

func TestGoldenProvenance(t *testing.T) {
	recs := recordCases()
	golden(t, "provenance-quantity-sample", FromRecordProvenance(testUser, read.ObservationRecord{Kind: read.KindQuantity, Sample: ptr(goldenCases()["observation-activity-period"])}))
	golden(t, "provenance-blood-pressure-panel", FromRecordProvenance(testUser, recs["observation-vital-blood-pressure-panel"]))
	golden(t, "provenance-daily-summary", FromRecordProvenance(testUser, recs["observation-daily-summary"]))
	p := FromRecordProvenance(testUser, recs["observation-workout"])
	golden(t, "bundle-provenance-searchset", ProvenanceSet(testBase, testBase+"/Provenance?target="+p.Target[0].Reference, []Provenance{p}))
}

func TestGoldenCapabilityStatement(t *testing.T) {
	golden(t, "capabilitystatement", Capability(testBase))
}

// everythingRecords is a page of every kind of record, two from one device.
func everythingRecords() []read.ObservationRecord {
	sys, dia := bpPair()
	hr := sample(1001, "heart_rate", 61, "bpm", "8867-4", "/min")
	sl, w, d := sleepSession(), workout(), dailySummary()
	return []read.ObservationRecord{
		{Kind: read.KindSleep, Sleep: &sl},
		{Kind: read.KindQuantity, Sample: &sys, Partner: &dia},
		{Kind: read.KindQuantity, Sample: &hr},
		{Kind: read.KindWorkout, Workout: &w},
		{Kind: read.KindDaily, Daily: &d},
	}
}

func TestGoldenEverything(t *testing.T) {
	p := FromUser(testTenant, testUser, "crm-0042")
	self := testBase + "/Patient/" + testUser.String() + "/$everything?limit=5"
	golden(t, "bundle-everything-first-page", Everything(testUser, testBase, self, self+"&cursor=abc", &p, everythingRecords()))
	golden(t, "bundle-everything-last-page", Everything(testUser, testBase, self+"&cursor=abc", "", nil, everythingRecords()[4:]))
}

func TestEverythingEntries(t *testing.T) {
	p := FromUser(testTenant, testUser, "")
	b := Everything(testUser, testBase, testBase+"/x", "", &p, everythingRecords())
	raw, err := json.Marshal(b)
	require.NoError(t, err)
	var back SearchBundle[json.RawMessage]
	require.NoError(t, json.Unmarshal(raw, &back))

	var kinds, modes []string
	full := map[string]bool{}
	for _, e := range back.Entry {
		var r struct{ ResourceType, ID string }
		require.NoError(t, json.Unmarshal(e.Resource, &r))
		assert.Equal(t, testBase+"/"+r.ResourceType+"/"+r.ID, e.FullURL, "fullUrl is the base, the type and the id")
		assert.False(t, full[e.FullURL], "each resource once per page: %s", e.FullURL)
		full[e.FullURL] = true
		kinds = append(kinds, r.ResourceType)
		modes = append(modes, e.Search.Mode)
	}
	// The Patient, five Observations, the three devices (the systolic sample
	// and the heart rate share one), five Provenance.
	assert.Equal(t, strings.Fields("Patient Observation Observation Observation Observation Observation "+
		"Device Device Device Provenance Provenance Provenance Provenance Provenance"), kinds)
	assert.Equal(t, strings.Fields("match match match match match match include include include "+
		"include include include include include"), modes)

	// Every reference of the page resolves to an entry of the page.
	for _, e := range b.Entry {
		var refs []string
		switch r := e.Resource.(type) {
		case Observation:
			refs = append(refs, r.Subject.Reference)
			if r.Device != nil {
				refs = append(refs, r.Device.Reference)
			}
		case Device:
			refs = append(refs, r.Patient.Reference)
		case Provenance:
			refs = append(refs, r.Target[0].Reference)
			for _, a := range r.Agent {
				if a.Who.Reference != "" {
					refs = append(refs, a.Who.Reference)
				}
			}
		}
		for _, ref := range refs {
			assert.True(t, full[testBase+"/"+ref], "%s does not resolve in the page", ref)
		}
	}

	// A later page has no Patient.
	b = Everything(testUser, testBase, testBase+"/x", "", nil, everythingRecords()[:1])
	require.Len(t, b.Entry, 3)
	assert.IsType(t, Observation{}, b.Entry[0].Resource)
}

func TestDeviceID(t *testing.T) {
	id := DeviceID(testUser, watch)
	assert.Regexp(t, `^dev-[0-9a-f]{16}$`, id)
	assert.Equal(t, id, DeviceID(testUser, watch), "stable")
	other := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	assert.NotEqual(t, id, DeviceID(other, watch), "per user")
	for _, d := range []read.Device{
		{Provider: "withings", Source: watch.Source, DeviceID: watch.DeviceID, DeviceModel: watch.DeviceModel, ProductType: watch.ProductType},
		{Provider: watch.Provider, DeviceID: watch.DeviceID, DeviceModel: watch.DeviceModel, ProductType: watch.ProductType},
		{Provider: watch.Provider, Source: watch.Source, DeviceModel: watch.DeviceModel, ProductType: watch.ProductType},
		{Provider: watch.Provider, Source: watch.Source, DeviceID: watch.DeviceID, ProductType: watch.ProductType},
		{Provider: watch.Provider, Source: watch.Source, DeviceID: watch.DeviceID, DeviceModel: watch.DeviceModel},
		// Fields do not run into each other.
		{Provider: watch.Provider, Source: watch.Source + "apple_watch", DeviceModel: watch.DeviceModel, ProductType: watch.ProductType},
	} {
		assert.NotEqual(t, id, DeviceID(testUser, d), "%+v", d)
	}
}

func TestObservationDeviceReference(t *testing.T) {
	s := sample(1, "heart_rate", 61, "bpm", "8867-4", "/min")
	s.DeviceModel, s.ProductType = ptr("Watch"), ptr("Watch7,2")
	o := FromQuantitySample(testUser, s)
	d := read.Device{Provider: "apple_health", Source: "apple", DeviceID: "apple_watch", DeviceModel: "Watch", ProductType: "Watch7,2"}
	assert.Equal(t, &Reference{Reference: "Device/" + DeviceID(testUser, d), Display: "apple_watch"}, o.Device)

	// No device id: the model names it, else the product type.
	s.Device = nil
	assert.Equal(t, "Watch", FromQuantitySample(testUser, s).Device.Display)
	s.DeviceModel = nil
	assert.Equal(t, "Watch7,2", FromQuantitySample(testUser, s).Device.Display)
	s.ProductType = ptr("")
	assert.Nil(t, FromQuantitySample(testUser, s).Device, "no device field, no device")

	// A daily summary names no device; its Provenance has the assembler only.
	p := FromRecordProvenance(testUser, read.ObservationRecord{Kind: read.KindDaily, Daily: ptr(dailySummary())})
	require.Len(t, p.Agent, 1)
	assert.Equal(t, "assembler", p.Agent[0].Type.Coding[0].Code)
	assert.Equal(t, "oura", p.Agent[0].Who.Identifier.Value)
	assert.Empty(t, p.Entity, "a daily summary has no external id")
}

func TestProvenanceIDs(t *testing.T) {
	for _, r := range everythingRecords() {
		p := FromRecordProvenance(testUser, r)
		obs, ok := ParseProvenanceID(p.ID)
		require.True(t, ok, p.ID)
		assert.Equal(t, RecordID(r), obs)
		assert.Equal(t, "Observation/"+obs, p.Target[0].Reference)
		assert.Equal(t, FromRecord(testUser, r).Issued, p.Recorded)
	}
	for _, bad := range []string{"", "prov-", "prov-qs-x", "qs-1", "prov-prov-qs-1", "prov-sleep-0"} {
		_, ok := ParseProvenanceID(bad)
		assert.False(t, ok, bad)
	}
}

func TestPatient(t *testing.T) {
	p := FromUser(testTenant, testUser, "crm-0042")
	assert.Equal(t, testUser.String(), p.ID)
	assert.True(t, p.Active)
	assert.Equal(t, []Identifier{{System: Namespace + "/tenant/" + testTenant.String() + "/external-ref", Value: "crm-0042"}}, p.Identifier)
	assert.Empty(t, FromUser(testTenant, testUser, "").Identifier)
}
