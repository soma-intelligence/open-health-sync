// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package whoop_test

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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/whoop"
	"github.com/soma-intelligence/open-health-sync/connectors/whoop/whoopfake"
	"github.com/soma-intelligence/open-health-sync/internal/app"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// The WHOOP connector end to end, through the real app (api, collect and etl
// roles, Postgres in a container) against the fake WHOOP: connect over OAuth,
// the backfill pull, the ETL, and the public REST API; then a re-scored sleep,
// workout and recovery, signed delete webhooks, and a re-pull of what WHOOP
// still lists. Every record uses the +05:30 offset, and the first night starts
// at 00:15 local, the previous day in UTC: the local days must come out right.

const (
	e2eClientID     = "e2e-client-id"
	e2eClientSecret = "e2e-client-secret"

	sleep1   = "aaaaaaaa-0000-4000-8000-0000000000e1"
	sleep2   = "aaaaaaaa-0000-4000-8000-0000000000e2"
	napNew   = "aaaaaaaa-0000-4000-8000-0000000000e3"
	workout1 = "bbbbbbbb-0000-4000-8000-0000000000e1"
	workout2 = "bbbbbbbb-0000-4000-8000-0000000000e2"
	workout3 = "bbbbbbbb-0000-4000-8000-0000000000e3"
)

var ist = time.FixedZone("+05:30", 5*3600+30*60)

// timeline is the invented WHOOP history of the test, anchored at base: a
// 00:15 local sleep onset far enough back that everything has ended, and close
// enough that a webhook's 72 hour pull still covers it.
type timeline struct{ base time.Time }

func newTimeline(now time.Time) timeline {
	latest := now.Add(-33 * time.Hour).In(ist)
	b := time.Date(latest.Year(), latest.Month(), latest.Day(), 0, 15, 0, 0, ist)
	if b.After(latest) {
		b = b.AddDate(0, 0, -1)
	}
	return timeline{base: b.UTC()}
}

func (tl timeline) at(d time.Duration) string {
	return tl.base.Add(d).Format("2006-01-02T15:04:05.000Z")
}

// localDay is the +05:30 calendar day of base plus days.
func (tl timeline) localDay(days int) string {
	return tl.base.In(ist).AddDate(0, 0, days).Format("2006-01-02")
}

func (tl timeline) cycles() string {
	return `{"records":[` +
		fmt.Sprintf(`{"id":5002,"user_id":10001,"created_at":%q,"updated_at":%q,"start":%q,"timezone_offset":"+05:30","score_state":"SCORED","score":{"strain":3.0,"kilojoule":2092.0,"average_heart_rate":55,"max_heart_rate":100},"step_count":1200},`,
			tl.at(22*time.Hour+50*time.Minute), tl.at(30*time.Hour), tl.at(22*time.Hour+45*time.Minute)) +
		fmt.Sprintf(`{"id":5001,"user_id":10001,"created_at":%q,"updated_at":%q,"start":%q,"end":%q,"timezone_offset":"+05:30","score_state":"SCORED","score":{"strain":12.5,"kilojoule":8368.0,"average_heart_rate":60,"max_heart_rate":150},"step_count":8000}`,
			tl.at(5*time.Minute), tl.at(23*time.Hour), tl.at(0), tl.at(22*time.Hour+45*time.Minute)) +
		`]}`
}

func sleepRecord(tl timeline, id string, cycle int, from, to time.Duration, nap bool, score string) string {
	state := "PENDING_SCORE"
	if score != "" {
		state = "SCORED"
		score = `,"score":` + score
	}
	return fmt.Sprintf(`{"id":%q,"cycle_id":%d,"user_id":10001,"created_at":%q,"updated_at":%q,"start":%q,"end":%q,"timezone_offset":"+05:30","nap":%t,"score_state":%q%s}`,
		id, cycle, tl.at(to+5*time.Minute), tl.at(to+10*time.Minute), tl.at(from), tl.at(to), nap, state, score)
}

