// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package vault

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	labconn "github.com/soma-intelligence/open-health-sync/connectors/labs"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/demo"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
	"github.com/soma-intelligence/open-health-sync/internal/vault/series"
)

// The canaries: values no other record holds, planted in the data and then
// looked for in a dump of the database and in the raw store.
const (
	canaryHeartRate = 173.4567
	canaryNote      = "canary-note-5f1c"
	canaryDevice    = "canary-device-9b2e"
	canaryLatitude  = 12.3456789
	canaryLongitude = -98.7654321
	canaryVoltage   = 2345.6789
	canaryLab       = 87.6543
	canaryLabText   = "canary-lab-3e8d"
)

// canaryTexts are the canaries as a dump would print them. The numbers are
// looked for without their sign and as text; canaryBinary covers the heart
// rate as a stored float.
var canaryTexts = []string{"173.4567", canaryNote, canaryDevice, "12.3456789", "98.7654321", "2345.6789",
	"87.6543", canaryLabText}

// canaryBinary is the heart rate as the 8 bytes of a float64, big endian:
// what a sealed row's plaintext, or a binary column, would hold.
func canaryBinary() []byte {
	return binary.BigEndian.AppendUint64(nil, math.Float64bits(canaryHeartRate))
}

// plantCanaries adds the canary heart rate (from the canary device), the
// canary route point and an ECG recording with the canary voltage to the
// demo batches, and returns the plain SHA-256
// content hash of every record of the batches: the hash a release without
// the vault stored, which a sealed instance must not hold anywhere.
func plantCanaries(t *testing.T, batches []demo.Batch) map[string]bool {
	t.Helper()
	planted := 0
	for _, b := range batches {
		mr := b.Records
		if planted&1 == 0 && len(mr.QuantitySamples) > 0 {
			s := mr.QuantitySamples[0]
			s.QuantityType, s.Unit, s.Value = "heart_rate", "bpm", canaryHeartRate
			s.SourceName, s.DeviceID, s.DeviceModel = canaryDevice, canaryDevice, canaryDevice
			s.StartDate, s.EndDate = s.StartDate.Add(13*time.Second), s.StartDate.Add(13*time.Second)
			s.ContentHash = model.ComputeContentHash(map[string]string{"canary": "heart_rate", "provider": b.Provider,
				"value": model.CanonicalFloat(s.Value)})
			mr.QuantitySamples = append(mr.QuantitySamples, s)
			planted |= 1
		}
		if planted&2 == 0 && len(mr.RoutePoints) > 0 {
			p := mr.RoutePoints[0]
			p.Latitude, p.Longitude = canaryLatitude, canaryLongitude
			p.Timestamp = p.Timestamp.Add(500 * time.Millisecond)
			p.ContentHash = ""
			mr.RoutePoints = append(mr.RoutePoints, p)
			planted |= 2
		}
		if planted&4 == 0 && b.Provider == "apple_health" && len(mr.QuantitySamples) > 0 {
			start := mr.QuantitySamples[0].StartDate.Add(17 * time.Second)
			rec := model.ECGRecording{StartDate: start, EndDate: start.Add(3 * time.Second / 512),
				Classification: 1, SamplingFrequency: 512, NumberOfMeasurements: 3,
				ContentHash: model.ComputeContentHash(map[string]string{"canary": "ecg", "provider": b.Provider})}
			mr.ECGRecordings = append(mr.ECGRecordings, rec)
			for k, uv := range []float64{-12.5, canaryVoltage, 40.25} {
				mr.ECGSamples = append(mr.ECGSamples, model.ECGVoltageSample{TimeSinceSampleStart: float64(k) / 512,
					VoltageUV: uv, ParentContentHash: rec.ContentHash})
			}
			planted |= 4
		}
	}
	require.Equal(t, 7, planted, "the demo data has samples and a route to plant the canaries in")

	hashes := map[string]bool{}
	add := func(h string) {
		if len(h) == 64 {
			hashes[h] = true
		}
	}
	for _, b := range batches {
		mr := b.Records
		for _, r := range mr.QuantitySamples {
			add(r.ContentHash)
		}
		for _, r := range mr.CategorySamples {
			add(r.ContentHash)
		}
		for _, r := range mr.SleepSessions {
			add(r.ContentHash)
		}
		for _, r := range mr.Workouts {
			add(r.ContentHash)
		}
		for _, r := range mr.DailySummaries {
			add(r.ContentHash)
		}
		for _, r := range mr.NutritionLogs {
			add(r.ContentHash)
		}
		for _, r := range mr.ECGRecordings {
			add(r.ContentHash)
		}
	}
	require.Greater(t, len(hashes), 1000, "the demo records carry their plain content hashes")
	return hashes
}

