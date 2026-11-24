// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// appFakeSlug is a test OAuth + push provider registered in the default
// registry. It is only enabled when its key is configured.
const appFakeSlug = "appfake"

var appFake = &appFakeConnector{}

func init() { connector.Register(appFake) }

// appFakeConnector consents at once (its authorize page redirects straight back
// with a code) and serves one heart rate sample two hours old.
type appFakeConnector struct {
	srv        *httptest.Server
	sampleAt   time.Time
	configured atomic.Value // connector.Config
	revoked    atomic.Int64
}

func (f *appFakeConnector) Slug() string { return appFakeSlug }

func (f *appFakeConnector) Enabled(cfg connector.Config) bool { return cfg["CLIENT_ID"] != "" }

func (f *appFakeConnector) Configure(cfg connector.Config) error {
	if cfg["CLIENT_ID"] == "refuse" {
		return fmt.Errorf("bad client id")
	}
	f.configured.Store(cfg)
	return nil
}

func (f *appFakeConnector) AuthorizeURL(state, redirect string) (string, error) {
	v := url.Values{"state": {state}, "redirect_uri": {redirect}}
	return f.srv.URL + "/authorize?" + v.Encode(), nil
}

func (f *appFakeConnector) Exchange(_ context.Context, code, _ string) (connector.Tokens, error) {
	if code != "good-code" {
		return connector.Tokens{}, connector.ErrNeedsReauth
	}
	return connector.Tokens{Access: "a", Refresh: "r", Expiry: time.Now().Add(time.Hour), ProviderUserID: "p1"}, nil
}

func (f *appFakeConnector) Refresh(_ context.Context, t connector.Tokens) (connector.Tokens, error) {
	t.Expiry = time.Now().Add(time.Hour)
	return t, nil
}

func (f *appFakeConnector) Fetch(ctx context.Context, _ connector.Connection, from, to time.Time, emit connector.RawSink) error {
	if f.sampleAt.Before(from) || !f.sampleAt.Before(to) {
		return nil
	}
	line := fmt.Sprintf(`{"bpm":72,"at":%q}`, f.sampleAt.Format(time.RFC3339))
	return emit.Emit(ctx, "heart", f.sampleAt, [][]byte{[]byte(line)})
}

func (f *appFakeConnector) Map(raw connector.RawRecord) (model.MapResult, error) {
	at, err := time.Parse(time.RFC3339, fmt.Sprint(raw.Fields["at"]))
	if err != nil {
		return model.MapResult{}, err
	}
	bpm, _ := raw.Fields["bpm"].(float64)
	return model.MapResult{QuantitySamples: []model.QuantitySample{{
		UserID: raw.UserID, Provider: appFakeSlug, QuantityType: model.QTHeartRate,
		Value: bpm, Unit: "bpm", StartDate: at, EndDate: at,
		ContentHash: model.ComputeContentHash(map[string]string{"at": at.String()}),
		Metadata:    map[string]any{"timezone": "UTC"},
	}}}, nil
}

func (f *appFakeConnector) HandleInbound(context.Context, *http.Request) ([]connector.InboundEvent, error) {
	return nil, nil
}

func (f *appFakeConnector) Revoke(context.Context, connector.Tokens) error {
	f.revoked.Add(1)
	return nil
}

func startFakeOAuth(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		back, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			http.Error(w, "bad redirect", http.StatusBadRequest)
			return
		}
		back.RawQuery = url.Values{"state": {q.Get("state")}, "code": {"good-code"}}.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	appFake.srv = httptest.NewServer(mux)
	appFake.sampleAt = time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	t.Cleanup(appFake.srv.Close)
}

func collectConfig(t *testing.T) config.Config {
	cfg := testConfig(t, db.TestDB(t))
	cfg.Roles = map[string]bool{"api": true, "etl": true, "collect": true}
	cfg.PublicURL = "http://" + cfg.Listen
	cfg.Providers = map[string]config.ProviderKeys{appFakeSlug: {"CLIENT_ID": "id"}}
	return cfg
}

type apiClient struct {
	t    *testing.T
	base string
	key  string
	c    *http.Client
}