func sleepScore(perf, resp float64) string {
	return fmt.Sprintf(`{"stage_summary":{"total_in_bed_time_milli":26100000,"total_awake_time_milli":1800000,"total_no_data_time_milli":0,`+
		`"total_light_sleep_time_milli":12600000,"total_slow_wave_sleep_time_milli":5400000,"total_rem_sleep_time_milli":6300000,`+
		`"sleep_cycle_count":4,"disturbance_count":7},"sleep_needed":{"baseline_milli":27000000,"need_from_sleep_debt_milli":0,`+
		`"need_from_recent_strain_milli":0,"need_from_recent_nap_milli":0},"respiratory_rate":%g,"sleep_performance_percentage":%g,`+
		`"sleep_consistency_percentage":80,"sleep_efficiency_percentage":93}`, resp, perf)
}

func (tl timeline) sleeps(rescored, withNap bool) string {
	s2 := ""
	if rescored {
		s2 = sleepScore(84, 14.25)
	}
	recs := []string{
		sleepRecord(tl, sleep2, 5002, 22*time.Hour+55*time.Minute, 30*time.Hour+45*time.Minute, false, s2),
		sleepRecord(tl, sleep1, 5001, 0, 7*time.Hour+15*time.Minute, false, sleepScore(91, 15.5)),
	}
	if withNap {
		recs = append([]string{sleepRecord(tl, napNew, 5002, 9*time.Hour, 9*time.Hour+30*time.Minute, true, "")}, recs...)
	}
	return `{"records":[` + strings.Join(recs, ",") + `]}`
}

func recoveryRecord(tl timeline, cycle int, sleepID string, created time.Duration, score string) string {
	state := "PENDING_SCORE"
	if score != "" {
		state, score = "SCORED", `,"score":`+score
	}
	return fmt.Sprintf(`{"cycle_id":%d,"sleep_id":%q,"user_id":10001,"created_at":%q,"updated_at":%q,"score_state":%q%s}`,
		cycle, sleepID, tl.at(created), tl.at(created+time.Minute), state, score)
}

func (tl timeline) recoveries(rescored bool) string {
	r2 := ""
	if rescored {
		r2 = `{"user_calibrating":false,"recovery_score":55,"resting_heart_rate":54,"hrv_rmssd_milli":48.5,"spo2_percentage":95.5,"skin_temp_celsius":34.1}`
	}
	return `{"records":[` +
		recoveryRecord(tl, 5002, sleep2, 30*time.Hour+55*time.Minute, r2) + "," +
		recoveryRecord(tl, 5001, sleep1, 7*time.Hour+25*time.Minute,
			`{"user_calibrating":false,"recovery_score":67,"resting_heart_rate":52,"hrv_rmssd_milli":61.5,"spo2_percentage":96.5,"skin_temp_celsius":33.9}`) +
		`]}`
}

func workoutRecord(tl timeline, id string, from, length time.Duration, sport, score string) string {
	state := "PENDING_SCORE"
	if score != "" {
		state, score = "SCORED", `,"score":`+score
	}
	return fmt.Sprintf(`{"id":%q,"user_id":10001,"created_at":%q,"updated_at":%q,"start":%q,"end":%q,"timezone_offset":"+05:30","sport_name":%q,"score_state":%q%s}`,
		id, tl.at(from+length+time.Minute), tl.at(from+length+2*time.Minute), tl.at(from), tl.at(from+length), sport, state, score)
}

