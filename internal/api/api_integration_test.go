// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/connectors/healthconnect"
	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/etl/automation"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

type env struct {
	t       *testing.T
	pool    *pgxpool.Pool
	store   *tenant.Store
	handler http.Handler
	tenantA uuid.UUID
	keyA    string
	tenantB uuid.UUID
	keyB    string
	metrics *automation.Metrics
}

// testMetricsToken is the OHS_METRICS_TOKEN of the test router.
const testMetricsToken = "test-metrics-token-0123"

func newEnv(t *testing.T, tweak ...func(*api.Deps)) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	raw, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	st := tenant.New(pool)
	a, keyA, err := st.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	b, keyB, err := st.CreateTenant(ctx, "Other")
	require.NoError(t, err)
	m := &automation.Metrics{}
	deps := api.Deps{
		Pool:      pool,
		Store:     st,
		Raw:       raw,
		Providers: []connector.Connector{applehealth.New()},
		Device:    applehealth.New(),
		// The Health Connect route shares the upload handler.
		HealthConnect: healthconnect.New(),
		Metrics:       true,
		MetricsToken:  testMetricsToken,
		ETLMetrics:    m,
	}
	for _, f := range tweak {
		f(&deps)
	}
	return &env{t: t, pool: pool, store: st, handler: api.NewRouter(deps),
		tenantA: a.ID, keyA: keyA, tenantB: b.ID, keyB: keyB, metrics: m}
}

type resp struct {
	code   int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(r.body, v), string(r.body))
}