// dumpDatabase returns the data of every table of the instance's database,
// as pg_dump --data-only writes it. Without pg_dump on the machine, or with
// one that cannot read this server, it copies every table out instead, which
// is the same data in the same text form.
func dumpDatabase(t *testing.T, f goldenFixture) []byte {
	t.Helper()
	if path, err := exec.LookPath("pg_dump"); err == nil {
		var stderr bytes.Buffer
		cmd := exec.Command(path, "--data-only", "--no-owner", "--dbname", f.dbURL)
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil {
			require.NotEmpty(t, out)
			return out
		}
		t.Logf("pg_dump failed (%v: %s): copying every table out instead", err, firstLine(stderr.String()))
	} else {
		t.Log("pg_dump is not installed: copying every table out instead")
	}
	return copyDatabase(t, f.pool)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// copyDatabase is the fallback of dumpDatabase: COPY ... TO STDOUT of every
// table outside the system schemas.
func copyDatabase(t *testing.T, pool *pgxpool.Pool) []byte {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT quote_ident(schemaname) || '.' || quote_ident(tablename) FROM pg_tables
		WHERE schemaname NOT IN ('pg_catalog', 'information_schema') ORDER BY 1`)
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, tables)
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	var out bytes.Buffer
	for _, table := range tables {
		fmt.Fprintf(&out, "COPY %s FROM stdin;\n", table)
		_, err := conn.Conn().PgConn().CopyTo(ctx, &out, "COPY "+table+" TO STDOUT")
		require.NoError(t, err, table)
	}
	return out.Bytes()
}

// rawFiles reads every file of the raw store's folder, by path.
func rawFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = body
		return nil
	}))
	return out
}

// blobContents decodes the series blobs stored clear (points not NULL) and
// writes their values as text and as float64 bits, so the search sees
// inside the compression: a clear instance's route and ECG canaries are
// found there. sealed counts the blobs stored sealed, which nothing decodes
// without the keys; their bytes are searched as they are, and once more
// through the zstd decoder, which must refuse them.
func blobContents(t *testing.T, pool *pgxpool.Pool) (plain []byte, sealed int) {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT kind, points, sealed FROM ohs_data.series_blobs`)
	require.NoError(t, err)
	defer rows.Close()
	var out bytes.Buffer
	for rows.Next() {
		var kind int16
		var points, sealedBytes []byte
		require.NoError(t, rows.Scan(&kind, &points, &sealedBytes))
		if points == nil {
			sealed++
			out.Write(sealedBytes)
			_, err := series.Decode(series.Kind(kind), sealedBytes)
			assert.Error(t, err, "a sealed blob decodes without the keys")
			continue
		}
		pts, err := series.Decode(series.Kind(kind), points)
		require.NoError(t, err)
		for _, p := range pts {
			for c := range series.MaxCols {
				if p.IsNull(c) {
					continue
				}
				fmt.Fprintf(&out, "%s ", strconv.FormatFloat(p.V[c], 'f', -1, 64))
				out.Write(binary.BigEndian.AppendUint64(nil, math.Float64bits(p.V[c])))
			}
		}
	}
	require.NoError(t, rows.Err())
	return out.Bytes(), sealed
}

var hex64 = regexp.MustCompile(`[0-9a-f]{64}`)

// leaks lists what of the canaries and of the plain content hashes data
// holds; where names it in the messages.
func leaks(where string, data []byte, hashes map[string]bool) []string {
	var found []string
	for _, c := range canaryTexts {
		if bytes.Contains(data, []byte(c)) {
			found = append(found, where+": the canary "+c)
		}
	}
	bin := canaryBinary()
	if bytes.Contains(data, bin) {
		found = append(found, where+": the canary heart rate as a float64")
	}
	if bytes.Contains(data, []byte(hex.EncodeToString(bin))) {
		found = append(found, where+": the canary heart rate as the hex of a float64")
	}
	plain := 0
	for _, h := range hex64.FindAll(data, -1) {
		if hashes[string(h)] {
			plain++
		}
	}
	if plain > 0 {
		found = append(found, fmt.Sprintf("%s: %d plain SHA-256 content hashes", where, plain))
	}
	return found
}

