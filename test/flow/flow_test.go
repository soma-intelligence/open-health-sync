// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package flow

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/app"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/fhir"
	"github.com/soma-intelligence/open-health-sync/internal/omh"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// The dataset: four heart rate samples from Apple Health and two from Health
// Connect, at known instants (invented values, no real person's data).
var (
	appleHR = []hr{
		{at: "2026-02-03T06:00:00Z", bpm: 58},
		{at: "2026-02-03T06:05:00Z", bpm: 61.5},
		{at: "2026-02-03T12:00:00Z", bpm: 73},
		{at: "2026-02-03T18:30:00Z", bpm: 120},
	}
	connectHR = []hr{
		{at: "2026-02-04T08:00:00Z", bpm: 64},
		{at: "2026-02-04T08:01:00Z", bpm: 66},
	}
)

type hr struct {
	at  string
	bpm float64
}

// want is every heart rate sample the outputs must agree on, by instant.
func want() map[time.Time]float64 {
	m := map[time.Time]float64{}
	for _, s := range slices.Concat(appleHR, connectHR) {
		at, err := time.Parse(time.RFC3339, s.at)
		if err != nil {
			panic(err)
		}
		m[at] = s.bpm
	}
	return m
}

func appleHealthPart() string {
	var b strings.Builder
	for _, s := range appleHR {
		fmt.Fprintf(&b, `{"quantityType":"HKQuantityTypeIdentifierHeartRate","startDate":%q,"endDate":%q,`+
			`"value":%v,"unit":"count/min","sourceName":"Flow Watch","sourceBundleId":"com.example.flow",`+
			`"device":"Flow Watch","deviceModel":"Watch","productType":"Watch7,1","wasUserEntered":false,`+
			`"metadata":{"HKTimeZone":"Europe/Paris"}}`+"\n", s.at, s.at, s.bpm)
	}
	return b.String()
}

func healthConnectPart() string {
	var samples []string
	for _, s := range connectHR {
		samples = append(samples, fmt.Sprintf(`{"time":%q,"beatsPerMinute":%v}`, s.at, s.bpm))
	}
	return `{"recordType":"HeartRate","metadata":{"id":"flow-hr-1","dataOrigin":"com.example.band",` +
		`"recordingMethod":"automatic"},"startTime":"` + connectHR[0].at + `","endTime":"` +
		connectHR[len(connectHR)-1].at + `","samples":[` + strings.Join(samples, ",") + "]}\n"
}

// receiver records the deliveries it gets and answers 500 to the very first
// one, so the worker's retry is part of the run.
type receiver struct {
	mu        sync.Mutex
	failed    bool
	attempts  int
	delivered []delivery
}

type delivery struct {
	header  http.Header
	body    []byte
	payload webhook.Payload
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if !r.failed {
		r.failed = true
		http.Error(w, "not yet", http.StatusInternalServerError)
		return
	}
	var p webhook.Payload
	_ = json.Unmarshal(body, &p)
	r.delivered = append(r.delivered, delivery{header: req.Header.Clone(), body: body, payload: p})
	w.WriteHeader(http.StatusNoContent)
}

func (r *receiver) ofType(typ string) []delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []delivery
	for _, d := range r.delivered {
		if d.payload.Type == typ {
			out = append(out, d)
		}
	}
	return out
}

type client struct {
	t    *testing.T
	base string
	key  string
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (r reply) decode(t *testing.T, v any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(r.body, v), "%s", r.body)
}

func (c *client) do(method, path, secret string, body io.Reader, header map[string]string) reply {
	c.t.Helper()
	if !strings.HasPrefix(path, "http") {
		path = c.base + path
	}
	req, err := http.NewRequest(method, path, body)
	require.NoError(c.t, err)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	require.NoError(c.t, err)
	return reply{status: res.StatusCode, header: res.Header, body: b}
}

func (c *client) get(path string) reply {
	c.t.Helper()
	return c.do("GET", path, c.key, nil, nil)
}

func (c *client) post(path, body string) reply {
	c.t.Helper()
	return c.do("POST", path, c.key, strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
}

func gz(t *testing.T, s string) io.Reader {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, err := w.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return &b
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		require.True(t, time.Now().Before(deadline), "timed out waiting for %s", what)
		time.Sleep(100 * time.Millisecond)
	}
}

