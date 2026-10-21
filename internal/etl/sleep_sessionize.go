// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Nights built from stage samples.
//
// Apple Health sends a night as separate samples: at most one "in bed" record,
// which the mapper turns into a session with no figures, and stage segments
// that name no session (modern watchOS often sends no in bed record at all).
// The SDK and the export converter file each sample under the local day it
// starts on, so a night that crosses midnight arrives in two uploads, processed
// in any order and possibly at the same time. writeSleepNights puts each night
// back together:
//
//   - the batch's stages, its sessions without figures and the stored sessions
//     of the same provider around them are grouped into nights: runs with no
//     gap longer than sleepSessionGap;
//   - in a night with a time in bed, each stage goes to the session that
//     contains it, or else to the nearest one;
//   - a night without one is a single session built from its stages (metadata
//     "synthesized": true) that starts at its first stage. A session built
//     earlier from part of the night is folded into it;
//   - every session that gained stages takes its figures from all of them, as
//     the Health app's time asleep does. A provider that sends stages without
//     their session reports no figures of its own.

// sleepSessionGap is the largest gap between two samples of the same night.
// watchOS emits stage segments roughly back to back; a larger gap starts a new
// night, or a nap.
const sleepSessionGap = time.Hour

// nightSession is a session that takes part in the night grouping.
type nightSession struct {
	id          int64
	hash        string
	start, end  time.Time
	synthesized bool // built from stages: folded into its night's session
	inBatch     bool
}

// sleepNight is one night: its sessions with a time in bed (anchors), the
// sessions built earlier from its stages, and the batch's stages.
type sleepNight struct {
	start, end time.Time
	anchors    []nightSession
	built      []nightSession
	stages     []model.SleepPhase
	fresh      bool // holds a stage or a session of this batch
}

// groupSleepNights groups stages and sessions into nights, in time order. A
// session id listed twice is kept once, at its first listing.
func groupSleepNights(stages []model.SleepPhase, sessions []nightSession) []sleepNight {
	type item struct {
		start, end time.Time
		stage      int // index in stages, or -1
		session    int // index in sessions, or -1
	}
	items := make([]item, 0, len(stages)+len(sessions))
	for i, p := range stages {
		items = append(items, item{p.StartDate, latest(p.StartDate, p.EndDate), i, -1})
	}
	seen := make(map[int64]bool, len(sessions))
	for i, s := range sessions {
		if seen[s.id] {
			continue
		}
		seen[s.id] = true
		items = append(items, item{s.start, latest(s.start, s.end), -1, i})
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].start.Before(items[b].start) })

	var nights []sleepNight
	for _, it := range items {
		if len(nights) == 0 || it.start.Sub(nights[len(nights)-1].end) > sleepSessionGap {
			nights = append(nights, sleepNight{start: it.start, end: it.end})
		}
		n := &nights[len(nights)-1]
		n.end = latest(n.end, it.end)
		if it.stage >= 0 {
			n.stages = append(n.stages, stages[it.stage])
			n.fresh = true
			continue
		}
		s := sessions[it.session]
		if s.synthesized {
			n.built = append(n.built, s)
		} else {
			n.anchors = append(n.anchors, s)
		}
		n.fresh = n.fresh || s.inBatch
	}
	return nights
}

