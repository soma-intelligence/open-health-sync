// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package automation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// The reconciler reads the whole window once, then only what is newer than
// the oldest unprocessed object, and the whole window again after FullEvery.
func TestReconcilerIsIncremental(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Now()
	r := &Reconciler{Pool: e.pool, Now: func() time.Time { return now }}

	done := e.file("withings", "2026-10-01")
	e.register("withings", done, 3*24*time.Hour)
	e.job(done, "done")
	stuck := e.file("withings", "2026-10-02")
	e.register("withings", stuck, 2*24*time.Hour)

	rep, err := r.Run(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, rep.UnprocessedObjects)
	require.WithinDuration(t, now.Add(-2*24*time.Hour-scanOverlap), r.mark, time.Minute,
		"the mark stops at the oldest unprocessed object")

	// The stuck object is still counted on the next pass, and it stays counted
	// until it is processed.
	now = now.Add(5 * time.Minute)
	rep, err = r.Run(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, rep.UnprocessedObjects)
	e.job(stuck, "done")
	rep, err = r.Run(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, rep.UnprocessedObjects)
	require.WithinDuration(t, now.Add(-StaleAfter-scanOverlap), r.mark, time.Minute, "healthy: the mark follows the clock")

	// An object that goes back to unprocessed behind the mark is not seen by
	// an incremental pass, and is found by the next full one.
	e.exec(`UPDATE ohs_sync.etl_jobs SET status = 'pending' WHERE key = $1`, done)
	now = now.Add(5 * time.Minute)
	rep, err = r.Run(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, rep.UnprocessedObjects)
	now = now.Add(ReconcileFullEvery)
	rep, err = r.Run(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, rep.UnprocessedObjects)

	// A new process has no mark: its first pass is a full one.
	rep, err = (&Reconciler{Pool: e.pool, Now: func() time.Time { return now }}).Run(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, rep.UnprocessedObjects)
}

// The orphan scanner reads the whole window once, then only the new rows; a
// failed pass does not move the mark, and a new process starts over.
func TestOrphanScannerIsIncremental(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Now()
	s := &OrphanScanner{Pool: e.pool, Now: func() time.Time { return now }}

	first := e.stored("withings", "2026-10-01")
	res, err := s.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Enqueued)
	require.Equal(t, 1, e.jobs(first))
	require.WithinDuration(t, now.Add(-orphanGrace-scanOverlap), s.mark, time.Second)

	// A row that arrives later is found by the next, incremental pass.
	now = now.Add(10 * time.Minute)
	late := e.file("withings", "2026-10-02")
	e.register("withings", late, -5*time.Minute) // created 5 minutes after the first pass
	res, err = s.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Enqueued)
	require.Equal(t, 1, e.jobs(late))

	// A row behind the mark that lost its job is out of an incremental pass's
	// range, in the range of a full pass, and in the range of a new process.
	e.exec(`DELETE FROM ohs_sync.etl_jobs WHERE key = $1`, first)
	now = now.Add(10 * time.Minute)
	res, err = s.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, res.Enqueued)
	res, err = (&OrphanScanner{Pool: e.pool, Now: func() time.Time { return now }}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Enqueued, "a restarted process scans the whole window")
	e.exec(`DELETE FROM ohs_sync.etl_jobs WHERE key = $1`, first)
	now = now.Add(OrphanFullEvery)
	res, err = s.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Enqueued, "the periodic full pass scans the whole window")

	// A pass that fails leaves the mark where it was.
	mark := s.mark
	now = now.Add(10 * time.Minute)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.Run(cancelled)
	require.Error(t, err)
	require.Equal(t, mark, s.mark)
}

func TestTrackingPrunerDeletesOldRowsOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	logRow := func(age time.Duration) {
		e.exec(`INSERT INTO ohs_sync.sync_log (tenant_id, user_id, provider, stage, status, created_at)
			VALUES ($1, $2, 'p', 'etl', 'success', now() - make_interval(secs => $3))`, e.tenant, e.user, age.Seconds())
	}
	job := func(key, status string, age time.Duration) {
		e.exec(`INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, status, created_at)
			VALUES ($1, $2, 'p', $3, $4, now() - make_interval(secs => $5))`, e.tenant, e.user, key, status, age.Seconds())
	}
	day := 24 * time.Hour
	logRow(200 * day)
	logRow(100 * day)
	logRow(20 * day)
	logRow(time.Hour)
	job("old-done", "done", 100*day)
	job("old-parked", "parked", 100*day)
	job("old-pending", "pending", 100*day)
	job("mid-done", "done", 20*day)
	job("new-done", "done", day)

	// Zero days does nothing.
	res, err := (&TrackingPruner{Pool: e.pool}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, TrackingPruneResult{}, res)

	m := &Metrics{}
	res, err = (&TrackingPruner{Pool: e.pool, Days: 90, Metrics: m}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, TrackingPruneResult{SyncLog: 2, Jobs: 1}, res)
	require.Equal(t, 0, e.jobs("old-done"))
	require.Equal(t, 1, e.jobs("old-parked"), "a parked job waits for an operator, whatever its age")
	require.Equal(t, 1, e.jobs("old-pending"))
	require.Equal(t, 2, e.scalar(`SELECT count(*) FROM ohs_sync.sync_log`))
	require.EqualValues(t, 3, m.Snapshot()["tracking_rows_pruned_total"])

	// A setting under the minimum is raised to it: the jobs the reconciler and
	// the orphan scanner still read are never pruned.
	res, err = (&TrackingPruner{Pool: e.pool, Days: 1}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, TrackingPruneResult{SyncLog: 1, Jobs: 1}, res)
	require.Equal(t, 0, e.jobs("mid-done"))
	require.Equal(t, 1, e.jobs("new-done"))
	require.Greater(t, MinTrackingRetentionDays*24*time.Hour, OrphanWindow)
	require.Greater(t, MinTrackingRetentionDays*24*time.Hour, ReconcileWindow)
}

// failingStore refuses to delete one prefix.
type failingStore struct {
	rawstore.Store
	refuse string
}

func (f failingStore) DeletePrefix(ctx context.Context, prefix string) error {
	if prefix == f.refuse {
		return errors.New("store down")
	}
	return f.Store.DeletePrefix(ctx, prefix)
}

// Raw deletions are driven by raw_objects, page by page, across users. The
// rows of a prefix the store refused stay for the next run.
func TestRetentionRawObjectsAcrossUsersAndPages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	old := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Three users, old files on two provider days each, more rows than a page.
	var prefixes []string
	n := 0
	for u := 0; u < 3; u++ {
		tenant := e.newTenant()
		user := e.newUser(tenant)
		for d := 0; d < 2; d++ {
			day := old.AddDate(0, 0, d)
			for f := 0; f < retentionRawBatch/4; f++ {
				n++
				key := rawstore.Key(tenant, user, "withings", day, fmt.Sprintf("b%d", n), 0)
				if f == 0 {
					require.NoError(t, e.rs.Put(ctx, key, bytesReader()))
					prefixes = append(prefixes, rawDayPrefix(key))
				}
				e.exec(`INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part)
					VALUES ($1, $2, 'withings', $3, $3, 1, 'b', 0)`, tenant, user, key)
			}
		}
	}
	recent := e.stored("withings", "2026-10-09")
	odd := "raw/not-a-day-key"
	e.exec(`INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part)
		VALUES ($1, $2, 'withings', $3, $3, 1, 'b', 0)`, e.tenant, e.user, odd)

	ret := &Retention{Pool: e.pool, Store: failingStore{Store: e.rs, refuse: prefixes[0]}, Days: 90, Now: func() time.Time { return now }}
	res, err := ret.Run(ctx)
	require.ErrorContains(t, err, "store down")
	require.Equal(t, 5, res.Prefixes)
	require.Equal(t, 3, res.Users)
	require.Equal(t, retentionRawBatch/4+2, e.scalar(`SELECT count(*) FROM ohs_sync.raw_objects`),
		"the refused prefix keeps its rows; the recent and the odd key stay")
	keys, err := e.rs.List(ctx, "raw/")
	require.NoError(t, err)
	require.Len(t, keys, 2)

	// The next run, with the store back, finishes the job.
	ret.Store = e.rs
	res, err = ret.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Prefixes)
	require.Equal(t, 2, e.scalar(`SELECT count(*) FROM ohs_sync.raw_objects`))
	keys, err = e.rs.List(ctx, "raw/")
	require.NoError(t, err)
	require.Equal(t, []string{recent}, keys)
}
