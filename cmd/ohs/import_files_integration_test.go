// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/app"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// startInstance runs app.Run with every role on a fresh database and returns
// its base URL and the database URL.
func startInstance(t *testing.T) (string, string) {
	t.Helper()
	url := db.TestDB(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	// A Config built here, not by Load: the vault settings are the ones Load
	// derives from the encryption key alone.
	encKey := make([]byte, 32)
	vaultCfg, err := config.DerivedVault(encKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		DatabaseURL:   url,
		RawStore:      "file://" + t.TempDir(),
		Listen:        addr,
		LogLevel:      "warn",
		PublicURL:     "http://" + addr,
		Roles:         map[string]bool{"api": true, "collect": true, "etl": true},
		EncryptionKey: encKey,
		Vault:         vaultCfg,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return "http://" + addr, url
			}
		}
		select {
		case err := <-done:
			t.Fatalf("server stopped: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("server never became healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// apiCall sends a JSON request and decodes the answer into out.
func apiCall(t *testing.T, method, url, secret, body string, want int, out any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	if res.StatusCode != want {
		t.Fatalf("%s %s: %d, want %d: %s", method, url, res.StatusCode, want, buf.String())
	}
	if out != nil {
		if err := json.Unmarshal(buf.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
	}
}

// TestImportFilesIntoInstance runs `ohs import files` against a running
// instance: the files are stored, mapped by the ETL, and a second run sends
// nothing new.
func TestImportFilesIntoInstance(t *testing.T) {
	base, url := startInstance(t)
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tn, key, err := tenant.New(pool).CreateTenant(ctx, "import-files")
	if err != nil {
		t.Fatal(err)
	}
	var user struct{ ID string }
	apiCall(t, "POST", base+"/v1/users", key, `{"external_ref":"import-files-user"}`, 201, &user)
	var tok struct{ Token string }
	apiCall(t, "POST", base+"/v1/users/"+user.ID+"/tokens", key, `{"scopes":["upload"]}`, 201, &tok)

	dir := activityExport(t)
	var out bytes.Buffer
	if err := run([]string{"import", "files", dir, "--url", base, "--token", tok.Token}, &out); err != nil {
		t.Fatalf("import: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "stored now              5") {
		t.Fatalf("first run:\n%s", out.String())
	}

	wait := func() {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var cov struct {
				PendingJobs int `json:"pending_jobs"`
			}
			apiCall(t, "GET", base+"/v1/users/"+user.ID+"/coverage", key, "", 200, &cov)
			if cov.PendingJobs == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("jobs still pending")
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	type counts struct {
		workouts, routes, hr, power, failed int
		sources                             string
	}
	read := func() counts {
		t.Helper()
		var c counts
		q := func(sql string, dst ...any) {
			t.Helper()
			if err := pool.QueryRow(ctx, sql, tn.ID, user.ID).Scan(dst...); err != nil {
				t.Fatal(err)
			}
		}
		q(`SELECT count(*) FROM ohs_data.workouts WHERE tenant_id = $1 AND user_id = $2 AND provider = 'activity_files'`, &c.workouts)
		// The device of a workout is sealed in the database: the API opens it.
		var page struct {
			Data []struct {
				Provider string  `json:"provider"`
				Source   *string `json:"source"`
				Device   *string `json:"device"`
			} `json:"data"`
		}
		apiCall(t, "GET", base+"/v1/users/"+user.ID+"/workouts?view=all&limit=100", key, "", 200, &page)
		seen := map[string]bool{}
		for _, w := range page.Data {
			if w.Provider != "activity_files" {
				continue
			}
			source, device := "activity_files", "-"
			if w.Source != nil {
				source = *w.Source
			}
			if w.Device != nil {
				device = *w.Device
			}
			seen[source+":"+device] = true
		}
		pairs := make([]string, 0, len(seen))
		for p := range seen {
			pairs = append(pairs, p)
		}
		sort.Strings(pairs)
		c.sources = strings.Join(pairs, ",")
		q(`SELECT coalesce(sum(point_count), 0) FROM ohs_data.series_blobs WHERE tenant_id = $1 AND user_id = $2 AND kind = 2`, &c.routes)
		q(`SELECT count(*) FILTER (WHERE quantity_type = 'heart_rate'),
				count(*) FILTER (WHERE quantity_type IN ('cycling_power', 'running_power'))
			FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2 AND provider = 'activity_files'`, &c.hr, &c.power)
		q(`SELECT count(*) FROM ohs_data.failed_records WHERE tenant_id = $1 AND user_id = $2`, &c.failed)
		return c
	}
	wait()
	first := read()
	// garmin_ride (1 workout), garmin_multisport (2), strava_ride.tcx (1),
	// strava_ride.gpx (1), garmin_run.gpx (1).
	want := counts{
		workouts: 6, routes: 9 + 8 + 3 + 3 + 5, hr: 8 + 8 + 3 + 3 + 5, power: 9 + 4 + 3 + 2,
		sources: "activity_files:-,garmin:-,garmin:Garmin edge_530,garmin:Garmin fr965,strava:-",
	}
	if first != want {
		t.Fatalf("stored %+v, want %+v", first, want)
	}

	// Again: every file is already stored, nothing new is written.
	out.Reset()
	if err := run([]string{"import", "files", dir, "--url", base, "--token", tok.Token}, &out); err != nil {
		t.Fatalf("second import: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "stored now              0") || !strings.Contains(out.String(), "already stored          5") {
		t.Fatalf("second run:\n%s", out.String())
	}
	wait()
	if again := read(); again != first {
		t.Fatalf("second run changed the data: %+v, was %+v", again, first)
	}
}
