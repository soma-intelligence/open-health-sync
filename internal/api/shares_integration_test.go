// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/shl"
)

// shareBase is the time of the invented records the share tests read.
var shareBase = time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)

// shareWindow is the start and end of a share around shareBase.
func shareWindow() (string, string) {
	return shareBase.Add(-24 * time.Hour).Format(time.RFC3339), shareBase.Add(24 * time.Hour).Format(time.RFC3339)
}

// seedShare stores invented records of several categories from the two
// providers the test binary registers: three heart rate samples and one
// step count (apple_health, in the window), one heart rate sample a month
// earlier, a blood pressure pair and a sleep session (health_connect), and a
// workout (apple_health).
func (e *env) seedShare(tn, u uuid.UUID) {
	e.t.Helper()
	for i := range 3 {
		e.exec(`INSERT INTO ohs_data.quantity_samples
			(tenant_id, user_id, provider, source_slug, device_id, device_model, quantity_type, value, unit, start_date, content_hash)
			VALUES ($1, $2, 'apple_health', 'apple', 'watch-1', 'Invented Watch', 'heart_rate', $3, 'count/min', $4, $5)`,
			tn, u, 60.0+float64(i), shareBase.Add(time.Duration(i)*time.Hour), fmt.Sprintf("share-hr-%d", i))
	}
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, quantity_type, value, unit, start_date, content_hash)
		VALUES ($1, $2, 'apple_health', 'heart_rate', 70, 'count/min', $3, 'share-hr-old'),
		       ($1, $2, 'apple_health', 'step_count', 1234, 'count', $4, 'share-steps')`,
		tn, u, shareBase.AddDate(0, -1, 0), shareBase)
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, source_slug, device_id, quantity_type, value, unit, start_date, end_date, content_hash)
		VALUES ($1, $2, 'health_connect', 'invented', 'cuff-1', 'blood_pressure_systolic', 118, 'mmHg', $3, $3, 'share-sys'),
		       ($1, $2, 'health_connect', 'invented', 'cuff-1', 'blood_pressure_diastolic', 76, 'mmHg', $3, $3, 'share-dia')`,
		tn, u, shareBase.Add(5*time.Hour))
	e.exec(`INSERT INTO ohs_data.sleep_sessions
		(tenant_id, user_id, provider, source_slug, bedtime_start, bedtime_end, total_sleep_seconds, content_hash)
		VALUES ($1, $2, 'health_connect', 'invented', $3, $4, 25200, 'share-sleep')`, tn, u, shareBase.Add(-8*time.Hour), shareBase)
	e.exec(`INSERT INTO ohs_data.workouts
		(tenant_id, user_id, provider, activity_type, start_date, end_date, duration_s, content_hash)
		VALUES ($1, $2, 'apple_health', 'running', $3, $4, 1800, 'share-run')`, tn, u, shareBase.Add(2*time.Hour),
		shareBase.Add(150*time.Minute))
}

type createdShareJSON struct {
	shl.Share
	URI       string `json:"uri"`
	ViewerURL string `json:"viewer_url"`
}

type shareDetailJSON struct {
	shl.Share
	RecentAccesses []shl.Access `json:"recent_accesses"`
}

type manifestJSON struct {
	Status string `json:"status"`
	Files  []struct {
		ContentType string `json:"contentType"`
		Embedded    string `json:"embedded"`
		Location    string `json:"location"`
		LastUpdated string `json:"lastUpdated"`
	} `json:"files"`
}

// createShare posts a share of categories over shareWindow and returns it
// with its parsed link.
func (e *env) createShare(u uuid.UUID, body map[string]any) (createdShareJSON, shl.Payload) {
	e.t.Helper()
	start, end := shareWindow()
	req := map[string]any{"start": start, "end": end,
		"expires_at": time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)}
	for k, v := range body {
		req[k] = v
	}
	r := e.do("POST", "/v1/users/"+u.String()+"/shares", e.keyA, req)
	require.Equal(e.t, http.StatusCreated, r.code, string(r.body))
	assert.Equal(e.t, "no-store", r.header.Get("Cache-Control"))
	var c createdShareJSON
	r.json(e.t, &c)
	assert.Equal(e.t, "/v1/users/"+u.String()+"/shares/"+c.ID.String(), r.header.Get("Location"))
	p, err := shl.Parse(c.URI)
	require.NoError(e.t, err)
	v, err := shl.Parse(c.ViewerURL)
	require.NoError(e.t, err)
	require.Equal(e.t, p, v, "the viewer URL carries the same link")
	assert.True(e.t, strings.HasPrefix(c.ViewerURL, "http://example.com/shl/view#shlink:/"))
	return c, p
}

// manifestPath is the path of a link's manifest URL on the test host.
func manifestPath(t *testing.T, p shl.Payload) string {
	t.Helper()
	path, ok := strings.CutPrefix(p.URL, "http://example.com")
	require.True(t, ok, p.URL)
	return path
}

