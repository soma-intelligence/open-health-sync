// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/mcp"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// env serves the real API router, /mcp included, on a migrated database.
type env struct {
	t            *testing.T
	pool         *pgxpool.Pool
	store        *tenant.Store
	srv          *httptest.Server
	keyA, keyB   string
	userA, userB uuid.UUID // userA in tenant A, userB in tenant B
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := tenant.New(pool)
	a, keyA, err := st.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	b, keyB, err := st.CreateTenant(ctx, "Other")
	require.NoError(t, err)
	ua, err := st.CreateUser(ctx, a.ID, "a-1")
	require.NoError(t, err)
	ub, err := st.CreateUser(ctx, b.ID, "b-1")
	require.NoError(t, err)
	srv := httptest.NewServer(api.NewRouter(api.Deps{Pool: pool, Store: st}))
	t.Cleanup(srv.Close)
	e := &env{t: t, pool: pool, store: st, srv: srv, keyA: keyA, keyB: keyB, userA: ua.ID, userB: ub.ID}
	e.seed(a.ID, ua.ID, 60)
	e.seed(b.ID, ub.ID, 90)
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(e.t, err)
}

// seed writes five heart rate samples (base, base+1...), a sleep session with
// a phase, a workout with a route point and a connection.
func (e *env) seed(tn, u uuid.UUID, base float64) {
	e.t.Helper()
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	for i := range 5 {
		e.exec(`INSERT INTO ohs_data.quantity_samples
			(tenant_id, user_id, provider, source_slug, source_name, device_id, quantity_type, value, unit, start_date, end_date, content_hash)
			VALUES ($1, $2, 'apple_health', 'apple', 'Watch', 'watch-1', 'heart_rate', $3, 'bpm', $4, $4, $5)`,
			tn, u, base+float64(i), at.Add(time.Duration(i)*time.Hour), "hr-"+string(rune('a'+i)))
	}
	var sleepID, workoutID int64
	require.NoError(e.t, e.pool.QueryRow(context.Background(), `INSERT INTO ohs_data.sleep_sessions
		(tenant_id, user_id, provider, bedtime_start, bedtime_end, total_sleep_seconds, content_hash)
		VALUES ($1, $2, 'apple_health', $3, $4, 25200, 'sleep-1') RETURNING id`, tn, u, at.Add(-10*time.Hour), at).Scan(&sleepID))
	e.exec(`INSERT INTO ohs_data.sleep_phases (tenant_id, user_id, session_id, phase, start_date, end_date, duration_s, content_hash)
		VALUES ($1, $2, $3, 'deep', $4, $5, 3600, 'phase-1')`, tn, u, sleepID, at.Add(-9*time.Hour), at.Add(-8*time.Hour))
	require.NoError(e.t, e.pool.QueryRow(context.Background(), `INSERT INTO ohs_data.workouts
		(tenant_id, user_id, provider, activity_type, start_date, end_date, content_hash)
		VALUES ($1, $2, 'apple_health', 'running', $3, $4, 'w-1') RETURNING id`, tn, u, at, at.Add(time.Hour)).Scan(&workoutID))
	e.exec(`INSERT INTO ohs_data.route_points (tenant_id, user_id, workout_id, "timestamp", latitude, longitude, content_hash)
		VALUES ($1, $2, $3, $4, 48.85, 2.35, 'rp-1')`, tn, u, workoutID, at)
	e.exec(`INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, status) VALUES ($1, $2, 'withings', 'active')`, tn, u)
}

func (e *env) token(key string, user uuid.UUID, scopes ...string) string {
	e.t.Helper()
	p, err := e.store.LookupKey(context.Background(), auth.Hash(key))
	require.NoError(e.t, err)
	tok, _, err := e.store.MintUserToken(context.Background(), p.TenantID, user, scopes, 0)
	require.NoError(e.t, err)
	return tok
}

func (e *env) rest(path, key string) []byte {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer res.Body.Close()
	require.Equal(e.t, http.StatusOK, res.StatusCode)
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(e.t, json.NewDecoder(res.Body).Decode(&body))
	return body.Data
}

type result struct {
	Records    json.RawMessage `json:"records"`
	NextCursor *string         `json:"next_cursor"`
	Truncated  bool            `json:"truncated"`
}

