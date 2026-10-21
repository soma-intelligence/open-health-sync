// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Parent and child families: sleep sessions and their phases, workouts and
// their route points, ECG recordings and their voltage samples. Children link
// to parents by plain id columns (no foreign key: the parents are hypertables).
// A parent is inserted with RETURNING id, or resolved by its content hash
// within the tenant and user when it already exists. Children whose parent is
// not in the batch are resolved against stored parents of the same tenant and
// user, and skipped (counted, never failed) when none matches: one orphan must
// not fail the job and take every other record of the upload with it.

// --- sleep_sessions -> sleep_phases ---

var sleepSessionColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "external_id", "bedtime_start", "bedtime_end",
	"total_sleep_seconds", "deep_sleep_seconds", "rem_sleep_seconds",
	"light_sleep_seconds", "awake_seconds", "sleep_score",
	"hrv_avg_ms", "resting_hr", "respiratory_rate", "spo2_avg",
	"content_hash", "metadata",
}

var (
	sleepSessionInsert = insertHead(tSleep, sleepSessionColumns) + placeholders(1, len(sleepSessionColumns)) +
		doNothing(hyperConflict("bedtime_start")) + " RETURNING id"
	// sleepSessionReplace is the insert of a Replaceable session: on conflict
	// the stored figures are replaced (latestWins); a copy that changes nothing
	// returns no row and resolves through sleepSessionSelect.
	sleepSessionReplace = insertHead(tSleep, sleepSessionColumns) + placeholders(1, len(sleepSessionColumns)) +
		latestWins(tSleep, hyperConflict("bedtime_start"), sleepFigures) + " RETURNING id"
	sleepSessionSelect = `SELECT id FROM ohs_data.sleep_sessions
		WHERE tenant_id = $1 AND user_id = $2 AND content_hash = $3 AND bedtime_start = $4`
	// sleepSessionSupersede removes the other stored copies of a Replaceable
	// session: same provider record, another (content_hash, bedtime_start)
	// key, which is what a night whose start moved leaves behind. The dedup
	// unique includes bedtime_start, so the upsert alone cannot reach them.
	sleepSessionSupersede = `DELETE FROM ohs_data.sleep_sessions
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND external_id = $4 AND id <> $5
		RETURNING id`
	// sleepPhasesReplace drops the phases a Replaceable session's new copy no
	// longer carries: every phase of a superseded copy ($3), and the phases of
	// the kept row ($4) whose hash is not in the new set ($5). The new phases
	// are then inserted as usual; the unchanged ones are skipped.
	sleepPhasesReplace = `DELETE FROM ohs_data.sleep_phases
		WHERE tenant_id = $1 AND user_id = $2
		  AND (session_id = ANY($3) OR (session_id = $4 AND content_hash <> ALL($5)))`
	// sleepPhasesAdopt moves the phases of superseded copies ($4) to the kept
	// row ($3) when the new copy carries none: an omitted hypnogram keeps the
	// stored one, as an omitted figure does.
	sleepPhasesAdopt = `UPDATE ohs_data.sleep_phases SET session_id = $3
		WHERE tenant_id = $1 AND user_id = $2 AND session_id = ANY($4)`
)

// sleepFigures are the columns a Replaceable session's new copy replaces.
var sleepFigures = []string{
	"bedtime_end", "total_sleep_seconds", "deep_sleep_seconds", "rem_sleep_seconds",
	"light_sleep_seconds", "awake_seconds", "sleep_score",
	"hrv_avg_ms", "resting_hr", "respiratory_rate", "spo2_avg",
}