// openShare posts a manifest request and returns the answer.
func (e *env) openShare(p shl.Payload, body map[string]any) resp {
	e.t.Helper()
	return e.do("POST", manifestPath(e.t, p), "", body)
}

// shareBundle decrypts a file with the link's key.
func shareBundle(t *testing.T, p shl.Payload, jwe string) bundleOfAny {
	t.Helper()
	key, err := shl.DecodeKey(p.Key)
	require.NoError(t, err)
	plain, hdr, err := shl.Decrypt(key, jwe, 64<<20)
	require.NoError(t, err)
	assert.Equal(t, shl.Header{Alg: "dir", Enc: "A256GCM", Cty: "application/fhir+json", Zip: "DEF"}, hdr)
	var b bundleOfAny
	require.NoError(t, json.Unmarshal(plain, &b), string(plain))
	return b
}

type bundleOfAny struct {
	ResourceType string `json:"resourceType"`
	Type         string `json:"type"`
	Timestamp    string `json:"timestamp"`
	Entry        []struct {
		FullURL  string         `json:"fullUrl"`
		Resource map[string]any `json:"resource"`
	} `json:"entry"`
}

// kinds counts the entries of each resource type, and lists the
// Observation ids.
func (b bundleOfAny) kinds() (map[string]int, []string) {
	n := map[string]int{}
	var obs []string
	for _, e := range b.Entry {
		rt, _ := e.Resource["resourceType"].(string)
		n[rt]++
		if rt == "Observation" {
			obs = append(obs, e.Resource["id"].(string))
		}
	}
	return n, obs
}

