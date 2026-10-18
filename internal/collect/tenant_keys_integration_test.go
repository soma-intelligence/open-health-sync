// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// appLog records which OAuth application (client id) made each provider
// call, across every instance of keyedFake.
type appLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *appLog) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *appLog) has(call string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Contains(l.calls, call)
}

func (l *appLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = nil
}

// keyedFake is the fake provider run with operator keys, one OAuth
// application per CLIENT_ID: it puts the client id in its consent URL, signs
// notifications with CLIENT_SECRET, and records which application exchanged
// and refreshed. It takes tenant keys (connector.Instancer).
type keyedFake struct {
	fakeConnector
	log *appLog

	mu   sync.Mutex
	keys connector.Config
}

func (k *keyedFake) RequiredKeys() []string { return []string{"CLIENT_ID", "CLIENT_SECRET"} }
func (k *keyedFake) Enabled(cfg connector.Config) bool {
	return cfg["CLIENT_ID"] != "" && cfg["CLIENT_SECRET"] != ""
}

func (k *keyedFake) Configure(cfg connector.Config) error {
	if cfg["CLIENT_ID"] == "" || cfg["CLIENT_SECRET"] == "" {
		return errors.New("fakefit: CLIENT_ID and CLIENT_SECRET are both required")
	}
	if cfg["CLIENT_ID"] == "refused-app" {
		return errors.New("fakefit: this client id is refused")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys = connector.Config{"CLIENT_ID": cfg["CLIENT_ID"], "CLIENT_SECRET": cfg["CLIENT_SECRET"]}
	return nil
}

func (k *keyedFake) NewInstance() connector.Connector {
	return &keyedFake{fakeConnector: k.fakeConnector, log: k.log}
}

func (k *keyedFake) creds() (string, string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keys["CLIENT_ID"], k.keys["CLIENT_SECRET"]
}

func (k *keyedFake) AuthorizeURL(state, redirect string) (string, error) {
	id, _ := k.creds()
	v := url.Values{"state": {state}, "redirect_uri": {redirect}, "client_id": {id}}
	return k.f.srv.URL + "/authorize?" + v.Encode(), nil
}

func (k *keyedFake) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	id, _ := k.creds()
	k.log.add("exchange:" + id)
	return k.fakeConnector.Exchange(ctx, code, redirect)
}

func (k *keyedFake) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	id, _ := k.creds()
	k.log.add("refresh:" + id)
	return k.fakeConnector.Refresh(ctx, t)
}

func (k *keyedFake) Revoke(ctx context.Context, t connector.Tokens) error {
	id, _ := k.creds()
	k.log.add("revoke:" + id)
	return k.fakeConnector.Revoke(ctx, t)
}

// HandleInbound verifies X-Fake-Signature with the application's secret.
func (k *keyedFake) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_, secret := k.creds()
	if !hmac.Equal([]byte(signWith(secret, body)), []byte(r.Header.Get("X-Fake-Signature"))) {
		return nil, errors.New("bad signature")
	}
	var n struct {
		UserID string `json:"userid"`
		Start  string `json:"startdate"`
		End    string `json:"enddate"`
	}
	if err := json.Unmarshal(body, &n); err != nil {
		return nil, err
	}
	s, _ := strconv.ParseInt(n.Start, 10, 64)
	e, _ := strconv.ParseInt(n.End, 10, 64)
	return []connector.InboundEvent{{ProviderUserID: n.UserID, DataType: "steps", From: time.Unix(s, 0), To: time.Unix(e, 0)}}, nil
}