// writeSleepWithPhases inserts sleep sessions, then their phases. Stages that
// name no session, and the nights of sessions without figures, go through
// writeSleepNights (sleep_sessionize.go).
func (w *Writer) writeSleepWithPhases(ctx context.Context, tx TxLike, sc scope, sessions []model.SleepSession, phases []model.SleepPhase, c Counts) error {
	// Group once instead of rescanning every phase per session.
	attached := make([]bool, len(phases))
	byParent := make(map[string][]int, len(sessions)+1)
	for i, p := range phases {
		byParent[p.ParentContentHash] = append(byParent[p.ParentContentHash], i)
	}
	// windows records where each in-batch session landed, so phases stamped for
	// a session missing from the batch can be attached by time containment
	// after the loop. nights holds the sessions without figures.
	windows := make([]sleepWindow, 0, len(sessions))
	var nights []nightSession

	for _, s := range sessions {
		p := sc.providerOr(s.Provider)
		insert := sleepSessionInsert
		if s.Replaceable {
			insert = sleepSessionReplace
		}
		parentID, isNew, err := insertParentWithFallback(ctx, tx, insert, []any{
			sc.tenant, sc.user, p, sourceSlugOrProvider("", p), s.ExternalID, s.BedtimeStart, nullTime(s.BedtimeEnd),
			s.TotalSleepSeconds, s.DeepSleepSeconds, s.REMSleepSeconds,
			s.LightSleepSeconds, s.AwakeSeconds, s.SleepScore,
			s.HRVAvgMs, s.RestingHR, s.RespiratoryRate, s.SpO2Avg,
			s.ContentHash, normalizeMetadata(s.Metadata),
		}, sleepSessionSelect, sc.tenant, sc.user, s.ContentHash, s.BedtimeStart)
		if err != nil {
			return fmt.Errorf("insert sleep session: %w", err)
		}
		if isNew {
			c.add(tSleep, 1, 0)
		} else {
			c.add(tSleep, 0, 1)
		}
		windows = append(windows, sleepWindow{id: parentID, hash: s.ContentHash, start: s.BedtimeStart, end: s.BedtimeEnd})
		if s.TotalSleepSeconds == nil && p == sc.provider {
			nights = append(nights, nightSession{id: parentID, hash: s.ContentHash, start: s.BedtimeStart, end: s.BedtimeEnd, inBatch: true})
		}

		var own []model.SleepPhase
		for _, i := range byParent[s.ContentHash] {
			if !attached[i] {
				own = append(own, phases[i])
				attached[i] = true
			}
		}
		if s.Replaceable {
			if err := replaceSleepChildren(ctx, tx, sc, p, s, parentID, own); err != nil {
				return err
			}
		}
		if len(own) > 0 {
			n, k, err := w.writeSleepPhases(ctx, tx, sc, parentID, s.ContentHash, own)
			if err != nil {
				return fmt.Errorf("insert sleep phases: %w", err)
			}
			c.add(tPhases, n, k)
		}
	}

	// Phases still unattached: stages that name no session make nights; a
	// phase stamped for a session missing from the batch is attached by time
	// containment, or counted skipped.
	var stages, orphans []model.SleepPhase
	for i, p := range phases {
		switch {
		case attached[i]:
		case p.ParentContentHash == "" && p.Phase != "in_bed":
			stages = append(stages, p)
		default:
			orphans = append(orphans, p)
		}
	}
	if len(stages) > 0 || len(nights) > 0 {
		if err := w.writeSleepNights(ctx, tx, sc, stages, nights, c); err != nil {
			return err
		}
	}
	if len(orphans) > 0 {
		return w.writeOrphanSleepPhases(ctx, tx, sc, orphans, windows, c)
	}
	return nil
}

// replaceSleepChildren runs after a Replaceable session is upserted to
// parentID, before its phases are written. It deletes the stored copies of the
// same provider record under another key (with an ExternalID only), then
// replaces the phases: when the new copy carries phases, those of the
// superseded copies and the kept row's phases missing from the new set are
// deleted; when it carries none, the superseded copies' phases move to the
// kept row.
func replaceSleepChildren(ctx context.Context, tx TxLike, sc scope, provider string, s model.SleepSession, parentID int64, own []model.SleepPhase) error {
	var superseded []int64
	if s.ExternalID != "" {
		rows, err := tx.Query(ctx, sleepSessionSupersede, sc.tenant, sc.user, provider, s.ExternalID, parentID)
		if err != nil {
			return fmt.Errorf("supersede sleep session: %w", err)
		}
		superseded, err = pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return fmt.Errorf("supersede sleep session: %w", err)
		}
	}
	if len(own) == 0 {
		if len(superseded) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, sleepPhasesAdopt, sc.tenant, sc.user, parentID, superseded); err != nil {
			return fmt.Errorf("adopt sleep phases: %w", err)
		}
		return nil
	}
	keep := make([]string, len(own))
	for i, ph := range own {
		ph.ParentContentHash = s.ContentHash
		keep[i] = model.SleepPhaseContentHash(ph)
	}
	if superseded == nil {
		superseded = []int64{}
	}
	if _, err := tx.Exec(ctx, sleepPhasesReplace, sc.tenant, sc.user, superseded, parentID, keep); err != nil {
		return fmt.Errorf("replace sleep phases: %w", err)
	}
	return nil
}