// deliveries returns the type and data of every webhook delivery of the
// tenant whose type starts with prefix, oldest first.
func (e *env) deliveries(tn uuid.UUID, prefix string) ([]string, []map[string]any) {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT event_type, payload FROM ohs_core.webhook_deliveries
		WHERE tenant_id = $1 AND starts_with(event_type, $2) ORDER BY id`, tn, prefix)
	require.NoError(e.t, err)
	defer rows.Close()
	var types []string
	var data []map[string]any
	for rows.Next() {
		var (
			typ string
			raw []byte
		)
		require.NoError(e.t, rows.Scan(&typ, &raw))
		var p struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(e.t, json.Unmarshal(raw, &p))
		types = append(types, typ)
		data = append(data, p.Data)
	}
	require.NoError(e.t, rows.Err())
	return types, data
}

func (e *env) subscribeShares() {
	e.t.Helper()
	r := e.do("POST", "/v1/webhooks", e.keyA, map[string]any{"url": "https://app.example.com/hooks",
		"event_types": []string{"share.created", "share.revoked", "share.accessed"}})
	require.Equal(e.t, http.StatusCreated, r.code, string(r.body))
}

func TestShareCreateOpenRevoke(t *testing.T) {
	e := webhookEnv(t, false)
	e.subscribeShares()
	ctx := context.Background()
	u := e.user(e.tenantA, "invented-ref-1")
	e.seedShare(e.tenantA, u)
	other := e.user(e.tenantA, "invented-ref-2")
	e.seedShare(e.tenantA, other)
	base := "/v1/users/" + u.String() + "/shares"

	c, p := e.createShare(u, map[string]any{"categories": []string{"heart", "blood_pressure"}, "label": "For my coach"})
	assert.Equal(t, "active", c.Status)
	assert.Equal(t, []string{"heart", "blood_pressure"}, c.Categories)
	assert.False(t, c.Passcode)
	assert.Equal(t, 4, c.Observations, "three heart rate samples in the window and one blood pressure panel")
	assert.Positive(t, c.FileBytes)
	assert.Equal(t, "For my coach", c.Label)
	assert.Zero(t, c.Accesses)
	assert.Empty(t, p.Flag, "no passcode, no P")
	assert.Equal(t, "For my coach", p.Label)
	assert.Equal(t, c.ExpiresAt.Unix(), p.Exp)
	manifestID := strings.TrimPrefix(p.URL, "http://example.com/shl/")
	require.True(t, shl.ValidSecret(manifestID), p.URL)

	// The database holds neither the key nor the manifest id.
	var row string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT row_to_json(s)::text FROM ohs_sync.shares s
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, e.tenantA, u, c.ID).Scan(&row))
	assert.NotContains(t, row, p.Key)
	assert.NotContains(t, row, manifestID)
	assert.Contains(t, row, shl.HashSecret(manifestID))
	assert.Contains(t, row, `"sources":["apple_health/heart","health_connect/blood_pressure"]`)

	// The manifest: a reader name is required, the file is embedded.
	r := e.openShare(p, map[string]any{})
	assert.Equal(t, http.StatusBadRequest, r.code, string(r.body))
	r = e.openShare(p, map[string]any{"recipient": strings.Repeat("x", shl.MaxRecipient+1)})
	assert.Equal(t, http.StatusBadRequest, r.code)
	for _, bad := range []string{"two\nlines", "two\u2028lines", "Dr. \u202eevil"} {
		r = e.openShare(p, map[string]any{"recipient": bad})
		assert.Equal(t, http.StatusBadRequest, r.code, "%q", bad)
	}
	// Another method on a public path still carries the public headers.
	for _, mp := range [][2]string{{"PUT", manifestPath(t, p)}, {"GET", manifestPath(t, p)}, {"POST", "/shl/file/abc"}} {
		r = e.do(mp[0], mp[1], "", nil)
		assert.Equal(t, http.StatusMethodNotAllowed, r.code, mp)
		assert.Equal(t, "*", r.header.Get("Access-Control-Allow-Origin"), mp)
	}
	assert.Equal(t, http.StatusBadRequest, e.do("POST", manifestPath(t, p), "", "not json").code)
	r = e.openShare(p, map[string]any{"recipient": "Dr. Invented", "futureMember": true})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, "application/json", r.header.Get("Content-Type"))
	assert.Equal(t, "*", r.header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	var m manifestJSON
	r.json(t, &m)
	assert.Equal(t, "finalized", m.Status)
	require.Len(t, m.Files, 1)
	assert.Equal(t, "application/fhir+json", m.Files[0].ContentType)
	assert.Empty(t, m.Files[0].Location)
	_, err := time.Parse(time.RFC3339, m.Files[0].LastUpdated)
	require.NoError(t, err)

	// The file holds the Patient (no identifier), the shared records only,
	// and their Devices.
	b := shareBundle(t, p, m.Files[0].Embedded)
	assert.Equal(t, "Bundle", b.ResourceType)
	assert.Equal(t, "collection", b.Type)
	kinds, obs := b.kinds()
	assert.Equal(t, map[string]int{"Patient": 1, "Observation": 4, "Device": 2}, kinds)
	assert.Equal(t, map[string]any{"resourceType": "Patient", "id": u.String(), "active": true}, b.Entry[0].Resource)
	assert.Equal(t, "http://example.com/v1/users/"+u.String()+"/fhir/Patient/"+u.String(), b.Entry[0].FullURL)
	panels := 0
	for _, id := range obs {
		if strings.HasPrefix(id, "bp-") {
			panels++
		}
	}
	assert.Equal(t, 1, panels)
	plain, err := json.Marshal(b)
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "invented-ref-1", "the tenant's reference stays in the tenant")
	assert.NotContains(t, string(plain), other.String())
	assert.NotContains(t, string(plain), "running")
	assert.NotContains(t, string(plain), "step_count")

	// A cap below the file's length: a single use location instead.
	r = e.openShare(p, map[string]any{"recipient": "Dr. Invented", "embeddedLengthMax": 10})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	m = manifestJSON{}
	r.json(t, &m)
	require.Empty(t, m.Files[0].Embedded)
	loc, ok := strings.CutPrefix(m.Files[0].Location, "http://example.com/shl/file/")
	require.True(t, ok, m.Files[0].Location)
	assert.Equal(t, http.StatusMethodNotAllowed, e.do("HEAD", "/shl/file/"+loc, "", nil).code, "a HEAD never spends it")
	r = e.do("GET", "/shl/file/"+loc, "", nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, "application/jose", r.header.Get("Content-Type"))
	assert.Equal(t, "*", r.header.Get("Access-Control-Allow-Origin"))
	b = shareBundle(t, p, string(r.body))
	kinds, _ = b.kinds()
	assert.Equal(t, 4, kinds["Observation"])
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/shl/file/"+loc, "", nil).code, "single use")
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/shl/file/"+manifestID, "", nil).code, "not a ticket")
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/shl/file/short", "", nil).code)

	// Unknown and malformed manifest ids: the same 404.
	unknown, err := shl.NewSecret()
	require.NoError(t, err)
	r = e.do("POST", "/shl/"+unknown, "", map[string]any{"recipient": "x"})
	assert.Equal(t, http.StatusNotFound, r.code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", "/shl/abc", "", map[string]any{"recipient": "x"}).code)

	// The owner's list and detail: counts and accesses, never a secret.
	r = e.do("GET", base, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	for _, secret := range []string{p.Key, manifestID, loc, "shlink:/", `"uri"`} {
		assert.NotContains(t, string(r.body), secret)
	}
	var list page[shl.Share]
	r.json(t, &list)
	require.Len(t, list.Data, 1)
	assert.Equal(t, 2, list.Data[0].Accesses, "two served manifests")
	require.NotNil(t, list.Data[0].LastAccessedAt)
	r = e.do("GET", base+"/"+c.ID.String(), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var d shareDetailJSON
	r.json(t, &d)
	require.Len(t, d.RecentAccesses, 2)
	assert.Equal(t, "Dr. Invented", d.RecentAccesses[0].Recipient)
	assert.Equal(t, shl.OutcomeServed, d.RecentAccesses[0].Outcome)

	// Another tenant sees nothing.
	assert.Equal(t, http.StatusNotFound, e.do("GET", base, e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/"+c.ID.String(), e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", base+"/"+c.ID.String(), e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/users/"+other.String()+"/shares/"+c.ID.String(), e.keyA, nil).code,
		"another user's share")
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", base, "", nil).code)

	// Revoke: idempotent, the file goes, the row stays.
	assert.Equal(t, http.StatusNoContent, e.do("DELETE", base+"/"+c.ID.String(), e.keyA, nil).code)
	assert.Equal(t, http.StatusNoContent, e.do("DELETE", base+"/"+c.ID.String(), e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", base+"/"+uuid.NewString(), e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", base+"/not-a-uuid", e.keyA, nil).code)
	r = e.openShare(p, map[string]any{"recipient": "Dr. Invented"})
	assert.Equal(t, http.StatusNotFound, r.code)
	var jweGone bool
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT jwe IS NULL FROM ohs_sync.shares
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, e.tenantA, u, c.ID).Scan(&jweGone))
	assert.True(t, jweGone)
	d = shareDetailJSON{}
	e.do("GET", base+"/"+c.ID.String(), e.keyA, nil).json(t, &d)
	assert.Equal(t, "revoked", d.Status)
	require.NotNil(t, d.RevokedReason)
	assert.Equal(t, "owner", *d.RevokedReason)
	assert.Equal(t, shl.OutcomeInactive, d.RecentAccesses[0].Outcome)

	// Webhooks: thin, no link, key, label or reader.
	types, data := e.deliveries(e.tenantA, "share.")
	assert.Equal(t, []string{"share.created", "share.accessed", "share.accessed", "share.revoked"}, types)
	assert.Equal(t, c.ID.String(), data[0]["share_id"])
	assert.Equal(t, []any{"heart", "blood_pressure"}, data[0]["categories"])
	assert.Equal(t, false, data[0]["passcode"])
	assert.Equal(t, "owner", data[3]["reason"])
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	for _, secret := range []string{p.Key, manifestID, "For my coach", "Dr. Invented"} {
		assert.NotContains(t, string(raw), secret)
	}

	// Audit: the management calls by the key, each access by the link.
	var ap auditPage
	e.do("GET", "/v1/audit?action=share.", e.keyA, nil).json(t, &ap)
	assert.Equal(t, 1, countStatus(ap.Data, audit.ShareCreate, http.StatusCreated))
	assert.Equal(t, 2, countStatus(ap.Data, audit.ShareRevoke, http.StatusNoContent))
	outcomes := map[string]int{}
	for _, en := range ap.Data {
		if en.Action != audit.ShareAccess {
			continue
		}
		assert.Equal(t, audit.ShareLink, en.Actor.Type)
		assert.Equal(t, c.ID.String(), en.Actor.ID)
		require.NotNil(t, en.UserID)
		assert.Equal(t, u, *en.UserID)
		assert.Equal(t, c.ID.String(), en.Detail["share_id"])
		outcomes[en.Detail["outcome"].(string)]++
	}
	assert.Equal(t, map[string]int{"served": 2, "file": 1, "inactive": 2}, outcomes,
		"two manifests, a download, the spent ticket and the revoked link")
	auditRaw, err := json.Marshal(ap)
	require.NoError(t, err)
	assert.NotContains(t, string(auditRaw), "Dr. Invented", "the reader's name stays out of the audit log")
}

func TestSharePasscode(t *testing.T) {
	e := webhookEnv(t, false)
	e.subscribeShares()
	u := e.user(e.tenantA, "a-1")
	e.seedShare(e.tenantA, u)

	start, end := shareWindow()
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for _, bad := range []string{"12345", strings.Repeat("x", 65), "abc\ndef"} {
		r := e.do("POST", "/v1/users/"+u.String()+"/shares", e.keyA, map[string]any{"categories": []string{"sleep"},
			"start": start, "end": end, "expires_at": exp, "passcode": bad})
		assert.Equal(t, http.StatusBadRequest, r.code, bad)
		assert.NotContains(t, string(r.body), bad, "a refusal never echoes the passcode")
	}

	c, p := e.createShare(u, map[string]any{"categories": []string{"sleep"}, "passcode": "invented-passcode"})
	assert.True(t, c.Passcode)
	assert.Equal(t, shl.FlagPasscode, p.Flag)
	var stored string
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT passcode_hash FROM ohs_sync.shares
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, e.tenantA, u, c.ID).Scan(&stored))
	assert.True(t, strings.HasPrefix(stored, "pbkdf2-sha256$600000$"))
	assert.NotContains(t, stored, "invented-passcode")

	remaining := func(r resp) int {
		t.Helper()
		require.Equal(t, http.StatusUnauthorized, r.code, string(r.body))
		assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"))
		var pb struct {
			Status            int `json:"status"`
			RemainingAttempts int `json:"remainingAttempts"`
		}
		r.json(t, &pb)
		assert.Equal(t, http.StatusUnauthorized, pb.Status)
		return pb.RemainingAttempts
	}
	assert.Equal(t, 9, remaining(e.openShare(p, map[string]any{"recipient": "Coach"})), "a missing passcode counts")
	assert.Equal(t, 8, remaining(e.openShare(p, map[string]any{"recipient": "Coach", "passcode": "wrong-one"})))
	r := e.openShare(p, map[string]any{"recipient": "Coach", "passcode": "invented-passcode"})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var m manifestJSON
	r.json(t, &m)
	b := shareBundle(t, p, m.Files[0].Embedded)
	kinds, _ := b.kinds()
	assert.Equal(t, 1, kinds["Observation"], "the sleep session")
	assert.Equal(t, 7, remaining(e.openShare(p, map[string]any{"recipient": "Coach", "passcode": "wrong-two"})),
		"failures are a lifetime count")
	for want := 6; want >= 0; want-- {
		assert.Equal(t, want, remaining(e.openShare(p, map[string]any{"recipient": "Coach", "passcode": "wrong"})))
	}
	r = e.openShare(p, map[string]any{"recipient": "Coach", "passcode": "invented-passcode"})
	assert.Equal(t, http.StatusNotFound, r.code, "disabled after the tenth failure")

	var d shareDetailJSON
	e.do("GET", "/v1/users/"+u.String()+"/shares/"+c.ID.String(), e.keyA, nil).json(t, &d)
	assert.Equal(t, "disabled", d.Status)
	assert.Equal(t, shl.MaxPasscodeFailures, d.PasscodeFailures)
	require.NotNil(t, d.RevokedReason)
	assert.Equal(t, "passcode_attempts", *d.RevokedReason)
	var jweGone bool
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT jwe IS NULL FROM ohs_sync.shares
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, e.tenantA, u, c.ID).Scan(&jweGone))
	assert.True(t, jweGone)
	types, data := e.deliveries(e.tenantA, "share.revoked")
	require.Equal(t, []string{"share.revoked"}, types)
	assert.Equal(t, "passcode_attempts", data[0]["reason"])
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "invented-passcode")

	var ap auditPage
	e.do("GET", "/v1/audit?action=share.access", e.keyA, nil).json(t, &ap)
	assert.Equal(t, 10, countStatus(ap.Data, audit.ShareAccess, http.StatusUnauthorized))
	assert.Equal(t, 1, countStatus(ap.Data, audit.ShareAccess, http.StatusOK))
	assert.Equal(t, 1, countStatus(ap.Data, audit.ShareAccess, http.StatusNotFound))
}

func TestShareRequestRulesAndExpiry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(e.tenantA, "a-1")
	e.seedShare(e.tenantA, u)
	path := "/v1/users/" + u.String() + "/shares"
	start, end := shareWindow()
	now := time.Now().UTC()
	valid := func() map[string]any {
		return map[string]any{"categories": []string{"heart"}, "start": start, "end": end,
			"expires_at": now.Add(time.Hour).Format(time.RFC3339)}
	}
	for name, change := range map[string]func(map[string]any){
		"no categories":       func(b map[string]any) { delete(b, "categories") },
		"ecg":                 func(b map[string]any) { b["categories"] = []string{"ecg"} },
		"location":            func(b map[string]any) { b["categories"] = []string{"heart", "location"} },
		"unknown category":    func(b map[string]any) { b["categories"] = []string{"genome"} },
		"no start":            func(b map[string]any) { delete(b, "start") },
		"end before start":    func(b map[string]any) { b["end"] = shareBase.Add(-48 * time.Hour).Format(time.RFC3339) },
		"bad time":            func(b map[string]any) { b["end"] = "yesterday" },
		"no expiry":           func(b map[string]any) { delete(b, "expires_at") },
		"expired":             func(b map[string]any) { b["expires_at"] = now.Add(-time.Minute).Format(time.RFC3339) },
		"more than 365 days":  func(b map[string]any) { b["expires_at"] = now.Add(366 * 24 * time.Hour).Format(time.RFC3339) },
		"label too long":      func(b map[string]any) { b["label"] = strings.Repeat("a", 81) },
		"label on two lines":  func(b map[string]any) { b["label"] = "a\nb" },
		"unknown member":      func(b map[string]any) { b["flag"] = "L" },
		"categories as words": func(b map[string]any) { b["categories"] = "heart" },
	} {
		body := valid()
		change(body)
		r := e.do("POST", path, e.keyA, body)
		assert.Equal(t, http.StatusBadRequest, r.code, "%s: %s", name, r.body)
	}
	assert.Equal(t, http.StatusNotFound, e.do("POST", "/v1/users/"+e.user(e.tenantB, "b-1").String()+"/shares", e.keyA,
		valid()).code, "another tenant's user")
	assert.Equal(t, http.StatusBadRequest, e.do("GET", path+"?cursor=abc", e.keyA, nil).code)
	assert.Equal(t, http.StatusBadRequest, e.do("GET", path+"?limit=0", e.keyA, nil).code)

	// Paging, oldest first.
	var ids []uuid.UUID
	for range 3 {
		c, _ := e.createShare(u, map[string]any{"categories": []string{"heart"}})
		ids = append(ids, c.ID)
	}
	var pg page[shl.Share]
	e.do("GET", path+"?limit=2", e.keyA, nil).json(t, &pg)
	require.Len(t, pg.Data, 2)
	require.NotNil(t, pg.NextCursor)
	got := []uuid.UUID{pg.Data[0].ID, pg.Data[1].ID}
	cursor := *pg.NextCursor
	pg = page[shl.Share]{}
	e.do("GET", path+"?limit=2&cursor="+cursor, e.keyA, nil).json(t, &pg)
	require.Len(t, pg.Data, 1)
	assert.Nil(t, pg.NextCursor)
	assert.Equal(t, ids, append(got, pg.Data[0].ID))

	// Expiry: the manifest answers 404 from expires_at on, the sweep then
	// removes the file.
	c, p := e.createShare(u, map[string]any{"categories": []string{"heart"}})
	e.exec(`UPDATE ohs_sync.shares SET expires_at = now() - interval '1 second'
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, e.tenantA, u, c.ID)
	assert.Equal(t, http.StatusNotFound, e.openShare(p, map[string]any{"recipient": "Coach"}).code)
	var d shareDetailJSON
	e.do("GET", path+"/"+c.ID.String(), e.keyA, nil).json(t, &d)
	assert.Equal(t, "expired", d.Status)
	n, err := shl.PurgeExpired(ctx, e.pool)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = shl.PurgeExpired(ctx, e.pool)
	require.NoError(t, err)
	assert.Zero(t, n)
	var withFile int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_sync.shares
		WHERE tenant_id = $1 AND user_id = $2 AND jwe IS NOT NULL`, e.tenantA, u).Scan(&withFile))
	assert.Equal(t, 3, withFile, "the three live shares keep their file")

	// An empty window still makes a share: the Patient alone.
	c, p = e.createShare(u, map[string]any{"categories": []string{"glucose"}})
	assert.Zero(t, c.Observations)
	r := e.openShare(p, map[string]any{"recipient": "Coach"})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var m manifestJSON
	r.json(t, &m)
	kinds, _ := shareBundle(t, p, m.Files[0].Embedded).kinds()
	assert.Equal(t, map[string]int{"Patient": 1}, kinds)
}

func TestShareRevokedByConsent(t *testing.T) {
	e := webhookEnv(t, false)
	e.subscribeShares()
	u := e.user(e.tenantA, "a-1")
	e.seedShare(e.tenantA, u)
	heart, _ := e.createShare(u, map[string]any{"categories": []string{"heart"}})
	sleep, sleepLink := e.createShare(u, map[string]any{"categories": []string{"sleep"}})
	consents := "/v1/users/" + u.String() + "/consents/"

	// A change that refuses nothing these shares hold changes nothing.
	r := e.do("PUT", consents+"health_connect", e.keyA, map[string]any{"granted": []string{"sleep", "heart", "blood_pressure"}})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	// Refusing heart from apple_health revokes the heart share only.
	r = e.do("PUT", consents+"apple_health", e.keyA, map[string]any{"granted": []string{"activity", "sleep"}})
	require.Equal(t, http.StatusOK, r.code, string(r.body))

	status := func(id uuid.UUID) (string, string) {
		var d shareDetailJSON
		e.do("GET", "/v1/users/"+u.String()+"/shares/"+id.String(), e.keyA, nil).json(t, &d)
		reason := ""
		if d.RevokedReason != nil {
			reason = *d.RevokedReason
		}
		return d.Status, reason
	}
	s, reason := status(heart.ID)
	assert.Equal(t, "revoked", s)
	assert.Equal(t, "consent", reason)
	s, _ = status(sleep.ID)
	assert.Equal(t, "active", s)
	assert.Equal(t, http.StatusOK, e.openShare(sleepLink, map[string]any{"recipient": "x"}).code)
	types, data := e.deliveries(e.tenantA, "share.revoked")
	require.Equal(t, []string{"share.revoked"}, types)
	assert.Equal(t, heart.ID.String(), data[0]["share_id"])
	assert.Equal(t, "consent", data[0]["reason"])

	// A new heart share leaves out what the consent refuses (its purge is
	// pending here): no record of apple_health's heart rate.
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, quantity_type, value, unit, start_date, content_hash)
		VALUES ($1, $2, 'health_connect', 'heart_rate', 55, 'count/min', $3, 'share-hr-hc')`,
		e.tenantA, u, shareBase.Add(-20*time.Hour))
	again, p := e.createShare(u, map[string]any{"categories": []string{"heart"}})
	assert.Equal(t, 1, again.Observations)
	r = e.openShare(p, map[string]any{"recipient": "x"})
	require.Equal(t, http.StatusOK, r.code)
	var m manifestJSON
	r.json(t, &m)
	b := shareBundle(t, p, m.Files[0].Embedded)
	raw, err := json.Marshal(b)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "/provider/apple_health")
	assert.Contains(t, string(raw), "/provider/health_connect")
}

