// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	spec "github.com/soma-intelligence/open-health-sync/api"
	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
	"github.com/soma-intelligence/open-health-sync/internal/tenantkeys"
	"github.com/soma-intelligence/open-health-sync/internal/tenantproviders"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// everyRoute turns every optional area of the router on, over the pool
// that never connects: the routes are mounted, none of them is served.
func everyRoute(t *testing.T) func(*Deps) {
	t.Helper()
	return func(d *Deps) {
		ctx := context.Background()
		box, err := crypto.NewBox(make([]byte, 32))
		require.NoError(t, err)
		dev := applehealth.New()
		d.Raw, err = rawstore.Open("file://" + t.TempDir())
		require.NoError(t, err)
		d.Providers, err = providerset.New(ctx, providerset.Options{Connectors: []connector.Connector{dev}})
		require.NoError(t, err)
		d.TenantProviders = tenantproviders.New(ctx, tenantproviders.NewPG(d.Pool))
		d.TenantKeys = tenantkeys.New(ctx, tenantkeys.Options{Connectors: []connector.Connector{dev}})
		d.Device, d.HealthConnect, d.Xiaomi, d.ActivityFiles, d.Bluetooth, d.SelfReport, d.Labs = dev, dev, dev, dev, dev, dev, dev
		d.Collect = collect.NewHandlers(collect.Deps{Pool: d.Pool, Box: box, Tenants: d.Store})
		d.Webhooks = webhook.NewStore(d.Pool, box)
		d.Settings = settings.NewLive(settings.LiveOptions{Store: settings.NewPG(d.Pool, box), Env: func(string) (string, bool) { return "", false }, Environ: []string{}})
		d.PublicURL = "http://localhost:8080" // sign in reaches its body check
		d.Console, d.Metrics, d.MetricsToken, d.SetupToken = true, true, "metrics-token-0123456789", "the-right-setup-token"
		d.RateLimitAPIOff = true
	}
}

// operation is one operation of api/openapi.yaml.
type operation struct {
	method, path string
	// scope is its x-required-scope, empty when it has none.
	scope string
	// public is true for an operation that takes no API key or user token.
	public bool
}

// specOperations reads the operations of api/openapi.yaml.
func specOperations(t *testing.T) []operation {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(spec.OpenAPI, &doc))
	var ops []operation
	for path, item := range doc.Paths {
		for method, node := range item {
			if !slices.Contains([]string{"get", "post", "put", "patch", "delete"}, method) {
				continue
			}
			var op struct {
				Scope    string              `yaml:"x-required-scope"`
				Security *[]map[string][]any `yaml:"security"`
			}
			require.NoError(t, node.Decode(&op), "%s %s", method, path)
			o := operation{method: strings.ToUpper(method), path: path, scope: op.Scope}
			if op.Security != nil {
				o.public = true
				for _, alt := range *op.Security {
					if _, ok := alt["tenantKey"]; ok {
						o.public = false
					}
					if _, ok := alt["userToken"]; ok {
						o.public = false
					}
					if _, ok := alt["consoleSession"]; ok {
						o.public = false
					}
				}
			}
			ops = append(ops, o)
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].path+" "+ops[i].method < ops[j].path+" "+ops[j].method })
	require.Greater(t, len(ops), 100)
	return ops
}

var (
	routeLiteral = regexp.MustCompile(`"(GET|POST|PUT|PATCH|DELETE) (/[^"\s]*)"`)
	pathValue    = regexp.MustCompile(`\{[^}]*\}`)
)

// sourceRoutes lists the routes the package's own source names in a string
// ("GET /v1/users"): the routes the spec may not know. A route an admin
// serves for a tenant is written without its prefix, so each one comes a
// second time under adminTenantPrefix; a path that is mounted nowhere
// answers 404 and counts for nothing.
func sourceRoutes(t *testing.T) [][2]string {
	t.Helper()
	files, err := os.ReadDir(".")
	require.NoError(t, err)
	var out [][2]string
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(f.Name())
		require.NoError(t, err)
		for _, m := range routeLiteral.FindAllStringSubmatch(string(src), -1) {
			out = append(out, [2]string{m[1], m[2]}, [2]string{m[1], adminTenantPrefix + m[2]})
		}
	}
	require.Greater(t, len(out), 100)
	return out
}

