// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package wahoo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/wahoo"
	"github.com/soma-intelligence/open-health-sync/connectors/wahoo/wahoofake"
	"github.com/soma-intelligence/open-health-sync/internal/app"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// The whole path, through the real app (api, collect and etl roles, a real
// database) and the registered Wahoo connector pointed at wahoofake: connect
// over OAuth, the scheduled backfill pulls workouts (one listed without its
// summary) and the profile's body figures, the ETL stores them, the public
// API serves them; then a webhook carries a revised summary that replaces the
// stored workout, a forged one is refused, and a manual sync duplicates
// nothing.

const (
	e2eClientID = "e2e-client-id"
	e2eSecret   = "e2e-client-secret"
	e2eToken    = "fake-webhook-token"
)

type e2eAPI struct {
	t    *testing.T
	base string
	key  string
	c    *http.Client
}

func (a e2eAPI) do(method, path, body string) (int, []byte) {
	a.t.Helper()
	req, err := http.NewRequest(method, a.base+path, strings.NewReader(body))
	require.NoError(a.t, err)
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.c.Do(req)
	require.NoError(a.t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func (a e2eAPI) page(path string, out any) bool {
	code, b := a.do(http.MethodGet, path, "")
	if code != http.StatusOK {
		return false
	}
	return json.Unmarshal(b, &struct {
		Data any `json:"data"`
	}{out}) == nil
}

type apiWorkout struct {
	Activity   string         `json:"activity_type"`
	Start      time.Time      `json:"start_date"`
	DistanceM  *float64       `json:"distance_m"`
	Kcal       *float64       `json:"active_energy_kcal"`
	AvgHR      *float64       `json:"avg_heart_rate"`
	ExternalID string         `json:"external_id"`
	Metadata   map[string]any `json:"metadata"`
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().String()
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func TestEndToEnd_ConnectPullWebhook(t *testing.T) {
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	d1, d2 := today.AddDate(0, 0, -1), today.AddDate(0, 0, -2)
	ride, run := d2.Add(8*time.Hour), d1.Add(7*time.Hour)

	f := wahoofake.New(t)
	f.Rotate = true
	base := wahoofake.Workouts()[0]
	f.User = wahoofake.Edit(wahoofake.File("user.json"), map[string]any{"updated_at": stamp(d2.Add(6 * time.Hour)), "weight": "71.5"})
	summary := wahoofake.File("workout_summary.json")
	f.SetWorkouts([]json.RawMessage{
		wahoofake.Edit(base, map[string]any{"id": 2001, "starts": stamp(ride), "workout_type_id": 15}),
		wahoofake.Edit(base, map[string]any{"id": 2002, "starts": stamp(run), "workout_type_id": 1}),
	}, map[string]json.RawMessage{"2001": summary})

	c, ok := connector.Get(wahoo.Slug)
	require.True(t, ok)
	wahoo.PointAt(c.(*wahoo.Connector), f.URL(), f.Client())

	dbURL := db.TestDB(t)
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, key, err := tenant.New(pool).CreateTenant(ctx, "wahoo-e2e")
	require.NoError(t, err)

	listen := freeAddr(t)
	encKey := make([]byte, 32)
	vaultCfg, err := config.DerivedVault(encKey)
	require.NoError(t, err)
	cfg := config.Config{
		DatabaseURL: dbURL, RawStore: "file://" + t.TempDir(), Listen: listen, LogLevel: "warn",
		PublicURL: "http://" + listen, EncryptionKey: encKey, Vault: vaultCfg,
		Roles: map[string]bool{"api": true, "etl": true, "collect": true},
		Providers: map[string]config.ProviderKeys{wahoo.Slug: {
			wahoo.KeyClientID: e2eClientID, wahoo.KeyClientSecret: e2eSecret, wahoo.KeyWebhookToken: e2eToken,
		}},
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx, cfg, settings.Start{}) }()
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

	// --- connect ---
	code, body := api.do(http.MethodPost, "/v1/users", `{"external_ref":"wahoo-e2e"}`)
	require.Equal(t, http.StatusCreated, code, string(body))
	var user struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &user))
	u := "/v1/users/" + user.ID

	code, body = api.do(http.MethodPost, u+"/connections/wahoo", `{"redirect_uri":"https://app.example.org/connected"}`)
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
	assert.Equal(t, "user_read workouts_read offline_data", q.Get("scope"))
	assert.Equal(t, api.base+"/oauth/wahoo/callback", q.Get("redirect_uri"))

	// The user consents: Wahoo sends the browser back with a code.
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

	// --- the backfill, through the ETL, read back ---
	rng := "start=" + today.AddDate(0, 0, -10).Format(time.DateOnly) + "&end=" + today.AddDate(0, 0, 1).Format(time.DateOnly)
	workouts := func() []apiWorkout {
		var out []apiWorkout
		api.page(u+"/workouts?"+rng, &out)
		return out
	}
	weights := func() []map[string]any {
		var out []map[string]any
		api.page(u+"/samples?type=body_mass&"+rng, &out)
		return out
	}
	require.Eventually(t, func() bool { return len(workouts()) == 2 && len(weights()) == 1 },
		90*time.Second, 250*time.Millisecond, "the backfill lands")

	byID := map[string]apiWorkout{}
	for _, w := range workouts() {
		byID[w.ExternalID] = w
	}
	r := byID["2001"]
	assert.Equal(t, "cycling", r.Activity, "type 15 is BIKING_ROAD")
	assert.True(t, r.Start.Equal(ride))
	require.NotNil(t, r.DistanceM)
	assert.Equal(t, 24909.71, *r.DistanceM)
	assert.Equal(t, 100.0, *r.AvgHR)
	assert.Equal(t, "America/Denver", r.Metadata["timezone"])
	rn := byID["2002"]
	assert.Equal(t, "running", rn.Activity)
	assert.Nil(t, rn.DistanceM, "listed without a summary: minutes only")
	assert.Equal(t, 71.5, weights()[0]["value"])

	// --- webhooks ---
	hook := func(token string, hr string) (int, []byte) {
		w := wahoofake.Edit(wahoofake.File("webhook.json"), nil)
		var m map[string]any
		require.NoError(t, json.Unmarshal(w, &m))
		m["webhook_token"] = token
		s := m["workout_summary"].(map[string]any)
		s["heart_rate_avg"] = hr
		wk := s["workout"].(map[string]any)
		wk["id"], wk["starts"], wk["workout_type_id"] = 2002, stamp(run), 1
		b, err := json.Marshal(m)
		require.NoError(t, err)
		return api.do(http.MethodPost, "/inbound/wahoo", string(b))
	}
	code, _ = hook("forged-token", "180.00")
	assert.Equal(t, http.StatusUnauthorized, code, "a webhook without the right token is refused")

	code, body = hook(e2eToken, "141.50")
	require.Equal(t, http.StatusOK, code, string(body))
	require.Eventually(t, func() bool {
		for _, w := range workouts() {
			if w.ExternalID == "2002" && w.AvgHR != nil && *w.AvgHR == 141.5 {
				return true
			}
		}
		return false
	}, 60*time.Second, 250*time.Millisecond, "the webhook's summary replaces the stored workout")
	assert.Len(t, workouts(), 2, "replaced, not added")

	// --- a manual sync: Wahoo now lists the summary too. Nothing is
	// duplicated. ---
	f.SetWorkouts([]json.RawMessage{
		wahoofake.Edit(base, map[string]any{"id": 2001, "starts": stamp(ride), "workout_type_id": 15}),
		wahoofake.Edit(base, map[string]any{"id": 2002, "starts": stamp(run), "workout_type_id": 1}),
	}, map[string]json.RawMessage{
		"2001": summary,
		"2002": wahoofake.Edit(summary, map[string]any{"heart_rate_avg": "141.50"}),
	})
	from := today.AddDate(0, 0, -5).Format(time.RFC3339)
	code, body = api.do(http.MethodPost, u+"/connections/wahoo/sync", fmt.Sprintf(`{"from":%q}`, from))
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, code, string(body))
	require.Eventually(t, func() bool {
		return len(f.Calls("/v1/workouts/2002/workout_summary")) >= 2
	}, 60*time.Second, 250*time.Millisecond, "the manual sync pulls")
	waitIdle(t, api, u)
	ws := workouts()
	assert.Len(t, ws, 2, "no duplicate workout")
	assert.Len(t, weights(), 1, "no duplicate weight")

	for _, r := range f.Requests() {
		if strings.HasPrefix(r.Path, "/v1/") {
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
		code, b := api.do(http.MethodGet, u+"/coverage", "")
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
