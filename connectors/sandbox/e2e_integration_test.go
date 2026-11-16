// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package sandbox_test

import (
	"context"
	"encoding/json"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/sandbox"
	"github.com/soma-intelligence/open-health-sync/internal/app"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// The whole connect flow through the real app (api, collect and etl roles, a
// real database), with nothing but OHS_SANDBOX_ENABLED set: the API hands out
// an authorize URL on the instance itself, the consent page is served there,
// the browser comes back to the callback with a code, the core exchanges it,
// the backfill refreshes the short-lived token and pulls, the ETL stores, and
// the public API serves sleep, heart rate, steps and workouts, every record
// from provider sandbox and marked synthetic.

type e2eAPI struct {
	t    *testing.T
	base string
	key  string
	c    *http.Client
}

func (a e2eAPI) do(method, path, body string, auth bool) (int, []byte, http.Header) {
	a.t.Helper()
	req, err := http.NewRequest(method, a.base+path, strings.NewReader(body))
	require.NoError(a.t, err)
	if auth {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.c.Do(req)
	require.NoError(a.t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b, res.Header
}

func (a e2eAPI) page(path string, out any) bool {
	code, b, _ := a.do(http.MethodGet, path, "", true)
	if code != http.StatusOK {
		return false
	}
	return json.Unmarshal(b, &struct {
		Data any `json:"data"`
	}{out}) == nil
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().String()
}

var hrefRE = regexp.MustCompile(`href="([^"]+)"`)

func TestEndToEnd_SandboxConnectFlow(t *testing.T) {
	ctx := context.Background()
	c, ok := connector.Get(sandbox.Slug)
	require.True(t, ok)
	sc := c.(*sandbox.Connector)
	// Shorter than the core's refresh margin: every pull refreshes first.
	sandbox.SetAccessTTL(sc, 2*time.Minute)

	dbURL := db.TestDB(t)
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, key, err := tenant.New(pool).CreateTenant(ctx, "sandbox-e2e")
	require.NoError(t, err)

	listen := freeAddr(t)
	encKey := make([]byte, 32)
	vaultCfg, err := config.DerivedVault(encKey)
	require.NoError(t, err)
	cfg := config.Config{
		DatabaseURL: dbURL, RawStore: "file://" + t.TempDir(), Listen: listen, LogLevel: "warn",
		PublicURL: "http://" + listen, EncryptionKey: encKey, Vault: vaultCfg,
		Roles:     map[string]bool{"api": true, "etl": true, "collect": true},
		Providers: map[string]config.ProviderKeys{sandbox.Slug: {sandbox.KeyEnabled: "true"}},
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
		Timeout:       30 * time.Second,
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

	code, body, _ := api.do(http.MethodGet, "/v1/providers", "", true)
	require.Equal(t, http.StatusOK, code, string(body))
	assert.Contains(t, string(body), `"slug":"sandbox"`)

	// --- connect ---
	code, body, _ = api.do(http.MethodPost, "/v1/users", `{"external_ref":"sandbox-e2e"}`, true)
	require.Equal(t, http.StatusCreated, code, string(body))
	var user struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &user))
	u := "/v1/users/" + user.ID

	code, body, _ = api.do(http.MethodPost, u+"/connections/sandbox", `{"redirect_uri":"https://app.example.org/connected"}`, true)
	require.Equal(t, http.StatusOK, code, string(body))
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	require.NoError(t, json.Unmarshal(body, &start))
	authz, err := url.Parse(start.AuthorizeURL)
	require.NoError(t, err)
	assert.Equal(t, api.base+"/inbound/sandbox", authz.Scheme+"://"+authz.Host+authz.Path, "the consent page is on the instance")
	assert.Equal(t, api.base+"/oauth/sandbox/callback", authz.Query().Get("redirect_uri"))

	// The browser opens the consent page (no API key).
	res, err := api.c.Get(start.AuthorizeURL)
	require.NoError(t, err)
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode, string(page))
	assert.Contains(t, res.Header.Get("Content-Type"), "text/html")
	assert.Contains(t, string(page), "This is not a real provider.")

	var allow string
	for _, m := range hrefRE.FindAllStringSubmatch(string(page), -1) {
		link := html.UnescapeString(m[1])
		if strings.Contains(link, "code=") && strings.Contains(page2text(page, m[0]), "Allow as runner") {
			allow = link
		}
	}
	require.NotEmpty(t, allow, "an allow link for the runner persona")

	// The user allows: the browser lands on the callback with the code.
	res, err = api.c.Get(allow)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusFound, res.StatusCode)
	assert.Contains(t, res.Header.Get("Location"), "status=connected")
	assert.EqualValues(t, 1, sandbox.Exchanges(sc), "the core traded the code at the sandbox token endpoint")

	code, body, _ = api.do(http.MethodGet, u+"/connections", "", true)
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(body), `"status":"active"`)
	assert.Contains(t, string(body), `"provider":"sandbox"`)

	// --- the backfill, through the ETL, read back ---
	today := time.Now().UTC().Truncate(24 * time.Hour)
	rng := "start=" + today.AddDate(0, 0, -10).Format(time.DateOnly) + "&end=" + today.AddDate(0, 0, 1).Format(time.DateOnly)
	type rec struct {
		Provider string         `json:"provider"`
		Source   string         `json:"source_name"`
		Metadata map[string]any `json:"metadata"`
	}
	list := func(path string) []rec {
		var out []rec
		api.page(u+path+rng, &out)
		return out
	}
	require.Eventually(t, func() bool {
		return len(list("/sleep?")) >= 5 && len(list("/samples?type=heart_rate&")) > 0 &&
			len(list("/samples?type=step_count&")) > 0 && len(list("/workouts?")) > 0
	}, 120*time.Second, 500*time.Millisecond, "the backfill lands")

	for _, path := range []string{"/sleep?", "/samples?type=heart_rate&", "/samples?type=step_count&", "/workouts?"} {
		for _, r := range list(path) {
			if r.Provider != "" {
				assert.Equal(t, sandbox.Slug, r.Provider, path)
			}
			assert.Equal(t, true, r.Metadata["synthetic"], "%s: every record is marked synthetic", path)
			assert.Equal(t, "runner", r.Metadata["sandbox_persona"], path)
		}
	}
	for _, r := range list("/samples?type=heart_rate&") {
		assert.Equal(t, sandbox.SourceName, r.Source)
	}
	assert.Positive(t, sandbox.Refreshes(sc), "the short-lived access token was refreshed by the core")

	// The sandbox takes no notifications.
	code, _, _ = api.do(http.MethodPost, "/inbound/sandbox", `{}`, false)
	assert.Equal(t, http.StatusUnauthorized, code)
}

// page2text returns the text of the anchor whose opening tag is tag.
func page2text(page []byte, tag string) string {
	s := string(page)
	i := strings.Index(s, tag)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], "</a>")
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}