func TestShareRevokedByProviderErasure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(e.tenantA, "a-1")
	e.seedShare(e.tenantA, u)
	bp, bpLink := e.createShare(u, map[string]any{"categories": []string{"blood_pressure"}})
	_, heartLink := e.createShare(u, map[string]any{"categories": []string{"heart"}})

	r := e.do("DELETE", "/v1/users/"+u.String()+"/data/health_connect", e.keyA, nil)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	raw, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	n, err := deletion.NewWorker(e.pool, raw, nil).Pass(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	assert.Equal(t, http.StatusNotFound, e.openShare(bpLink, map[string]any{"recipient": "x"}).code)
	assert.Equal(t, http.StatusOK, e.openShare(heartLink, map[string]any{"recipient": "x"}).code,
		"a share of another provider's records")
	var reason *string
	var jweGone bool
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT revoked_reason, jwe IS NULL FROM ohs_sync.shares
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, e.tenantA, u, bp.ID).Scan(&reason, &jweGone))
	require.NotNil(t, reason)
	assert.Equal(t, "provider_erased", *reason)
	assert.True(t, jweGone)
	var rows int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_sync.shares
		WHERE tenant_id = $1 AND user_id = $2`, e.tenantA, u).Scan(&rows))
	assert.Equal(t, 2, rows, "the rows stay, for the person's history")

	// A share past its expiry whose file the sweep has not removed yet
	// loses it to the erasure, and stays expired.
	old, _ := e.createShare(u, map[string]any{"categories": []string{"heart"}})
	e.exec(`UPDATE ohs_sync.shares SET expires_at = now() - interval '1 second'
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, e.tenantA, u, old.ID)
	r = e.do("DELETE", "/v1/users/"+u.String()+"/data/apple_health", e.keyA, nil)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	n, err = deletion.NewWorker(e.pool, raw, nil).Pass(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	var status string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT status, revoked_reason, jwe IS NULL FROM ohs_sync.shares
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, e.tenantA, u, old.ID).Scan(&status, &reason, &jweGone))
	assert.Equal(t, "active", status, "read back as expired")
	assert.Nil(t, reason)
	assert.True(t, jweGone)
}

func TestShareGoesWithTheUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(e.tenantA, "a-1")
	e.seedShare(e.tenantA, u)
	c, p := e.createShare(u, map[string]any{"categories": []string{"heart"}})
	require.Equal(t, http.StatusOK, e.openShare(p, map[string]any{"recipient": "Coach"}).code)

	require.Equal(t, http.StatusAccepted, e.do("DELETE", "/v1/users/"+u.String(), e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.openShare(p, map[string]any{"recipient": "Coach"}).code,
		"a user being deleted shares nothing")
	raw, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	n, err := deletion.NewWorker(e.pool, raw, nil).Pass(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	for _, table := range []string{"ohs_sync.shares", "ohs_sync.share_accesses"} {
		var left int
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1 AND user_id = $2`,
			e.tenantA, u).Scan(&left))
		assert.Zero(t, left, table)
	}
	assert.Equal(t, http.StatusNotFound, e.openShare(p, map[string]any{"recipient": "Coach"}).code)
	// The audit log outlives the user, naming the share by id only.
	var accesses int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.audit_events
		WHERE tenant_id = $1 AND user_id = $2 AND action = 'share.access' AND actor_id = $3`,
		e.tenantA, u, c.ID.String()).Scan(&accesses))
	assert.Equal(t, 2, accesses)
}