// Every route behind an API key or a user token needs exactly one key scope,
// declared in routeScopes (or, under /v1/admin, by its method), and the
// spec says which. The test calls every operation of api/openapi.yaml and
// every route the source names through the real router, with credentials
// that are valid and hold no scope at all: a route behind a credential
// answers 403 naming the scope it needs, before its handler runs. A new
// route without a scope needs "*" and fails here.
func TestEveryAuthenticatedRouteHasAScope(t *testing.T) {
	user := uuid.New()
	key, keyHash := newKey(t, auth.PrefixSecretKey)
	token, tokenHash := newKey(t, auth.PrefixUserToken)
	tenantID := uuid.New()
	g := newGuardServer(t, map[string]auth.Principal{
		keyHash:   {TenantID: tenantID, KeyID: uuid.NewString(), Admin: true, KeyScopes: []string{}},
		tokenHash: {TenantID: tenantID, UserID: &user, Scopes: []string{"upload", "connect", "read"}, KeyID: "aaaa", KeyScopes: []string{}},
	}, everyRoute(t))

	// call answers the scope a route needs, "" when it takes no API key or
	// user token (or is not mounted).
	call := func(method, path string) string {
		target := pathValue.ReplaceAllStringFunc(path, func(v string) string { return strings.Trim(v, "{}.") })
		for _, secret := range []string{key, token} {
			req := httptest.NewRequest(method, target, nil)
			req.Header.Set("Authorization", "Bearer "+secret)
			rec := httptest.NewRecorder()
			g.h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				return ""
			}
			_, detail := problemOf(t, rec)
			if scope, ok := strings.CutPrefix(detail, "this endpoint needs the scope "); ok {
				scope, _, _ = strings.Cut(scope, ",")
				return scope
			}
			// The wrong kind of credential: try the other.
		}
		t.Errorf("%s %s refuses an API key and a user token alike", method, path)
		return ""
	}

	for _, op := range specOperations(t) {
		got := call(op.method, op.path)
		switch {
		case op.public:
			assert.Empty(t, got, "%s %s takes no credential in the spec and needs a scope", op.method, op.path)
			assert.Empty(t, op.scope, "%s %s takes no credential and has an x-required-scope", op.method, op.path)
		case got == "":
			t.Errorf("%s %s takes a credential in the spec; the router serves it without one, or not at all", op.method, op.path)
		default:
			assert.NotEqual(t, auth.ScopeAll, got, "%s %s has no scope: add it to routeScopes", op.method, op.path)
			assert.Equal(t, got, op.scope, "%s %s: x-required-scope in api/openapi.yaml", op.method, op.path)
		}
	}
	for _, rt := range sourceRoutes(t) {
		assert.NotEqual(t, auth.ScopeAll, call(rt[0], rt[1]), "%s %s has no scope: add it to routeScopes", rt[0], rt[1])
	}

	// What was refused is in the audit log, with the scope it lacked. The
	// events also tell which routes were reached: an entry of routeScopes
	// that names no route is a leftover, and every scope opens a route.
	reached, used := map[string]string{}, map[string]bool{}
	g.mu.Lock()
	for _, ev := range g.events {
		require.Equal(t, http.StatusForbidden, ev.Status, ev.Route)
		scope, _ := ev.Detail["missing_scope"].(string)
		require.NotEmpty(t, scope, ev.Route)
		reached[ev.Route], used[scope] = scope, true
	}
	g.mu.Unlock()
	for pattern, scope := range routeScopes {
		assert.Equal(t, scope, reached[pattern], "routeScopes names %q, which no route answers to", pattern)
		assert.Contains(t, auth.KeyScopes, scope, pattern)
	}
	for _, scope := range auth.KeyScopes[1:] {
		assert.True(t, used[scope], "no route needs the scope %s", scope)
	}
	if out := os.Getenv("OHS_DUMP_ROUTE_SCOPES"); out != "" {
		var b strings.Builder
		for _, op := range specOperations(t) {
			b.WriteString(op.method + " " + op.path + "\t" + call(op.method, op.path) + "\n")
		}
		require.NoError(t, os.WriteFile(out, []byte(b.String()), 0o600))
	}
}

