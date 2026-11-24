// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/logutil/logtest"
)

const fakeInboundURL = "https://ohs.example.test/inbound/" + fakeSlug

func (e *connEnv) subStatus(t *testing.T) string {
	t.Helper()
	var s string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT status FROM ohs_sync.inbound_subscriptions WHERE user_id = $1`, e.userA).Scan(&s))
	return s
}

func TestSubscriptionPendingThenSubscribed(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")

	// The callback only writes the row; nothing is called at the provider.
	assert.Equal(t, "pending", e.subStatus(t))
	assert.Empty(t, e.fake.subscriptionCalls())

	// A failing subscribe is recorded and retried after a backoff.
	e.fake.subscribeFail.Store(true)
	n, err := e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, "pending", e.subStatus(t))
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.inbound_subscriptions
		WHERE attempts = 1 AND last_error LIKE '%refused%' AND next_attempt_at > now()`))
	n, err = e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "not due before the backoff")

	e.fake.subscribeFail.Store(false)
	e.setNow(time.Now().Add(2 * time.Minute))
	n, err = e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, "subscribed", e.subStatus(t))
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.inbound_subscriptions
		WHERE subscribed_at IS NOT NULL AND last_error IS NULL`))
	calls := e.fake.subscriptionCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "subscribe", calls[0].action)
	assert.Equal(t, fakeInboundURL, calls[0].url)
	assert.True(t, strings.HasPrefix(calls[0].access, "access-secret-"))

	// Nothing left to do.
	n, err = e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestSubscriptionOfDisconnectedConnectionIsDropped(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET status = 'needs_reauth'`)
	require.NoError(t, err)
	n, err := e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, "removed", e.subStatus(t))
	assert.Empty(t, e.fake.subscriptionCalls())
}

// subscribed connects user A and subscribes it, returning the access token
// the subscribe used.
func (e *connEnv) subscribed(t *testing.T) string {
	t.Helper()
	e.connect(t, e.keyA, e.userA, "pu-1")
	n, err := e.sched.SubscribePending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	calls := e.fake.subscriptionCalls()
	require.Len(t, calls, 1)
	return calls[0].access
}

func TestDisconnectUnsubscribesWithFreshToken(t *testing.T) {
	e := newConnEnv(t)
	old := e.subscribed(t)
	// The stored token has expired by the time the user disconnects.
	e.setNow(time.Now().Add(2 * time.Hour))

	rec := e.do(t, http.MethodDelete, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug, e.keyA, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	calls := e.fake.subscriptionCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, "unsubscribe", calls[1].action)
	assert.Equal(t, fakeInboundURL, calls[1].url)
	assert.NotEqual(t, old, calls[1].access, "the expired token was refreshed first")
	assert.Equal(t, int64(1), e.fake.refreshN.Load())
	require.Len(t, e.fake.revokedTokens(), 1)
	assert.Equal(t, calls[1].access, e.fake.revokedTokens()[0])
	assert.Equal(t, "removed", e.subStatus(t))
	assert.Equal(t, collect.StatusDisconnected, e.connStatus(t, e.userA))
}

func TestRevokeAllUnsubscribes(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.subscribed(t)
	require.NoError(t, e.deps.RevokeAll(ctx, e.tenantA, e.userA))
	calls := e.fake.subscriptionCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, "unsubscribe", calls[1].action)
	assert.Len(t, e.fake.revokedTokens(), 1)
	assert.Equal(t, "removed", e.subStatus(t))

	// The deletion worker runs it again on retry: nothing more is called.
	require.NoError(t, e.deps.RevokeAll(ctx, e.tenantA, e.userA))
	assert.Len(t, e.fake.subscriptionCalls(), 2)
}