// sleepWindow is where one in-batch session landed.
type sleepWindow struct {
	id    int64
	hash  string
	start time.Time
	end   time.Time
}

// sleepPhaseAttachTolerance absorbs the lead/lag between a stage segment and
// the session boundary a provider reports for the same night.
const sleepPhaseAttachTolerance = 5 * time.Minute

// writeOrphanSleepPhases attaches phases stamped for a session missing from
// the batch to the in-batch session whose interval contains them. A phase no
// session covers is counted skipped; reprocessing the object once the parent
// lands re-attaches it.
func (w *Writer) writeOrphanSleepPhases(ctx context.Context, tx TxLike, sc scope, phases []model.SleepPhase, windows []sleepWindow, c Counts) error {
	byID := make(map[int64][]model.SleepPhase)
	hashByID := make(map[int64]string, len(windows))
	for _, wnd := range windows {
		hashByID[wnd.id] = wnd.hash
	}
	unresolved := 0
	for _, p := range phases {
		id, ok := containingSleepWindow(windows, p)
		if !ok {
			unresolved++
			continue
		}
		byID[id] = append(byID[id], p)
	}
	for id, group := range byID {
		n, k, err := w.writeSleepPhases(ctx, tx, sc, id, hashByID[id], group)
		if err != nil {
			return fmt.Errorf("insert orphan sleep phases: %w", err)
		}
		c.add(tPhases, n, k)
	}
	if unresolved > 0 {
		slog.DebugContext(ctx, "sleep phases skipped, no covering session",
			"count", unresolved, "sessions_in_batch", len(windows))
		c.add(tPhases, 0, unresolved)
	}
	return nil
}

// containingSleepWindow returns the id of the first session whose interval
// contains the phase, within tolerance.
func containingSleepWindow(windows []sleepWindow, p model.SleepPhase) (int64, bool) {
	for _, wnd := range windows {
		if wnd.end.Before(wnd.start) {
			continue
		}
		if !p.StartDate.Before(wnd.start.Add(-sleepPhaseAttachTolerance)) &&
			!p.EndDate.After(wnd.end.Add(sleepPhaseAttachTolerance)) {
			return wnd.id, true
		}
	}
	return 0, false
}

var sleepPhaseColumns = []string{"tenant_id", "user_id", "session_id", "phase", "start_date", "end_date", "duration_s", "content_hash"}

// writeSleepPhases inserts phases under a resolved session. Each phase is
// identified by the session it is written under (parentHash), whether or not
// the mapper stamped it, so sibling stages of different nights never collide.
func (w *Writer) writeSleepPhases(ctx context.Context, tx TxLike, sc scope, sessionID int64, parentHash string, phases []model.SleepPhase) (written, skipped int, err error) {
	rows := make([][]any, len(phases))
	for i, p := range phases {
		p.ParentContentHash = parentHash
		rows[i] = []any{sc.tenant, sc.user, sessionID, p.Phase, p.StartDate, nullTime(p.EndDate), p.DurationS, model.SleepPhaseContentHash(p)}
	}
	return w.insertBatches(ctx, tx, tPhases, insertHead(tPhases, sleepPhaseColumns), doNothing(plainConflict), len(sleepPhaseColumns), rows)
}

// --- workouts -> route_points ---

var workoutColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "device_id", "external_id", "activity_type",
	"start_date", "end_date", "duration_s",
	"active_energy_kcal", "distance_m", "avg_heart_rate",
	"max_heart_rate", "avg_speed_mps", "elevation_gain_m",
	"vo2_max", "content_hash", "metadata",
}

