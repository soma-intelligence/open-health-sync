// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Freshness states of a source.
const (
	FreshnessFresh        = "fresh"        // data within the expected lag
	FreshnessStale        = "stale"        // behind by more than expected
	FreshnessFailing      = "failing"      // the last FailingAfter pulls failed
	FreshnessSyncing      = "syncing"      // the first pull has not finished yet
	FreshnessNeedsReauth  = "needs_reauth" // the user must reconnect
	FreshnessDisconnected = "disconnected" // no longer synced
)

// Source kinds.
const (
	KindOAuth  = "oauth"  // the server pulls from the provider
	KindDevice = "device" // an app on the phone uploads (Apple Health)
)

// FailingAfter is the number of failed pulls in a row after which a
// connection is reported failing.
const FailingAfter = 3

// DeviceStaleAfter is how long a device source may go without an upload
// before it is reported stale. Phones upload when the OS lets the app run,
// at least daily in practice.
const DeviceStaleAfter = 26 * time.Hour

// SourceFreshness is how up to date one source of a user is.
type SourceFreshness struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	// Status is the connection status (active, needs_reauth, disconnected);
	// a device source is always active.
	Status string `json:"status"`
	State  string `json:"state"`
	// SyncedThrough is the end of the most recent window pulled (OAuth), or
	// the time of the last upload (device).
	SyncedThrough *time.Time `json:"synced_through"`
	// LagSeconds is now minus SyncedThrough.
	LagSeconds  *int64     `json:"lag_seconds"`
	LastSyncAt  *time.Time `json:"last_sync_at"`
	LastEventAt *time.Time `json:"last_event_at"`
	// PushHealthy: the provider's notifications arrived within PushSilence,
	// so the regular pull is only a backstop.
	PushHealthy bool `json:"push_healthy"`
	// PullEverySeconds is the regular pull interval (0 for a device source).
	PullEverySeconds    int64      `json:"pull_every_seconds"`
	NextPullAt          *time.Time `json:"next_pull_at"`
	LastAttemptAt       *time.Time `json:"last_attempt_at"`
	LastError           *string    `json:"last_error"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
}

// FreshnessList is the Freshness response.
type FreshnessList struct {
	AsOf time.Time         `json:"as_of"`
	Data []SourceFreshness `json:"data"`
}

// freshRow is what the freshness of one connection is computed from.
type freshRow struct {
	provider, status    string
	syncedThrough       *time.Time
	lastSync, lastEvent *time.Time
	lastAttempt         *time.Time
	lastError           *string
	failures            int
}

// oauthFreshness derives the view of a connection at now. push says whether
// the provider sends notifications.
func oauthFreshness(r freshRow, push bool, now time.Time) SourceFreshness {
	healthy := pushHealthy(push, r.lastEvent, now)
	every := pullEvery(healthy)
	f := SourceFreshness{
		Provider: r.provider, Kind: KindOAuth, Status: r.status,
		SyncedThrough: utcPtr(r.syncedThrough), LastSyncAt: utcPtr(r.lastSync),
		LastEventAt: utcPtr(r.lastEvent), LastAttemptAt: utcPtr(r.lastAttempt),
		LastError: r.lastError, ConsecutiveFailures: r.failures,
		PushHealthy: healthy, PullEverySeconds: int64(every / time.Second),
	}
	if r.syncedThrough != nil {
		lag := int64(now.Sub(*r.syncedThrough) / time.Second)
		f.LagSeconds = &lag
	}
	switch r.status {
	case StatusNeedsReauth:
		f.State = FreshnessNeedsReauth
		return f
	case StatusDisconnected:
		f.State = FreshnessDisconnected
		return f
	}
	next := now.UTC().Truncate(every).Add(every)
	f.NextPullAt = &next
	switch {
	case r.failures >= FailingAfter:
		f.State = FreshnessFailing
	case r.syncedThrough == nil:
		f.State = FreshnessSyncing
	case now.Sub(*r.syncedThrough) > staleAfter(every):
		f.State = FreshnessStale
	default:
		f.State = FreshnessFresh
	}
	return f
}

// staleAfter is the lag past which a connection pulled every `every` is
// stale: just before a slot the data can be one interval plus the time the
// previous slot's pull took behind, so two intervals is the normal worst case.
func staleAfter(every time.Duration) time.Duration { return 2*every + pullOverlap }

// deviceFreshness derives the view of a device source from its last upload.
func deviceFreshness(provider string, lastUpload time.Time, now time.Time) SourceFreshness {
	at := lastUpload.UTC()
	lag := int64(now.Sub(at) / time.Second)
	state := FreshnessFresh
	if now.Sub(at) > DeviceStaleAfter {
		state = FreshnessStale
	}
	return SourceFreshness{
		Provider: provider, Kind: KindDevice, Status: StatusActive, State: state,
		SyncedThrough: &at, LastSyncAt: &at, LagSeconds: &lag,
	}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// isPush reports whether provider sends notifications on this instance.
func (d Deps) isPush(provider string) bool {
	c, ok := d.lookup(provider)
	if !ok {
		return false
	}
	_, push := connector.AsPush(c)
	return push
}

// typed reports whether provider's connector can pull only what a
// notification names.
func (d Deps) typed(provider string) bool {
	oc, ok := d.oauth(provider)
	if !ok {
		return false
	}
	_, ok = oc.(connector.NotifiedFetcher)
	return ok
}

// isDevice reports whether provider is an on-device store uploaded by an app.
func (d Deps) isDevice(provider string) bool {
	c, ok := d.lookup(provider)
	if !ok {
		return false
	}
	_, dev := connector.AsDevice(c)
	return dev
}

// Freshness lists how up to date every source of a user is: one entry per
// provider connection, then one per device source that uploaded, ordered by
// provider. read.ErrNotFound when the user is not the tenant's.
func (d Deps) Freshness(ctx context.Context, tenantID, userID uuid.UUID) (FreshnessList, error) {
	now := d.now()
	var exists bool
	if err := d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_core.end_users
		WHERE tenant_id = $1 AND id = $2)`, tenantID, userID).Scan(&exists); err != nil {
		return FreshnessList{}, fmt.Errorf("freshness: user: %w", err)
	}
	if !exists {
		return FreshnessList{}, read.ErrNotFound
	}
	rows, err := d.Pool.Query(ctx, `
		SELECT provider, status, synced_through, last_sync_at, last_event_at, last_attempt_at,
			last_error, consecutive_failures
		FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2 ORDER BY provider`, tenantID, userID)
	if err != nil {
		return FreshnessList{}, fmt.Errorf("freshness: connections: %w", err)
	}
	conns, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (freshRow, error) {
		var x freshRow
		err := r.Scan(&x.provider, &x.status, &x.syncedThrough, &x.lastSync, &x.lastEvent,
			&x.lastAttempt, &x.lastError, &x.failures)
		return x, err
	})
	if err != nil {
		return FreshnessList{}, fmt.Errorf("freshness: connections: %w", err)
	}
	out := FreshnessList{AsOf: now.UTC(), Data: []SourceFreshness{}}
	connected := map[string]bool{}
	for _, c := range conns {
		connected[c.provider] = true
		out.Data = append(out.Data, oauthFreshness(c, d.isPush(c.provider), now))
	}

	// Device sources have no connection row: their last upload is the
	// newest raw object they stored.
	rows, err = d.Pool.Query(ctx, `
		SELECT provider, max(created_at) FROM ohs_sync.raw_objects
		WHERE tenant_id = $1 AND user_id = $2 GROUP BY provider`, tenantID, userID)
	if err != nil {
		return FreshnessList{}, fmt.Errorf("freshness: uploads: %w", err)
	}
	type upload struct {
		provider string
		at       time.Time
	}
	ups, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (upload, error) {
		var x upload
		err := r.Scan(&x.provider, &x.at)
		return x, err
	})
	if err != nil {
		return FreshnessList{}, fmt.Errorf("freshness: uploads: %w", err)
	}
	for _, u := range ups {
		if connected[u.provider] || !d.isDevice(u.provider) {
			continue
		}
		out.Data = append(out.Data, deviceFreshness(u.provider, u.at, now))
	}
	sort.SliceStable(out.Data, func(i, j int) bool { return out.Data[i].Provider < out.Data[j].Provider })
	return out, nil
}