func startServer(t *testing.T) (string, string) {
	t.Helper()
	dbURL := db.TestDB(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	cfg := config.Config{
		DatabaseURL:         dbURL,
		RawStore:            "file://" + t.TempDir(),
		Listen:              addr,
		LogLevel:            "warn",
		PublicURL:           "http://" + addr,
		Roles:               map[string]bool{"api": true, "collect": true, "etl": true},
		EncryptionKey:       bytes.Repeat([]byte{7}, 32),
		WebhookAllowPrivate: true, // the receiver listens on loopback
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
	base := "http://" + addr
	eventually(t, 30*time.Second, "/healthz", func() bool {
		select {
		case err := <-done:
			t.Fatalf("server stopped: %v", err)
		default:
		}
		res, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == http.StatusOK
	})
	return base, dbURL
}

// TestOneDatasetEveryOutput ingests the dataset through the Apple Health
// and Health Connect upload routes, then checks that the REST samples, the
// FHIR Observations, the Open mHealth data points and the CSV export all
// carry exactly the same heart rate values at the same instants, that a
// signed data.written webhook reached the receiver for each provider, and
// that deleting the user empties every output and announces user.deleted.
func TestOneDatasetEveryOutput(t *testing.T) {
	base, dbURL := startServer(t)
	pool, err := db.Open(context.Background(), dbURL)
	require.NoError(t, err)
	_, key, err := tenant.New(pool).CreateTenant(context.Background(), "flow")
	pool.Close()
	require.NoError(t, err)
	c := &client{t: t, base: base, key: key}

	// A webhook endpoint on a local receiver.
	rcv := &receiver{}
	hook := httptest.NewServer(rcv)
	defer hook.Close()
	r := c.post("/v1/webhooks", `{"url":"`+hook.URL+`/hooks","event_types":["data.written","user.deleted"]}`)
	require.Equal(t, http.StatusCreated, r.status, "%s", r.body)
	var endpoint struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	r.decode(t, &endpoint)

	// A user and an upload token.
	r = c.post("/v1/users", `{"external_ref":"flow-user"}`)
	require.Equal(t, http.StatusCreated, r.status, "%s", r.body)
	var user struct {
		ID string `json:"id"`
	}
	r.decode(t, &user)
	u := "/v1/users/" + user.ID
	r = c.post(u+"/tokens", `{"scopes":["upload"]}`)
	require.Equal(t, http.StatusCreated, r.status, "%s", r.body)
	var tok struct {
		Token string `json:"token"`
	}
	r.decode(t, &tok)

	// Ingest: Apple Health twice (the second is a duplicate), Health Connect once.
	uploadApple := func() reply {
		return c.do("POST", "/v1/ingest/apple-health", tok.Token, gz(t, appleHealthPart()), map[string]string{
			"Content-Type": "application/gzip", "X-OHS-Type": "HKQuantityTypeIdentifierHeartRate",
			"X-OHS-Day": "2026-02-03", "X-OHS-Batch": "flow", "X-OHS-Part": "0",
		})
	}
	r = uploadApple()
	require.Equal(t, http.StatusOK, r.status, "%s", r.body)
	r = c.do("POST", "/v1/ingest/health-connect", tok.Token, gz(t, healthConnectPart()), map[string]string{
		"Content-Type": "application/gzip", "X-OHS-Type": "HeartRate",
		"X-OHS-Day": "2026-02-04", "X-OHS-Batch": "flow", "X-OHS-Part": "1",
	})
	require.Equal(t, http.StatusOK, r.status, "%s", r.body)
	r = uploadApple()
	require.Equal(t, http.StatusOK, r.status, "the duplicate is accepted: %s", r.body)

	waitProcessed(t, c, u)
	expected := want()

	t.Run("REST samples", func(t *testing.T) {
		got := map[time.Time]float64{}
		providers := map[string]int{}
		next := u + "/samples?type=heart_rate&limit=4"
		for next != "" {
			r := c.get(next)
			require.Equal(t, http.StatusOK, r.status, "%s", r.body)
			var page struct {
				Data []struct {
					Value     float64   `json:"value"`
					Unit      *string   `json:"unit"`
					StartDate time.Time `json:"start_date"`
					Provider  string    `json:"provider"`
				} `json:"data"`
				NextCursor *string `json:"next_cursor"`
			}
			r.decode(t, &page)
			for _, s := range page.Data {
				_, dup := got[s.StartDate.UTC()]
				assert.False(t, dup, "one row per instant, the duplicate upload stored nothing: %v", s.StartDate)
				got[s.StartDate.UTC()] = s.Value
				providers[s.Provider]++
				require.NotNil(t, s.Unit)
				assert.Equal(t, "bpm", *s.Unit, "the canonical unit, whatever the provider sent")
			}
			next = ""
			if page.NextCursor != nil {
				next = u + "/samples?type=heart_rate&limit=4&cursor=" + url.QueryEscape(*page.NextCursor)
			}
		}
		assert.Equal(t, expected, got)
		assert.Equal(t, map[string]int{"apple_health": len(appleHR), "health_connect": len(connectHR)}, providers)
	})

	t.Run("FHIR Observations", func(t *testing.T) {
		got := map[time.Time]float64{}
		next := u + "/fhir/Observation?type=heart_rate&limit=4"
		for next != "" {
			r := c.get(next)
			require.Equal(t, http.StatusOK, r.status, "%s", r.body)
			assert.Contains(t, r.header.Get("Content-Type"), "application/fhir+json")
			var b struct {
				ResourceType string `json:"resourceType"`
				Type         string `json:"type"`
				Link         []struct {
					Relation string `json:"relation"`
					URL      string `json:"url"`
				} `json:"link"`
				Entry []struct {
					Resource fhir.Observation `json:"resource"`
				} `json:"entry"`
			}
			r.decode(t, &b)
			assert.Equal(t, "Bundle", b.ResourceType)
			for _, e := range b.Entry {
				o := e.Resource
				assert.Equal(t, "Observation", o.ResourceType)
				assert.Equal(t, "Patient/"+user.ID, o.Subject.Reference)
				assert.Contains(t, o.Code.Coding, fhir.Coding{System: fhir.LOINC, Code: "8867-4"})
				require.NotNil(t, o.ValueQuantity)
				require.NotNil(t, o.ValueQuantity.Value)
				assert.Equal(t, "/min", o.ValueQuantity.Code, "UCUM")
				assert.Equal(t, "http://unitsofmeasure.org", o.ValueQuantity.System)
				at := o.EffectiveDateTime
				if at == "" && o.EffectivePeriod != nil {
					at = o.EffectivePeriod.Start
				}
				ts, err := time.Parse(time.RFC3339Nano, at)
				require.NoError(t, err, "effective time %q", at)
				got[ts.UTC()] = *o.ValueQuantity.Value
			}
			next = ""
			for _, l := range b.Link {
				if l.Relation == "next" {
					next = l.URL
				}
			}
		}
		assert.Equal(t, expected, got)
	})

	t.Run("Open mHealth data points", func(t *testing.T) {
		got := map[time.Time]float64{}
		next := u + "/omh/data-points?schema=omh:heart-rate&limit=4"
		for next != "" {
			r := c.get(next)
			require.Equal(t, http.StatusOK, r.status, "%s", r.body)
			var page struct {
				Data       []omh.DataPoint `json:"data"`
				NextCursor *string         `json:"next_cursor"`
			}
			r.decode(t, &page)
			for _, p := range page.Data {
				assert.Equal(t, "heart-rate", p.Header.SchemaID.Name)
				assert.Equal(t, user.ID, p.Header.UserID)
				v, ok := p.Body["heart_rate"].(map[string]any)
				require.True(t, ok, "body %v", p.Body)
				assert.Equal(t, "beats/min", v["unit"])
				tf, _ := p.Body["effective_time_frame"].(map[string]any)
				at, _ := tf["date_time"].(string)
				if at == "" {
					ti, _ := tf["time_interval"].(map[string]any)
					at, _ = ti["start_date_time"].(string)
				}
				ts, err := time.Parse(time.RFC3339Nano, at)
				require.NoError(t, err, "effective_time_frame %v", tf)
				got[ts.UTC()] = v["value"].(float64)
			}
			next = ""
			if page.NextCursor != nil {
				next = u + "/omh/data-points?schema=omh:heart-rate&limit=4&cursor=" + url.QueryEscape(*page.NextCursor)
			}
		}
		assert.Equal(t, expected, got)
	})

	t.Run("CSV export", func(t *testing.T) {
		r := c.get(u + "/exports/samples?type=heart_rate")
		require.Equal(t, http.StatusOK, r.status, "%s", r.body)
		rows, err := csv.NewReader(bytes.NewReader(r.body)).ReadAll()
		require.NoError(t, err)
		require.NotEmpty(t, rows)
		col := map[string]int{}
		for i, name := range rows[0] {
			col[name] = i
		}
		got := map[time.Time]float64{}
		for _, row := range rows[1:] {
			assert.Equal(t, "heart_rate", row[col["type"]])
			assert.Equal(t, "bpm", row[col["unit"]])
			ts, err := time.Parse(time.RFC3339Nano, row[col["start_date"]])
			require.NoError(t, err)
			v, err := strconv.ParseFloat(row[col["value"]], 64)
			require.NoError(t, err)
			got[ts.UTC()] = v
		}
		assert.Equal(t, expected, got)
	})

	t.Run("data.written webhooks, signed, after one retry", func(t *testing.T) {
		eventually(t, 60*time.Second, "a data.written delivery per provider", func() bool {
			seen := map[string]bool{}
			for _, d := range rcv.ofType(webhook.DataWritten) {
				p, _ := d.payload.Data["provider"].(string)
				seen[p] = true
			}
			return seen["apple_health"] && seen["health_connect"]
		})
		for _, d := range rcv.ofType(webhook.DataWritten) {
			assert.NoError(t, webhook.Verify(endpoint.Secret, d.header.Get(webhook.HeaderID),
				d.header.Get(webhook.HeaderTimestamp), d.header.Get(webhook.HeaderSignature), d.body, time.Now()))
			assert.Equal(t, d.payload.ID.String(), d.header.Get(webhook.HeaderID))
			assert.Equal(t, user.ID, d.payload.Data["user_id"])
			assert.Equal(t, "flow-user", d.payload.Data["external_ref"])
			records, _ := d.payload.Data["records"].(map[string]any)
			assert.NotEmpty(t, records, "%s", d.body)
		}
		assert.Len(t, rcv.ofType(webhook.DataWritten), 2, "the duplicate upload wrote nothing, so it announced nothing")
		rcv.mu.Lock()
		attempts := rcv.attempts
		rcv.mu.Unlock()
		assert.Greater(t, attempts, len(rcv.ofType(webhook.DataWritten)), "the refused first attempt was retried")

		// The receiver can see a delivery before the worker records its
		// outcome: read the log until every delivery is settled.
		var log []byte
		retried := false
		eventually(t, 30*time.Second, "the delivery log to settle", func() bool {
			r := c.get("/v1/webhooks/" + endpoint.ID + "/deliveries")
			require.Equal(t, http.StatusOK, r.status, "%s", r.body)
			var deliveries struct {
				Data []struct {
					EventType string `json:"event_type"`
					Status    string `json:"status"`
					Attempts  int    `json:"attempts"`
				} `json:"data"`
			}
			r.decode(t, &deliveries)
			log, retried = r.body, false
			n := 0
			for _, d := range deliveries.Data {
				if d.EventType != webhook.DataWritten {
					continue
				}
				if d.Status != "done" {
					return false
				}
				n++
				retried = retried || d.Attempts > 1
			}
			return n == 2
		})
		assert.True(t, retried, "the delivery log shows the retry: %s", log)
	})

	t.Run("deleting the user empties every output", func(t *testing.T) {
		r := c.do("DELETE", u, key, nil, nil)
		require.Equal(t, http.StatusAccepted, r.status, "%s", r.body)
		var job struct {
			JobID string `json:"job_id"`
		}
		r.decode(t, &job)
		eventually(t, 60*time.Second, "the deletion job", func() bool {
			r := c.get("/v1/deletions/" + job.JobID)
			require.Equal(t, http.StatusOK, r.status, "%s", r.body)
			var dj struct {
				Status string `json:"status"`
			}
			r.decode(t, &dj)
			return dj.Status == "done"
		})
		for _, p := range []string{
			u, u + "/samples?type=heart_rate", u + "/fhir/Observation?type=heart_rate",
			u + "/omh/data-points", u + "/exports/samples",
		} {
			assert.Equal(t, http.StatusNotFound, c.get(p).status, p)
		}
		eventually(t, 30*time.Second, "the user.deleted delivery", func() bool {
			return len(rcv.ofType(webhook.UserDeleted)) == 1
		})
		d := rcv.ofType(webhook.UserDeleted)[0]
		assert.NoError(t, webhook.Verify(endpoint.Secret, d.header.Get(webhook.HeaderID),
			d.header.Get(webhook.HeaderTimestamp), d.header.Get(webhook.HeaderSignature), d.body, time.Now()))
		assert.Equal(t, user.ID, d.payload.Data["user_id"])
	})
}

// waitProcessed polls the coverage route until no job of the user is pending.
func waitProcessed(t *testing.T, c *client, u string) {
	t.Helper()
	eventually(t, 60*time.Second, "the ETL jobs", func() bool {
		r := c.get(u + "/coverage")
		require.Equal(t, http.StatusOK, r.status, "%s", r.body)
		var cov struct {
			PendingJobs int `json:"pending_jobs"`
		}
		r.decode(t, &cov)
		return cov.PendingJobs == 0
	})
}