func (tl timeline) workouts(rescored, withNew bool) string {
	w2 := ""
	if rescored {
		w2 = `{"strain":6.0,"average_heart_rate":112,"max_heart_rate":140,"kilojoule":1046.0,"percent_recorded":100,` +
			`"zone_durations":{"zone_zero_milli":0,"zone_one_milli":1800000,"zone_two_milli":1200000,"zone_three_milli":600000,"zone_four_milli":0,"zone_five_milli":0}}`
	}
	recs := []string{
		workoutRecord(tl, workout2, 15*time.Hour, time.Hour, "weightlifting", w2),
		workoutRecord(tl, workout1, 12*time.Hour, 45*time.Minute, "running",
			`{"strain":10.25,"average_heart_rate":150,"max_heart_rate":178,"kilojoule":2092.0,"percent_recorded":100,"distance_meter":8000,`+
				`"altitude_gain_meter":40,"altitude_change_meter":2,"zone_durations":{"zone_zero_milli":0,"zone_one_milli":300000,`+
				`"zone_two_milli":900000,"zone_three_milli":1200000,"zone_four_milli":300000,"zone_five_milli":0}}`),
	}
	if withNew {
		recs = append([]string{workoutRecord(tl, workout3, 26*time.Hour, 30*time.Minute, "walking", "")}, recs...)
	}
	return `{"records":[` + strings.Join(recs, ",") + `]}`
}

// api is a client of the public REST API.
type api struct {
	t    *testing.T
	base string
	key  string
	c    *http.Client
}

