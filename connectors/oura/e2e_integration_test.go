// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package oura_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/oura"
	"github.com/soma-intelligence/open-health-sync/connectors/oura/ourafake"
	"github.com/soma-intelligence/open-health-sync/internal/app"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// The whole path, through the real app (api, collect and etl roles, a real
// database) and the registered Oura connector pointed at ourafake: connect
// over OAuth, the scheduled backfill pulls, the ETL stores, the public API
// serves the values, units and local days; then a re-scored night arrives by
// webhook and replaces the stored one, a deleted workout and session arrive
// by webhook and are removed, and a later pull duplicates nothing and brings
// nothing back.

const (
	e2eClientID = "e2e-client-id"
	e2eSecret   = "e2e-client-secret"
	e2eVerify   = "e2e-verification-token"
)

// e2eDays are the days of the invented data, relative to today (UTC): the
// backfill covers them, and a webhook's three day pull covers the last two.
type e2eDays struct{ d1, d2 time.Time }

func (d e2eDays) s(t time.Time) string { return t.Format(time.DateOnly) }

// doc returns document i of a recorded answer with fields replaced.
func doc(t *testing.T, dataType string, i int, edits map[string]any) map[string]any {
	t.Helper()
	var p struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(ourafake.File(dataType+".json"), &p))
	m := p.Data[i]
	for k, v := range edits {
		m[k] = v
	}
	return m
}

// seed serves one user's last days, in India Standard Time (+05:30) so a
// local day differs from the UTC day.
func seed(t *testing.T, f *ourafake.Server, d e2eDays, night map[string]any) {
	ist := func(day time.Time, clock string) string { return d.s(day) + "T" + clock + "+05:30" }
	f.SetDocuments("daily_activity", doc(t, "daily_activity", 0, map[string]any{
		"id": "act-1", "day": d.s(d.d2), "timestamp": ist(d.d2, "04:00:00.000"),
		"steps": 8000, "active_calories": 400, "equivalent_walking_distance": 6000,
	}))
	f.SetDocuments("sleep",
		doc(t, "sleep", 0, map[string]any{"id": "sleep-del", "day": d.s(d.d2),
			"bedtime_start": ist(d.d2, "15:00:00"), "bedtime_end": ist(d.d2, "15:30:00")}),
		night,
	)
	f.SetDocuments("daily_sleep", doc(t, "daily_sleep", 0, map[string]any{"id": "ds-1", "day": d.s(d.d1), "timestamp": ist(d.d1, "00:00:00.000"), "score": 77}))
	f.SetDocuments("daily_readiness", doc(t, "daily_readiness", 0, map[string]any{"id": "rd-1", "day": d.s(d.d1), "timestamp": ist(d.d1, "00:00:00.000"), "score": 80}))
	f.SetDocuments("daily_spo2", doc(t, "daily_spo2", 0, map[string]any{"id": "spo2-1", "day": d.s(d.d1), "spo2_percentage": map[string]any{"average": 96.5}}))
	f.SetDocuments("daily_stress", doc(t, "daily_stress", 0, map[string]any{"id": "st-1", "day": d.s(d.d2), "stress_high": 5400, "recovery_high": 1800}))
	f.SetDocuments("workout", doc(t, "workout", 0, map[string]any{"id": "wk-1", "day": d.s(d.d2),
		"start_datetime": ist(d.d2, "18:00:00"), "end_datetime": ist(d.d2, "18:45:00")}))
	f.SetDocuments("session", doc(t, "session", 0, map[string]any{"id": "ses-1", "day": d.s(d.d2),
		"start_datetime": ist(d.d2, "20:00:00"), "end_datetime": ist(d.d2, "20:10:00")}))
	f.SetDocuments("vo2_max", doc(t, "vo2_max", 0, map[string]any{"id": "vo2-1", "day": d.s(d.d2), "timestamp": ist(d.d2, "08:00:00.000"), "vo2_max": 44}))
	var hr []map[string]any
	for i, bpm := range []int{58, 61, 64} {
		at := d.d1.Add(time.Duration(10*60+5*i) * time.Minute)
		hr = append(hr, map[string]any{"bpm": bpm, "source": "awake", "timestamp": at.Format("2006-01-02T15:04:05+00:00"), "timestamp_unix": at.UnixMilli()})
	}
	f.SetDocuments("heartrate", hr...)
}