func signWith(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

var instanceKeys = map[string]string{"CLIENT_ID": "instance-app", "CLIENT_SECRET": "instance-secret"}

// newKeyedEnv runs keyedFake with the instance keys, unless instanceKeys is
// false: the instance then does not run the provider (its Lookup misses).
func newKeyedEnv(t *testing.T, withInstanceKeys bool) (*connEnv, *appLog) {
	t.Helper()
	log := &appLog{}
	var inst *keyedFake
	e := newConnEnvTweaked(t, collect.SchedulerConfig{Concurrency: 8}, func(c fakeConnector) connector.Connector {
		inst = &keyedFake{fakeConnector: c, log: log}
		if withInstanceKeys {
			require.NoError(t, inst.Configure(instanceKeys))
		}
		return inst
	}, func(d *collect.Deps) {
		if !withInstanceKeys {
			d.Lookup = func(string) (connector.Connector, bool) { return nil, false }
		}
	})
	return e, log
}

// authorizeURL starts a connection and returns the consent URL.
func (e *connEnv) authorizeURL(t *testing.T, key string, user uuid.UUID) *url.URL {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/v1/users/"+user.String()+"/connections/"+fakeSlug, key, []byte(`{}`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp collect.StartResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	u, err := url.Parse(resp.AuthorizeURL)
	require.NoError(t, err)
	return u
}

func (e *connEnv) setTenantKeys(t *testing.T, tenantID uuid.UUID, id, secret string) int {
	t.Helper()
	_, retired, err := e.tenantKeys.Put(context.Background(), tenantID, fakeSlug,
		map[string]string{"CLIENT_ID": id, "CLIENT_SECRET": secret}, "test-key", nil)
	require.NoError(t, err)
	return retired
}

func (e *connEnv) connKeys(t *testing.T, user uuid.UUID) *uuid.UUID {
	t.Helper()
	var id *uuid.UUID
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT tenant_keys_id FROM ohs_sync.connections WHERE user_id = $1 AND provider = $2`, user, fakeSlug).Scan(&id))
	return id
}

func (e *connEnv) notifyWith(t *testing.T, secret, providerUser string) int {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"userid":%q,"startdate":"1760000000","enddate":"1760003600"}`, providerUser))
	req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
	req.Header.Set("X-Fake-Signature", signWith(secret, body))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec.Code
}