// get calls a tool that must succeed and decodes its page.
func get(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) result {
	t.Helper()
	text, isErr := call(t, cs, name, args)
	require.False(t, isErr, text)
	var r result
	require.NoError(t, json.Unmarshal([]byte(text), &r), text)
	return r
}

func count(t *testing.T, r result) int {
	t.Helper()
	var recs []json.RawMessage
	require.NoError(t, json.Unmarshal(r.Records, &recs))
	return len(recs)
}

func TestGetSamplesMatchesREST(t *testing.T) {
	e := newEnv(t)
	cs := Connect(t, e.srv.URL+"/mcp", e.keyA)
	r := get(t, cs, "get_samples", map[string]any{"user_id": e.userA.String(), "type": "heart_rate"})
	want := e.rest("/v1/users/"+e.userA.String()+"/samples?type=heart_rate", e.keyA)
	assert.JSONEq(t, string(want), string(r.Records))
	assert.False(t, r.Truncated)
	assert.Nil(t, r.NextCursor)

	// Paging: truncated with a cursor that continues where REST does.
	r = get(t, cs, "get_samples", map[string]any{"user_id": e.userA.String(), "type": "heart_rate", "limit": 2})
	assert.True(t, r.Truncated)
	require.NotNil(t, r.NextCursor)
	assert.Equal(t, 2, count(t, r))
	r2 := get(t, cs, "get_samples", map[string]any{"user_id": e.userA.String(), "type": "heart_rate", "limit": 2, "cursor": *r.NextCursor})
	want = e.rest("/v1/users/"+e.userA.String()+"/samples?type=heart_rate&limit=2&cursor="+*r.NextCursor, e.keyA)
	assert.JSONEq(t, string(want), string(r2.Records))

	// Window.
	r = get(t, cs, "get_samples", map[string]any{"user_id": e.userA.String(), "type": "heart_rate",
		"start": "2026-03-01T09:00:00Z", "end": "2026-03-01T11:00:00Z"})
	assert.Equal(t, 2, count(t, r))
}

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	csB := Connect(t, e.srv.URL+"/mcp", e.keyB)
	for _, tool := range []string{"get_samples", "get_category_samples", "get_sleep", "get_workouts",
		"get_nutrition", "get_daily", "get_rr_intervals", "get_hrv", "get_menstrual_cycles", "get_coverage", "list_connections", "get_sample_stats"} {
		args := map[string]any{"user_id": e.userA.String()}
		if tool == "get_samples" {
			args["type"] = "heart_rate"
		}
		if tool == "get_sample_stats" {
			args["type"], args["start"], args["end"] = "heart_rate", "2026-03-01", "2026-03-02"
		}
		msg, isErr := call(t, csB, tool, args)
		assert.True(t, isErr, tool)
		assert.Equal(t, "user not found", msg, tool)
	}
	msg, isErr := call(t, csB, "get_workout_route", map[string]any{"user_id": e.userA.String(), "workout_id": 1})
	assert.True(t, isErr)
	assert.Equal(t, "workout not found", msg)
	// Tenant B's own user, tenant A's workout: still not found, no points.
	var workoutA int64
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT id FROM ohs_data.workouts WHERE user_id = $1`, e.userA).Scan(&workoutA))
	msg, isErr = call(t, csB, "get_workout_route", map[string]any{"user_id": e.userB.String(), "workout_id": workoutA})
	assert.True(t, isErr)
	assert.Equal(t, "workout not found", msg)

	// list_users sees only the tenant's own users.
	r := get(t, csB, "list_users", nil)
	var users []struct {
		ID uuid.UUID `json:"id"`
	}
	require.NoError(t, json.Unmarshal(r.Records, &users))
	require.Len(t, users, 1)
	assert.Equal(t, e.userB, users[0].ID)
}

func TestUserToken(t *testing.T) {
	e := newEnv(t)
	tok := e.token(e.keyA, e.userA, "read")
	cs := Connect(t, e.srv.URL+"/mcp", tok)

	r := get(t, cs, "get_samples", map[string]any{"type": "heart_rate"})
	want := e.rest("/v1/users/"+e.userA.String()+"/samples?type=heart_rate", e.keyA)
	assert.JSONEq(t, string(want), string(r.Records))
	// Its own id is accepted, any other refused.
	get(t, cs, "get_samples", map[string]any{"type": "heart_rate", "user_id": e.userA.String()})
	msg, isErr := call(t, cs, "get_samples", map[string]any{"type": "heart_rate", "user_id": e.userB.String()})
	assert.True(t, isErr)
	assert.Equal(t, "a user token reads only its own user: omit user_id", msg)
	msg, isErr = call(t, cs, "list_users", nil)
	assert.True(t, isErr)
	assert.Equal(t, "list_users needs a tenant API key", msg)

	// Without the read scope the endpoint itself refuses.
	up := e.token(e.keyA, e.userA, "upload", "connect")
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+up)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = res.Body.Close()
	assert.Equal(t, http.StatusForbidden, res.StatusCode)
	req.Header.Del("Authorization")
	res, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = res.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, res.StatusCode)
}

func TestEveryTool(t *testing.T) {
	e := newEnv(t)
	cs := Connect(t, e.srv.URL+"/mcp", e.keyA)
	u := e.userA.String()

	r := get(t, cs, "list_quantity_types", nil)
	var types []struct {
		Type string  `json:"type"`
		Unit *string `json:"unit"`
	}
	require.NoError(t, json.Unmarshal(r.Records, &types))
	found := false
	for _, q := range types {
		if q.Type == "heart_rate" {
			found = true
			require.NotNil(t, q.Unit)
			assert.Equal(t, "bpm", *q.Unit)
		}
	}
	assert.True(t, found)

	r = get(t, cs, "get_sleep", map[string]any{"user_id": u})
	var sessions []struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(r.Records, &sessions))
	require.Len(t, sessions, 1)
	r = get(t, cs, "get_sleep", map[string]any{"user_id": u, "session_id": sessions[0].ID})
	assert.JSONEq(t, string(e.rest("/v1/users/"+u+"/sleep/"+strconv.FormatInt(sessions[0].ID, 10)+"/phases", e.keyA)), string(r.Records))
	msg, isErr := call(t, cs, "get_sleep", map[string]any{"user_id": u, "session_id": 999999})
	assert.True(t, isErr)
	assert.Equal(t, "sleep session not found", msg)

	r = get(t, cs, "get_workouts", map[string]any{"user_id": u, "type": "running"})
	var workouts []struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(r.Records, &workouts))
	require.Len(t, workouts, 1)
	r = get(t, cs, "get_workout_route", map[string]any{"user_id": u, "workout_id": workouts[0].ID})
	assert.Equal(t, 1, count(t, r))

	for _, tool := range []string{"get_category_samples", "get_nutrition", "get_daily", "get_menstrual_cycles"} {
		r = get(t, cs, tool, map[string]any{"user_id": u})
		assert.Equal(t, 0, count(t, r), tool)
	}

	text, isErr := call(t, cs, "get_coverage", map[string]any{"user_id": u})
	require.False(t, isErr, text)
	var cov struct {
		Records     []any `json:"records"`
		Truncated   bool  `json:"truncated"`
		PendingJobs int64 `json:"pending_jobs"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &cov))
	assert.NotNil(t, cov.Records)

	// Without start, the last 30 days only: the seeded day (2026-03-01) is
	// in the window ending 2026-03-10, not in the one ending 2026-06-01,
	// and back with an explicit start.
	days := func(args map[string]any) int {
		text, isErr := call(t, cs, "get_coverage", args)
		require.False(t, isErr, text)
		var c struct {
			Records []any `json:"records"`
		}
		require.NoError(t, json.Unmarshal([]byte(text), &c))
		return len(c.Records)
	}
	e.exec(`INSERT INTO ohs_sync.pull_coverage (tenant_id, user_id, provider, metric, date, records_count)
		SELECT tenant_id, id, 'apple_health', 'heart_rate', '2026-03-01', 5 FROM ohs_core.end_users WHERE id = $1`, e.userA)
	restore := mcp.SetNow(func() time.Time { return time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC) })
	defer restore()
	assert.Positive(t, days(map[string]any{"user_id": u}))
	mcp.SetNow(func() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) })
	assert.Zero(t, days(map[string]any{"user_id": u}))
	assert.Positive(t, days(map[string]any{"user_id": u, "start": "2026-01-01"}))

	r = get(t, cs, "list_connections", map[string]any{"user_id": u})
	var conns []map[string]any
	require.NoError(t, json.Unmarshal(r.Records, &conns))
	require.Len(t, conns, 1)
	assert.Equal(t, "withings", conns[0]["provider"])
	assert.Equal(t, "active", conns[0]["status"])
	assert.Nil(t, conns[0]["last_sync_at"])
	assert.NotEmpty(t, conns[0]["connected_at"])
	assert.Equal(t, true, conns[0]["health"].(map[string]any)["healthy"])
	// No token column ever leaves: exactly these fields.
	var keys []string
	for k := range conns[0] {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"provider", "status", "scopes", "last_sync_at", "initial_backfill_at",
		"created_at", "updated_at", "connected_at", "health"}, keys)

	msg, isErr = call(t, cs, "get_samples", map[string]any{"user_id": u, "type": "heart_rate", "cursor": "%%%"})
	assert.True(t, isErr)
	assert.Equal(t, "the cursor is not valid", msg)
	msg, isErr = call(t, cs, "get_samples", map[string]any{"user_id": u, "type": "heart_rate", "start": "2026-03-02", "end": "2026-03-01"})
	assert.True(t, isErr)
	assert.Contains(t, msg, "start")
}