var (
	workoutInsert = insertHead(tWorkouts, workoutColumns) + placeholders(1, len(workoutColumns)) +
		doNothing(hyperConflict("start_date")) + " RETURNING id"
	// workoutReplace is the insert of a Replaceable workout: on conflict the
	// stored figures are replaced (latestWins); a copy that changes nothing
	// returns no row and resolves through workoutSelect.
	workoutReplace = insertHead(tWorkouts, workoutColumns) + placeholders(1, len(workoutColumns)) +
		latestWins(tWorkouts, hyperConflict("start_date"), workoutFigures) + " RETURNING id"
	workoutSelect = `SELECT id FROM ohs_data.workouts
		WHERE tenant_id = $1 AND user_id = $2 AND content_hash = $3 AND start_date = $4`
	// workoutSupersede removes the other stored copies of a Replaceable
	// workout (same provider record, another key: its start moved).
	workoutSupersede = `DELETE FROM ohs_data.workouts
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND external_id = $4 AND id <> $5
		RETURNING id`
	// workoutRoutesAdopt moves the route points of superseded copies ($4) to
	// the kept workout ($3).
	workoutRoutesAdopt = `UPDATE ohs_data.route_points SET workout_id = $3
		WHERE tenant_id = $1 AND user_id = $2 AND workout_id = ANY($4)`
	// workoutRoutesDrop deletes the route points of superseded copies when the
	// new copy carries its own route.
	workoutRoutesDrop = `DELETE FROM ohs_data.route_points
		WHERE tenant_id = $1 AND user_id = $2 AND workout_id = ANY($3)`
	// workoutTracksDrop deletes the derived track summaries of superseded
	// copies; they are rebuilt from the route points.
	workoutTracksDrop = `DELETE FROM ohs_data.workout_tracks
		WHERE tenant_id = $1 AND user_id = $2 AND workout_id = ANY($3)`
)

// workoutFigures are the columns a Replaceable workout's new copy replaces.
var workoutFigures = []string{
	"activity_type", "end_date", "duration_s",
	"active_energy_kcal", "distance_m", "avg_heart_rate",
	"max_heart_rate", "avg_speed_mps", "elevation_gain_m", "vo2_max",
}

// writeWorkoutsWithRoutes inserts workouts, then their route points. Route
// points that arrive in their own object are linked to a stored workout by time
// overlap (writeOrphanRoutePoints).
func (w *Writer) writeWorkoutsWithRoutes(ctx context.Context, tx TxLike, sc scope, workouts []model.Workout, routes []model.RoutePoint, c Counts) error {
	attached := make([]bool, len(routes))
	byParent := make(map[string][]int, len(workouts)+1)
	for i, r := range routes {
		byParent[r.ParentContentHash] = append(byParent[r.ParentContentHash], i)
	}

	for _, wk := range workouts {
		p := sc.providerOr(wk.Provider)
		insert := workoutInsert
		if wk.Replaceable {
			insert = workoutReplace
		}
		parentID, isNew, err := insertParentWithFallback(ctx, tx, insert, []any{
			sc.tenant, sc.user, p, sourceSlugOrProvider(model.Slug(wk.SourceSlug), p), nullString(wk.DeviceID), wk.ExternalID, wk.ActivityType,
			wk.StartDate, nullTime(wk.EndDate), wk.DurationS,
			wk.ActiveEnergyKcal, wk.DistanceM, wk.AvgHeartRate,
			wk.MaxHeartRate, wk.AvgSpeedMps, wk.ElevationGainM,
			wk.VO2Max, wk.ContentHash, normalizeMetadata(wk.Metadata),
		}, workoutSelect, sc.tenant, sc.user, wk.ContentHash, wk.StartDate)
		if err != nil {
			return fmt.Errorf("insert workout: %w", err)
		}
		if isNew {
			c.add(tWorkouts, 1, 0)
		} else {
			c.add(tWorkouts, 0, 1)
		}

		var own []model.RoutePoint
		for _, i := range byParent[wk.ContentHash] {
			if !attached[i] {
				own = append(own, routes[i])
				attached[i] = true
			}
		}
		if len(workouts) == 1 {
			for _, i := range byParent[""] {
				if !attached[i] {
					own = append(own, routes[i])
					attached[i] = true
				}
			}
		}
		if wk.Replaceable && wk.ExternalID != "" {
			if err := supersedeWorkout(ctx, tx, sc, p, wk.ExternalID, parentID, len(own) > 0); err != nil {
				return err
			}
		}
		if len(own) > 0 {
			n, k, err := w.writeRoutePoints(ctx, tx, sc, parentID, own)
			if err != nil {
				return fmt.Errorf("insert route points: %w", err)
			}
			c.add(tRoutes, n, k)
		}
	}

	var orphans []model.RoutePoint
	for i, r := range routes {
		if !attached[i] {
			orphans = append(orphans, r)
		}
	}
	if len(orphans) > 0 {
		return w.writeOrphanRoutePoints(ctx, tx, sc, orphans, c)
	}
	return nil
}

