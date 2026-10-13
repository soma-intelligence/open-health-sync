// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// ErrUserNotActive is returned by WriteSynthetic when the user does not exist
// in the tenant or is being deleted.
var ErrUserNotActive = errors.New("etl: user not found or not active")

// WriteSynthetic writes records built in process (the demo seed), not mapped
// from a raw object, through the same steps as ProcessWith: owner stamping,
// source attribution, unit canonicalization, validation, the writer with its
// content hash dedup, the derived daily summaries, coverage and the outgoing
// webhooks, in one transaction. Writing the same records again adds nothing.
//
// It is strict where ProcessWith is lenient: a unit error or a rejected value
// fails the whole call, because generated data has no raw line to park. A
// suspect value is written and flagged, as in the ETL. mr is modified in
// place. No raw object, sync log row or failed record is written.
func WriteSynthetic(ctx context.Context, pool *pgxpool.Pool, tenant, user uuid.UUID, provider string, mr *model.MapResult) (Counts, error) {
	if provider == "" {
		return nil, errors.New("etl: write synthetic: empty provider")
	}
	if mr == nil || mr.IsEmpty() {
		return Counts{}, nil
	}
	sc := scope{tenant: tenant, user: user, provider: provider}
	cov, err := prepareSynthetic(mr, sc, time.Now())
	if err != nil {
		return nil, err
	}

	write := func() (Counts, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin transaction: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		if active, err := userActive(ctx, tx, sc, true); err != nil {
			return nil, err
		} else if !active {
			return nil, ErrUserNotActive
		}
		// The writer filters its input in place: give each attempt a copy.
		acc := &model.MapResult{}
		Accumulate(acc, mr)
		counts, err := NewWriter(WriterConfig{}).WriteAccumulated(ctx, tx, sc, acc)
		if err != nil {
			return nil, fmt.Errorf("write records: %w", err)
		}
		if days := touchedDays(sc, acc); len(days) > 0 {
			n, k, err := deriveDailySummaries(ctx, tx, sc, days)
			if err != nil {
				return nil, fmt.Errorf("derive daily summaries: %w", err)
			}
			if n+k > 0 {
				counts.add(tDaily, n, k)
			}
		}
		if err := writeCoverage(ctx, tx, sc, cov); err != nil {
			return nil, err
		}
		if err := enqueueWebhooks(ctx, tx, sc, counts, cov); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit transaction: %w", err)
		}
		return counts, nil
	}
	for attempt := 1; ; attempt++ {
		counts, err := write()
		if err == nil || attempt == maxWriteAttempts || !retryableWrite(err) {
			return counts, err
		}
		if err := sleepCtx(ctx, time.Duration(attempt)*100*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// prepareSynthetic runs the in-memory steps of the ETL on mr for sc: owner,
// attribution, units, validation and suspect flags. It returns the coverage
// entries of the records, or an error when a record cannot be written as it
// is.
func prepareSynthetic(mr *model.MapResult, sc scope, now time.Time) ([]coverageEntry, error) {
	stampOwner(mr, sc)
	model.CanonicalizeSourceAttribution(mr, nil, sc.provider)
	if unitErrs := model.CanonicalizeUnits(mr); len(unitErrs) > 0 {
		return nil, fmt.Errorf("etl: write synthetic: %d unit error(s), first on %s", len(unitErrs), unitErrs[0].QuantityType)
	}
	valErrors := ValidateMapResultAt(mr, now)
	if rejects := Rejections(valErrors); len(rejects) > 0 {
		// The record type and field only: a value never reaches an error.
		return nil, fmt.Errorf("etl: write synthetic: %d rejected value(s), first in %s.%s",
			len(rejects), rejects[0].RecordType, rejects[0].Field)
	}
	if s := Suspicions(valErrors); len(s) > 0 {
		FlagSuspects(mr, s)
	}
	for j := range mr.HeartbeatSamples {
		if mr.HeartbeatSamples[j].SeriesID == "" {
			mr.HeartbeatSamples[j].SeriesID = sc.provider
		}
	}
	cov := newCoverageBuilder(now)
	cov.add(mr)
	return cov.entries(), nil
}