// nearestSession returns the index of the session that contains the phase,
// within sleepPhaseAttachTolerance, or else of the closest one in time.
func nearestSession(sessions []nightSession, p model.SleepPhase) int {
	best, bestGap := 0, time.Duration(math.MaxInt64)
	for i, s := range sessions {
		if !p.StartDate.Before(s.start.Add(-sleepPhaseAttachTolerance)) &&
			!p.EndDate.After(s.end.Add(sleepPhaseAttachTolerance)) {
			return i
		}
		var gap time.Duration
		switch {
		case p.EndDate.Before(s.start):
			gap = s.start.Sub(p.EndDate)
		case p.StartDate.After(s.end):
			gap = p.StartDate.Sub(s.end)
		}
		if gap < bestGap {
			best, bestGap = i, gap
		}
	}
	return best
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

var (
	// sleepNightLock serializes the night grouping of one user, so the two
	// uploads of one night, processed at the same time, build one session.
	sleepNightLock = `SELECT pg_advisory_xact_lock(hashtextextended('ohs_data.sleep_nights:' || $1, 0))`
	// sleepNightsAround lists the provider's stored sessions that reach into
	// [$4, $5].
	sleepNightsAround = `SELECT id, content_hash, bedtime_start, coalesce(bedtime_end, bedtime_start),
		coalesce(metadata->>'synthesized' = 'true', false)
		FROM ohs_data.sleep_sessions
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		  AND bedtime_start <= $5 AND coalesce(bedtime_end, bedtime_start) >= $4`
	// sleepPhasesTake removes the phases of a session built from stages, to be
	// written again under the session of their night.
	sleepPhasesTake = `DELETE FROM ohs_data.sleep_phases
		WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3
		RETURNING phase, start_date, end_date, duration_s`
	sleepSessionDrop = `DELETE FROM ohs_data.sleep_sessions
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`
	sleepSessionExtend = `UPDATE ohs_data.sleep_sessions SET bedtime_end = $4
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3 AND (bedtime_end IS NULL OR bedtime_end < $4)`
	// sleepFiguresFromStages sets the figures of the sessions $3 from their
	// stages ($4 deep, $5 rem, $6 light, $7 awake): time asleep is the sum of
	// the first three.
	sleepFiguresFromStages = `UPDATE ohs_data.sleep_sessions s SET
		total_sleep_seconds = f.deep + f.rem + f.light,
		deep_sleep_seconds  = f.deep,
		rem_sleep_seconds   = f.rem,
		light_sleep_seconds = f.light,
		awake_seconds       = f.awake
		FROM (SELECT session_id,
			coalesce(sum(duration_s) FILTER (WHERE phase = $4), 0) AS deep,
			coalesce(sum(duration_s) FILTER (WHERE phase = $5), 0) AS rem,
			coalesce(sum(duration_s) FILTER (WHERE phase = $6), 0) AS light,
			coalesce(sum(duration_s) FILTER (WHERE phase = $7), 0) AS awake
			FROM ohs_data.sleep_phases
			WHERE tenant_id = $1 AND user_id = $2 AND session_id = ANY($3)
			GROUP BY session_id) f
		WHERE s.tenant_id = $1 AND s.user_id = $2 AND s.id = f.session_id`
)

// writeSleepNights writes stages that name no session, and gathers the nights
// of the batch's sessions without figures (sessions), as the comment at the
// top of this file says.
func (w *Writer) writeSleepNights(ctx context.Context, tx TxLike, sc scope, stages []model.SleepPhase, sessions []nightSession, c Counts) error {
	if _, err := tx.Exec(ctx, sleepNightLock, sc.tenant.String()+"/"+sc.user.String()); err != nil {
		return fmt.Errorf("lock sleep nights: %w", err)
	}
	stored, err := storedNightSessions(ctx, tx, sc, stages, sessions)
	if err != nil {
		return err
	}
	var touched []int64
	for _, n := range groupSleepNights(stages, append(sessions, stored...)) {
		if !n.fresh || (len(n.stages) == 0 && len(n.built) == 0) {
			continue
		}
		var ids []int64
		if len(n.anchors) > 0 {
			ids, err = w.fillNight(ctx, tx, sc, n, c)
		} else {
			ids, err = w.buildNight(ctx, tx, sc, n, c)
		}
		if err != nil {
			return err
		}
		touched = append(touched, ids...)
	}
	if len(touched) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, sleepFiguresFromStages, sc.tenant, sc.user, touched,
		model.SleepStageDeep, model.SleepStageREM, model.SleepStageLight, model.SleepStageAwake); err != nil {
		return fmt.Errorf("sleep figures from stages: %w", err)
	}
	return nil
}

// storedNightSessions returns the provider's stored sessions within a gap of
// the batch's stages and sessions. The span widens with what it finds, since
// a stored night can reach further than the batch.
func storedNightSessions(ctx context.Context, tx TxLike, sc scope, stages []model.SleepPhase, sessions []nightSession) ([]nightSession, error) {
	var lo, hi time.Time
	widen := func(start, end time.Time) {
		if lo.IsZero() || start.Before(lo) {
			lo = start
		}
		hi = latest(latest(hi, start), end)
	}
	for _, p := range stages {
		widen(p.StartDate, p.EndDate)
	}
	for _, s := range sessions {
		widen(s.start, s.end)
	}
	var stored []nightSession
	for range 3 {
		wantLo, wantHi := lo, hi
		rows, err := tx.Query(ctx, sleepNightsAround, sc.tenant, sc.user, sc.provider,
			lo.Add(-sleepSessionGap), hi.Add(sleepSessionGap))
		if err != nil {
			return nil, fmt.Errorf("stored sleep nights: %w", err)
		}
		stored, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (nightSession, error) {
			var s nightSession
			err := r.Scan(&s.id, &s.hash, &s.start, &s.end, &s.synthesized)
			return s, err
		})
		if err != nil {
			return nil, fmt.Errorf("stored sleep nights: %w", err)
		}
		for _, s := range stored {
			widen(s.start, s.end)
		}
		if lo.Equal(wantLo) && hi.Equal(wantHi) {
			break
		}
	}
	return stored, nil
}