// A key without the scope of a route is refused with 403 and a problem
// naming the scope, the request is recorded with it, and the same key
// passes where its scopes reach. A key with a :write scope reads too.
func TestKeyScopeIsEnforcedAndAudited(t *testing.T) {
	reader, readerHash := newKey(t, auth.PrefixSecretKey)
	full, fullHash := newKey(t, auth.PrefixSecretKey)
	hooks, hooksHash := newKey(t, auth.PrefixSecretKey)
	token, tokenHash := newKey(t, auth.PrefixUserToken)
	tenantID, readerID, user := uuid.New(), uuid.NewString(), uuid.New()
	g := newGuardServer(t, map[string]auth.Principal{
		readerHash: {TenantID: tenantID, KeyID: readerID, KeyScopes: []string{auth.ScopeDataRead}},
		fullHash:   {TenantID: tenantID, KeyID: uuid.NewString(), KeyScopes: []string{auth.ScopeAll}},
		hooksHash:  {TenantID: tenantID, KeyID: uuid.NewString(), Admin: true, KeyScopes: []string{auth.ScopeWebhooksWrite, auth.ScopeAdminRead}},
		tokenHash:  {TenantID: tenantID, UserID: &user, Scopes: []string{"read"}, KeyID: "aaaa", KeyScopes: []string{auth.ScopeUsersRead}},
	}, everyRoute(t))

	// guardPath is a data read answered 404 before any query.
	assert.Equal(t, http.StatusNotFound, g.get(guardPath, reader, "").Code)
	assert.Equal(t, http.StatusNotFound, g.get(guardPath, full, "").Code)

	rec := g.get("/v1/users/not-a-user", reader, "")
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	_, detail := problemOf(t, rec)
	assert.Equal(t, "this endpoint needs the scope users:read, which this API key does not hold", detail)
	events := g.recorded(audit.UserRead)
	require.Len(t, events, 1)
	assert.Equal(t, http.StatusForbidden, events[0].Status)
	assert.Equal(t, readerID, events[0].ActorID)
	assert.Equal(t, tenantID, events[0].Tenant)
	assert.Equal(t, "GET /v1/users/{id}", events[0].Route)
	assert.Equal(t, "users:read", events[0].Detail["missing_scope"])

	// A request that passes carries no missing scope.
	for _, ev := range g.recorded(audit.DataRead) {
		assert.NotContains(t, ev.Detail, "missing_scope")
	}

	// webhooks:write reads webhooks; it does not read data.
	assert.NotEqual(t, http.StatusForbidden, g.get("/v1/webhooks/not-a-webhook", hooks, "").Code)
	assert.Equal(t, http.StatusForbidden, g.get(guardPath, hooks, "").Code)

	// An admin key with admin:read alone reads the instance and changes
	// nothing; a key that is not an admin's is refused as before, whatever
	// its scopes.
	post := func(path, secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+secret)
		rec := httptest.NewRecorder()
		g.h.ServeHTTP(rec, req)
		return rec
	}
	assert.NotEqual(t, http.StatusForbidden, g.get("/v1/admin/tenants", hooks, "").Code)
	rec = post("/v1/admin/tenants", hooks)
	require.Equal(t, http.StatusForbidden, rec.Code)
	_, detail = problemOf(t, rec)
	assert.Contains(t, detail, "needs the scope admin:write")
	rec = g.get("/v1/admin/tenants", full, "")
	require.Equal(t, http.StatusForbidden, rec.Code)
	_, detail = problemOf(t, rec)
	assert.Contains(t, detail, "an API key of the instance admin tenant")

	// A user token is held to the key that minted it: /mcp needs data:read.
	rec = g.get("/mcp", token, "")
	require.Equal(t, http.StatusForbidden, rec.Code)
	_, detail = problemOf(t, rec)
	assert.Equal(t, "this endpoint needs the scope data:read, which the API key that minted this user token does not hold", detail)
}