// supersedeWorkout deletes the stored copies of a Replaceable workout's
// provider record under another key than parentID's, and their derived
// tracks. Their route points move to parentID, or are deleted when the new
// copy carries its own route (hasRoute).
func supersedeWorkout(ctx context.Context, tx TxLike, sc scope, provider, externalID string, parentID int64, hasRoute bool) error {
	rows, err := tx.Query(ctx, workoutSupersede, sc.tenant, sc.user, provider, externalID, parentID)
	if err != nil {
		return fmt.Errorf("supersede workout: %w", err)
	}
	superseded, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("supersede workout: %w", err)
	}
	if len(superseded) == 0 {
		return nil
	}
	if hasRoute {
		_, err = tx.Exec(ctx, workoutRoutesDrop, sc.tenant, sc.user, superseded)
	} else {
		_, err = tx.Exec(ctx, workoutRoutesAdopt, sc.tenant, sc.user, parentID, superseded)
	}
	if err != nil {
		return fmt.Errorf("supersede workout route points: %w", err)
	}
	if _, err := tx.Exec(ctx, workoutTracksDrop, sc.tenant, sc.user, superseded); err != nil {
		return fmt.Errorf("supersede workout tracks: %w", err)
	}
	return nil
}

// routeAttachTolerance absorbs the lead/lag between GPS samples and the
// workout boundaries.
const routeAttachTolerance = 5 * time.Minute

// writeOrphanRoutePoints links route points without an in-batch workout to a
// stored workout of the same tenant and user whose span overlaps theirs (the
// most recently started match), then writes them. One route object belongs to
// one workout. With no matching workout the points are skipped; reprocessing
// the route object after the workout lands re-attaches them.
func (w *Writer) writeOrphanRoutePoints(ctx context.Context, tx TxLike, sc scope, pts []model.RoutePoint, c Counts) error {
	minTS, maxTS := pts[0].Timestamp, pts[0].Timestamp
	for _, p := range pts {
		if p.Timestamp.Before(minTS) {
			minTS = p.Timestamp
		}
		if p.Timestamp.After(maxTS) {
			maxTS = p.Timestamp
		}
	}
	var workoutID int64
	err := tx.QueryRow(ctx, `
		SELECT id FROM ohs_data.workouts
		WHERE tenant_id = $1 AND user_id = $2
		  AND start_date <= $3
		  AND COALESCE(end_date, start_date) >= $4
		ORDER BY start_date DESC
		LIMIT 1`,
		sc.tenant, sc.user, maxTS.Add(routeAttachTolerance), minTS.Add(-routeAttachTolerance),
	).Scan(&workoutID)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.DebugContext(ctx, "route points skipped, no parent workout", "count", len(pts))
		c.add(tRoutes, 0, len(pts))
		return nil
	}
	if err != nil {
		return fmt.Errorf("orphan route points lookup: %w", err)
	}
	n, k, err := w.writeRoutePoints(ctx, tx, sc, workoutID, pts)
	if err != nil {
		return fmt.Errorf("insert orphan route points: %w", err)
	}
	c.add(tRoutes, n, k)
	return nil
}

var routePointColumns = []string{"tenant_id", "user_id", "workout_id", "timestamp", "latitude", "longitude", "altitude_m", "speed_mps", "course_deg", "content_hash"}