func TestRevokeAllHonoursCallerContext(t *testing.T) {
	e := newConnEnv(t)
	e.subscribed(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Error(t, e.deps.RevokeAll(ctx, e.tenantA, e.userA))
	assert.Len(t, e.fake.subscriptionCalls(), 1, "a cancelled caller stops before the provider")
}

func TestInboundBodyReadDeadline(t *testing.T) {
	e := newConnEnv(t)
	defer collect.SetInboundReadTimeout(200 * time.Millisecond)()
	srv := httptest.NewServer(e.mux)
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	// Announce 100 bytes, send 2, then stall.
	_, err = fmt.Fprintf(conn, "POST /inbound/%s HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n{}", fakeSlug)
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	start := time.Now()
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err, "the server answers instead of waiting for the body")
	res.Body.Close()
	assert.Equal(t, http.StatusBadRequest, res.StatusCode)
	assert.Less(t, time.Since(start), 3*time.Second)
}

// Rows of a provider this instance lacks, older and more than a batch, do
// not keep the enabled provider's row from being subscribed.
func TestSubscriptionsOfDisabledProvidersDoNotStarve(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	for i := range 5 {
		u, err := e.store.CreateUser(ctx, e.tenantA, fmt.Sprintf("off-%d", i))
		require.NoError(t, err)
		_, err = e.pool.Exec(ctx, `INSERT INTO ohs_sync.inbound_subscriptions
			(tenant_id, user_id, provider, topic, callback_url, next_attempt_at)
			VALUES ($1, $2, 'disabledfit', 'all', 'https://x.test/inbound/disabledfit', now() - interval '1 day')`,
			e.tenantA, u.ID)
		require.NoError(t, err)
	}
	e.connect(t, e.keyA, e.userA, "pu-1")
	n, err := e.sched.SubscribePending(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, "subscribed", e.subStatus(t))
	assert.Equal(t, 5, e.scalar(t, `SELECT count(*) FROM ohs_sync.inbound_subscriptions
		WHERE provider = 'disabledfit' AND status = 'pending' AND attempts = 0`), "skipped rows are left alone")
}