func (e *connEnv) inboundJobs(t *testing.T, user uuid.UUID) int {
	t.Helper()
	return e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE user_id = $1 AND status = 'pending'
		AND from_time = to_timestamp(1760000000)`, user)
}

// A tenant with its own keys connects its users through its own
// application, end to end: consent, exchange, refresh, notifications. Other
// tenants keep the instance's. A secret rotated keeps the connections; keys
// removed set them needs_reauth and send new connections back to the
// instance's keys.
func TestTenantKeysEndToEnd(t *testing.T) {
	e, log := newKeyedEnv(t, true)
	ctx := context.Background()
	e.addEndpoint(t)
	userB, err := e.store.CreateUser(ctx, e.tenantB, "b-1")
	require.NoError(t, err)

	assert.Zero(t, e.setTenantKeys(t, e.tenantA, "tenant-a-app", "tenant-a-secret"))
	keysA := e.tenantKeys.Tenant(e.tenantA)[fakeSlug].ID

	// The consent screen is the tenant's application; the callback URL
	// stays the instance's.
	u := e.authorizeURL(t, e.keyA, e.userA)
	assert.Equal(t, "tenant-a-app", u.Query().Get("client_id"))
	assert.Equal(t, "https://ohs.example.test/oauth/"+fakeSlug+"/callback", u.Query().Get("redirect_uri"))
	assert.Equal(t, "instance-app", e.authorizeURL(t, e.keyB, userB.ID).Query().Get("client_id"))
	_, err = e.pool.Exec(ctx, `DELETE FROM ohs_sync.oauth_states`)
	require.NoError(t, err)

	e.connect(t, e.keyA, e.userA, "pu-a")
	e.connect(t, e.keyB, userB.ID, "pu-b")
	assert.True(t, log.has("exchange:tenant-a-app"))
	assert.True(t, log.has("exchange:instance-app"))
	require.NotNil(t, e.connKeys(t, e.userA))
	assert.Equal(t, keysA, *e.connKeys(t, e.userA))
	assert.Nil(t, e.connKeys(t, userB.ID))
	e.drain(t)

	// Refresh with the application the grant was issued to.
	log.reset()
	_, err = e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET token_expires_at = now() WHERE provider = $1`, fakeSlug)
	require.NoError(t, err)
	code, _, _ := e.syncNow(t, "")
	require.Equal(t, http.StatusAccepted, code)
	e.drain(t)
	assert.True(t, log.has("refresh:tenant-a-app"), "%v", log.calls)
	assert.False(t, log.has("refresh:instance-app"), "%v", log.calls)

	// Notifications: each application's signature reaches only the
	// connections made with it.
	assert.Equal(t, http.StatusOK, e.notifyWith(t, "tenant-a-secret", "pu-a"))
	assert.Equal(t, 1, e.inboundJobs(t, e.userA))
	assert.Equal(t, http.StatusOK, e.notifyWith(t, "instance-secret", "pu-b"))
	assert.Equal(t, 1, e.inboundJobs(t, userB.ID))
	_, err = e.pool.Exec(ctx, `DELETE FROM ohs_sync.pull_jobs WHERE status = 'pending'`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, e.notifyWith(t, "instance-secret", "pu-a"), "verified by the instance's keys")
	assert.Zero(t, e.inboundJobs(t, e.userA), "but the connection was made with the tenant's")
	assert.Equal(t, http.StatusUnauthorized, e.notifyWith(t, "someone-else", "pu-a"))

	// A secret rotated: same application, the connection is kept.
	assert.Zero(t, e.setTenantKeys(t, e.tenantA, "tenant-a-app", "tenant-a-secret-2"))
	assert.Equal(t, keysA, e.tenantKeys.Tenant(e.tenantA)[fakeSlug].ID)
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA))
	assert.Equal(t, http.StatusUnauthorized, e.notifyWith(t, "tenant-a-secret", "pu-a"), "the old secret no longer verifies")
	assert.Equal(t, http.StatusOK, e.notifyWith(t, "tenant-a-secret-2", "pu-a"))
	assert.Equal(t, 1, e.inboundJobs(t, e.userA))

	// Removed: the tenant's connection needs a new consent, told by the
	// usual webhook; the other tenant is untouched.
	_, retired, found, err := e.tenantKeys.Delete(ctx, e.tenantA, fakeSlug, nil)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 1, retired)
	assert.Equal(t, collect.StatusNeedsReauth, e.connStatus(t, e.userA))
	assert.Equal(t, collect.StatusActive, e.connStatus(t, userB.ID))
	assert.Equal(t, "needs_reauth", *e.health(t).Health.Reason)
	events := e.events(t, "connection.needs_reauth")
	require.Len(t, events, 1)
	assert.Equal(t, fakeSlug, events[0]["provider"])
	assert.Zero(t, e.jobs(t, e.userA, "pending"), "pending pulls parked")
	assert.Equal(t, http.StatusUnauthorized, e.notifyWith(t, "tenant-a-secret-2", "pu-a"))

	// Connecting again goes through the instance's keys.
	u = e.authorizeURL(t, e.keyA, e.userA)
	assert.Equal(t, "instance-app", u.Query().Get("client_id"))
	e.connect(t, e.keyA, e.userA, "pu-a")
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA))
	assert.Nil(t, e.connKeys(t, e.userA))
}