// Freshness serves GET /v1/users/{id}/freshness. Mount behind
// auth.Middleware with auth.TenantKey.
func (h *Handlers) Freshness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID, ok := h.caller(w, r)
		if !ok {
			return
		}
		list, err := h.d.Freshness(r.Context(), tenantID, userID)
		if errors.Is(err, read.ErrNotFound) {
			writeNotFound(w, "user not found")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "collect: freshness", "tenant_id", tenantID, "user_id", userID, "error", err)
			writeInternal(w)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
}

// ProviderSync is the instance-wide sync state of one provider's active
// connections, for /metrics.
type ProviderSync struct {
	Provider string
	// States counts active connections by freshness state.
	States map[string]int64
	// MaxLagSeconds is the largest lag among active connections that
	// finished their first pull (0 when there is none).
	MaxLagSeconds int64
	// PushSilent counts connections of a notifying provider whose
	// notifications did not arrive within PushSilence.
	PushSilent int64
}

// SyncMetrics aggregates the freshness of every active connection per
// provider. It reads every active connection: fine for a scrape every few
// seconds up to hundreds of thousands of connections.
func (h *Handlers) SyncMetrics(ctx context.Context) ([]ProviderSync, error) {
	d := h.d
	now := d.now()
	by := map[string]*ProviderSync{}
	push := map[string]bool{}
	rows, err := d.Pool.Query(ctx, `
		SELECT provider, status, synced_through, last_sync_at, last_event_at, last_attempt_at,
			last_error, consecutive_failures
		FROM ohs_sync.connections WHERE status = 'active'`)
	if err != nil {
		return nil, fmt.Errorf("sync metrics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var x freshRow
		if err := rows.Scan(&x.provider, &x.status, &x.syncedThrough, &x.lastSync, &x.lastEvent,
			&x.lastAttempt, &x.lastError, &x.failures); err != nil {
			return nil, fmt.Errorf("sync metrics: %w", err)
		}
		p, ok := by[x.provider]
		if !ok {
			p = &ProviderSync{Provider: x.provider, States: map[string]int64{}}
			by[x.provider] = p
			push[x.provider] = d.isPush(x.provider)
		}
		f := oauthFreshness(x, push[x.provider], now)
		p.States[f.State]++
		if f.LagSeconds != nil && *f.LagSeconds > p.MaxLagSeconds {
			p.MaxLagSeconds = *f.LagSeconds
		}
		if push[x.provider] && !f.PushHealthy {
			p.PushSilent++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync metrics: %w", err)
	}
	out := make([]ProviderSync, 0, len(by))
	for _, p := range by {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

// WriteSyncMetrics writes the per-provider sync gauges in the Prometheus text
// format.
func WriteSyncMetrics(w io.Writer, ps []ProviderSync) {
	states := []string{FreshnessFresh, FreshnessStale, FreshnessFailing, FreshnessSyncing}
	fmt.Fprint(w, "# HELP ohs_sync_connections Active provider connections by freshness state.\n")
	fmt.Fprint(w, "# TYPE ohs_sync_connections gauge\n")
	for _, p := range ps {
		for _, s := range states {
			fmt.Fprintf(w, "ohs_sync_connections{provider=%q,state=%q} %d\n", p.Provider, s, p.States[s])
		}
	}
	fmt.Fprint(w, "# HELP ohs_sync_max_lag_seconds Largest lag of an active connection, by provider.\n")
	fmt.Fprint(w, "# TYPE ohs_sync_max_lag_seconds gauge\n")
	for _, p := range ps {
		fmt.Fprintf(w, "ohs_sync_max_lag_seconds{provider=%q} %d\n", p.Provider, p.MaxLagSeconds)
	}
	fmt.Fprint(w, "# HELP ohs_sync_push_silent_connections Connections whose provider notifications stopped arriving.\n")
	fmt.Fprint(w, "# TYPE ohs_sync_push_silent_connections gauge\n")
	for _, p := range ps {
		fmt.Fprintf(w, "ohs_sync_push_silent_connections{provider=%q} %d\n", p.Provider, p.PushSilent)
	}
}
