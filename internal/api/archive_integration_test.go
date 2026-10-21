// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// openArchive reads a zip response body into its files, by name.
func openArchive(t *testing.T, body []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		_, dup := files[f.Name]
		require.False(t, dup, "%s twice", f.Name)
		files[f.Name] = b
	}
	return files
}

// ndjsonLines decodes every line of an NDJSON file.
func ndjsonLines(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var v map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &v), sc.Text())
		out = append(out, v)
	}
	require.NoError(t, sc.Err())
	return out
}

func TestUserArchive(t *testing.T) {
	var raw rawstore.Store
	e := newEnv(t, func(d *api.Deps) { raw = d.Raw; d.Version = "9.9.9-test" })
	ctx := context.Background()
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	// A connection with sealed tokens that must never leave the store.
	e.exec(`INSERT INTO ohs_sync.connections
		(tenant_id, user_id, provider, status, provider_user_id, access_token_enc, refresh_token_enc, token_type, scopes, last_sync_at)
		VALUES ($1, $2, 'withings', 'active', 'prov-user-1', 'sealed-access-x'::bytea, 'sealed-refresh-x'::bytea,
			'Bearer', '{user.metrics,user.activity}', '2026-03-02T00:00:00Z')`, e.tenantA, u)
	// Raw objects, one gzip part and one nested deeper, as providers and devices leave them.
	rawA := []byte{0x1f, 0x8b, 0x08, 0x00, 0x01, 0x02, 0x03, 0x04, 0xfe, 0xff}
	rawB := []byte("{\"invented\":true}\n")
	prefix := "raw/" + e.tenantA.String() + "/" + u.String() + "/"
	require.NoError(t, raw.Put(ctx, prefix+"apple_health/2026-03-01/b1_0.ndjson.gz", bytes.NewReader(rawA)))
	require.NoError(t, raw.Put(ctx, prefix+"withings/2026-03-02/sub/b2_1.ndjson", bytes.NewReader(rawB)))
	// Another user's raw object stays out.
	other := e.user(e.tenantA, "a-2")
	e.seed(e.tenantA, other)
	require.NoError(t, raw.Put(ctx, "raw/"+e.tenantA.String()+"/"+other.String()+"/apple_health/2026-03-01/x_0.ndjson.gz",
		strings.NewReader("other")))

	base := "/v1/users/" + u.String()
	r := e.do("GET", base+"/archive", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, "application/zip", r.header.Get("Content-Type"))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	assert.Regexp(t, regexp.MustCompile(`^attachment; filename="ohs-user-`+u.String()+`-\d{8}T\d{6}Z\.zip"$`),
		r.header.Get("Content-Disposition"))
	files := openArchive(t, r.body)

	// Nothing sealed, no token member, nothing of the other user.
	for name, b := range files {
		assert.NotContains(t, string(b), "sealed-", name)
		assert.NotContains(t, string(b), "access_token", name)
		assert.NotContains(t, string(b), "refresh_token", name)
		assert.NotContains(t, string(b), other.String(), name)
		assert.NotContains(t, name, other.String())
	}

	// Canonical lists: the REST list items, one per line.
	want := map[string]int{
		"canonical/samples.ndjson":          6,
		"canonical/category-samples.ndjson": 1,
		"canonical/sleep.ndjson":            1,
		"canonical/sleep-phases.ndjson":     2,
		"canonical/workouts.ndjson":         1,
		"canonical/workout-routes.ndjson":   3,
		"canonical/nutrition.ndjson":        1,
		"canonical/daily.ndjson":            2,
		"canonical/ecg.ndjson":              0,
	}
	for name, n := range want {
		require.Contains(t, files, name)
		assert.Len(t, ndjsonLines(t, files[name]), n, name)
	}
	var restSamples page[map[string]any]
	e.do("GET", base+"/samples?type=heart_rate&limit=1", e.keyA, nil).json(t, &restSamples)
	first := ndjsonLines(t, files["canonical/samples.ndjson"])[0]
	assert.Equal(t, restSamples.Data[0], first, "same JSON as the REST list")
	assert.Equal(t, "lunch", ndjsonLines(t, files["canonical/nutrition.ndjson"])[0]["meal_type"])

	// user.json: the user as GET /v1/users/{id} returns it, and its connections.
	var uj struct {
		ID          uuid.UUID `json:"id"`
		ExternalRef string    `json:"external_ref"`
		Status      string    `json:"status"`
		Connections []struct {
			Provider       string     `json:"provider"`
			Status         string     `json:"status"`
			ProviderUserID *string    `json:"provider_user_id"`
			Scopes         []string   `json:"scopes"`
			LastSyncAt     *time.Time `json:"last_sync_at"`
			CreatedAt      time.Time  `json:"created_at"`
			UpdatedAt      time.Time  `json:"updated_at"`
		} `json:"connections"`
	}
	require.NoError(t, json.Unmarshal(files["user.json"], &uj))
	assert.Equal(t, u, uj.ID)
	assert.Equal(t, "a-1", uj.ExternalRef)
	assert.Equal(t, "active", uj.Status)
	require.Len(t, uj.Connections, 1)
	assert.Equal(t, "withings", uj.Connections[0].Provider)
	assert.Equal(t, []string{"user.metrics", "user.activity"}, uj.Connections[0].Scopes)
	require.NotNil(t, uj.Connections[0].LastSyncAt)
	var userMembers map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(files["user.json"], &userMembers))
	var getUser map[string]json.RawMessage
	e.do("GET", base, e.keyA, nil).json(t, &getUser)
	for k, v := range getUser {
		assert.JSONEq(t, string(v), string(userMembers[k]), k)
	}
	var connMembers []map[string]any
	require.NoError(t, json.Unmarshal(userMembers["connections"], &connMembers))
	for k := range connMembers[0] {
		assert.NotContains(t, k, "token", "connection member %s", k)
	}

	// FHIR: one collection Bundle of every Observation the FHIR search serves.
	var bundle struct {
		ResourceType string `json:"resourceType"`
		Type         string `json:"type"`
		Timestamp    string `json:"timestamp"`
		Entry        []struct {
			FullURL  string `json:"fullUrl"`
			Resource struct {
				ResourceType string `json:"resourceType"`
				ID           string `json:"id"`
				Subject      struct {
					Reference string `json:"reference"`
				} `json:"subject"`
			} `json:"resource"`
		} `json:"entry"`
	}
	require.NoError(t, json.Unmarshal(files["fhir/bundle.json"], &bundle))
	assert.Equal(t, "Bundle", bundle.ResourceType)
	assert.Equal(t, "collection", bundle.Type)
	assert.NotEmpty(t, bundle.Timestamp)
	var search struct {
		Entry []json.RawMessage `json:"entry"`
	}
	e.do("GET", base+"/fhir/Observation?limit=1000", e.keyA, nil).json(t, &search)
	require.Len(t, bundle.Entry, len(search.Entry))
	assert.Len(t, bundle.Entry, 11, "six samples, a sleep session, a workout, a meal, two days")
	for _, en := range bundle.Entry {
		assert.Equal(t, "Observation", en.Resource.ResourceType)
		assert.Equal(t, "Patient/"+u.String(), en.Resource.Subject.Reference)
		assert.True(t, strings.HasSuffix(en.FullURL, "/v1/users/"+u.String()+"/fhir/Observation/"+en.Resource.ID), en.FullURL)
	}

	// Open mHealth: the data points the omh list serves.
	var omhPage page[json.RawMessage]
	e.do("GET", base+"/omh/data-points?limit=1000", e.keyA, nil).json(t, &omhPage)
	assert.Len(t, ndjsonLines(t, files["omh/data-points.ndjson"]), len(omhPage.Data))

	// Raw objects: the exact bytes, under their path below the user prefix.
	assert.Equal(t, rawA, files["raw/apple_health/2026-03-01/b1_0.ndjson.gz"])
	assert.Equal(t, rawB, files["raw/withings/2026-03-02/sub/b2_1.ndjson"])

	// The manifest counts every file and describes every folder.
	var m struct {
		UserID      uuid.UUID `json:"user_id"`
		ExternalRef *string   `json:"external_ref"`
		CreatedAt   time.Time `json:"created_at"`
		GeneratedAt time.Time `json:"generated_at"`
		Version     string    `json:"version"`
		Files       []struct {
			Path    string `json:"path"`
			Records int    `json:"records"`
		} `json:"files"`
		Raw struct {
			Objects int   `json:"objects"`
			Bytes   int64 `json:"bytes"`
		} `json:"raw"`
		Folders map[string]string `json:"folders"`
	}
	require.NoError(t, json.Unmarshal(files["manifest.json"], &m))
	assert.Equal(t, u, m.UserID)
	require.NotNil(t, m.ExternalRef)
	assert.Equal(t, "a-1", *m.ExternalRef)
	assert.False(t, m.CreatedAt.IsZero())
	assert.WithinDuration(t, time.Now(), m.GeneratedAt, time.Minute)
	assert.Equal(t, "9.9.9-test", m.Version)
	counts := map[string]int{}
	for _, f := range m.Files {
		counts[f.Path] = f.Records
		assert.Contains(t, files, f.Path)
	}
	for name, n := range want {
		assert.Equal(t, n, counts[name], name)
	}
	assert.Equal(t, 1, counts["user.json"])
	assert.Equal(t, 11, counts["fhir/bundle.json"])
	assert.Equal(t, len(omhPage.Data), counts["omh/data-points.ndjson"])
	assert.Equal(t, 2, m.Raw.Objects)
	assert.EqualValues(t, len(rawA)+len(rawB), m.Raw.Bytes)
	for _, folder := range []string{"canonical/", "fhir/", "omh/", "raw/"} {
		assert.NotEmpty(t, m.Folders[folder], folder)
	}
	// Every file in the zip is the manifest, user.json, a counted file or a raw object.
	for name := range files {
		_, counted := counts[name]
		assert.True(t, counted || name == "manifest.json" || strings.HasPrefix(name, "raw/"), name)
	}
}