// writeRoutePoints inserts route points under a resolved workout, using COPY
// above the threshold.
func (w *Writer) writeRoutePoints(ctx context.Context, tx TxLike, sc scope, workoutID int64, points []model.RoutePoint) (written, skipped int, err error) {
	row := func(sc scope, p model.RoutePoint) []any {
		return []any{sc.tenant, sc.user, workoutID, p.Timestamp, p.Latitude, p.Longitude, p.AltitudeM, p.SpeedMps, p.CourseDeg, model.RoutePointContentHash(p)}
	}
	return copyOrInsert(ctx, w, tx, sc, tRoutes, routePointColumns, hyperConflict(`"timestamp"`), points, row,
		func(p model.RoutePoint) (string, time.Time) { return model.RoutePointContentHash(p), p.Timestamp })
}

// --- ecg_recordings -> ecg_samples ---

var ecgColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "start_date", "end_date", "classification",
	"symptoms_status", "average_heart_rate", "sampling_frequency",
	"number_of_measurements", "voltage_path", "content_hash", "metadata",
}

var (
	ecgInsert = insertHead(tECG, ecgColumns) + placeholders(1, len(ecgColumns)) +
		doNothing(hyperConflict("start_date")) + " RETURNING id"
	ecgSelect = `SELECT id FROM ohs_data.ecg_recordings
		WHERE tenant_id = $1 AND user_id = $2 AND content_hash = $3 AND start_date = $4`
)

// writeECGWithSamples inserts ECG recordings, then their voltage samples.
// Samples whose recording arrived in a different object are linked to a stored
// recording (writeOrphanECGVoltageSamples).
func (w *Writer) writeECGWithSamples(ctx context.Context, tx TxLike, sc scope, recordings []model.ECGRecording, samples []model.ECGVoltageSample, sourceKey string, c Counts) error {
	attached := make([]bool, len(samples))
	// Group once: one Apple Watch ECG is about 15k samples.
	byParent := make(map[string][]int, len(recordings)+1)
	for i, s := range samples {
		byParent[s.ParentContentHash] = append(byParent[s.ParentContentHash], i)
	}

	for _, ecg := range recordings {
		parentID, isNew, err := insertParentWithFallback(ctx, tx, ecgInsert, []any{
			sc.tenant, sc.user, sc.provider, sourceSlugOrProvider("", sc.provider), ecg.StartDate, nullTime(ecg.EndDate), ecg.Classification,
			ecg.SymptomsStatus, ecg.AverageHeartRate, ecg.SamplingFrequency,
			ecg.NumberOfMeasurements, ecg.VoltagePath, ecg.ContentHash, normalizeMetadata(ecg.Metadata),
		}, ecgSelect, sc.tenant, sc.user, ecg.ContentHash, ecg.StartDate)
		if err != nil {
			return fmt.Errorf("insert ecg recording: %w", err)
		}
		if isNew {
			c.add(tECG, 1, 0)
		} else {
			c.add(tECG, 0, 1)
		}

		var own []model.ECGVoltageSample
		for _, i := range byParent[ecg.ContentHash] {
			if !attached[i] {
				own = append(own, samples[i])
				attached[i] = true
			}
		}
		if len(recordings) == 1 {
			for _, i := range byParent[""] {
				if !attached[i] {
					own = append(own, samples[i])
					attached[i] = true
				}
			}
		}
		if len(own) > 0 {
			n, k, err := w.writeECGVoltageSamples(ctx, tx, sc, parentID, ecg.ContentHash, ecg.StartDate, own)
			if err != nil {
				return fmt.Errorf("insert ecg voltage samples: %w", err)
			}
			c.add(tECGSamples, n, k)
		}
	}

	var orphans []model.ECGVoltageSample
	for i, s := range samples {
		if !attached[i] {
			orphans = append(orphans, s)
		}
	}
	if len(orphans) > 0 {
		return w.writeOrphanECGVoltageSamples(ctx, tx, sc, orphans, sourceKey, c)
	}
	return nil
}