// A sleep session, a workout or a daily summary carries figures of other
// categories: each one is shared, consented to and revoked as its own
// category.
func TestShareFiguresFollowTheirCategory(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	e.exec(`INSERT INTO ohs_data.sleep_sessions
		(tenant_id, user_id, provider, source_slug, bedtime_start, bedtime_end, total_sleep_seconds,
		 resting_hr, hrv_avg_ms, spo2_avg, content_hash)
		VALUES ($1, $2, 'health_connect', 'invented', $3, $4, 25200, 51.5, 77.25, 96.5, 'share-sleep-figures')`,
		e.tenantA, u, shareBase.Add(-8*time.Hour), shareBase)
	consents := "/v1/users/" + u.String() + "/consents/health_connect"
	r := e.do("PUT", consents, e.keyA, map[string]any{"granted": []string{"sleep", "respiratory"}})
	require.Equal(t, http.StatusOK, r.code, string(r.body))

	file := func(p shl.Payload) string {
		t.Helper()
		r := e.openShare(p, map[string]any{"recipient": "Coach"})
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		var m manifestJSON
		r.json(t, &m)
		raw, err := json.Marshal(shareBundle(t, p, m.Files[0].Embedded))
		require.NoError(t, err)
		return string(raw)
	}
	sleepOnly, p := e.createShare(u, map[string]any{"categories": []string{"sleep"}})
	b := file(p)
	assert.Contains(t, b, "25200")
	for _, v := range []string{"51.5", "77.25", "96.5"} {
		assert.NotContains(t, b, v, "a figure of a category the share does not hold")
	}
	withSpO2, p := e.createShare(u, map[string]any{"categories": []string{"sleep", "heart", "respiratory"}})
	b = file(p)
	assert.Contains(t, b, "96.5")
	for _, v := range []string{"51.5", "77.25"} {
		assert.NotContains(t, b, v, "a figure of a category the consent refuses")
	}
	var sources []string
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT sources FROM ohs_sync.shares
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, e.tenantA, u, withSpO2.ID).Scan(&sources))
	assert.Equal(t, []string{"health_connect/respiratory", "health_connect/sleep"}, sources)

	// Refusing respiratory revokes the share that holds the oxygen
	// saturation, not the one that left it out.
	r = e.do("PUT", consents, e.keyA, map[string]any{"granted": []string{"sleep"}})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	for id, want := range map[uuid.UUID]string{withSpO2.ID: "revoked", sleepOnly.ID: "active"} {
		var d shareDetailJSON
		e.do("GET", "/v1/users/"+u.String()+"/shares/"+id.String(), e.keyA, nil).json(t, &d)
		assert.Equal(t, want, d.Status)
	}
}