func TestUserArchiveEmptyUser(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "")
	r := e.do("GET", "/v1/users/"+u.String()+"/archive", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	files := openArchive(t, r.body)
	for _, name := range []string{"manifest.json", "user.json", "canonical/samples.ndjson", "fhir/bundle.json"} {
		assert.Contains(t, files, name)
	}
	assert.Empty(t, files["canonical/samples.ndjson"])
	// An empty Bundle has no entry member (FHIR JSON has no empty arrays).
	var bundle map[string]any
	require.NoError(t, json.Unmarshal(files["fhir/bundle.json"], &bundle))
	assert.Equal(t, "collection", bundle["type"])
	assert.NotContains(t, bundle, "entry")
	var uj map[string]any
	require.NoError(t, json.Unmarshal(files["user.json"], &uj))
	assert.Nil(t, uj["external_ref"])
	assert.Equal(t, []any{}, uj["connections"])
}

func TestUserArchiveAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	mate := e.user(e.tenantA, "a-2")
	path := "/v1/users/" + u.String() + "/archive"

	// The user's own token with scope read.
	read, _, err := e.store.MintUserToken(ctx, e.tenantA, u, []string{"read"}, time.Hour)
	require.NoError(t, err)
	r := e.do("GET", path, read, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Contains(t, openArchive(t, r.body), "manifest.json")

	// Another user's token, even with read: that user is not theirs.
	mateRead, _, err := e.store.MintUserToken(ctx, e.tenantA, mate, []string{"read"}, time.Hour)
	require.NoError(t, err)
	r = e.do("GET", path, mateRead, nil)
	assert.Equal(t, http.StatusNotFound, r.code)
	assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"))
	assert.NotContains(t, string(r.body), "heart_rate")

	// A token without read.
	upload, _, err := e.store.MintUserToken(ctx, e.tenantA, u, []string{"upload", "connect"}, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, e.do("GET", path, upload, nil).code)

	// Another tenant, no credential, unknown and malformed ids.
	assert.Equal(t, http.StatusNotFound, e.do("GET", path, e.keyB, nil).code)
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", path, "", nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/users/"+uuid.NewString()+"/archive", e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/users/not-a-uuid/archive", e.keyA, nil).code)

	// A user being deleted reads as absent.
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u)
	r = e.do("GET", path, e.keyA, nil)
	assert.Equal(t, http.StatusNotFound, r.code)
	assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"))
}

// failingRaw is a raw store whose objects cannot be read.
type failingRaw struct{ rawstore.Store }

func (f failingRaw) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("disk on fire")
}

// TestUserArchiveAbortsMidStream: a failure once the archive has started
// cuts the response instead of finishing a zip that looks complete.
func TestUserArchiveAbortsMidStream(t *testing.T) {
	var raw rawstore.Store
	e := newEnv(t, func(d *api.Deps) {
		raw = d.Raw
		d.Raw = failingRaw{d.Raw}
	})
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	require.NoError(t, raw.Put(context.Background(),
		"raw/"+e.tenantA.String()+"/"+u.String()+"/apple_health/2026-03-01/b_0.ndjson.gz", strings.NewReader("x")))
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		e.do("GET", "/v1/users/"+u.String()+"/archive", e.keyA, nil)
	})
}