func TestDeletingUserIsNotFound(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, e.userA)
	cs := Connect(t, e.srv.URL+"/mcp", e.keyA)
	msg, isErr := call(t, cs, "get_samples", map[string]any{"user_id": e.userA.String(), "type": "heart_rate"})
	assert.True(t, isErr)
	assert.Equal(t, "user not found", msg)
}

// get_menstrual_cycles answers what GET /v1/users/{id}/menstrual-cycles
// answers, page by page.
func TestGetMenstrualCyclesMatchesREST(t *testing.T) {
	e := newEnv(t)
	for i, d := range []string{"2026-01-02", "2026-01-03", "2026-01-30", "2026-02-27"} {
		at, _ := time.Parse(time.DateOnly, d)
		e.exec(`INSERT INTO ohs_data.category_samples
			(tenant_id, user_id, provider, category_type, value_label, start_date, content_hash)
			SELECT tenant_id, id, 'apple_health', 'menstrual_flow', 'light', $2, $3
			FROM ohs_core.end_users WHERE id = $1`, e.userA, at.Add(12*time.Hour), "mf-"+strconv.Itoa(i))
	}
	cs := Connect(t, e.srv.URL+"/mcp", e.keyA)
	u := e.userA.String()
	r := get(t, cs, "get_menstrual_cycles", map[string]any{"user_id": u})
	want := e.rest("/v1/users/"+u+"/menstrual-cycles", e.keyA)
	assert.JSONEq(t, string(want), string(r.Records))
	assert.Equal(t, 3, count(t, r))
	assert.False(t, r.Truncated)

	r = get(t, cs, "get_menstrual_cycles", map[string]any{"user_id": u, "limit": 1})
	require.True(t, r.Truncated)
	require.NotNil(t, r.NextCursor)
	r2 := get(t, cs, "get_menstrual_cycles", map[string]any{"user_id": u, "limit": 1, "cursor": *r.NextCursor})
	want = e.rest("/v1/users/"+u+"/menstrual-cycles?limit=1&cursor="+*r.NextCursor, e.keyA)
	assert.JSONEq(t, string(want), string(r2.Records))

	r = get(t, cs, "get_menstrual_cycles", map[string]any{"user_id": u, "start": "2026-01-15", "end": "2026-02-01"})
	var cycles []struct {
		StartDate       string `json:"start_date"`
		CycleLengthDays *int   `json:"cycle_length_days"`
	}
	require.NoError(t, json.Unmarshal(r.Records, &cycles))
	require.Len(t, cycles, 1)
	assert.Equal(t, "2026-01-30", cycles[0].StartDate)
	require.NotNil(t, cycles[0].CycleLengthDays)
	assert.Equal(t, 28, *cycles[0].CycleLengthDays)

	// A user token reads its own cycles.
	tok := e.token(e.keyA, e.userA, "read")
	r = get(t, Connect(t, e.srv.URL+"/mcp", tok), "get_menstrual_cycles", nil)
	assert.Equal(t, 3, count(t, r))
}
