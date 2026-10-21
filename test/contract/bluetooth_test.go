// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package contract

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// TestBluetoothMatchesOpenAPI uploads Bluetooth device values to
// POST /v1/ingest/bluetooth, reads back what the ETL decoded, and checks
// every call against api/openapi.yaml. The values are invented readings in
// the GATT Specification Supplement layouts.
func TestBluetoothMatchesOpenAPI(t *testing.T) {
	base, url := startServer(t)
	rec, err := NewRecorder(context.Background(), "../../api/openapi.yaml", base)
	require.NoError(t, err)
	c := &client{t: t, base: base, http: &http.Client{Transport: rec, Timeout: 30 * time.Second}}

	pool, err := db.Open(context.Background(), url)
	require.NoError(t, err)
	_, key, err := tenant.New(pool).CreateTenant(context.Background(), "contract-bluetooth")
	pool.Close()
	require.NoError(t, err)
	r := c.json("POST", "/v1/users", key, `{"external_ref":"contract-bluetooth-user"}`)
	require.Equal(t, 201, r.status, "%s", r.body)
	var user struct{ ID string }
	r.json(t, &user)
	r = c.json("POST", "/v1/users/"+user.ID+"/tokens", key, `{"scopes":["upload"]}`)
	require.Equal(t, 201, r.status, "%s", r.body)
	var tok struct{ Token string }
	r.json(t, &tok)

	strap := map[string]any{"name": "Polar H10 0A1B2C3D", "manufacturer": "Polar Electro Oy", "model": "H10"}
	cuff := map[string]any{"name": "BLEsmart_00000397", "manufacturer": "OMRON HEALTHCARE Co., Ltd.", "model": "HEM-7600T"}
	lineOf := func(at, char, value, session string, dev map[string]any) string {
		b, err := json.Marshal(map[string]any{"at": at, "char": char, "value": value, "session": session, "device": dev})
		require.NoError(t, err)
		return string(b) + "\n"
	}
	lines := lineOf("2026-03-01T08:00:00+01:00", "2a37", "1048"+"0004"+"3303"+"0004", "strap-session-1", strap) + // 72 bpm, RR 1000, 799.805, 1000 ms
		lineOf("2026-03-01T08:00:01+01:00", "2a37", "0049", "strap-session-1", strap) + // 73 bpm
		lineOf("2026-03-01T08:00:02+01:00", "2a19", "50", "strap-session-1", strap) + // battery: not mapped
		// 121/79 mmHg, MAP 93, device time 2026-03-01 07:30:00, pulse 64.
		lineOf("2026-03-01T08:01:00+01:00", "2a35", "06"+"7900"+"4f00"+"5d00"+"ea07"+"03"+"01"+"07"+"1e"+"00"+"4000", "cuff-session-1", cuff)

	part := func(body string) io.Reader {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		_, err := w.Write([]byte(body))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		return &b
	}
	upload := func(secret, typ, batch string, body io.Reader) reply {
		return c.do("POST", "/v1/ingest/bluetooth", secret, body, map[string]string{
			"Content-Type": "application/gzip",
			"X-OHS-Type":   typ, "X-OHS-Day": "2026-03-01", "X-OHS-Batch": batch, "X-OHS-Part": "0",
		})
	}

	type stored struct {
		Key       string `json:"key"`
		Duplicate bool   `json:"duplicate"`
	}
	r = upload(tok.Token, "gatt", "bt1-contract-1", part(lines))
	require.Equal(t, 200, r.status, "%s", r.body)
	var first stored
	r.json(t, &first)
	assert.False(t, first.Duplicate)
	assert.Contains(t, first.Key, "/bluetooth/2026-03-01/bt1-contract-1_0.ndjson.gz")
	r = upload(tok.Token, "gatt", "bt1-contract-1", part(lines))
	require.Equal(t, 200, r.status, "%s", r.body)
	var again stored
	r.json(t, &again)
	assert.True(t, again.Duplicate, "the same part again")

	// Refusals: nothing is stored.
	assert.Equal(t, 400, upload(tok.Token, "gatt", "bt1-bad", part(`{"at":"2026-03-01T08:00:00+01:00","char":"2a37","value":"zz","session":"strap-session-1"}`+"\n")).status, "not hex")
	assert.Equal(t, 400, upload(tok.Token, "fit", "bt1-type", part(lines)).status, "type is not gatt")
	assert.Equal(t, 400, upload(tok.Token, "gatt", "bt1-plain", strings.NewReader("x")).status, "not gzip")
	assert.Equal(t, 401, upload("", "gatt", "bt1-anon", part(lines)).status)
	assert.Equal(t, 403, upload(key, "gatt", "bt1-key", part(lines)).status, "a tenant key cannot upload")
	assert.Equal(t, 409, upload(tok.Token, "gatt", "bt1-contract-1", part(lineOf("2026-03-01T09:00:00+01:00", "2a37", "0050", "strap-session-2", strap))).status,
		"other bytes under a stored key")

	// Processed.
	deadline := time.Now().Add(60 * time.Second)
	for {
		r := c.json("GET", "/v1/users/"+user.ID+"/coverage", key, "")
		require.Equal(t, 200, r.status, "%s", r.body)
		var cov struct {
			PendingJobs int `json:"pending_jobs"`
		}
		r.json(t, &cov)
		if cov.PendingJobs == 0 {
			break
		}
		require.True(t, time.Now().Before(deadline), "jobs still pending: %s", r.body)
		time.Sleep(200 * time.Millisecond)
	}

	type sample struct {
		Value     float64 `json:"value"`
		Unit      string  `json:"unit"`
		StartDate string  `json:"start_date"`
		Provider  string  `json:"provider"`
		Source    string  `json:"source"`
		Device    string  `json:"device"`
	}
	samples := func(typ string) []sample {
		r := c.json("GET", "/v1/users/"+user.ID+"/samples?type="+typ+"&view=all&start=2026-03-01&end=2026-03-02", key, "")
		require.Equal(t, 200, r.status, "%s: %s", typ, r.body)
		var page struct{ Data []sample }
		r.json(t, &page)
		return page.Data
	}
	hr := samples("heart_rate")
	require.Len(t, hr, 3, "two from the strap, the cuff's pulse")
	var strapHR []float64
	for _, s := range hr {
		assert.Equal(t, "bluetooth", s.Provider)
		if s.Source == "polar" {
			assert.Equal(t, "Polar H10", s.Device)
			strapHR = append(strapHR, s.Value)
		}
	}
	assert.ElementsMatch(t, []float64{72, 73}, strapHR)

	sys := samples("blood_pressure_systolic")
	require.Len(t, sys, 1)
	assert.Equal(t, 121.0, sys[0].Value)
	assert.Equal(t, "mmHg", sys[0].Unit)
	assert.Equal(t, "omron", sys[0].Source)
	assert.Equal(t, "OMRON HEM-7600T", sys[0].Device)
	at, err := time.Parse(time.RFC3339, sys[0].StartDate)
	require.NoError(t, err)
	assert.True(t, at.Equal(time.Date(2026, 3, 1, 6, 30, 0, 0, time.UTC)), "the cuff's own clock: %s", sys[0].StartDate)

	r = c.json("GET", "/v1/users/"+user.ID+"/rr-intervals?start=2026-03-01&end=2026-03-02", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	var rr struct {
		Data []struct {
			RRMS     float64 `json:"rr_ms"`
			SeriesID string  `json:"series_id"`
			Provider string  `json:"provider"`
		} `json:"data"`
	}
	r.json(t, &rr)
	require.Len(t, rr.Data, 3, "%s", r.body)
	for _, v := range rr.Data {
		assert.Equal(t, "bluetooth", v.Provider)
		assert.Equal(t, "strap-session-1", v.SeriesID)
	}

	// The provider's data can be erased like any other provider's.
	r = c.json("DELETE", "/v1/users/"+user.ID+"/data/bluetooth", key, "")
	require.Equal(t, 202, r.status, "%s", r.body)

	calls := rec.Calls()
	require.NotEmpty(t, calls)
	for _, call := range calls {
		if len(call.Problems) > 0 {
			t.Errorf("%s %s -> %d (%s):\n  %s", call.Method, call.Path, call.Status, call.Operation,
				strings.Join(call.Problems, "\n  "))
		}
	}
}