// nightDoc is the user's main sleep: from 23:00 on d2 to 06:30 on d1, IST.
func nightDoc(t *testing.T, d e2eDays, edits map[string]any) map[string]any {
	m := doc(t, "sleep", 1, map[string]any{
		"id": "night-1", "day": d.s(d.d1),
		"bedtime_start": d.s(d.d2) + "T23:00:00+05:30", "bedtime_end": d.s(d.d1) + "T06:30:00+05:30",
		"total_sleep_duration": 25200, "lowest_heart_rate": 50, "average_breath": 15.0, "average_hrv": 40,
		"sleep_phase_5_min": "442222111133",
	})
	for k, v := range edits {
		m[k] = v
	}
	return m
}

type e2eAPI struct {
	t    *testing.T
	base string
	key  string
	c    *http.Client
}

func (a e2eAPI) do(method, path, body string, header http.Header) (int, []byte) {
	a.t.Helper()
	req, err := http.NewRequest(method, a.base+path, strings.NewReader(body))
	require.NoError(a.t, err)
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	res, err := a.c.Do(req)
	require.NoError(a.t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

// page reads one list endpoint into out's data.
func (a e2eAPI) page(path string, out any) bool {
	code, b := a.do(http.MethodGet, path, "", nil)
	if code != http.StatusOK {
		return false
	}
	return json.Unmarshal(b, &struct {
		Data any `json:"data"`
	}{out}) == nil
}

type apiSample struct {
	Value      float64        `json:"value"`
	Unit       string         `json:"unit"`
	StartDate  time.Time      `json:"start_date"`
	EndDate    time.Time      `json:"end_date"`
	ExternalID string         `json:"external_id"`
	Metadata   map[string]any `json:"metadata"`
}

type apiSleep struct {
	ID         int64     `json:"id"`
	Start      time.Time `json:"bedtime_start"`
	End        time.Time `json:"bedtime_end"`
	Total      *int      `json:"total_sleep_seconds"`
	RestingHR  *float64  `json:"resting_hr"`
	HRV        *float64  `json:"hrv_avg_ms"`
	Resp       *float64  `json:"respiratory_rate"`
	ExternalID string    `json:"external_id"`
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().String()
}

func TestEndToEnd_ConnectPullWebhooks(t *testing.T) {
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	d := e2eDays{d1: today.AddDate(0, 0, -1), d2: today.AddDate(0, 0, -2)}

	f := ourafake.New(t)
	f.Rotate = true
	f.PageSize = 1
	seed(t, f, d, nightDoc(t, d, nil))

	c, ok := connector.Get(oura.Slug)
	require.True(t, ok)
	oura.PointAt(c.(*oura.Connector), f.URL(), f.Client())

	dbURL := db.TestDB(t)
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, key, err := tenant.New(pool).CreateTenant(ctx, "oura-e2e")
	require.NoError(t, err)

	listen := freeAddr(t)
	cfg := config.Config{
		DatabaseURL: dbURL, RawStore: "file://" + t.TempDir(), Listen: listen, LogLevel: "warn",
		PublicURL: "http://" + listen, EncryptionKey: make([]byte, 32),
		Roles: map[string]bool{"api": true, "etl": true, "collect": true},
		Providers: map[string]config.ProviderKeys{oura.Slug: {
			oura.KeyClientID: e2eClientID, oura.KeyClientSecret: e2eSecret, oura.KeyVerificationToken: e2eVerify,
		}},
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the app did not stop")
		}
	})
	api := e2eAPI{t: t, base: "http://" + listen, key: key, c: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	require.Eventually(t, func() bool {
		res, err := http.Get(api.base + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == http.StatusOK
	}, 30*time.Second, 50*time.Millisecond, "the app never became healthy")

	// The subscription challenge.
	code, body := api.do(http.MethodGet, "/inbound/oura?verification_token="+e2eVerify+"&challenge=ch-1", "", nil)
	require.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"challenge":"ch-1"}`, string(body))
	code, _ = api.do(http.MethodGet, "/inbound/oura?verification_token=wrong&challenge=ch-1", "", nil)
	assert.Equal(t, http.StatusUnauthorized, code)

	// --- connect ---
	code, body = api.do(http.MethodPost, "/v1/users", `{"external_ref":"oura-e2e"}`, nil)
	require.Equal(t, http.StatusCreated, code, string(body))
	var user struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &user))
	u := "/v1/users/" + user.ID

	code, body = api.do(http.MethodPost, u+"/connections/oura", `{"redirect_uri":"https://app.example.org/connected"}`, nil)
	require.Equal(t, http.StatusOK, code, string(body))
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	require.NoError(t, json.Unmarshal(body, &start))
	authz, err := url.Parse(start.AuthorizeURL)
	require.NoError(t, err)
	assert.Equal(t, f.URL()+"/oauth/authorize", authz.Scheme+"://"+authz.Host+authz.Path)
	q := authz.Query()
	assert.Equal(t, e2eClientID, q.Get("client_id"))
	assert.Equal(t, "personal daily heartrate workout session spo2Daily", q.Get("scope"))
	assert.Equal(t, api.base+"/oauth/oura/callback", q.Get("redirect_uri"))

	// The user consents: Oura sends the browser back with a code.
	cb := q.Get("redirect_uri") + "?" + url.Values{"state": {q.Get("state")}, "code": {"consent-code-1"}}.Encode()
	res, err := api.c.Get(cb)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusFound, res.StatusCode)
	assert.Contains(t, res.Header.Get("Location"), "status=connected")
	tokenCalls := f.Calls("/oauth/token")
	require.Len(t, tokenCalls, 1)
	assert.Equal(t, "authorization_code", tokenCalls[0].Form.Get("grant_type"))
	assert.Equal(t, e2eSecret, tokenCalls[0].Form.Get("client_secret"))

	code, body = api.do(http.MethodGet, u+"/connections", "", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(body), `"status":"active"`)

	// --- the backfill, through the ETL, read back ---
	rng := "start=" + d.s(today.AddDate(0, 0, -10)) + "&end=" + d.s(today.AddDate(0, 0, 1))
	samples := func(qt string) []apiSample {
		var out []apiSample
		api.page(u+"/samples?type="+qt+"&"+rng, &out)
		return out
	}
	sleeps := func() []apiSleep {
		var out []apiSleep
		api.page(u+"/sleep?"+rng, &out)
		return out
	}
	require.Eventually(t, func() bool {
		return len(sleeps()) == 1 && len(samples("heart_rate")) == 3 && len(samples("vo2_max")) == 1 &&
			len(samples("step_count")) == 1 && len(samples("oxygen_saturation")) == 1
	}, 90*time.Second, 250*time.Millisecond, "the backfill lands")

	ist := time.FixedZone("IST", 5*3600+1800)
	unit := func(qt string) string {
		u, ok := model.StorageUnit(qt)
		require.True(t, ok, qt)
		return u
	}

	steps := samples("step_count")[0]
	assert.Equal(t, 8000.0, steps.Value)
	assert.Equal(t, unit("step_count"), steps.Unit)
	assert.True(t, steps.StartDate.Equal(time.Date(d.d2.Year(), d.d2.Month(), d.d2.Day(), 0, 0, 0, 0, ist)), "the local day starts at local midnight: %s", steps.StartDate)
	assert.Equal(t, "Asia/Kolkata", steps.Metadata["timezone"])
	assert.Equal(t, "+05:30", steps.Metadata["utc_offset"])

	stress := samples("stress_high_time")
	require.Len(t, stress, 1)
	assert.Equal(t, 90.0, stress[0].Value, "5400 s stored in minutes")
	assert.Equal(t, "min", stress[0].Unit)

	spo2 := samples("oxygen_saturation")[0]
	assert.Equal(t, 96.5, spo2.Value)
	assert.Equal(t, unit("oxygen_saturation"), spo2.Unit)
	assert.Equal(t, 44.0, samples("vo2_max")[0].Value)
	assert.Equal(t, 80.0, samples("readiness_score")[0].Value)
	assert.Equal(t, 77.0, samples("sleep_score")[0].Value)

	hr := samples("heart_rate")
	assert.Equal(t, []float64{58, 61, 64}, []float64{hr[0].Value, hr[1].Value, hr[2].Value})
	assert.True(t, hr[0].StartDate.Equal(d.d1.Add(10*time.Hour)))

	night := sleeps()[0]
	assert.Equal(t, "night-1", night.ExternalID)
	assert.True(t, night.Start.Equal(time.Date(d.d2.Year(), d.d2.Month(), d.d2.Day(), 23, 0, 0, 0, ist)))
	assert.True(t, night.End.Equal(time.Date(d.d1.Year(), d.d1.Month(), d.d1.Day(), 6, 30, 0, 0, ist)))
	require.NotNil(t, night.Total)
	assert.Equal(t, 25200, *night.Total)
	assert.Equal(t, 50.0, *night.RestingHR)
	assert.Equal(t, 40.0, *night.HRV)
	phases := func(id int64) []map[string]any {
		var out []map[string]any
		api.page(fmt.Sprintf("%s/sleep/%d/phases?%s", u, id, rng), &out)
		return out
	}
	assert.Len(t, phases(night.ID), 4, "awake, light, deep, rem: one phase per run of the hypnogram")
	rhr := samples("resting_heart_rate")
	require.Len(t, rhr, 1)
	assert.Equal(t, 50.0, rhr[0].Value)
	assert.Equal(t, unit("resting_heart_rate"), rhr[0].Unit)

	var workouts []struct {
		Activity   string   `json:"activity_type"`
		DistanceM  *float64 `json:"distance_m"`
		Kcal       *float64 `json:"active_energy_kcal"`
		ExternalID string   `json:"external_id"`
	}
	require.True(t, api.page(u+"/workouts?"+rng, &workouts))
	require.Len(t, workouts, 1)
	assert.Equal(t, "running", workouts[0].Activity)
	assert.Equal(t, 5000.0, *workouts[0].DistanceM)
	assert.Equal(t, 400.0, *workouts[0].Kcal)

	mindful := func() int {
		var out []map[string]any
		api.page(u+"/category-samples?type=mindful_session&"+rng, &out)
		return len(out)
	}
	assert.Equal(t, 1, mindful())

	var daily []struct {
		Date      string `json:"date"`
		StepCount *int   `json:"step_count"`
	}
	require.True(t, api.page(u+"/daily?"+rng, &daily))
	foundDay := false
	for _, row := range daily {
		if row.Date == d.s(d.d2) && row.StepCount != nil {
			assert.Equal(t, 8000, *row.StepCount)
			foundDay = true
		}
		assert.NotEqual(t, d.s(d.d2.AddDate(0, 0, -1)), row.Date, "the steps are not on the UTC day before")
	}
	assert.True(t, foundDay, "a daily row on the user's local day: %+v", daily)

	// --- webhooks ---
	notify := func(event, dataType, objectID string) {
		t.Helper()
		b := fmt.Sprintf(`{"event_type":%q,"data_type":%q,"object_id":%q,"event_time":%q,"user_id":"fake-oura-user-0001"}`,
			event, dataType, objectID, time.Now().UTC().Format(time.RFC3339))
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		code, body := api.do(http.MethodPost, "/inbound/oura", b, http.Header{
			"X-Oura-Timestamp": {ts}, "X-Oura-Signature": {oura.Sign(e2eSecret, ts, []byte(b))},
		})
		require.Equal(t, http.StatusOK, code, string(body))
	}
	// A forged notification is refused.
	code, _ = api.do(http.MethodPost, "/inbound/oura", `{"event_type":"delete","data_type":"sleep","object_id":"night-1","user_id":"fake-oura-user-0001"}`,
		http.Header{"X-Oura-Timestamp": {strconv.FormatInt(time.Now().Unix(), 10)}, "X-Oura-Signature": {"00"}})
	assert.Equal(t, http.StatusUnauthorized, code)

	// Oura re-scores the night: an update notification pulls it again.
	f.SetDocuments("sleep",
		doc(t, "sleep", 0, map[string]any{"id": "sleep-del", "day": d.s(d.d2),
			"bedtime_start": d.s(d.d2) + "T15:00:00+05:30", "bedtime_end": d.s(d.d2) + "T15:30:00+05:30"}),
		nightDoc(t, d, map[string]any{"total_sleep_duration": 24000, "lowest_heart_rate": 48, "sleep_phase_5_min": "4422222222"}),
	)
	notify("update", "sleep", "night-1")
	require.Eventually(t, func() bool {
		s := sleeps()
		return len(s) == 1 && s[0].Total != nil && *s[0].Total == 24000
	}, 60*time.Second, 250*time.Millisecond, "the re-scored night replaces the stored one")
	night = sleeps()[0]
	assert.Equal(t, 48.0, *night.RestingHR)
	assert.Len(t, phases(night.ID), 2, "the phases follow the new hypnogram")
	rhr = samples("resting_heart_rate")
	require.Len(t, rhr, 1, "replaced, not added")
	assert.Equal(t, 48.0, rhr[0].Value)

	// The user deletes the workout and the meditation.
	notify("delete", "workout", "wk-1")
	notify("delete", "session", "ses-1")
	require.Eventually(t, func() bool {
		var w []map[string]any
		return api.page(u+"/workouts?"+rng, &w) && len(w) == 0 && mindful() == 0
	}, 60*time.Second, 250*time.Millisecond, "the deleted workout and session are removed")

	// --- a later pull: Oura still lists the workout and session (a stale
	// cache), and every other document again. ---
	before := len(f.Calls("/v2/usercollection/heartrate"))
	notify("create", "daily_activity", "act-1")
	require.Eventually(t, func() bool {
		return len(f.Calls("/v2/usercollection/heartrate")) > before
	}, 60*time.Second, 250*time.Millisecond, "the notification queues a pull")
	waitIdle(t, api, u)

	var w []map[string]any
	require.True(t, api.page(u+"/workouts?"+rng, &w))
	assert.Empty(t, w, "a deleted workout does not come back")
	assert.Zero(t, mindful(), "a deleted session does not come back")
	assert.Len(t, sleeps(), 1, "no duplicate night; the deleted period stays out")
	for qt, n := range map[string]int{
		"heart_rate": 3, "step_count": 1, "stress_high_time": 1, "vo2_max": 1, "oxygen_saturation": 1,
		"readiness_score": 1, "sleep_score": 1, "resting_heart_rate": 1,
	} {
		assert.Len(t, samples(qt), n, "%s not duplicated", qt)
	}

	// The tokens were never refreshed needlessly, and every data request
	// carried an issued token.
	for _, r := range f.Requests() {
		if strings.HasPrefix(r.Path, "/v2/usercollection/") {
			assert.True(t, strings.HasPrefix(r.Auth, "Bearer fake-access-token-r"), r.Auth)
		}
	}
}

// waitIdle waits until the user's ETL work is done: no pending job, twice in
// a row a second apart.
func waitIdle(t *testing.T, api e2eAPI, u string) {
	t.Helper()
	idle := 0
	require.Eventually(t, func() bool {
		code, b := api.do(http.MethodGet, u+"/coverage", "", nil)
		if code != http.StatusOK {
			return false
		}
		var cov struct {
			Pending int `json:"pending_jobs"`
			Parked  int `json:"parked_jobs"`
		}
		if json.Unmarshal(b, &cov) != nil {
			return false
		}
		require.Zero(t, cov.Parked, "no ETL job gave up")
		if cov.Pending == 0 {
			idle++
		} else {
			idle = 0
		}
		return idle >= 2
	}, 60*time.Second, time.Second)
}