func (e *env) do(method, path, key string, body any, hdr ...string) resp {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		require.NoError(e.t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return resp{code: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(e.t, err)
}

func (e *env) user(tenantID uuid.UUID, ref string) uuid.UUID {
	e.t.Helper()
	u, err := e.store.CreateUser(context.Background(), tenantID, ref)
	require.NoError(e.t, err)
	return u.ID
}

type userJSON struct {
	ID          uuid.UUID `json:"id"`
	ExternalRef *string   `json:"external_ref"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

type page[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func TestCreateAndGetUser(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "patient-7"})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var created userJSON
	r.json(t, &created)
	require.NotEqual(t, uuid.Nil, created.ID)
	require.NotNil(t, created.ExternalRef)
	assert.Equal(t, "patient-7", *created.ExternalRef)
	assert.Equal(t, "active", created.Status)
	assert.Equal(t, "/v1/users/"+created.ID.String(), r.header.Get("Location"))

	r = e.do("GET", "/v1/users/"+created.ID.String(), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var got userJSON
	r.json(t, &got)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, "UTC", got.CreatedAt.Location().String())

	// No body at all is a user without an external reference.
	r = e.do("POST", "/v1/users", e.keyA, nil)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var bare userJSON
	r.json(t, &bare)
	assert.Nil(t, bare.ExternalRef)

	// Same external reference twice: conflict.
	r = e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "patient-7"})
	assert.Equal(t, http.StatusConflict, r.code)
	assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"))

	// Unknown fields are refused.
	r = e.do("POST", "/v1/users", e.keyA, map[string]any{"nope": 1})
	assert.Equal(t, http.StatusBadRequest, r.code)

	for _, path := range []string{"/v1/users/" + uuid.NewString(), "/v1/users/not-a-uuid"} {
		r = e.do("GET", path, e.keyA, nil)
		assert.Equal(t, http.StatusNotFound, r.code, path)
	}
	r = e.do("GET", "/v1/users/"+created.ID.String(), "", nil)
	assert.Equal(t, http.StatusUnauthorized, r.code)
}

func TestListUsersPagination(t *testing.T) {
	e := newEnv(t)
	for i := range 250 {
		e.user(e.tenantA, fmt.Sprintf("u-%03d", i))
	}
	e.user(e.tenantB, "someone-else")

	seen := map[uuid.UUID]bool{}
	cursor := ""
	var sizes []int
	for range 10 {
		path := "/v1/users?limit=100"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		r := e.do("GET", path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		var p page[userJSON]
		r.json(t, &p)
		sizes = append(sizes, len(p.Data))
		for _, u := range p.Data {
			require.False(t, seen[u.ID], "duplicate %s", u.ID)
			seen[u.ID] = true
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	assert.Equal(t, []int{100, 100, 50}, sizes)
	assert.Len(t, seen, 250)

	r := e.do("GET", "/v1/users?cursor=not!base64", e.keyA, nil)
	assert.Equal(t, http.StatusBadRequest, r.code)
}

// seed writes one record of each kind for user u of tenant tn and returns the
// sleep session id and workout id.
func (e *env) seed(tn, u uuid.UUID) (sleepID, workoutID int64) {
	e.t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	for i := range 5 {
		e.exec(`INSERT INTO ohs_data.quantity_samples
			(tenant_id, user_id, provider, source_slug, source_name, device_id, quantity_type, value, unit, start_date, end_date, content_hash, metadata)
			VALUES ($1, $2, 'apple_health', 'apple', 'Watch', 'watch-1', 'heart_rate', $3, 'bpm', $4, $4, $5, '{"timezone":"Europe/Paris"}')`,
			tn, u, 60.0+float64(i), base.Add(time.Duration(i)*time.Hour), fmt.Sprintf("hr-%d", i))
	}
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, quantity_type, value, unit, start_date, content_hash)
		VALUES ($1, $2, 'apple_health', 'step_count', 1000, 'count', $3, 'steps-1')`, tn, u, base)
	e.exec(`INSERT INTO ohs_data.category_samples
		(tenant_id, user_id, provider, category_type, value_int, value_label, start_date, end_date, content_hash)
		VALUES ($1, $2, 'apple_health', 'mindful_session', 0, 'done', $3, $3, 'cat-1')`, tn, u, base)
	require.NoError(e.t, e.pool.QueryRow(ctx, `INSERT INTO ohs_data.sleep_sessions
		(tenant_id, user_id, provider, source_slug, bedtime_start, bedtime_end, total_sleep_seconds, content_hash)
		VALUES ($1, $2, 'apple_health', 'apple', $3, $4, 25200, 'sleep-1') RETURNING id`,
		tn, u, base.Add(-10*time.Hour), base).Scan(&sleepID))
	e.exec(`INSERT INTO ohs_data.sleep_phases
		(tenant_id, user_id, session_id, phase, start_date, end_date, duration_s, content_hash)
		VALUES ($1, $2, $3, 'deep', $4, $5, 3600, 'phase-1'), ($1, $2, $3, 'rem', $5, $6, 1800, 'phase-2')`,
		tn, u, sleepID, base.Add(-9*time.Hour), base.Add(-8*time.Hour), base.Add(-7*time.Hour-30*time.Minute))
	require.NoError(e.t, e.pool.QueryRow(ctx, `INSERT INTO ohs_data.workouts
		(tenant_id, user_id, provider, activity_type, start_date, end_date, duration_s, distance_m, content_hash)
		VALUES ($1, $2, 'apple_health', 'running', $3, $4, 1800, 5000, 'wo-1') RETURNING id`,
		tn, u, base, base.Add(30*time.Minute)).Scan(&workoutID))
	for i := range 3 {
		e.exec(`INSERT INTO ohs_data.route_points
			(tenant_id, user_id, workout_id, "timestamp", latitude, longitude, content_hash)
			VALUES ($1, $2, $3, $4, $5, 2.35, $6)`,
			tn, u, workoutID, base.Add(time.Duration(i)*time.Minute), 48.85+float64(i)/1000, fmt.Sprintf("rp-%d", i))
	}
	e.exec(`INSERT INTO ohs_data.nutrition_logs
		(tenant_id, user_id, provider, start_date, meal_type, energy_kcal, nutrients_json, content_hash)
		VALUES ($1, $2, 'apple_health', $3, 'lunch', 650, '{"iron_mg": 3}', 'meal-1')`, tn, u, base.Add(4*time.Hour))
	e.exec(`INSERT INTO ohs_data.daily_summaries
		(tenant_id, user_id, provider, summary_date, step_count, content_hash)
		VALUES ($1, $2, 'apple_health', '2026-03-01', 9000, 'day-1'), ($1, $2, 'apple_health', '2026-03-02', 4000, 'day-2')`, tn, u)
	return sleepID, workoutID
}

func TestCrossTenantRead(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	sleepID, workoutID := e.seed(e.tenantA, u)
	base := "/v1/users/" + u.String()
	paths := []string{
		base,
		base + "/samples?type=heart_rate",
		base + "/category-samples",
		base + "/sleep",
		fmt.Sprintf("%s/sleep/%d/phases", base, sleepID),
		base + "/workouts",
		fmt.Sprintf("%s/workouts/%d/route", base, workoutID),
		base + "/nutrition",
		base + "/daily",
		base + "/coverage",
	}
	for _, p := range paths {
		r := e.do("GET", p, e.keyA, nil)
		assert.Equal(t, http.StatusOK, r.code, "owner %s: %s", p, r.body)
		r = e.do("GET", p, e.keyB, nil)
		assert.Equal(t, http.StatusNotFound, r.code, "other tenant %s: %s", p, r.body)
		assert.NotContains(t, string(r.body), "heart_rate")
	}
	// The other tenant's user cannot reach tenant A's workout or session by id.
	ub := e.user(e.tenantB, "b-1")
	for _, p := range []string{
		fmt.Sprintf("/v1/users/%s/workouts/%d/route", ub, workoutID),
		fmt.Sprintf("/v1/users/%s/sleep/%d/phases", ub, sleepID),
	} {
		r := e.do("GET", p, e.keyB, nil)
		assert.Equal(t, http.StatusNotFound, r.code, p)
	}
	// A user token is not a tenant key.
	tok, _, err := e.store.MintUserToken(context.Background(), e.tenantA, u, []string{"read"}, time.Hour)
	require.NoError(t, err)
	r := e.do("GET", base+"/samples?type=heart_rate", tok, nil)
	assert.Equal(t, http.StatusForbidden, r.code)
}

type sampleJSON struct {
	ID         int64           `json:"id"`
	Type       string          `json:"type"`
	Value      float64         `json:"value"`
	Unit       string          `json:"unit"`
	StartDate  time.Time       `json:"start_date"`
	Provider   string          `json:"provider"`
	Source     *string         `json:"source"`
	Device     *string         `json:"device"`
	IngestedAt time.Time       `json:"ingested_at"`
	Metadata   json.RawMessage `json:"metadata"`
}

func TestSamplesFilter(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	base := "/v1/users/" + u.String() + "/samples"

	r := e.do("GET", base, e.keyA, nil)
	assert.Equal(t, http.StatusBadRequest, r.code, "type is required")
	var p problem
	r.json(t, &p)
	assert.Contains(t, p.Detail, "type")

	// A type that is not in the catalog (a typo) is a 400, not an empty page.
	r = e.do("GET", base+"?type=heartrate", e.keyA, nil)
	require.Equal(t, http.StatusBadRequest, r.code, string(r.body))
	r.json(t, &p)
	assert.Contains(t, p.Detail, `unknown type "heartrate"`)
	// A catalog type without data is an empty page.
	r = e.do("GET", base+"?type=body_mass", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.JSONEq(t, `{"data":[],"next_cursor":null}`, string(r.body))
	// A type outside the catalog that a connector did store is still served.
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, quantity_type, value, unit, start_date, content_hash)
		VALUES ($1, $2, 'apple_health', 'undeclared_metric', 1, 'u', '2026-03-01T08:00:00Z', 'undeclared-1')`, e.tenantA, u)
	r = e.do("GET", base+"?type=undeclared_metric", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Contains(t, string(r.body), `"undeclared_metric"`)

	r = e.do("GET", base+"?type=heart_rate&start=2026-03-02T00:00:00Z&end=2026-03-01T00:00:00Z", e.keyA, nil)
	assert.Equal(t, http.StatusBadRequest, r.code, "start after end")
	r = e.do("GET", base+"?type=heart_rate&start=yesterday", e.keyA, nil)
	assert.Equal(t, http.StatusBadRequest, r.code)

	// Window [09:00, 11:00) holds the 09:00 and 10:00 samples.
	r = e.do("GET", base+"?type=heart_rate&start=2026-03-01T09:00:00Z&end=2026-03-01T11:00:00Z", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var got page[sampleJSON]
	r.json(t, &got)
	require.Len(t, got.Data, 2)
	assert.Nil(t, got.NextCursor)
	s := got.Data[0]
	assert.Equal(t, "heart_rate", s.Type)
	assert.Equal(t, 61.0, s.Value)
	assert.Equal(t, "bpm", s.Unit)
	assert.Equal(t, "apple_health", s.Provider)
	require.NotNil(t, s.Source)
	assert.Equal(t, "apple", *s.Source)
	require.NotNil(t, s.Device)
	assert.Equal(t, "watch-1", *s.Device)
	assert.False(t, s.IngestedAt.IsZero())
	assert.JSONEq(t, `{"timezone":"Europe/Paris"}`, string(s.Metadata))
	assert.True(t, strings.Contains(string(r.body), `"start_date":"2026-03-01T09:00:00Z"`), "RFC 3339 UTC: %s", r.body)

	// Pagination by two: 2 + 2 + 1, in time order, no step_count mixed in.
	var values []float64
	cursor := ""
	for range 5 {
		path := base + "?type=heart_rate&limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		r = e.do("GET", path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		var pg page[sampleJSON]
		r.json(t, &pg)
		for _, s := range pg.Data {
			values = append(values, s.Value)
		}
		if pg.NextCursor == nil {
			break
		}
		cursor = *pg.NextCursor
	}
	assert.Equal(t, []float64{60, 61, 62, 63, 64}, values)

	r = e.do("GET", base+"?type=heart_rate&cursor=bm9wZQ", e.keyA, nil)
	assert.Equal(t, http.StatusBadRequest, r.code, "garbage cursor")
}

func TestLimitCap(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	for _, p := range []string{
		"/v1/users?limit=5000",
		"/v1/users/" + u.String() + "/samples?type=heart_rate&limit=5000",
		"/v1/users/" + u.String() + "/sleep?limit=1001",
		"/v1/users/" + u.String() + "/sleep?limit=0",
		"/v1/users/" + u.String() + "/sleep?limit=ten",
	} {
		r := e.do("GET", p, e.keyA, nil)
		assert.Equal(t, http.StatusBadRequest, r.code, p)
	}
	r := e.do("GET", "/v1/users/"+u.String()+"/sleep?limit=1000", e.keyA, nil)
	assert.Equal(t, http.StatusOK, r.code)
}

func TestIdempotentCreateUser(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"external_ref": "idem-1"}
	r1 := e.do("POST", "/v1/users", e.keyA, body, "Idempotency-Key", "k-1")
	require.Equal(t, http.StatusCreated, r1.code, string(r1.body))
	r2 := e.do("POST", "/v1/users", e.keyA, body, "Idempotency-Key", "k-1")
	require.Equal(t, http.StatusCreated, r2.code, string(r2.body))
	var a, b userJSON
	r1.json(t, &a)
	r2.json(t, &b)
	assert.Equal(t, a.ID, b.ID)
	assert.Equal(t, "application/json", r2.header.Get("Content-Type"))
	assert.Equal(t, "true", r2.header.Get("Idempotent-Replayed"))

	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ohs_core.end_users WHERE tenant_id = $1`, e.tenantA).Scan(&n))
	assert.Equal(t, 1, n)

	// Same key, other body: 422.
	r := e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "idem-2"}, "Idempotency-Key", "k-1")
	assert.Equal(t, http.StatusUnprocessableEntity, r.code)
	// Same key under another tenant is another key.
	r = e.do("POST", "/v1/users", e.keyB, body, "Idempotency-Key", "k-1")
	assert.Equal(t, http.StatusCreated, r.code)
	// Too long a key: 400.
	r = e.do("POST", "/v1/users", e.keyA, body, "Idempotency-Key", strings.Repeat("k", 256))
	assert.Equal(t, http.StatusBadRequest, r.code)
	// A stored 4xx is replayed too.
	e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "dup"})
	r = e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "dup"}, "Idempotency-Key", "k-2")
	assert.Equal(t, http.StatusConflict, r.code)
	var first map[string]any
	r.json(t, &first)
	assert.Equal(t, r.header.Get("X-Request-Id"), first["request_id"], "a problem inside the wrapper names the request id")
	r = e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "dup"}, "Idempotency-Key", "k-2")
	assert.Equal(t, http.StatusConflict, r.code)
	assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"))
	var replay map[string]any
	r.json(t, &replay)
	assert.Equal(t, r.header.Get("X-Request-Id"), replay["request_id"], "a replayed problem names this request's id")
	assert.NotEqual(t, first["request_id"], replay["request_id"])
	assert.Equal(t, first["detail"], replay["detail"])
}