// TestDumpHoldsNoHealthValue is the leak test of the vault: after the demo
// data set and the canaries are ingested (written by the ETL, uploaded
// through the device route, recorded as a time tag) and the seal command has
// run, a pg_dump of the database and the files of the raw store hold none of
// the canaries, as text or as a float, and no plain SHA-256 of a record's
// content. The same search finds them in an instance without the vault, so
// a clean result is a result.
func TestDumpHoldsNoHealthValue(t *testing.T) {
	ctx := context.Background()

	t.Run("an instance without the vault leaks them", func(t *testing.T) {
		var hashes map[string]bool
		f := seedFixture(t, fixtureClear, func(b []demo.Batch) { hashes = plantCanaries(t, b) })
		found := strings.Join(leaks("dump", dumpDatabase(t, f), hashes), "\n")
		for _, want := range []string{"173.4567", canaryDevice, "plain SHA-256"} {
			assert.Contains(t, found, want, "the search must see a clear value")
		}
		// The route and the ECG are compressed series blobs: the canaries
		// are inside them, where the decoded search finds them.
		blobs, sealed := blobContents(t, f.pool)
		assert.Zero(t, sealed)
		found = strings.Join(leaks("series blobs", blobs, hashes), "\n")
		for _, want := range []string{"12.3456789", "98.7654321", "2345.6789"} {
			assert.Contains(t, found, want, "the search must see a value inside a clear blob")
		}
	})

	t.Run("a sealed instance holds none", func(t *testing.T) {
		var hashes map[string]bool
		f := seedFixture(t, fixtureSealed, func(b []demo.Batch) { hashes = plantCanaries(t, b) })
		base := "/v1/users/" + f.user.String()
		do := func(method, path, bearer string, headers map[string]string, body []byte) (int, []byte) {
			t.Helper()
			req, err := http.NewRequest(method, f.srv.URL+path, bytes.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+bearer)
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			res, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer res.Body.Close()
			out, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			return res.StatusCode, out
		}

		// A device upload of the canary heart rate: it lands in the raw
		// store, and the ETL writes it from there.
		token, _, err := f.store.MintUserToken(ctx, f.tenant, f.user, []string{"upload"}, time.Hour)
		require.NoError(t, err)
		line, err := json.Marshal(map[string]any{
			"quantityType": "HKQuantityTypeIdentifierHeartRate", "value": canaryHeartRate, "unit": "count/min",
			"startDate": "2026-09-25T10:00:00.000Z", "endDate": "2026-09-25T10:00:00.000Z",
			"sourceName": canaryDevice, "sourceBundleId": "com.example." + canaryDevice, "device": canaryDevice,
			"deviceModel": canaryDevice, "productType": "Watch7,1", "wasUserEntered": false,
			"metadata": map[string]any{"HKTimeZone": "Europe/Paris", "note": canaryNote},
		})
		require.NoError(t, err)
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, err = zw.Write(append(line, '\n'))
		require.NoError(t, err)
		require.NoError(t, zw.Close())
		code, body := do(http.MethodPost, "/v1/ingest/apple-health", token, map[string]string{
			"Content-Type": "application/gzip", collect.HeaderType: "HKQuantityTypeIdentifierHeartRate",
			collect.HeaderDay: "2026-09-25", collect.HeaderBatch: "canary-1", collect.HeaderPart: "0",
		}, gz.Bytes())
		require.Equal(t, http.StatusOK, code, string(body))
		// A lab result with the canary value, its lab, specimen and
		// reference text: it goes through the raw store and the ETL too.
		lab, err := json.Marshal(map[string]any{"reports": []any{map[string]any{
			"id": "report-1", "loinc": "24331-1", "display": "Lipid panel", "collected_at": "2026-09-25T07:30:00Z",
			"performer": canaryLabText, "results": []any{
				map[string]any{"id": "result-1", "loinc": "2093-3", "value": canaryLab, "unit": "mmol/L",
					"reference_text": canaryLabText, "specimen": canaryLabText, "interpretation": "H"},
				map[string]any{"id": "result-2", "code": "XYZ", "value_string": canaryLabText},
			}}}})
		require.NoError(t, err)
		code, body = do(http.MethodPost, base+"/labs", f.key, map[string]string{"Content-Type": "application/json"}, lab)
		require.Equal(t, http.StatusAccepted, code, string(body))
		q := queue.NewETL(f.pool)
		processed := 0
		// One job of a user runs at a time: claim again until none is left.
		for round := 0; round < 3; round++ {
			for _, tier := range []string{queue.TierHighFreq, queue.TierStandard} {
				jobs, err := q.Claim(ctx, tier, 10)
				require.NoError(t, err)
				for _, j := range jobs {
					_, err := etl.ProcessWith(ctx, f.pool, f.raw, j, etl.Options{Vault: f.vault,
						Lookup: func(slug string) (connector.Connector, bool) {
							switch slug {
							case applehealth.Slug:
								return applehealth.New(), true
							case labconn.Slug:
								return labconn.New(), true
							}
							return nil, false
						}})
					require.NoError(t, err)
					require.NoError(t, q.Complete(ctx, j))
					processed++
				}
			}
		}
		require.Equal(t, 2, processed, "each upload queued one ETL job")

		// A time tag with the canary note.
		tag, err := json.Marshal(map[string]any{"kind": "illness", "label": canaryDevice, "start_at": "2026-09-25T08:00:00Z",
			"note": canaryNote})
		require.NoError(t, err)
		code, body = do(http.MethodPost, base+"/tags", f.key, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "canary-key-1"}, tag)
		require.Equal(t, http.StatusCreated, code, string(body))

		// The seal command converts whatever is still clear (the ECG rows
		// the fixture inserts by hand).
		_, err = vault.Seal(ctx, f.vault, f.pool, f.raw, vault.SealOptions{Rows: etl.Sealer{}})
		require.NoError(t, err)
		st, err := vault.Status(ctx, f.pool)
		require.NoError(t, err)
		require.True(t, st.Sealed(), "rows or raw objects left clear: %+v", st)

		// The canaries are stored, and served: the dump is searched for
		// values the instance really holds.
		for path, want := range map[string][]string{
			base + "/samples?type=heart_rate&view=all&limit=1000&start=2026-09-25T09:59:00Z&end=2026-09-25T10:01:00Z": {"173.4567", canaryDevice, canaryNote},
			base + "/samples?type=heart_rate&view=all&limit=1000":                                                     {"173.4567"},
			base + "/tags":                  {canaryNote},
			base + "/labs":                  {"87.6543", canaryLabText},
			base + "/labs/reports/report-1": {"87.6543", canaryLabText},
			base + "/fhir/Observation?category=laboratory": {"87.6543", canaryLabText},
		} {
			code, body := do(http.MethodGet, path, f.key, nil, nil)
			require.Equal(t, http.StatusOK, code, path+": "+string(body))
			for _, w := range want {
				require.Contains(t, string(body), w, path)
			}
		}
		code, body = do(http.MethodGet, base+"/workouts?limit=1000", f.key, nil, nil)
		require.Equal(t, http.StatusOK, code, string(body))
		var workouts struct {
			Data []struct {
				ID json.Number `json:"id"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(body, &workouts))
		served := false
		for _, w := range workouts.Data {
			_, body := do(http.MethodGet, base+"/workouts/"+w.ID.String()+"/route?limit=1000", f.key, nil, nil)
			served = served || (strings.Contains(string(body), "12.3456789") && strings.Contains(string(body), "-98.7654321"))
		}
		require.True(t, served, "the canary route point is served")
		code, body = do(http.MethodGet, base+"/ecg?limit=1000", f.key, nil, nil)
		require.Equal(t, http.StatusOK, code, string(body))
		var ecgs struct {
			Data []struct {
				ID json.Number `json:"id"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(body, &ecgs))
		served = false
		for _, e := range ecgs.Data {
			_, body := do(http.MethodGet, base+"/ecg/"+e.ID.String(), f.key, nil, nil)
			served = served || strings.Contains(string(body), "2345.6789")
		}
		require.True(t, served, "the canary ECG voltage is served")

		var found []string
		found = append(found, leaks("pg_dump", dumpDatabase(t, f), hashes)...)
		blobs, sealed := blobContents(t, f.pool)
		require.NotZero(t, sealed, "the route and the ECG are sealed blobs")
		found = append(found, leaks("series blobs", blobs, hashes)...)
		files := rawFiles(t, f.rawDir)
		require.NotEmpty(t, files, "the upload is in the raw store")
		for path, data := range files {
			found = append(found, leaks(path, data, hashes)...)
			// A sealed object, not the plain gzip a release without the
			// vault stored: gzip would hide the canary from the search.
			if !bytes.HasPrefix(data, []byte("OHSV")) {
				found = append(found, path+": not a sealed object")
			}
			if zr, err := gzip.NewReader(bytes.NewReader(data)); err == nil {
				if plain, err := io.ReadAll(zr); err == nil {
					found = append(found, path+": readable gzip")
					found = append(found, leaks(path+" (unzipped)", plain, hashes)...)
				}
			}
		}
		assert.Empty(t, found, "health values readable without the keys")
	})
}