// writeOrphanECGVoltageSamples links voltage samples without an in-batch
// recording to a stored recording of the same tenant and user: by their parent
// content hash, or, when they carry none (an Apple Health voltage object holds
// samples and nothing else), by the raw key of the object they came from, which
// the recording stored as voltage_path. With neither, or with no match, they are
// skipped: a wrong parent on an ECG waveform is worse than none.
func (w *Writer) writeOrphanECGVoltageSamples(ctx context.Context, tx TxLike, sc scope, samples []model.ECGVoltageSample, sourceKey string, c Counts) error {
	byParent := make(map[string][]model.ECGVoltageSample)
	for _, s := range samples {
		byParent[s.ParentContentHash] = append(byParent[s.ParentContentHash], s)
	}
	for parentHash, group := range byParent {
		var (
			ecgID     int64
			recHash   string
			recStart  time.Time
			err       error
			lookupKey string
		)
		switch {
		case parentHash != "":
			lookupKey = "content_hash"
			recHash = parentHash
			err = tx.QueryRow(ctx, `
				SELECT id, start_date FROM ohs_data.ecg_recordings
				WHERE tenant_id = $1 AND user_id = $2 AND content_hash = $3
				ORDER BY start_date DESC LIMIT 1`,
				sc.tenant, sc.user, parentHash).Scan(&ecgID, &recStart)
		case sourceKey != "":
			lookupKey = "voltage_path"
			err = tx.QueryRow(ctx, `
				SELECT id, content_hash, start_date FROM ohs_data.ecg_recordings
				WHERE tenant_id = $1 AND user_id = $2 AND voltage_path = $3
				ORDER BY start_date DESC LIMIT 1`,
				sc.tenant, sc.user, sourceKey).Scan(&ecgID, &recHash, &recStart)
		default:
			slog.DebugContext(ctx, "ecg voltage skipped, no parent hash or source key", "count", len(group))
			c.add(tECGSamples, 0, len(group))
			continue
		}
		if errors.Is(err, pgx.ErrNoRows) {
			slog.DebugContext(ctx, "ecg voltage skipped, no parent recording", "by", lookupKey, "count", len(group))
			c.add(tECGSamples, 0, len(group))
			continue
		}
		if err != nil {
			return fmt.Errorf("orphan ecg voltage lookup by %s: %w", lookupKey, err)
		}
		n, k, err := w.writeECGVoltageSamples(ctx, tx, sc, ecgID, recHash, recStart, group)
		if err != nil {
			return fmt.Errorf("insert orphan ecg voltage samples: %w", err)
		}
		c.add(tECGSamples, n, k)
	}
	return nil
}

var ecgSampleColumns = []string{"tenant_id", "user_id", "ecg_id", "ts", "time_since_sample_start", "voltage_uv", "content_hash"}

// writeECGVoltageSamples inserts voltage samples under a resolved recording.
// Every sample is identified by the recording it is written under (parentHash),
// whether or not the mapper stamped it: the 512 Hz offset grid is the same for
// every recording, so an unparented hash would collide across recordings. ts is
// the recording start plus the sample's offset.
func (w *Writer) writeECGVoltageSamples(ctx context.Context, tx TxLike, sc scope, ecgID int64, parentHash string, recStart time.Time, samples []model.ECGVoltageSample) (written, skipped int, err error) {
	owned := make([]model.ECGVoltageSample, len(samples))
	for i, s := range samples {
		s.ParentContentHash = parentHash
		owned[i] = s
	}
	// Rounded to the microsecond Postgres stores, so a sample resolved against
	// the stored recording gets the same ts as one written with it in-batch.
	base := recStart.Round(time.Microsecond)
	ts := func(s model.ECGVoltageSample) time.Time {
		return base.Add(time.Duration(s.TimeSinceSampleStart * float64(time.Second))).Round(time.Microsecond)
	}
	row := func(sc scope, s model.ECGVoltageSample) []any {
		return []any{sc.tenant, sc.user, ecgID, ts(s), s.TimeSinceSampleStart, s.VoltageUV, model.ECGVoltageSampleContentHash(s)}
	}
	return copyOrInsert(ctx, w, tx, sc, tECGSamples, ecgSampleColumns, hyperConflict("ts"), owned, row,
		func(s model.ECGVoltageSample) (string, time.Time) { return model.ECGVoltageSampleContentHash(s), ts(s) })
}