func TestMintToken(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	path := "/v1/users/" + u.String() + "/tokens"

	r := e.do("POST", path, e.keyA, map[string]any{"scopes": []string{"upload"}})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	var tok struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
		Scopes    []string  `json:"scopes"`
	}
	r.json(t, &tok)
	assert.True(t, strings.HasPrefix(tok.Token, "ohs_ut_"))
	assert.Equal(t, []string{"upload"}, tok.Scopes)
	assert.WithinDuration(t, time.Now().Add(time.Hour), tok.ExpiresAt, time.Minute)

	// The token reaches the ingest route (and fails there on missing headers, not auth).
	r = e.do("POST", "/v1/ingest/apple-health", tok.Token, []byte("x"))
	assert.Equal(t, http.StatusBadRequest, r.code, string(r.body))
	// A tenant key does not.
	r = e.do("POST", "/v1/ingest/apple-health", e.keyA, []byte("x"))
	assert.Equal(t, http.StatusForbidden, r.code)
	r = e.do("POST", "/v1/ingest/health-connect", tok.Token, []byte("x"))
	assert.Equal(t, http.StatusBadRequest, r.code, string(r.body))
	r = e.do("POST", "/v1/ingest/health-connect", e.keyA, []byte("x"))
	assert.Equal(t, http.StatusForbidden, r.code)

	// Read only: up to 30 days.
	r = e.do("POST", path, e.keyA, map[string]any{"scopes": []string{"read"}, "ttl_seconds": 2592000})
	assert.Equal(t, http.StatusCreated, r.code, string(r.body))
	for name, body := range map[string]any{
		"read too long":     map[string]any{"scopes": []string{"read"}, "ttl_seconds": 2592001},
		"upload too long":   map[string]any{"scopes": []string{"upload", "read"}, "ttl_seconds": 86401},
		"upload 25 hours":   map[string]any{"scopes": []string{"upload"}, "ttl_seconds": 90000},
		"mixed 30 days":     map[string]any{"scopes": []string{"read", "connect"}, "ttl_seconds": 2592000},
		"unknown scope":     map[string]any{"scopes": []string{"admin"}},
		"no scope":          map[string]any{},
		"negative ttl":      map[string]any{"scopes": []string{"upload"}, "ttl_seconds": -5},
		"not json":          "{",
		"ttl not a integer": map[string]any{"scopes": []string{"upload"}, "ttl_seconds": "1h"},
	} {
		r = e.do("POST", path, e.keyA, body)
		assert.Equal(t, http.StatusBadRequest, r.code, name)
		if strings.Contains(name, "long") || strings.Contains(name, "hours") || strings.Contains(name, "days") {
			var p problem
			r.json(t, &p)
			assert.Equal(t, "ttl_seconds is too long for these scopes: at most 86400, or 2592000 when read is the only scope", p.Detail, name)
		}
	}
	// Another tenant's user, an unknown user: 404.
	r = e.do("POST", path, e.keyB, map[string]any{"scopes": []string{"upload"}})
	assert.Equal(t, http.StatusNotFound, r.code)
	r = e.do("POST", "/v1/users/"+uuid.NewString()+"/tokens", e.keyA, map[string]any{"scopes": []string{"upload"}})
	assert.Equal(t, http.StatusNotFound, r.code)

	// Idempotency-Key never stores a minted token: the second call mints another.
	r1 := e.do("POST", path, e.keyA, map[string]any{"scopes": []string{"upload"}}, "Idempotency-Key", "t-1")
	r2 := e.do("POST", path, e.keyA, map[string]any{"scopes": []string{"upload"}}, "Idempotency-Key", "t-1")
	require.Equal(t, http.StatusCreated, r1.code)
	require.Equal(t, http.StatusCreated, r2.code)
	assert.NotEqual(t, string(r1.body), string(r2.body))
	var stored int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ohs_core.idempotency_keys WHERE response_body::text LIKE '%ohs_ut_%' OR convert_from(response_body, 'UTF8') LIKE '%ohs_ut_%'`).Scan(&stored))
	assert.Zero(t, stored)
}

func TestReadShapes(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	sleepID, workoutID := e.seed(e.tenantA, u)
	base := "/v1/users/" + u.String()

	get := func(path string) []map[string]any {
		t.Helper()
		r := e.do("GET", path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, "%s: %s", path, r.body)
		var p page[map[string]any]
		r.json(t, &p)
		return p.Data
	}

	cat := get(base + "/category-samples?type=mindful_session")
	require.Len(t, cat, 1)
	assert.Equal(t, "mindful_session", cat[0]["type"])
	assert.Equal(t, "done", cat[0]["value_label"])
	assert.Len(t, get(base+"/category-samples?type=other"), 0)

	sleep := get(base + "/sleep")
	require.Len(t, sleep, 1)
	assert.Equal(t, float64(sleepID), sleep[0]["id"])
	assert.Equal(t, float64(25200), sleep[0]["total_sleep_seconds"])
	assert.Nil(t, sleep[0]["deep_sleep_seconds"])
	assert.Contains(t, sleep[0], "ingested_at")
	assert.Contains(t, sleep[0], "device")

	phases := get(fmt.Sprintf("%s/sleep/%d/phases", base, sleepID))
	require.Len(t, phases, 2)
	assert.Equal(t, "deep", phases[0]["phase"])
	assert.Equal(t, "apple_health", phases[0]["provider"])

	wo := get(base + "/workouts?type=running")
	require.Len(t, wo, 1)
	assert.Equal(t, float64(5000), wo[0]["distance_m"])

	route := get(fmt.Sprintf("%s/workouts/%d/route?limit=2", base, workoutID))
	require.Len(t, route, 2)
	assert.InDelta(t, 48.85, route[0]["latitude"], 1e-9)
	r := e.do("GET", fmt.Sprintf("%s/workouts/%d/route", base, workoutID+999), e.keyA, nil)
	assert.Equal(t, http.StatusNotFound, r.code)
	r = e.do("GET", base+"/workouts/abc/route", e.keyA, nil)
	assert.Equal(t, http.StatusNotFound, r.code)

	meals := get(base + "/nutrition")
	require.Len(t, meals, 1)
	assert.Equal(t, map[string]any{"iron_mg": float64(3)}, meals[0]["nutrients"])

	days := get(base + "/daily?start=2026-03-02")
	require.Len(t, days, 1)
	assert.Equal(t, "2026-03-02", days[0]["date"])
	assert.Equal(t, float64(4000), days[0]["step_count"])
	assert.Len(t, get(base+"/daily?start=2026-03-01&end=2026-03-02"), 1)
}

func TestCoverage(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	e.exec(`INSERT INTO ohs_sync.pull_coverage (tenant_id, user_id, provider, metric, date, records_count) VALUES
		($1, $2, 'apple_health', 'heart_rate', '2026-03-01', 10),
		($1, $2, 'apple_health', 'step_count', '2026-03-01', 5),
		($1, $2, 'apple_health', 'heart_rate', '2026-03-02', 7),
		($1, $2, 'withings', 'body_mass', '2026-03-01', 1)`, e.tenantA, u)
	e.exec(`INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, status) VALUES
		($1, $2, 'apple_health', 'k1', 'pending'), ($1, $2, 'apple_health', 'k2', 'running'),
		($1, $2, 'apple_health', 'k3', 'done'), ($1, $2, 'apple_health', 'k4', 'parked')`, e.tenantA, u)
	// Lines rejected by the ETL. A line counts once however many rows a
	// replay left for it; recovered lines and another user's do not count;
	// a job-level row (no line number) counts only for an object without
	// line rows. Here: k1 lines 1 and 2, and object k2.
	other := e.user(e.tenantA, "a-2")
	e.exec(`INSERT INTO ohs_data.failed_records (tenant_id, user_id, provider, raw_key, line_number, reason_code, status) VALUES
		($1, $2, 'apple_health', 'k1', 1, 'validation', 'pending'),
		($1, $2, 'apple_health', 'k1', 1, 'validation', 'pending'),
		($1, $2, 'apple_health', 'k1', 2, 'mapping', 'permanent'),
		($1, $2, 'apple_health', 'k1', 3, 'unit', 'recovered'),
		($1, $2, 'apple_health', 'k1', NULL, 'job', 'pending'),
		($1, $2, 'apple_health', 'k2', NULL, 'job', 'pending'),
		($1, $2, 'apple_health', 'k2', NULL, 'job', 'pending'),
		($1, $3, 'apple_health', 'k9', 1, 'validation', 'pending')`, e.tenantA, u, other)

	r := e.do("GET", "/v1/users/"+u.String()+"/coverage?start=2026-03-01&end=2026-03-02", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var c struct {
		PendingJobs     int  `json:"pending_jobs"`
		ParkedJobs      int  `json:"parked_jobs"`
		RejectedRecords *int `json:"rejected_records"`
		Days            []struct {
			Provider string         `json:"provider"`
			Date     string         `json:"date"`
			Records  int            `json:"records"`
			Metrics  map[string]int `json:"metrics"`
		} `json:"days"`
	}
	r.json(t, &c)
	assert.Equal(t, 2, c.PendingJobs)
	assert.Equal(t, 1, c.ParkedJobs)
	require.NotNil(t, c.RejectedRecords)
	assert.Equal(t, 3, *c.RejectedRecords)
	require.Len(t, c.Days, 2)
	assert.Equal(t, "apple_health", c.Days[0].Provider)
	assert.Equal(t, "2026-03-01", c.Days[0].Date)
	assert.Equal(t, 15, c.Days[0].Records)
	assert.Equal(t, map[string]int{"heart_rate": 10, "step_count": 5}, c.Days[0].Metrics)
	assert.Equal(t, "withings", c.Days[1].Provider)

	r = e.do("GET", "/v1/users/"+u.String()+"/coverage?provider=apple_health", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	r.json(t, &c)
	assert.Len(t, c.Days, 2)
}

func TestProvidersAndOps(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/v1/providers", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.JSONEq(t, `{"data":[{"slug":"apple_health","kinds":["device"]}]}`, string(r.body))
	r = e.do("GET", "/v1/providers", "", nil)
	assert.Equal(t, http.StatusUnauthorized, r.code)

	r = e.do("GET", "/healthz", "", nil)
	assert.Equal(t, http.StatusOK, r.code)
	r = e.do("GET", "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, r.code, string(r.body))

	e.metrics.ParkedJobs.Store(3)
	r = e.do("GET", "/metrics", "", nil)
	assert.Equal(t, http.StatusUnauthorized, r.code, "no token")
	r = e.do("GET", "/metrics", "wrong-token", nil)
	assert.Equal(t, http.StatusUnauthorized, r.code, "wrong token")
	r = e.do("GET", "/metrics", e.keyA, nil)
	assert.Equal(t, http.StatusUnauthorized, r.code, "an API key is not the metrics token")
	r = e.do("GET", "/metrics", testMetricsToken, nil)
	require.Equal(t, http.StatusOK, r.code)
	text := string(r.body)
	assert.Contains(t, r.header.Get("Content-Type"), "text/plain")
	assert.Contains(t, text, `ohs_http_requests_total{route="GET /v1/providers",code="200"} 1`)
	assert.Contains(t, text, `ohs_http_requests_total{route="GET /v1/providers",code="401"} 1`)
	assert.Contains(t, text, "# TYPE ohs_etl_parked_jobs gauge\nohs_etl_parked_jobs 3\n")
	assert.Contains(t, text, "# TYPE ohs_etl_run_failures_total counter")
	assert.Contains(t, text, "ohs_ingest_in_flight 0")

	r = e.do("GET", "/nowhere", "", nil)
	assert.Equal(t, http.StatusNotFound, r.code)
	assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"))

	// No token configured: the API listener refuses every scrape.
	noToken := newEnv(t, func(d *api.Deps) { d.MetricsToken = "" })
	r = noToken.do("GET", "/metrics", "", nil)
	assert.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, string(r.body), "OHS_METRICS_TOKEN")

	// A listener of its own takes /metrics; the token is optional there.
	own := http.NewServeMux()
	sep := newEnv(t, func(d *api.Deps) { d.MetricsToken = ""; d.MetricsMux = own })
	r = sep.do("GET", "/metrics", "", nil)
	assert.Equal(t, http.StatusNotFound, r.code, "not on the API listener")
	rec := httptest.NewRecorder()
	own.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ohs_ingest_in_flight 0")
	ownTok := http.NewServeMux()
	newEnv(t, func(d *api.Deps) { d.MetricsMux = ownTok })
	rec = httptest.NewRecorder()
	ownTok.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "a token set applies on its own listener too")

	off := newEnv(t, func(d *api.Deps) { d.Metrics = false })
	r = off.do("GET", "/metrics", "", nil)
	assert.Equal(t, http.StatusNotFound, r.code)

	// readyz fails when the database is gone.
	off.pool.Close()
	r = off.do("GET", "/readyz", "", nil)
	assert.Equal(t, http.StatusServiceUnavailable, r.code)
}

func TestDeletingUserReadsAsAbsent(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	_, workoutID := e.seed(e.tenantA, u)
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u)
	base := "/v1/users/" + u.String()
	for _, p := range []string{base + "/samples?type=heart_rate", base + "/sleep", base + "/coverage",
		fmt.Sprintf("%s/workouts/%d/route", base, workoutID)} {
		r := e.do("GET", p, e.keyA, nil)
		assert.Equal(t, http.StatusNotFound, r.code, p)
	}
	// The user record itself reads as absent too.
	assert.Equal(t, http.StatusNotFound, e.do("GET", base, e.keyA, nil).code)
}

// collectIDs walks every page of path with limit 2 and returns the ids seen.
func (e *env) collectIDs(path string) []int64 {
	e.t.Helper()
	var ids []int64
	cursor := ""
	for range 20 {
		p := path + "limit=2"
		if cursor != "" {
			p += "&cursor=" + cursor
		}
		r := e.do("GET", p, e.keyA, nil)
		require.Equal(e.t, http.StatusOK, r.code, string(r.body))
		var pg page[struct {
			ID int64 `json:"id"`
		}]
		r.json(e.t, &pg)
		require.LessOrEqual(e.t, len(pg.Data), 2)
		for _, d := range pg.Data {
			ids = append(ids, d.ID)
		}
		if pg.NextCursor == nil {
			return ids
		}
		cursor = *pg.NextCursor
	}
	e.t.Fatal("pagination did not end")
	return nil
}

func TestCursorTies(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	var want []int64
	for i := range 5 {
		var id int64
		require.NoError(t, e.pool.QueryRow(context.Background(), `INSERT INTO ohs_data.quantity_samples
			(tenant_id, user_id, provider, quantity_type, value, unit, start_date, content_hash)
			VALUES ($1, $2, 'apple_health', 'heart_rate', $3, 'bpm', $4, $5) RETURNING id`,
			e.tenantA, u, 60+i, at, fmt.Sprintf("tie-%d", i)).Scan(&id))
		want = append(want, id)
	}
	got := e.collectIDs("/v1/users/" + u.String() + "/samples?type=heart_rate&")
	assert.Equal(t, want, got, "every sample once, in id order")

	want = nil
	for _, p := range []string{"apple_health", "withings", "oura"} {
		var id int64
		require.NoError(t, e.pool.QueryRow(context.Background(), `INSERT INTO ohs_data.daily_summaries
			(tenant_id, user_id, provider, summary_date, step_count, content_hash)
			VALUES ($1, $2, $3, '2026-03-01', 100, $3) RETURNING id`, e.tenantA, u, p).Scan(&id))
		want = append(want, id)
	}
	got = e.collectIDs("/v1/users/" + u.String() + "/daily?")
	assert.Equal(t, want, got, "every provider's row of one date once, in id order")
}

func TestCoverageTruncated(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	e.exec(`INSERT INTO ohs_sync.pull_coverage (tenant_id, user_id, provider, metric, date, records_count) VALUES
		($1, $2, 'apple_health', 'heart_rate', '2026-03-01', 1),
		($1, $2, 'apple_health', 'heart_rate', '2026-03-02', 1)`, e.tenantA, u)
	var c struct {
		Truncated *bool             `json:"truncated"`
		Days      []json.RawMessage `json:"days"`
	}
	r := e.do("GET", "/v1/users/"+u.String()+"/coverage", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	r.json(t, &c)
	require.NotNil(t, c.Truncated)
	assert.False(t, *c.Truncated)
	assert.Len(t, c.Days, 2)

	old := read.MaxCoverageDays
	read.MaxCoverageDays = 1
	t.Cleanup(func() { read.MaxCoverageDays = old })
	r = e.do("GET", "/v1/users/"+u.String()+"/coverage", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	c.Truncated = nil
	r.json(t, &c)
	require.NotNil(t, c.Truncated)
	assert.True(t, *c.Truncated)
	assert.Len(t, c.Days, 1)
}

func TestReadyzRawRootGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "raw-root")
	raw, err := rawstore.Open("file://" + dir)
	require.NoError(t, err)
	e := newEnv(t, func(d *api.Deps) { d.Raw = raw })
	r := e.do("GET", "/readyz", "", nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	require.NoError(t, os.RemoveAll(dir))
	r = e.do("GET", "/readyz", "", nil)
	assert.Equal(t, http.StatusServiceUnavailable, r.code)
}

func TestDeleteUserRoute(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(e.tenantA, "gone")
	other := e.user(e.tenantA, "stays")
	path := "/v1/users/" + u.String()

	assert.Equal(t, http.StatusUnauthorized, e.do("DELETE", path, "", nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", path, e.keyB, nil).code, "another tenant's user")
	// A user token, even of this very user, cannot delete.
	tr := e.do("POST", path+"/tokens", e.keyA, map[string]any{"scopes": []string{"upload", "read"}})
	require.Equal(t, http.StatusCreated, tr.code, string(tr.body))
	var tok struct {
		Token string `json:"token"`
	}
	tr.json(t, &tok)
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, e.do("DELETE", path, tok.Token, nil).code,
		"a user token is refused")
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", "/v1/users/"+uuid.NewString(), e.keyA, nil).code)

	r := e.do("DELETE", path, e.keyA, nil)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	var job struct {
		JobID uuid.UUID `json:"job_id"`
	}
	r.json(e.t, &job)
	require.NotEqual(t, uuid.Nil, job.JobID)

	// The job can be read back by the tenant that owns it, and only by it.
	type deletionJSON struct {
		JobID      uuid.UUID  `json:"job_id"`
		UserID     uuid.UUID  `json:"user_id"`
		Status     string     `json:"status"`
		CreatedAt  time.Time  `json:"created_at"`
		FinishedAt *time.Time `json:"finished_at"`
	}
	jobPath := "/v1/deletions/" + job.JobID.String()
	jr := e.do("GET", jobPath, e.keyA, nil)
	require.Equal(t, http.StatusOK, jr.code, string(jr.body))
	var dj deletionJSON
	jr.json(t, &dj)
	assert.Equal(t, job.JobID, dj.JobID)
	assert.Equal(t, u, dj.UserID)
	assert.Equal(t, "pending", dj.Status)
	assert.False(t, dj.CreatedAt.IsZero())
	assert.Nil(t, dj.FinishedAt)
	assert.Contains(t, string(jr.body), `"finished_at":null`)
	assert.Equal(t, http.StatusNotFound, e.do("GET", jobPath, e.keyB, nil).code, "another tenant's job")
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/deletions/"+uuid.NewString(), e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/deletions/not-a-uuid", e.keyA, nil).code)
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", jobPath, "", nil).code)

	// While the job is unfinished the user reads as gone.
	assert.Equal(t, http.StatusNotFound, e.do("GET", path, e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", path+"/samples?type=heart_rate", e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", path+"/tokens", e.keyA, map[string]any{"scopes": []string{"read"}}).code)
	assert.Equal(t, http.StatusOK, e.do("GET", "/v1/users/"+other.String(), e.keyA, nil).code)
	lr := e.do("GET", "/v1/users", e.keyA, nil)
	require.Equal(t, http.StatusOK, lr.code, string(lr.body))
	assert.NotContains(t, string(lr.body), u.String(), "a deleting user is not listed")
	assert.Contains(t, string(lr.body), other.String())

	raw, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	n, err := deletion.NewWorker(e.pool, raw, nil).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, http.StatusNotFound, e.do("GET", path, e.keyA, nil).code)
	jr = e.do("GET", jobPath, e.keyA, nil)
	require.Equal(t, http.StatusOK, jr.code, string(jr.body))
	dj = deletionJSON{}
	jr.json(t, &dj)
	assert.Equal(t, "done", dj.Status)
	require.NotNil(t, dj.FinishedAt)
	assert.False(t, dj.FinishedAt.Before(dj.CreatedAt))
	var left int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.end_users WHERE id = $1`, u).Scan(&left))
	assert.Zero(t, left)
}