// A share is made in a transaction that holds the user and their consent:
// an erasure or a consent change waits for it. A provider queued for
// erasure is left out.
func TestShareLocksAndPendingErasure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(e.tenantA, "a-1")
	e.seedShare(e.tenantA, u)

	tx, err := shl.Begin(ctx, e.pool, e.tenantA, u)
	require.NoError(t, err)
	waits := func(name, sql string, args ...any) {
		t.Helper()
		other, err := e.pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = other.Rollback(ctx) }()
		_, err = other.Exec(ctx, `SET LOCAL lock_timeout = '200ms'`)
		require.NoError(t, err)
		_, err = other.Exec(ctx, sql, args...)
		var pg *pgconn.PgError
		require.ErrorAs(t, err, &pg, name)
		assert.Equal(t, "55P03", pg.Code, name)
	}
	waits("an erasure", `SELECT 1 FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, e.tenantA, u)
	waits("a consent change", `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"ohs.consent:"+e.tenantA.String()+":"+u.String())
	require.NoError(t, tx.Rollback(ctx))
	_, err = shl.Begin(ctx, e.pool, e.tenantB, u)
	assert.ErrorIs(t, err, shl.ErrUserNotFound, "another tenant's user")

	r := e.do("DELETE", "/v1/users/"+u.String()+"/data/health_connect", e.keyA, nil)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	c, _ := e.createShare(u, map[string]any{"categories": []string{"blood_pressure", "heart"}})
	assert.Equal(t, 3, c.Observations, "apple_health's heart rate only: health_connect's blood pressure is being erased")
	var sources []string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT sources FROM ohs_sync.shares
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, e.tenantA, u, c.ID).Scan(&sources))
	assert.Equal(t, []string{"apple_health/heart"}, sources)
}

