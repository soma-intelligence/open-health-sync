// SPDX-License-Identifier: AGPL-3.0-only

//go:build live

package smartfhir

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// TestLive_SMARTLauncher runs a whole standalone patient launch against the
// public SMART Health IT sandbox (synthetic Synthea patients, no account):
// discovery, authorize (the launcher skips login and consent), PKCE
// exchange, a pull of the whole record and the mapping of every Observation.
//
//	go test -tags live -run TestLive_SMARTLauncher -v ./connectors/smartfhir/
//
// OHS_LIVE_PATIENT picks the sandbox patient (default 4905105).
func TestLive_SMARTLauncher(t *testing.T) {
	patient := os.Getenv("OHS_LIVE_PATIENT")
	if patient == "" {
		patient = "4905105"
	}
	// The launcher's encoded launch parameters (smart-launcher-v2,
	// src/isomorphic/codec.ts): patient-standalone, the patient, skip login
	// and consent, a public client, PKCE always.
	sim, _ := json.Marshal([]any{3, patient, "", "AUTO", 1, 1, 0, "", "", "", "", "", "", "", 0, 2, ""})
	base := "https://launch.smarthealthit.org/v/r4/sim/" + base64.RawURLEncoding.EncodeToString(sim) + "/fhir"

	ctx := context.Background()
	c := New(Options{})
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "ohs-live-test", KeyFHIRBase: base}))

	redirect := "http://localhost:8080/oauth/smart_fhir/callback"
	auth, err := c.AuthorizeURL("live-state", redirect)
	require.NoError(t, err)

	// Follow the launcher's redirects until it sends the browser back to
	// the redirect URI with the code.
	noFollow := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	next, code := auth, ""
	for i := 0; i < 5 && code == ""; i++ {
		resp, err := noFollow.Get(next)
		require.NoError(t, err)
		resp.Body.Close()
		loc, err := resp.Location()
		require.NoError(t, err, "authorize answered %d without a redirect", resp.StatusCode)
		if loc.Host == "localhost:8080" {
			require.Equal(t, "live-state", loc.Query().Get("state"))
			code = loc.Query().Get("code")
			require.NotEmpty(t, code, "redirect without a code: %s", loc.Query().Get("error"))
		}
		next = loc.String()
	}
	require.NotEmpty(t, code)

	tok, err := c.ExchangeState(ctx, code, "live-state", redirect)
	require.NoError(t, err)
	t.Logf("connected: patient %s, %d scopes granted, refresh token: %t, expires %s",
		tok.ProviderUserID, len(tok.Scopes), tok.Refresh != "", tok.Expiry.Format(time.RFC3339))

	var s sink
	now := time.Now().UTC()
	require.NoError(t, c.Fetch(ctx, connector.Connection{Provider: Slug, Tokens: tok}, now.AddDate(-20, 0, 0), now, &s))

	lines := map[string]int{}
	samples, observations := map[string]int{}, map[string]int{}
	for _, o := range s.objects {
		lines[o.dataType] += len(o.lines)
		if o.dataType != DataTypeObservation {
			continue
		}
		for _, l := range o.lines {
			var fields map[string]any
			require.NoError(t, json.Unmarshal(l, &fields))
			r, err := c.Map(connector.RawRecord{UserID: "live", Provider: Slug, DataType: o.dataType, Fields: fields})
			require.NoError(t, err)
			for _, q := range r.QuantitySamples {
				samples[q.QuantityType+" ("+q.Unit+")"]++
			}
			for _, ob := range r.Observations {
				observations[ob.CanonicalCode]++
			}
		}
	}
	t.Logf("raw lines by resource: %v", lines)
	t.Logf("quantity samples: %v", samples)
	t.Logf("generic observations: %d codes, %d records", len(observations), sum(observations))
	require.NotZero(t, lines[DataTypePatient])
	require.NotZero(t, lines[DataTypeObservation])
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