// A stored POST /v1/users response names the user: deleting the user deletes
// it, and a replay after the deletion never answers 201 with the deleted id.
func TestIdempotentCreateUserErasedWithUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	body := map[string]any{"external_ref": "erase-me"}
	r := e.do("POST", "/v1/users", e.keyA, body, "Idempotency-Key", "erase-1")
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var u userJSON
	r.json(t, &u)
	rows := func() int {
		var n int
		require.NoError(t, e.pool.QueryRow(ctx,
			`SELECT count(*) FROM ohs_core.idempotency_keys WHERE tenant_id = $1 AND key = 'erase-1' AND user_id = $2`,
			e.tenantA, u.ID).Scan(&n))
		return n
	}
	require.Equal(t, 1, rows(), "the stored response records the user it names")

	// A response that names no user keeps user_id NULL.
	r = e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "erase-me"}, "Idempotency-Key", "erase-dup")
	require.Equal(t, http.StatusConflict, r.code)
	var nullUser bool
	require.NoError(t, e.pool.QueryRow(ctx,
		`SELECT user_id IS NULL FROM ohs_core.idempotency_keys WHERE tenant_id = $1 AND key = 'erase-dup'`, e.tenantA).Scan(&nullUser))
	assert.True(t, nullUser)

	require.Equal(t, http.StatusAccepted, e.do("DELETE", "/v1/users/"+u.ID.String(), e.keyA, nil).code)
	assert.Equal(t, 0, rows(), "the deletion request drops the stored response at once")
	r = e.do("POST", "/v1/users", e.keyA, body, "Idempotency-Key", "erase-1")
	assert.NotEqual(t, "true", r.header.Get("Idempotent-Replayed"))
	assert.NotContains(t, string(r.body), u.ID.String(), "no replay of the deleting user")

	raw, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	n, err := deletion.NewWorker(e.pool, raw, nil).Pass(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	var left int
	require.NoError(t, e.pool.QueryRow(ctx,
		`SELECT count(*) FROM ohs_core.idempotency_keys WHERE user_id = $1`, u.ID).Scan(&left))
	assert.Zero(t, left, "no idempotency row of the deleted user remains")

	// Replay after the deletion: whatever it answers, never the deleted user.
	r = e.do("POST", "/v1/users", e.keyA, body, "Idempotency-Key", "erase-1")
	assert.NotContains(t, string(r.body), u.ID.String())
	if r.code == http.StatusCreated {
		var again userJSON
		r.json(t, &again)
		assert.NotEqual(t, u.ID, again.ID)
	}
}

// A user deleted while their create response is being stored: the response
// is sent once and never stored.
func TestIdempotentStoreSkipsInactiveUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st := tenant.New(e.pool)
	u := e.user(e.tenantA, "racing")
	_, err := e.pool.Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u)
	require.NoError(t, err)
	ictx := tenant.WithIdempotencySubject(ctx)
	status, _, err := st.Idempotent(ictx, e.tenantA, "race-1", "h", func() (int, []byte, error) {
		tenant.SetIdempotencyUser(ictx, u)
		return http.StatusCreated, []byte(`{"id":"` + u.String() + `"}`), nil
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, status)
	var n int
	require.NoError(t, e.pool.QueryRow(ctx,
		`SELECT count(*) FROM ohs_core.idempotency_keys WHERE tenant_id = $1 AND key = 'race-1'`, e.tenantA).Scan(&n))
	assert.Zero(t, n)
}