// A pending row whose subscribe was tried may be live at the provider: a
// disconnect unsubscribes it too. One never tried is just removed.
func TestDisconnectUnsubscribesTriedPendingRow(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	path := "/v1/users/" + e.userA.String() + "/connections/" + fakeSlug

	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.inbound_subscriptions SET attempts = 1`)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, e.do(t, http.MethodDelete, path, e.keyA, nil).Code)
	calls := e.fake.subscriptionCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "unsubscribe", calls[0].action)
	assert.Equal(t, "removed", e.subStatus(t))

	// Reconnect: the row is pending again with no attempt; disconnecting
	// calls nothing.
	e.connect(t, e.keyA, e.userA, "pu-2")
	assert.Equal(t, "pending", e.subStatus(t))
	require.Equal(t, http.StatusNoContent, e.do(t, http.MethodDelete, path, e.keyA, nil).Code)
	assert.Len(t, e.fake.subscriptionCalls(), 1)
	assert.Equal(t, "removed", e.subStatus(t))
}

// A row listed as due but handled meanwhile (rescheduled) is not called again.
func TestSubscribeRechecksDueUnderLock(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.fake.subscribeFail.Store(true)
	n, err := e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, 1, e.scalar(t, `SELECT attempts FROM ohs_sync.inbound_subscriptions`))
	n, err = e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	e.setNow(time.Now().Add(10 * time.Minute))
	n, err = e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 2, e.scalar(t, `SELECT attempts FROM ohs_sync.inbound_subscriptions`), "attempts counted in SQL")
}

// A provider slower than the disconnect budget: the call is cut, and the
// subscription is still recorded removed and the connection disconnected.
func TestDisconnectRecordsEvenWhenProviderOutlivesBudget(t *testing.T) {
	e := newConnEnv(t)
	e.subscribed(t)
	defer collect.SetRevokeTimeout(300 * time.Millisecond)()
	e.fake.unsubscribeHang.Store(true)

	start := time.Now()
	rec := e.do(t, http.MethodDelete, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug, e.keyA, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond, "the provider call used the budget")
	assert.Equal(t, "removed", e.subStatus(t))
	assert.Equal(t, collect.StatusDisconnected, e.connStatus(t, e.userA))
}

func TestRevokeAllRecordsEvenWhenProviderOutlivesBudget(t *testing.T) {
	e := newConnEnv(t)
	e.subscribed(t)
	defer collect.SetRevokeTimeout(300 * time.Millisecond)()
	e.fake.unsubscribeHang.Store(true)

	require.NoError(t, e.deps.RevokeAll(context.Background(), e.tenantA, e.userA))
	assert.Equal(t, "removed", e.subStatus(t))
}

// The attempt is committed before the provider is called, and a disconnect
// that lands during the call is honoured: the subscription just made is
// undone.
func TestSubscribeAttemptCommittedBeforeCallAndRaceUndone(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	path := "/v1/users/" + e.userA.String() + "/connections/" + fakeSlug
	var seenAttempts int
	var deleteCode int
	hook := func() {
		seenAttempts = e.scalar(t, `SELECT attempts FROM ohs_sync.inbound_subscriptions`)
		deleteCode = e.do(t, http.MethodDelete, path, e.keyA, nil).Code
	}
	e.fake.onSubscribe.Store(&hook)

	n, err := e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, seenAttempts, "the attempt is durable before the call")
	assert.Equal(t, http.StatusNoContent, deleteCode, "no lock is held during the call")

	calls := e.fake.subscriptionCalls()
	require.Len(t, calls, 3)
	assert.Equal(t, "unsubscribe", calls[0].action, "the disconnect unsubscribes the tried row")
	assert.Equal(t, "subscribe", calls[1].action)
	assert.Equal(t, "unsubscribe", calls[2].action, "the raced subscribe is undone")
	assert.Equal(t, "removed", e.subStatus(t))
}

// A disconnect and reconnect during a slow subscribe reset the same row: the
// old attempt must not record its outcome on the new incarnation, and the
// subscription it made is undone with the tokens it used.
func TestSubscribeRacedByReconnectLeavesNewRowPending(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	path := "/v1/users/" + e.userA.String() + "/connections/" + fakeSlug
	var once bool
	hook := func() {
		if once {
			return
		}
		once = true
		require.Equal(t, http.StatusNoContent, e.do(t, http.MethodDelete, path, e.keyA, nil).Code)
		e.connect(t, e.keyA, e.userA, "pu-1")
	}
	e.fake.onSubscribe.Store(&hook)

	n, err := e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	assert.Equal(t, "pending", e.subStatus(t), "the new incarnation waits for its own attempt")
	assert.Equal(t, 0, e.scalar(t, `SELECT attempts FROM ohs_sync.inbound_subscriptions`))
	calls := e.fake.subscriptionCalls()
	require.Len(t, calls, 3)
	assert.Equal(t, "unsubscribe", calls[0].action, "the disconnect unsubscribes the tried row")
	assert.Equal(t, "subscribe", calls[1].action)
	assert.Equal(t, "unsubscribe", calls[2].action, "the raced subscribe is undone")
	assert.Equal(t, calls[1].access, calls[2].access, "undone with the tokens it used")

	// The new incarnation subscribes normally on the next pass.
	e.fake.onSubscribe.Store(nil)
	n, err = e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, "subscribed", e.subStatus(t))
}

// A process without the provider's keys cannot call the provider: RevokeAll
// says so in the log (no secret in it) instead of skipping silently, and
// still marks the subscription removed.
func TestRevokeAllWarnsWhenProviderNotEnabled(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-off")

	buf := logtest.Capture(t, zerolog.WarnLevel)

	d := e.deps
	d.Lookup = func(string) (connector.Connector, bool) { return nil, false }
	require.NoError(t, d.RevokeAll(ctx, e.tenantA, e.userA))
	out := buf.String()
	assert.Contains(t, out, `"message":"provider_not_enabled_grant_not_revoked"`)
	assert.NotContains(t, out, "secret")
	assert.Empty(t, e.fake.revokedTokens())
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.inbound_subscriptions WHERE status <> 'removed'`))
}