// fillNight writes a night that has a time in bed: each stage, and each stage
// of a session built earlier for the night, goes to its anchor. It returns the
// anchors that gained stages.
func (w *Writer) fillNight(ctx context.Context, tx TxLike, sc scope, n sleepNight, c Counts) ([]int64, error) {
	own := make([][]model.SleepPhase, len(n.anchors))
	for _, p := range n.stages {
		i := nearestSession(n.anchors, p)
		own[i] = append(own[i], p)
	}
	moved := make([][]model.SleepPhase, len(n.anchors))
	for _, b := range n.built {
		phases, err := takeBuiltSession(ctx, tx, sc, b.id)
		if err != nil {
			return nil, err
		}
		for _, p := range phases {
			i := nearestSession(n.anchors, p)
			moved[i] = append(moved[i], p)
		}
	}
	var touched []int64
	for i, a := range n.anchors {
		if len(own[i]) == 0 && len(moved[i]) == 0 {
			continue
		}
		if err := w.writeNightPhases(ctx, tx, sc, a.id, a.hash, own[i], moved[i], c); err != nil {
			return nil, err
		}
		touched = append(touched, a.id)
	}
	return touched, nil
}

// buildNight writes a night with no time in bed as one session that starts at
// the night's first stage: the one built earlier when it starts there, else a
// new one. The other sessions built for the night are folded into it.
func (w *Writer) buildNight(ctx context.Context, tx TxLike, sc scope, n sleepNight, c Counts) ([]int64, error) {
	var target *nightSession
	for i := range n.built {
		if n.built[i].start.Equal(n.start) {
			target = &n.built[i]
			break
		}
	}
	if target == nil {
		s := nightSession{start: n.start, end: n.end, hash: model.ComputeContentHash(map[string]string{
			"user_id":       sc.user.String(),
			"provider":      sc.provider,
			"bedtime_start": n.start.UTC().Format(time.RFC3339Nano),
			"bedtime_end":   n.end.UTC().Format(time.RFC3339Nano),
		})}
		id, isNew, err := insertParentWithFallback(ctx, tx, sleepSessionInsert, []any{
			sc.tenant, sc.user, sc.provider, sourceSlugOrProvider("", sc.provider), "", s.start, s.end,
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			s.hash, map[string]any{"synthesized": true},
		}, sleepSessionSelect, sc.tenant, sc.user, s.hash, s.start)
		if err != nil {
			return nil, fmt.Errorf("insert sleep session: %w", err)
		}
		if isNew {
			c.add(tSleep, 1, 0)
		} else {
			c.add(tSleep, 0, 1)
		}
		s.id = id
		target = &s
	}
	var moved []model.SleepPhase
	for _, b := range n.built {
		if b.id == target.id {
			continue
		}
		phases, err := takeBuiltSession(ctx, tx, sc, b.id)
		if err != nil {
			return nil, err
		}
		moved = append(moved, phases...)
	}
	if err := w.writeNightPhases(ctx, tx, sc, target.id, target.hash, n.stages, moved, c); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, sleepSessionExtend, sc.tenant, sc.user, target.id, n.end); err != nil {
		return nil, fmt.Errorf("extend sleep session: %w", err)
	}
	return []int64{target.id}, nil
}

// writeNightPhases writes the batch's stages (counted) and the stages moved
// from a folded session (already counted when they first landed) under one
// session.
func (w *Writer) writeNightPhases(ctx context.Context, tx TxLike, sc scope, id int64, hash string, stages, moved []model.SleepPhase, c Counts) error {
	if len(stages) > 0 {
		n, k, err := w.writeSleepPhases(ctx, tx, sc, id, hash, stages)
		if err != nil {
			return fmt.Errorf("insert sleep phases: %w", err)
		}
		c.add(tPhases, n, k)
	}
	if len(moved) > 0 {
		if _, _, err := w.writeSleepPhases(ctx, tx, sc, id, hash, moved); err != nil {
			return fmt.Errorf("move sleep phases: %w", err)
		}
	}
	return nil
}

// takeBuiltSession deletes a session built from stages and returns its phases.
func takeBuiltSession(ctx context.Context, tx TxLike, sc scope, id int64) ([]model.SleepPhase, error) {
	rows, err := tx.Query(ctx, sleepPhasesTake, sc.tenant, sc.user, id)
	if err != nil {
		return nil, fmt.Errorf("take sleep phases: %w", err)
	}
	phases, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.SleepPhase, error) {
		var p model.SleepPhase
		var end *time.Time
		if err := r.Scan(&p.Phase, &p.StartDate, &end, &p.DurationS); err != nil {
			return p, err
		}
		if end != nil {
			p.EndDate = *end
		}
		return p, nil
	})
	if err != nil {
		return nil, fmt.Errorf("take sleep phases: %w", err)
	}
	if _, err := tx.Exec(ctx, sleepSessionDrop, sc.tenant, sc.user, id); err != nil {
		return nil, fmt.Errorf("drop sleep session: %w", err)
	}
	return phases, nil
}