func (a apiClient) call(method, path, body string) (int, []byte) {
	a.t.Helper()
	req, err := http.NewRequest(method, a.base+path, strings.NewReader(body))
	require.NoError(a.t, err)
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	res, err := a.c.Do(req)
	require.NoError(a.t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func TestRunConnectsPullsAndServesSamples(t *testing.T) {
	startFakeOAuth(t)
	cfg := collectConfig(t)
	// Debug: the request log is on, so the query string assertion covers it.
	cfg.LogLevel = "debug"
	logs := &syncBuffer{}
	start(t, cfg, logs)
	keys := bootstrapKeys(t, cfg, logs.String())
	require.Len(t, keys, 1)
	assert.Contains(t, logs.String(), `"providers_enabled"`)
	api := apiClient{t: t, base: "http://" + cfg.Listen, key: keys[0], c: &http.Client{Timeout: 10 * time.Second}}

	got, _ := appFake.configured.Load().(connector.Config)
	assert.Equal(t, "id", got["CLIENT_ID"], "Configure got the provider keys")

	code, b := api.call("POST", "/v1/users", `{"external_ref":"fit"}`)
	require.Equal(t, http.StatusCreated, code, string(b))
	var u struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(b, &u))

	code, b = api.call("POST", "/v1/users/"+u.ID+"/connections/"+appFakeSlug, "")
	require.Equal(t, http.StatusOK, code, string(b))
	var startResp struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	require.NoError(t, json.Unmarshal(b, &startResp))

	// The browser follows the provider's consent redirect to our callback.
	res, err := (&http.Client{Timeout: 10 * time.Second}).Get(startResp.AuthorizeURL)
	require.NoError(t, err)
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode, string(page))
	assert.Equal(t, "/oauth/"+appFakeSlug+"/callback", res.Request.URL.Path)
	assert.Contains(t, res.Header.Get("Content-Type"), "text/html")

	code, b = api.call("GET", "/v1/users/"+u.ID+"/connections", "")
	require.Equal(t, http.StatusOK, code, string(b))
	assert.Contains(t, string(b), `"status":"active"`)

	// The collect role pulls the backfill, the etl role maps it.
	require.Eventually(t, func() bool {
		code, b := api.call("GET", "/v1/users/"+u.ID+"/samples?type=heart_rate", "")
		if code != http.StatusOK {
			return false
		}
		var p struct {
			Data []struct {
				Value     float64   `json:"value"`
				StartDate time.Time `json:"start_date"`
			} `json:"data"`
		}
		if json.Unmarshal(b, &p) != nil || len(p.Data) != 1 {
			return false
		}
		return p.Data[0].Value == 72 && p.Data[0].StartDate.Equal(appFake.sampleAt)
	}, 60*time.Second, 250*time.Millisecond, "the pulled sample is readable:\n%s", logs)

	// Deleting the user revokes its provider grant through collect.
	code, b = api.call("DELETE", "/v1/users/"+u.ID, "")
	require.Equal(t, http.StatusAccepted, code, string(b))
	require.Eventually(t, func() bool { return appFake.revoked.Load() >= 1 },
		20*time.Second, 100*time.Millisecond, "the deletion worker revokes the grant:\n%s", logs)

	// Provider facing routes never log their query string; the request log
	// names the route pattern.
	assert.Contains(t, logs.String(), `"route":"GET /oauth/{provider}/callback"`)
	assert.NotContains(t, logs.String(), "good-code")
	assert.NotContains(t, logs.String(), "state=")
}

func TestRunProviderFacingRoutes(t *testing.T) {
	startFakeOAuth(t)
	cfg := collectConfig(t)
	cfg.Providers["withings"] = config.ProviderKeys{"CLIENT_ID": "partial-id-value"}
	logs := &syncBuffer{}
	start(t, cfg, logs)
	assert.Contains(t, logs.String(), "provider_partially_configured")
	assert.Contains(t, logs.String(), "OHS_WITHINGS_CLIENT_SECRET")
	assert.NotContains(t, logs.String(), "partial-id-value", "key values are never logged")
	noFollow := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	anon := apiClient{t: t, base: "http://" + cfg.Listen, c: noFollow}

	code, _ := anon.call("GET", "/inbound/"+appFakeSlug, "")
	assert.Equal(t, http.StatusOK, code, "probe GET")
	code, _ = anon.call("HEAD", "/inbound/"+appFakeSlug, "")
	assert.Equal(t, http.StatusOK, code, "probe HEAD")
	code, _ = anon.call("GET", "/inbound/withings", "")
	assert.Equal(t, http.StatusNotFound, code, "a provider not enabled here")

	req, err := http.NewRequest("GET", anon.base+"/oauth/"+appFakeSlug+"/callback?state=bogus&code=good-code", nil)
	require.NoError(t, err)
	res, err := noFollow.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	assert.Equal(t, http.StatusBadRequest, res.StatusCode)
	assert.Empty(t, res.Header.Get("Location"), "a bad state never redirects")
	assert.Contains(t, res.Header.Get("Content-Type"), "text/html")
	assert.NotEmpty(t, body)

	code, _ = anon.call("GET", "/v1/users/"+"00000000-0000-0000-0000-000000000000"+"/connections", "")
	assert.Equal(t, http.StatusUnauthorized, code, "connection routes need a tenant key")
}

func TestRunRefusesBadProviderConfig(t *testing.T) {
	cfg := collectConfig(t)
	cfg.Providers[appFakeSlug] = config.ProviderKeys{"CLIENT_ID": "refuse"}
	err := run(context.Background(), cfg, &syncBuffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider appfake: configuration refused")
}