// Keys naming another application (another client id) replace the old ones
// and set the connections made with them needs_reauth; a consent started
// with the old ones cannot complete.
func TestTenantKeysOtherApplication(t *testing.T) {
	e, log := newKeyedEnv(t, true)
	e.setTenantKeys(t, e.tenantA, "tenant-a-app", "tenant-a-secret")
	e.connect(t, e.keyA, e.userA, "pu-a")
	first := e.connKeys(t, e.userA)
	require.NotNil(t, first)

	state := e.start(t, e.keyA, e.userA, "https://app.example.test/done")
	assert.Equal(t, 1, e.setTenantKeys(t, e.tenantA, "tenant-a-app-2", "tenant-a-secret"))
	assert.Equal(t, collect.StatusNeedsReauth, e.connStatus(t, e.userA))
	assert.Zero(t, e.scalar(t, `SELECT count(*) FROM ohs_sync.oauth_states`), "the pending consent is dropped")
	e.fake.grant("late", "pu-a")
	rec := e.callback(t, state, "late")
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	log.reset()
	e.connect(t, e.keyA, e.userA, "pu-a")
	assert.True(t, log.has("exchange:tenant-a-app-2"))
	second := e.connKeys(t, e.userA)
	require.NotNil(t, second)
	assert.NotEqual(t, *first, *second)

	// Disconnecting revokes with the keys the connection was made with.
	log.reset()
	del := e.do(t, http.MethodDelete, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug, e.keyA, nil)
	require.Equal(t, http.StatusNoContent, del.Code, del.Body.String())
	assert.True(t, log.has("revoke:tenant-a-app-2"), "%v", log.calls)
}

// A provider the instance has no keys for is available to the tenants that
// bring theirs, and to no other.
func TestTenantKeysWithoutInstanceKeys(t *testing.T) {
	e, log := newKeyedEnv(t, false)
	userB, err := e.store.CreateUser(context.Background(), e.tenantB, "b-1")
	require.NoError(t, err)
	rec := e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug, e.keyA, []byte(`{}`))
	assert.Equal(t, http.StatusNotFound, rec.Code, "nobody has keys yet")

	e.setTenantKeys(t, e.tenantA, "tenant-a-app", "tenant-a-secret")
	assert.Equal(t, "tenant-a-app", e.authorizeURL(t, e.keyA, e.userA).Query().Get("client_id"))
	rec = e.do(t, http.MethodPost, "/v1/users/"+userB.ID.String()+"/connections/"+fakeSlug, e.keyB, []byte(`{}`))
	assert.Equal(t, http.StatusNotFound, rec.Code, "another tenant does not get it")

	e.connect(t, e.keyA, e.userA, "pu-a")
	assert.True(t, log.has("exchange:tenant-a-app"))
	e.drain(t)
	assert.Positive(t, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1`, e.userA),
		"the scheduler pulls a provider only tenants run")
	assert.Equal(t, http.StatusOK, e.notifyWith(t, "tenant-a-secret", "pu-a"))
	assert.Equal(t, 1, e.inboundJobs(t, e.userA))
	assert.Equal(t, http.StatusUnauthorized, e.notifyWith(t, "instance-secret", "pu-a"))
}

// Keys the connector refuses are not stored; keys stored that do not run
// leave the tenant without the provider rather than on the instance's
// application.
func TestTenantKeysRefused(t *testing.T) {
	e, _ := newKeyedEnv(t, true)
	_, _, err := e.tenantKeys.Put(context.Background(), e.tenantA, fakeSlug,
		map[string]string{"CLIENT_ID": "refused-app", "CLIENT_SECRET": "s"}, "test-key", nil)
	require.Error(t, err)
	assert.Zero(t, e.scalar(t, `SELECT count(*) FROM ohs_core.tenant_provider_keys`))

	e.setTenantKeys(t, e.tenantA, "tenant-a-app", "tenant-a-secret")
	// Sealed under a key this process does not have: they no longer open.
	_, err = e.pool.Exec(context.Background(), `UPDATE ohs_core.tenant_provider_keys SET keys_sealed = '\x00'`)
	require.NoError(t, err)
	require.NoError(t, e.tenantKeys.Reload(context.Background()))
	rec := e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug, e.keyA, []byte(`{}`))
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	userB, err := e.store.CreateUser(context.Background(), e.tenantB, "b-1")
	require.NoError(t, err)
	assert.Equal(t, "instance-app", e.authorizeURL(t, e.keyB, userB.ID).Query().Get("client_id"))
}