// A link whose user is locked past shl.LockTimeout answers 503, and a link
// that no longer works fills its access history once a day at most.
func TestShareBusyAndInactiveHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(e.tenantA, "a-1")
	e.seedShare(e.tenantA, u)
	c, p := e.createShare(u, map[string]any{"categories": []string{"heart"}})

	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SELECT 1 FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, e.tenantA, u)
	require.NoError(t, err)
	r := e.openShare(p, map[string]any{"recipient": "Coach"})
	assert.Equal(t, http.StatusServiceUnavailable, r.code, string(r.body))
	assert.Equal(t, "2", r.header.Get("Retry-After"))
	assert.Equal(t, "*", r.header.Get("Access-Control-Allow-Origin"))
	require.NoError(t, tx.Rollback(ctx))
	require.Equal(t, http.StatusOK, e.openShare(p, map[string]any{"recipient": "Coach"}).code)

	path := "/v1/users/" + u.String() + "/shares/" + c.ID.String()
	require.Equal(t, http.StatusNoContent, e.do("DELETE", path, e.keyA, nil).code)
	for range 3 {
		assert.Equal(t, http.StatusNotFound, e.openShare(p, map[string]any{"recipient": "Someone"}).code)
	}
	var d shareDetailJSON
	e.do("GET", path, e.keyA, nil).json(t, &d)
	outcomes := []string{}
	for _, a := range d.RecentAccesses {
		outcomes = append(outcomes, a.Outcome)
	}
	assert.Equal(t, []string{"inactive", "served"}, outcomes)
	var audited int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.audit_events
		WHERE tenant_id = $1 AND user_id = $2 AND action = 'share.access' AND detail->>'outcome' = 'inactive'`,
		e.tenantA, u).Scan(&audited))
	assert.Equal(t, 3, audited, "the audit log has every one")
}