func (a api) do(method, path, body string) (int, []byte) {
	a.t.Helper()
	req, err := http.NewRequest(method, a.base+path, strings.NewReader(body))
	require.NoError(a.t, err)
	req.Header.Set("Authorization", "Bearer "+a.key)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.c.Do(req)
	require.NoError(a.t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

// list reads every page of a list endpoint into out (a slice pointer).
func (a api) list(path string, out any) bool {
	code, b := a.do("GET", path, "")
	if code != http.StatusOK {
		a.t.Logf("GET %s: %d %s", path, code, b)
		return false
	}
	var p struct {
		Data       json.RawMessage `json:"data"`
		NextCursor *string         `json:"next_cursor"`
	}
	if json.Unmarshal(b, &p) != nil {
		return false
	}
	require.Nil(a.t, p.NextCursor, "one page holds the test's records")
	return json.Unmarshal(p.Data, out) == nil
}

type sampleRow struct {
	Value      float64        `json:"value"`
	Unit       string         `json:"unit"`
	StartDate  time.Time      `json:"start_date"`
	ExternalID string         `json:"external_id"`
	Metadata   map[string]any `json:"metadata"`
}

type sleepRow struct {
	ExternalID      string         `json:"external_id"`
	BedtimeStart    time.Time      `json:"bedtime_start"`
	SleepScore      *float64       `json:"sleep_score"`
	RespiratoryRate *float64       `json:"respiratory_rate"`
	DeepSleep       *int           `json:"deep_sleep_seconds"`
	TotalSleep      *int           `json:"total_sleep_seconds"`
	Metadata        map[string]any `json:"metadata"`
}

type workoutRow struct {
	ExternalID   string   `json:"external_id"`
	ActivityType string   `json:"activity_type"`
	ActiveKcal   *float64 `json:"active_energy_kcal"`
	DistanceM    *float64 `json:"distance_m"`
	DurationS    *int     `json:"duration_s"`
}

type dailyRow struct {
	Date          string   `json:"date"`
	Provider      string   `json:"provider"`
	RecoveryScore *float64 `json:"recovery_score"`
	StrainScore   *float64 `json:"strain_score"`
	StepCount     *int     `json:"step_count"`
	TotalKcal     *float64 `json:"total_kcal"`
	RestingHR     *float64 `json:"resting_hr"`
}

func TestWhoopEndToEnd(t *testing.T) {
	now := time.Now().UTC()
	tl := newTimeline(now)

	fake := whoopfake.New(t)
	fake.Strict = true
	fake.ClientID, fake.ClientSecret = e2eClientID, e2eClientSecret
	fake.PageSize = 25
	fake.SetRecords("/v2/cycle", tl.cycles())
	fake.SetRecords("/v2/recovery", tl.recoveries(false))
	fake.SetRecords("/v2/activity/sleep", tl.sleeps(false, false))
	fake.SetRecords("/v2/activity/workout", tl.workouts(false, false))

	registered, ok := connector.Get(whoop.Slug)
	require.True(t, ok)
	whoop.PointAt(registered.(*whoop.Connector), fake.URL(), fake.Client())

	// The app, with a tenant whose key the test holds.
	dbURL := db.TestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := db.Open(ctx, dbURL)
	require.NoError(t, err)
	_, key, err := tenant.New(pool).EnsureTenant(ctx, "e2e")
	require.NoError(t, err)
	pool.Close()
	require.NotEmpty(t, key)

	listen := freeAddr(t)
	cfg := config.Config{
		DatabaseURL: dbURL, RawStore: "file://" + t.TempDir(), Listen: listen, LogLevel: "warn",
		Roles:         map[string]bool{"api": true, "etl": true, "collect": true},
		EncryptionKey: make([]byte, 32), PublicURL: "http://" + listen,
		Providers: map[string]config.ProviderKeys{whoop.Slug: {"CLIENT_ID": e2eClientID, "CLIENT_SECRET": e2eClientSecret}},
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		assert.NoError(t, app.Run(ctx, cfg))
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })
	a := api{t: t, base: "http://" + listen, key: key, c: &http.Client{Timeout: 10 * time.Second}}
	require.Eventually(t, func() bool {
		res, err := http.Get(a.base + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == http.StatusOK
	}, 30*time.Second, 50*time.Millisecond)

	// Connect: consent at the fake, callback, code exchanged for tokens.
	code, b := a.do("POST", "/v1/users", `{"external_ref":"whoop-e2e"}`)
	require.Equal(t, http.StatusCreated, code, string(b))
	var user struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(b, &user))
	u := "/v1/users/" + user.ID
	code, b = a.do("POST", u+"/connections/whoop", "")
	require.Equal(t, http.StatusOK, code, string(b))
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	require.NoError(t, json.Unmarshal(b, &start))
	authURL, err := url.Parse(start.AuthorizeURL)
	require.NoError(t, err)
	assert.Equal(t, "offline", lastScope(authURL.Query().Get("scope")))
	res, err := (&http.Client{Timeout: 10 * time.Second}).Get(start.AuthorizeURL)
	require.NoError(t, err)
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode, string(page))
	assert.Equal(t, "/oauth/whoop/callback", res.Request.URL.Path)
	code, b = a.do("GET", u+"/connections", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, string(b), `"status":"active"`, string(b))

	window := "&start=" + tl.base.Add(-48*time.Hour).Format(time.RFC3339) + "&end=" + now.Add(24*time.Hour).Format(time.RFC3339)
	q := "?limit=1000" + window
	sleeps := func() (out []sleepRow) { a.list(u+"/sleep"+q, &out); return }
	workouts := func() (out []workoutRow) { a.list(u+"/workouts"+q, &out); return }
	samples := func(typ string) (out []sampleRow) { a.list(u+"/samples?type="+typ+"&limit=1000"+window, &out); return }
	daily := func() map[string]dailyRow {
		var rows []dailyRow
		a.list(u+"/daily"+q, &rows)
		out := map[string]dailyRow{}
		for _, r := range rows {
			if r.Provider == whoop.Slug {
				_, dup := out[r.Date]
				require.False(t, dup, "one WHOOP summary a day: %s", r.Date)
				out[r.Date] = r
			}
		}
		return out
	}

	// The backfill lands.
	require.Eventually(t, func() bool {
		d := daily()
		return len(sleeps()) == 2 && len(workouts()) == 2 && d[tl.localDay(0)].RecoveryScore != nil &&
			d[tl.localDay(1)].StepCount != nil && len(samples("height")) == 1
	}, 90*time.Second, 250*time.Millisecond, "the backfill is readable")

	// Values, units and local days.
	byID := map[string]sleepRow{}
	for _, s := range sleeps() {
		byID[s.ExternalID] = s
	}
	s1 := byID[sleep1]
	require.NotNil(t, s1.SleepScore)
	assert.InDelta(t, 91, *s1.SleepScore, 1e-9)
	assert.InDelta(t, 15.5, *s1.RespiratoryRate, 1e-9)
	assert.Equal(t, 5400, *s1.DeepSleep, "slow wave sleep is deep, in seconds")
	assert.Equal(t, 24300, *s1.TotalSleep)
	assert.True(t, s1.BedtimeStart.Equal(tl.base))
	assert.Equal(t, "Asia/Kolkata", s1.Metadata["timezone"], "+05:30 gets a zone with that offset")
	assert.Nil(t, byID[sleep2].SleepScore, "pending: times only")

	d := daily()
	day0, day1 := d[tl.localDay(0)], d[tl.localDay(1)]
	assert.InDelta(t, 12.5, *day0.StrainScore, 1e-9)
	assert.InDelta(t, 2000, *day0.TotalKcal, 1e-6, "8368 kJ")
	assert.Equal(t, 8000, *day0.StepCount)
	assert.InDelta(t, 67, *day0.RecoveryScore, 1e-9, "the recovery of the night that opens the cycle")
	assert.InDelta(t, 52, *day0.RestingHR, 1e-9)
	assert.Equal(t, 1200, *day1.StepCount, "the open cycle")
	assert.Nil(t, day1.RecoveryScore, "its recovery is pending")
	_, utcDay := d[tl.base.Format("2006-01-02")]
	assert.False(t, utcDay && tl.base.Format("2006-01-02") != tl.localDay(0), "no summary on the UTC day of a 00:15 local onset")

	hrv := samples("hrv_rmssd")
	require.Len(t, hrv, 1)
	assert.Equal(t, "ms", hrv[0].Unit)
	assert.InDelta(t, 61.5, hrv[0].Value, 1e-9)
	assert.Equal(t, sleep1+"_recovery_hrv_rmssd", hrv[0].ExternalID)
	rhr := samples("resting_heart_rate")
	require.Len(t, rhr, 1)
	assert.Equal(t, "bpm", rhr[0].Unit)
	height := samples("height")
	assert.Equal(t, "cm", height[0].Unit)
	assert.InDelta(t, 180, height[0].Value, 1e-9)
	assert.Empty(t, samples("hrv_sdnn"), "RMSSD never lands as SDNN")

	ws := map[string]workoutRow{}
	for _, w := range workouts() {
		ws[w.ExternalID] = w
	}
	assert.Equal(t, "running", ws[workout1].ActivityType)
	assert.InDelta(t, 500, *ws[workout1].ActiveKcal, 1e-6, "2092 kJ")
	assert.InDelta(t, 8000, *ws[workout1].DistanceM, 1e-9)
	assert.Equal(t, 2700, *ws[workout1].DurationS)
	assert.Equal(t, "traditional_strength_training", ws[workout2].ActivityType)
	assert.Nil(t, ws[workout2].ActiveKcal, "pending")

	var cov struct {
		Days []struct {
			Provider string         `json:"provider"`
			Date     string         `json:"date"`
			Metrics  map[string]int `json:"metrics"`
		} `json:"days"`
	}
	code, b = a.do("GET", u+"/coverage?provider=whoop", "")
	require.Equal(t, http.StatusOK, code, string(b))
	require.NoError(t, json.Unmarshal(b, &cov))
	sleepDays := map[string]int{}
	for _, day := range cov.Days {
		if n := day.Metrics["sleep_analysis"]; n > 0 {
			sleepDays[day.Date] += n
		}
	}
	// Both nights start on local day 0 (00:15 and 23:10); the first is on the
	// previous day in UTC, where nothing may land. The count is a high-water
	// mark per raw object (see writeCoverage): whether the two nights come in
	// one page or two depends on the hour the test runs, so it is 1 or 2.
	assert.Contains(t, []int{1, 2}, sleepDays[tl.localDay(0)], "the 00:15 onset counts on its local day: %v", sleepDays)
	assert.Zero(t, sleepDays[tl.localDay(-1)], "nothing on the UTC day of the 00:15 onset: %v", sleepDays)

	// WHOOP scores the second night, its recovery and the lifting session,
	// and says so by webhook.
	fake.SetRecords("/v2/activity/sleep", tl.sleeps(true, false))
	fake.SetRecords("/v2/recovery", tl.recoveries(true))
	fake.SetRecords("/v2/activity/workout", tl.workouts(true, false))
	webhook(t, a.base, whoop.EventSleepUpdated, sleep2)
	webhook(t, a.base, whoop.EventRecoveryUpdated, sleep2)
	webhook(t, a.base, whoop.EventWorkoutUpdated, workout2)
	require.Eventually(t, func() bool {
		var s2 *float64
		for _, s := range sleeps() {
			if s.ExternalID == sleep2 {
				s2 = s.SleepScore
			}
		}
		var w2 *float64
		for _, w := range workouts() {
			if w.ExternalID == workout2 {
				w2 = w.ActiveKcal
			}
		}
		return s2 != nil && w2 != nil && len(samples("hrv_rmssd")) == 2
	}, 60*time.Second, 250*time.Millisecond, "the scored copies land")
	assert.Len(t, sleeps(), 2, "replaced, not added")
	assert.Len(t, workouts(), 2)
	for _, s := range sleeps() {
		if s.ExternalID == sleep2 {
			assert.InDelta(t, 84, *s.SleepScore, 1e-9)
		}
	}
	assert.Len(t, samples("respiratory_rate"), 2)
	assert.Len(t, samples("workout_strain"), 2)
	assert.InDelta(t, 55, *daily()[tl.localDay(1)].RecoveryScore, 1e-9)
	assert.Equal(t, 1200, *daily()[tl.localDay(1)].StepCount, "the recovery keeps the cycle's figures of the day")

	// The user deletes the first night and the run in the WHOOP app.
	webhook(t, a.base, whoop.EventSleepDeleted, sleep1)
	webhook(t, a.base, whoop.EventRecoveryDeleted, sleep1)
	webhook(t, a.base, whoop.EventWorkoutDeleted, workout1)
	require.Eventually(t, func() bool {
		return len(sleeps()) == 1 && len(workouts()) == 1
	}, 60*time.Second, 250*time.Millisecond, "the deleted records go")
	assertAfterDeletion := func(when string) {
		t.Helper()
		ss := sleeps()
		require.Len(t, ss, 1, when)
		assert.Equal(t, sleep2, ss[0].ExternalID, when)
		wk := workouts()
		require.Len(t, wk, 1, when)
		assert.Equal(t, workout2, wk[0].ExternalID, when)
		for typ, want := range map[string]string{
			"respiratory_rate": sleep2 + "_respiratory_rate", "hrv_rmssd": sleep2 + "_recovery_hrv_rmssd",
			"resting_heart_rate": sleep2 + "_recovery_resting_heart_rate", "workout_strain": workout2 + "_workout_strain",
		} {
			rows := samples(typ)
			require.Len(t, rows, 1, "%s %s", when, typ)
			assert.Equal(t, want, rows[0].ExternalID, "%s %s", when, typ)
		}
		// The deleted recovery's figures leave the day's summary; the
		// cycle's stay.
		day0 := daily()[tl.localDay(0)]
		assert.Nil(t, day0.RecoveryScore, when)
		assert.Nil(t, day0.RestingHR, when)
		require.NotNil(t, day0.StrainScore, when)
		assert.InDelta(t, 12.5, *day0.StrainScore, 1e-9, when)
	}
	assertAfterDeletion("after the delete webhooks")

	// WHOOP still lists them for a while (or a webhook was lost): a re-pull
	// brings nothing back and doubles nothing. A new nap and walk show when
	// the re-pull's raw objects have been processed.
	fake.SetRecords("/v2/activity/sleep", tl.sleeps(true, true))
	fake.SetRecords("/v2/activity/workout", tl.workouts(true, true))
	webhook(t, a.base, whoop.EventWorkoutUpdated, workout3)
	require.Eventually(t, func() bool {
		return len(workouts()) == 2 && len(sleeps()) == 2
	}, 60*time.Second, 250*time.Millisecond, "the re-pull lands")
	require.Eventually(t, func() bool {
		var c struct {
			Pending int `json:"pending_jobs"`
		}
		_, b := a.do("GET", u+"/coverage", "")
		return json.Unmarshal(b, &c) == nil && c.Pending == 0
	}, 30*time.Second, 250*time.Millisecond)
	ss := sleeps()
	ids := []string{ss[0].ExternalID, ss[1].ExternalID}
	assert.ElementsMatch(t, []string{sleep2, napNew}, ids, "the deleted night stays deleted")
	ws = map[string]workoutRow{}
	for _, w := range workouts() {
		ws[w.ExternalID] = w
	}
	assert.Contains(t, ws, workout3)
	assert.NotContains(t, ws, workout1, "the deleted run stays deleted")
	for typ, n := range map[string]int{"respiratory_rate": 1, "hrv_rmssd": 1, "resting_heart_rate": 1, "workout_strain": 1, "strain": 2, "height": 1} {
		assert.Len(t, samples(typ), n, "after the re-pull: %s", typ)
	}
	assert.Len(t, daily(), 2, "one summary per local day")

	// WHOOP deletes the second night's recovery alone (the sleep stays): its
	// samples and its figures of the day go, the sleep and the cycle's
	// figures stay.
	webhook(t, a.base, whoop.EventRecoveryDeleted, sleep2)
	require.Eventually(t, func() bool {
		return len(samples("hrv_rmssd")) == 0 && daily()[tl.localDay(1)].RecoveryScore == nil
	}, 60*time.Second, 250*time.Millisecond, "the recovery goes")
	assert.Empty(t, samples("resting_heart_rate"))
	assert.Len(t, samples("respiratory_rate"), 1, "the sleep's own sample stays")
	assert.Len(t, sleeps(), 2)
	day1 = daily()[tl.localDay(1)]
	assert.Nil(t, day1.RestingHR)
	require.NotNil(t, day1.StepCount)
	assert.Equal(t, 1200, *day1.StepCount, "the cycle's figures stay")
	assert.Zero(t, fake.Revocations())

	// Deleting the user revokes the WHOOP grant.
	code, b = a.do("DELETE", u, "")
	require.Equal(t, http.StatusAccepted, code, string(b))
	require.Eventually(t, func() bool { return fake.Revocations() == 1 }, 30*time.Second, 100*time.Millisecond,
		"the deletion worker revokes the grant at WHOOP")
}

// webhook posts a signed v2 webhook to the instance and expects 2xx.
func webhook(t *testing.T, base, typ, id string) {
	t.Helper()
	body := fmt.Sprintf(`{"user_id":10001,"id":%q,"type":%q,"trace_id":"e2e-%s"}`, id, typ, typ)
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	req, err := http.NewRequest(http.MethodPost, base+"/inbound/whoop", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(whoop.HeaderTimestamp, ts)
	req.Header.Set(whoop.HeaderSignature, whoop.Sign(e2eClientSecret, ts, []byte(body)))
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Less(t, res.StatusCode, 300, "webhook %s answered %d", typ, res.StatusCode)

	// A delivery signed with another secret is refused.
	bad, err := http.NewRequest(http.MethodPost, base+"/inbound/whoop", strings.NewReader(body))
	require.NoError(t, err)
	bad.Header.Set(whoop.HeaderTimestamp, ts)
	bad.Header.Set(whoop.HeaderSignature, whoop.Sign("not-the-secret", ts, []byte(body)))
	res, err = http.DefaultClient.Do(bad)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
}

func lastScope(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}
