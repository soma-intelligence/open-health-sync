// SPDX-License-Identifier: AGPL-3.0-only

package settings

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Heartbeat timing. A process beats every HeartbeatEvery; the API leaves out
// a row not seen for HeartbeatHideAfter, and the daily cleanup deletes one
// not seen for HeartbeatDeleteAfter. A process whose live revision trails the
// stored one for longer than StaleAfter is stale.
const (
	HeartbeatEvery       = 30 * time.Second
	HeartbeatHideAfter   = 2 * time.Minute
	HeartbeatDeleteAfter = 5 * time.Minute
	StaleAfter           = 60 * time.Second
)

// Heartbeat is what a process says about itself, fixed for its life.
type Heartbeat struct {
	ID        uuid.UUID // random at start
	Host      string
	Roles     []string // sorted
	Version   string
	StartedAt time.Time
}

// HeartbeatWriter keeps the process row of ohs_core.process_heartbeats.
type HeartbeatWriter struct {
	pool *pgxpool.Pool
	live *Live
	hb   Heartbeat
}

// NewHeartbeatWriter returns the writer of hb's row. A zero ID is replaced
// by a random one and Roles are sorted, on a copy.
func NewHeartbeatWriter(pool *pgxpool.Pool, live *Live, hb Heartbeat) *HeartbeatWriter {
	if hb.ID == uuid.Nil {
		hb.ID = uuid.New()
	}
	hb.Roles = slices.Clone(hb.Roles)
	slices.Sort(hb.Roles)
	return &HeartbeatWriter{pool: pool, live: live, hb: hb}
}

// Beat upserts the row with the current Status and seen_at = now(). The
// skipped list and the reload error carry names and messages, never values.
func (w *HeartbeatWriter) Beat(ctx context.Context) error {
	st := w.live.Status()
	skipped := st.Skipped
	if skipped == nil {
		skipped = []string{}
	}
	roles := w.hb.Roles
	if roles == nil {
		roles = []string{}
	}
	_, err := w.pool.Exec(ctx, `
INSERT INTO ohs_core.process_heartbeats
    (process_id, host, roles, version, started_at, start_revision, live_revision, skipped, reload_error, seen_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), now())
ON CONFLICT (process_id) DO UPDATE SET
    host = EXCLUDED.host, roles = EXCLUDED.roles, version = EXCLUDED.version,
    started_at = EXCLUDED.started_at, start_revision = EXCLUDED.start_revision,
    live_revision = EXCLUDED.live_revision, skipped = EXCLUDED.skipped,
    reload_error = EXCLUDED.reload_error, seen_at = now()`,
		w.hb.ID, w.hb.Host, roles, w.hb.Version, w.hb.StartedAt,
		st.StartRevision, st.LiveRevision, skipped, st.ReloadError)
	if err != nil {
		return fmt.Errorf("settings: heartbeat: %w", err)
	}
	return nil
}

// Run beats at once, on every swap of the live settings and every interval
// (HeartbeatEvery when 0), until ctx ends. A failed beat is logged and the
// loop goes on. On stop it deletes its row, best effort within 2 seconds.
func (w *HeartbeatWriter) Run(ctx context.Context, every time.Duration) error {
	if every <= 0 {
		every = HeartbeatEvery
	}
	swapped := make(chan struct{}, 1)
	w.live.Subscribe(func(Snapshot) {
		select {
		case swapped <- struct{}{}:
		default:
		}
	})
	beat := func() {
		if err := w.Beat(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("settings: heartbeat failed", "error", err)
		}
	}
	beat()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.forget()
			return nil
		case <-t.C:
			beat()
		case <-swapped:
			beat()
		}
	}
}

// forget deletes the row; the process is going away.
func (w *HeartbeatWriter) forget() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := w.pool.Exec(ctx, `DELETE FROM ohs_core.process_heartbeats WHERE process_id = $1`, w.hb.ID); err != nil {
		slog.Warn("settings: heartbeat row not removed", "error", err)
	}
}

// Process is one row as the console shows it.
type Process struct {
	Heartbeat
	SeenAt                      time.Time
	StartRevision, LiveRevision int64
	Skipped                     []string
	ReloadError                 string
	// Behind: it started before the last change to a restart setting.
	// Stale: its live revision trails the stored one and the change is
	// older than StaleAfter.
	Behind, Stale bool
}

// ListProcesses returns the processes seen within HeartbeatHideAfter, by
// start then id, and the current settings revision. Behind and stale are
// computed against the revision row.
func ListProcesses(ctx context.Context, db *pgxpool.Pool) ([]Process, int64, error) {
	var revision int64
	if err := db.QueryRow(ctx, `SELECT revision FROM ohs_core.instance_settings_revision`).Scan(&revision); err != nil {
		return nil, 0, fmt.Errorf("settings: read revision: %w", err)
	}
	rows, err := db.Query(ctx, `
SELECT p.process_id, p.host, p.roles, p.version, p.started_at, p.seen_at,
       p.start_revision, p.live_revision, p.skipped, COALESCE(p.reload_error, ''),
       p.start_revision < r.restart_revision,
       (p.live_revision < r.revision AND now() - r.changed_at > make_interval(secs => $2))
FROM ohs_core.process_heartbeats p
CROSS JOIN ohs_core.instance_settings_revision r
WHERE p.seen_at > now() - make_interval(secs => $1)
ORDER BY p.started_at, p.process_id`,
		HeartbeatHideAfter.Seconds(), StaleAfter.Seconds())
	if err != nil {
		return nil, 0, fmt.Errorf("settings: list processes: %w", err)
	}
	defer rows.Close()
	out := []Process{}
	for rows.Next() {
		var p Process
		if err := rows.Scan(&p.ID, &p.Host, &p.Roles, &p.Version, &p.StartedAt, &p.SeenAt,
			&p.StartRevision, &p.LiveRevision, &p.Skipped, &p.ReloadError, &p.Behind, &p.Stale); err != nil {
			return nil, 0, fmt.Errorf("settings: list processes: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("settings: list processes: %w", err)
	}
	return out, revision, nil
}

// PruneHeartbeats deletes the rows not seen for HeartbeatDeleteAfter, the
// processes that died without saying so, and returns how many.
func PruneHeartbeats(ctx context.Context, db *pgxpool.Pool) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM ohs_core.process_heartbeats WHERE seen_at < now() - make_interval(secs => $1)`,
		HeartbeatDeleteAfter.Seconds())
	if err != nil {
		return 0, fmt.Errorf("settings: prune heartbeats: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Processes lists the running processes through the pool the store uses.
func (l *Live) Processes(ctx context.Context) ([]Process, int64, error) {
	pg, ok := l.store.(*PG)
	if !ok {
		return nil, 0, fmt.Errorf("settings: processes need the Postgres store")
	}
	return ListProcesses(ctx, pg.pool)
}
