// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/shl"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// sealedVersions returns the user key versions the sealed rows of table
// (in ohs_data) carry for the fixture's user, ascending. A sealed row
// starts with its format byte, then the version (uint32 big endian).
func sealedVersions(t *testing.T, f goldenFixture, table, where string) []uint32 {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT DISTINCT
		(get_byte(sealed, 1)::bigint << 24) | (get_byte(sealed, 2) << 16) | (get_byte(sealed, 3) << 8) | get_byte(sealed, 4)
		FROM ohs_data.`+table+` WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NOT NULL `+where, f.tenant, f.user)
	require.NoError(t, err)
	defer rows.Close()
	var out []uint32
	for rows.Next() {
		var v int64
		require.NoError(t, rows.Scan(&v))
		out = append(out, uint32(v))
	}
	require.NoError(t, rows.Err())
	slices.Sort(out)
	return out
}

// shareSnapshot creates a share link of every shareable category over the
// golden window, opens its manifest as a recipient and returns the FHIR
// bundle of its file, normalized like the golden outputs (and without the
// bundle's own timestamp, the time of the snapshot).
func shareSnapshot(t *testing.T, f goldenFixture) []byte {
	t.Helper()
	post := func(path, key string, body any) []byte {
		t.Helper()
		b, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, f.srv.URL+path, bytes.NewReader(b))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		out, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.Less(t, res.StatusCode, 300, path+": "+string(out))
		return out
	}
	var created struct {
		URI          string `json:"uri"`
		Observations int    `json:"observations"`
	}
	require.NoError(t, json.Unmarshal(post("/v1/users/"+f.user.String()+"/shares", f.key, map[string]any{
		"categories": shl.Shareable, "start": "2026-09-24T00:00:00Z", "end": "2026-10-01T00:00:00Z",
		"expires_at": time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}), &created))
	require.Positive(t, created.Observations, "the share holds no record")
	p, err := shl.Parse(created.URI)
	require.NoError(t, err)
	u, err := url.Parse(p.URL)
	require.NoError(t, err)

	var m struct {
		Files []struct {
			Embedded string `json:"embedded"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal(post(u.Path, "", map[string]any{"recipient": "Dr. Invented"}), &m))
	require.Len(t, m.Files, 1)
	key, err := shl.DecodeKey(p.Key)
	require.NoError(t, err)
	plain, _, err := shl.Decrypt(key, m.Files[0].Embedded, 64<<20)
	require.NoError(t, err)
	return normalize(plain, "timestamp")
}

// TestReadsAcrossTwoKeyVersions is Review Focus 2 of the vault plan: after
// `ohs vault rotate user`, the user's rows are sealed under two key
// versions, and every read (samples, statistics, the FHIR export, the
// other captured outputs and a share link) opens both and answers what the
// same data answered before the vault. After Reseal, which leaves version 2
// alone, the answers do not change.
func TestReadsAcrossTwoKeyVersions(t *testing.T) {
	want := goldenOutputs(t)
	f := seedFixture(t, fixtureRotated, nil)
	ctx := context.Background()

	// The fixture really holds both versions, in the samples of one type
	// (several providers write heart rate) and in the hourly buckets the
	// statistics read; and nothing is left clear.
	assert.Equal(t, []uint32{1, 2}, sealedVersions(t, f, "quantity_samples", "AND quantity_type = 'heart_rate'"))
	assert.Equal(t, []uint32{1, 2}, sealedVersions(t, f, "quantity_hourly_sealed", "AND quantity_type = 'heart_rate'"))
	st, err := vault.Status(ctx, f.pool)
	require.NoError(t, err)
	assert.Zero(t, st.ClearRows(), "rows left clear: %+v", st)

	compare := func(t *testing.T, got map[string][]byte) {
		t.Helper()
		for name, body := range want {
			out, ok := got[name]
			if !assert.True(t, ok, "%s: the output is no longer captured", name) {
				continue
			}
			if string(body) != string(out) {
				assert.Fail(t, name+": the output differs from the golden file", firstDifference(body, out))
			}
		}
	}

	// The share link has no golden file: it is compared with the link of
	// the same data stored clear.
	clear := shareSnapshot(t, seedGolden(t))

	compare(t, captureOutputs(t, f.srv, f.key, f.user))
	shared := shareSnapshot(t, f)
	if string(clear) != string(shared) {
		assert.Fail(t, "the share link differs from the one of clear rows", firstDifference(clear, shared))
	}

	// Reseal rewrites the version 1 rows under version 2 and retires 1.
	_, err = vault.Reseal(ctx, f.vault, f.pool, f.raw, vault.SealOptions{Rows: etl.Sealer{}})
	require.NoError(t, err)
	assert.Equal(t, []uint32{2}, sealedVersions(t, f, "quantity_samples", ""))
	assert.Equal(t, []uint32{2}, sealedVersions(t, f, "quantity_hourly_sealed", ""))
	compare(t, captureOutputs(t, f.srv, f.key, f.user))
	shared = shareSnapshot(t, f)
	if string(clear) != string(shared) {
		assert.Fail(t, "after reseal, the share link differs from the one of clear rows", firstDifference(clear, shared))
	}
}
