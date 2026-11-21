// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
	"github.com/soma-intelligence/open-health-sync/internal/vault/series"
)

// Series blobs (migration 0053, package series).
//
// The voltages of an ECG recording, the route of a workout and the beats of a
// heartbeat series are stored as one blob per series in series_blobs, not
// one row per point. A write merges the points it brings into the stored
// series (series.Merge: the union, each point once; a point stored already is
// replaced only by a copy from a newer capture that differs in a column that
// does not identify it) and stores the result again, sealed with keys,
// compressed as it is without. A series can therefore arrive in any number
// of parts, in any order, chunk by chunk (stream.go) or object by object.
//
// The per-point rows of earlier releases are read along with the blob and
// deleted when the series is stored again: a series is either rows not
// converted yet or one blob, never both once written. `ohs vault seal`
// converts the rest (convertPerPoint).

const tSeries = vault.SeriesTable

// seriesRef names one stored series.
type seriesRef struct {
	kind     series.Kind
	provider string
	parent   string
}

// ecgSeries, routeSeries and heartbeatSeries name the series of an ECG
// recording, of a workout and of a heartbeat series. The provider of an ECG
// or route series is its parent's: a provider erasure deletes the blobs by
// it. Its parent id alone finds it (byParent).
func ecgSeries(ecgID int64, provider string) seriesRef {
	return seriesRef{series.ECG, provider, strconv.FormatInt(ecgID, 10)}
}
func routeSeries(workoutID int64, provider string) seriesRef {
	return seriesRef{series.Route, provider, strconv.FormatInt(workoutID, 10)}
}
func heartbeatSeries(provider, id string) seriesRef { return seriesRef{series.Heartbeat, provider, id} }

// byParent reports whether the series of ref.kind is found by its parent id
// alone: an ECG recording or a workout id is unique, a heartbeat series id
// only within its provider.
func (ref seriesRef) byParent() bool { return ref.kind != series.Heartbeat }

// where is the condition on series_blobs that finds ref, from $3.
func (ref seriesRef) where() (string, []any) {
	if ref.byParent() {
		return `kind = $3 AND parent = $4`, []any{int16(ref.kind), ref.parent}
	}
	return `kind = $3 AND parent = $4 AND provider = $5`, []any{int16(ref.kind), ref.parent, ref.provider}
}

// storedSeries is a series as stored: the points of its blob and of its
// per-point rows not converted yet.
type storedSeries struct {
	pts []series.Point
	// blob is whether a blob is stored, rows the per-point rows read.
	blob bool
	rows int
	// ingested is the earliest ingestion time of what is stored, captured
	// the newest known capture time; zero when none.
	ingested, captured time.Time
	// first and last bound the stored point times (an ECG sample's time is
	// its recording's start plus its offset).
	first, last time.Time
}

func (s *storedSeries) span(t time.Time) {
	if s.first.IsZero() || t.Before(s.first) {
		s.first = t
	}
	if t.After(s.last) {
		s.last = t
	}
}

func (s *storedSeries) seen(ingested any) {
	if t, ok := ingested.(time.Time); ok && (s.ingested.IsZero() || t.Before(s.ingested)) {
		s.ingested = t
	}
}

// perPoint is how the per-point rows of a kind are found.
var perPoint = map[series.Kind]struct {
	table, where, order string
}{
	series.ECG:       {tECGSamples, `ecg_id = $3::bigint`, `ts, id`},
	series.Route:     {tRoutes, `workout_id = $3::bigint`, `"timestamp", id`},
	series.Heartbeat: {tHeartbeat, `provider = $3 AND series_id = $4`, `"timestamp"`},
}

// refArgs are the arguments of perPoint[ref.kind].where.
func (ref seriesRef) refArgs() []any {
	if ref.kind == series.Heartbeat {
		return []any{ref.provider, ref.parent}
	}
	return []any{ref.parent}
}

// pointOfRow is the point a per-point row holds, and its time.
func pointOfRow(kind series.Kind, r Row) (series.Point, time.Time) {
	var p series.Point
	f := func(name string) *float64 {
		if v, ok := r.float(name); ok {
			return &v
		}
		return nil
	}
	switch kind {
	case series.ECG:
		p.Set(series.ECGOffset, f("time_since_sample_start"))
		p.Set(series.ECGVoltage, f("voltage_uv"))
		at, _ := r["ts"].(time.Time)
		return p, at
	case series.Route:
		at, _ := r["timestamp"].(time.Time)
		p.At = series.Micro(at)
		p.Set(series.RouteLatitude, f("latitude"))
		p.Set(series.RouteLongitude, f("longitude"))
		p.Set(series.RouteAltitude, f("altitude_m"))
		p.Set(series.RouteSpeed, f("speed_mps"))
		p.Set(series.RouteCourse, f("course_deg"))
		return p, at
	default:
		at, _ := r["timestamp"].(time.Time)
		p.At = series.Micro(at)
		p.Set(series.HeartbeatRR, f("rr_ms"))
		return p, at
	}
}

// loadSeries reads the series ref of the scope's user: its blob, opened, and
// its per-point rows, opened.
func loadSeries(ctx context.Context, tx TxLike, sc scope, ref seriesRef) (storedSeries, error) {
	var st storedSeries
	where, args := ref.where()
	blobs, err := collectRows(openRows(ctx, tx, sc.uv, tSeries, `SELECT * FROM ohs_data.series_blobs
		WHERE tenant_id = $1 AND user_id = $2 AND `+where+` ORDER BY provider`,
		append([]any{sc.tenant, sc.user}, args...)))
	if err != nil {
		return st, fmt.Errorf("read %s series: %w", ref.kind, err)
	}
	for _, b := range blobs {
		raw, _ := b["points"].([]byte)
		pts, err := series.Decode(ref.kind, raw)
		if err != nil {
			return st, fmt.Errorf("read %s series %s: %w", ref.kind, ref.parent, err)
		}
		// One blob per parent; two (a parent whose provider changed between
		// writes) are merged and stored as one.
		if st.pts, _, err = series.Merge(ref.kind, st.pts, pts); err != nil {
			return st, err
		}
		st.blob = true
		st.seen(b["ingested_at"])
		if t, ok := b["captured_at"].(time.Time); ok {
			st.captured = t
		}
		if t, ok := b["start_at"].(time.Time); ok {
			st.span(t)
		}
		if t, ok := b["end_at"].(time.Time); ok {
			st.span(t)
		}
	}
	pp := perPoint[ref.kind]
	rows, err := collectRows(openRows(ctx, tx, sc.uv, pp.table, `SELECT * FROM `+qualified(pp.table)+`
		WHERE tenant_id = $1 AND user_id = $2 AND `+pp.where+` ORDER BY `+pp.order,
		append([]any{sc.tenant, sc.user}, ref.refArgs()...)))
	if err != nil {
		return st, fmt.Errorf("read %s rows: %w", pp.table, err)
	}
	if len(rows) > 0 {
		legacy := make([]series.Point, 0, len(rows))
		for _, r := range rows {
			p, at := pointOfRow(ref.kind, r)
			legacy = append(legacy, p)
			st.span(at)
			st.seen(r["ingested_at"])
		}
		// The rows come first: the blob of a series that has rows left was
		// written by a process that did not read them, after them.
		merged, _, err := series.Merge(ref.kind, legacy, st.pts)
		if err != nil {
			return st, err
		}
		st.pts, st.rows = merged, len(rows)
	}
	return st, nil
}

// ecgTime is the time of an ECG sample at offset off of a recording that
// started at start, as the per-point rows stored it.
func ecgTime(start time.Time, off float64) time.Time {
	return start.Round(time.Microsecond).Add(time.Duration(off * float64(time.Second))).Round(time.Microsecond)
}

// storeSeries writes pts as the series ref, replacing the stored blob, and
// deletes its per-point rows (st.rows > 0). A series left with no point is
// deleted. first and last bound the point times.
func storeSeries(ctx context.Context, tx TxLike, sc scope, ref seriesRef, st storedSeries, pts []series.Point, first, last time.Time) error {
	if st.rows > 0 {
		pp := perPoint[ref.kind]
		if _, err := tx.Exec(ctx, `DELETE FROM `+qualified(pp.table)+` WHERE tenant_id = $1 AND user_id = $2 AND `+pp.where,
			append([]any{sc.tenant, sc.user}, ref.refArgs()...)...); err != nil {
			return fmt.Errorf("delete %s rows of a series: %w", pp.table, err)
		}
	}
	if st.blob && (len(pts) == 0 || ref.byParent()) {
		// Gone, or stored under another provider than ref's: the upsert
		// below would not replace it.
		where, args := ref.where()
		if len(pts) > 0 {
			where, args = where+` AND provider <> $5`, append(args, ref.provider)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ohs_data.series_blobs WHERE tenant_id = $1 AND user_id = $2 AND `+where,
			append([]any{sc.tenant, sc.user}, args...)...); err != nil {
			return fmt.Errorf("delete %s series: %w", ref.kind, err)
		}
	}
	if len(pts) == 0 {
		return nil
	}
	blob, err := series.Encode(ref.kind, pts)
	if err != nil {
		return err
	}
	// Without keys the compressed blob is stored as it is; with keys it is
	// sealed like any row, its identity binding it to this series.
	var points, sealed any = blob, nil
	if sc.uv != nil {
		t := vault.Schema[tSeries]
		id, err := rowIdentity(t, func(c string) any {
			switch c {
			case "kind":
				return int16(ref.kind)
			case "format":
				return int16(series.Format)
			case "provider":
				return ref.provider
			}
			return ref.parent
		})
		if err != nil {
			return err
		}
		if sealed, err = sc.uv.SealRow(t, id, []any{blob}); err != nil {
			return fmt.Errorf("seal %s series: %w", ref.kind, err)
		}
		points = nil
	}
	if _, err := tx.Exec(ctx, seriesUpsert, sc.tenant, sc.user, int16(ref.kind), ref.provider, ref.parent, int16(series.Format),
		first, last, len(pts), points, sealed, nullTime(st.ingested), nullTime(st.captured)); err != nil {
		return fmt.Errorf("write %s series: %w", ref.kind, err)
	}
	return nil
}

// seriesUpsert stores a series blob; an unknown ingestion time ($12) is now.
const seriesUpsert = `INSERT INTO ohs_data.series_blobs
	(tenant_id, user_id, kind, provider, parent, format, start_at, end_at, point_count, points, sealed, ingested_at, captured_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, COALESCE($12::timestamptz, now()), $13)
	ON CONFLICT (tenant_id, user_id, kind, parent, provider) DO UPDATE SET
		format = EXCLUDED.format, start_at = EXCLUDED.start_at, end_at = EXCLUDED.end_at,
		point_count = EXCLUDED.point_count, points = EXCLUDED.points, sealed = EXCLUDED.sealed,
		ingested_at = EXCLUDED.ingested_at, captured_at = EXCLUDED.captured_at`

// writeSeries merges in, the points one write brings to the series ref, into
// the stored series and stores it. recStart is the start of the ECG
// recording of an ECG series (the times of its samples), zero otherwise. It
// returns the points written (new, or replaced by a newer copy) and skipped
// (stored already).
func writeSeries(ctx context.Context, tx TxLike, sc scope, ref seriesRef, in []series.Point, recStart time.Time) (written, skipped int, err error) {
	if err := lockUserTable(ctx, tx, tSeries, sc); err != nil {
		return 0, 0, err
	}
	st, err := loadSeries(ctx, tx, sc, ref)
	if err != nil {
		return 0, 0, err
	}
	capture := int64(0)
	if !sc.captured.IsZero() {
		capture = series.Micro(sc.captured)
	}
	for i := range in {
		in[i].Capture = capture
	}
	merged, ms, err := series.Merge(ref.kind, st.pts, in)
	if err != nil {
		return 0, 0, err
	}
	written, skipped = ms.Added+ms.Replaced, ms.Skipped
	newerCapture := !sc.captured.IsZero() && (st.captured.IsZero() || sc.captured.After(st.captured))
	if written == 0 && st.rows == 0 && (!newerCapture || !st.blob) {
		return 0, skipped, nil
	}
	if newerCapture {
		st.captured = sc.captured
	}
	first, last := seriesSpan(ref.kind, merged, recStart, st)
	return written, skipped, storeSeries(ctx, tx, sc, ref, st, merged, first, last)
}

// seriesSpan bounds the point times of pts: from their own times, or for an
// ECG from its recording's start (or, when unknown, what was stored).
func seriesSpan(kind series.Kind, pts []series.Point, recStart time.Time, st storedSeries) (time.Time, time.Time) {
	if kind != series.ECG {
		return series.Span(pts)
	}
	if recStart.IsZero() || len(pts) == 0 {
		return st.first, st.last
	}
	first, last := ecgTime(recStart, pts[0].V[series.ECGOffset]), ecgTime(recStart, pts[len(pts)-1].V[series.ECGOffset])
	if last.Before(first) {
		first, last = last, first
	}
	return first, last
}

// moveSeries merges every series of from into the series to and deletes
// them: the route of superseded copies of a workout goes to the kept one.
func moveSeries(ctx context.Context, tx TxLike, sc scope, to seriesRef, from []seriesRef) error {
	if err := lockUserTable(ctx, tx, tSeries, sc); err != nil {
		return err
	}
	dst, err := loadSeries(ctx, tx, sc, to)
	if err != nil {
		return err
	}
	pts, changed := dst.pts, dst.rows > 0
	for _, ref := range from {
		src, err := loadSeries(ctx, tx, sc, ref)
		if err != nil {
			return err
		}
		if len(src.pts) == 0 && !src.blob && src.rows == 0 {
			continue
		}
		if pts, _, err = series.Merge(to.kind, pts, src.pts); err != nil {
			return err
		}
		dst.seen(src.ingested)
		if src.captured.After(dst.captured) {
			dst.captured = src.captured
		}
		if !src.first.IsZero() {
			dst.span(src.first)
			dst.span(src.last)
		}
		if err := storeSeries(ctx, tx, sc, ref, src, nil, time.Time{}, time.Time{}); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	first, last := seriesSpan(to.kind, pts, time.Time{}, dst)
	return storeSeries(ctx, tx, sc, to, dst, pts, first, last)
}

// deleteSeries deletes the blobs of the series of kind whose parents are ids
// (ECG recordings or workouts).
func deleteSeries(ctx context.Context, tx TxLike, sc scope, kind series.Kind, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, deletionSeriesBlobs, sc.tenant, sc.user, int16(kind), ids); err != nil {
		return fmt.Errorf("delete %s series: %w", kind, err)
	}
	return nil
}

// deletionSeriesBlobs deletes the blobs of kind ($3) whose parents are the
// ids $4 (ECG recordings or workouts).
const deletionSeriesBlobs = `DELETE FROM ohs_data.series_blobs
	WHERE tenant_id = $1 AND user_id = $2 AND kind = $3 AND parent = ANY($4::bigint[]::text[])`

// --- the writers of the three series ---

// writeHeartbeatSamples writes heartbeat samples, one series blob per series
// id of the scope's provider.
func (w *Writer) writeHeartbeatSamples(ctx context.Context, tx TxLike, sc scope, samples []model.HeartbeatSample) (written, skipped int, err error) {
	var order []string
	bySeries := map[string][]series.Point{}
	for _, s := range samples {
		if _, ok := bySeries[s.SeriesID]; !ok {
			order = append(order, s.SeriesID)
		}
		p := series.Point{At: series.Micro(s.Timestamp)}
		p.V[series.HeartbeatRR] = s.RRMS
		bySeries[s.SeriesID] = append(bySeries[s.SeriesID], p)
	}
	for _, id := range order {
		n, k, err := writeSeries(ctx, tx, sc, heartbeatSeries(sc.provider, id), bySeries[id], time.Time{})
		if err != nil {
			return written, skipped, err
		}
		written, skipped = written+n, skipped+k
	}
	return written, skipped, nil
}

// writeRoutePoints writes route points into the route of a resolved workout
// of provider.
func (w *Writer) writeRoutePoints(ctx context.Context, tx TxLike, sc scope, workoutID int64, provider string, points []model.RoutePoint) (written, skipped int, err error) {
	pts := make([]series.Point, len(points))
	for i, rp := range points {
		p := series.Point{At: series.Micro(rp.Timestamp)}
		p.V[series.RouteLatitude], p.V[series.RouteLongitude] = rp.Latitude, rp.Longitude
		p.Set(series.RouteAltitude, rp.AltitudeM)
		p.Set(series.RouteSpeed, rp.SpeedMps)
		p.Set(series.RouteCourse, rp.CourseDeg)
		pts[i] = p
	}
	return writeSeries(ctx, tx, sc, routeSeries(workoutID, provider), pts, time.Time{})
}

// writeECGVoltageSamples writes voltage samples into the series of a
// resolved recording of provider that started at recStart.
func (w *Writer) writeECGVoltageSamples(ctx context.Context, tx TxLike, sc scope, ecgID int64, provider string, recStart time.Time, samples []model.ECGVoltageSample) (written, skipped int, err error) {
	pts := make([]series.Point, len(samples))
	for i, s := range samples {
		pts[i].V[series.ECGOffset], pts[i].V[series.ECGVoltage] = s.TimeSinceSampleStart, s.VoltageUV
	}
	return writeSeries(ctx, tx, sc, ecgSeries(ecgID, provider), pts, recStart)
}

// --- conversion of the per-point rows (ohs vault seal) ---

// convertParentSeries moves the per-point rows of the series ref into its
// blob, and returns how many rows it moved.
func convertParentSeries(ctx context.Context, tx TxLike, sc scope, ref seriesRef, recStart time.Time) (int, error) {
	if err := lockUserTable(ctx, tx, tSeries, sc); err != nil {
		return 0, err
	}
	st, err := loadSeries(ctx, tx, sc, ref)
	if err != nil || st.rows == 0 {
		return 0, err
	}
	first, last := seriesSpan(ref.kind, st.pts, recStart, st)
	return st.rows, storeSeries(ctx, tx, sc, ref, st, st.pts, first, last)
}

// perPointParents is how the seal command finds the series of a per-point
// table: the parents of its oldest rows.
var perPointParents = map[string]struct {
	sql  string
	kind series.Kind
}{
	tECGSamples: {`SELECT DISTINCT p.ecg_id::text, coalesce(r.provider, ''), r.start_date FROM (
			SELECT ecg_id FROM ohs_data.ecg_samples WHERE tenant_id = $1 AND user_id = $2 ORDER BY ts LIMIT $3) p
		LEFT JOIN ohs_data.ecg_recordings r ON r.tenant_id = $1 AND r.user_id = $2 AND r.id = p.ecg_id`, series.ECG},
	tRoutes: {`SELECT DISTINCT p.workout_id::text, coalesce(w.provider, ''), NULL::timestamptz FROM (
			SELECT workout_id FROM ohs_data.route_points WHERE tenant_id = $1 AND user_id = $2 ORDER BY "timestamp" LIMIT $3) p
		LEFT JOIN ohs_data.workouts w ON w.tenant_id = $1 AND w.user_id = $2 AND w.id = p.workout_id`, series.Route},
	tHeartbeat: {`SELECT DISTINCT series_id, provider, NULL::timestamptz FROM (
			SELECT series_id, provider FROM ohs_data.heartbeat_samples WHERE tenant_id = $1 AND user_id = $2 ORDER BY "timestamp" LIMIT $3) p`, series.Heartbeat},
}

// convertPerPoint moves the per-point rows of table into series blobs: the
// whole series of the parents of the oldest limit rows, sealed or clear.
// Each batch empties at least one series, so a run ends.
func convertPerPoint(ctx context.Context, tx TxLike, sc scope, table string, limit int) (vault.SealCount, error) {
	var c vault.SealCount
	pp := perPointParents[table]
	type parent struct {
		id, provider string
		start        *time.Time
	}
	found, err := tx.Query(ctx, pp.sql, sc.tenant, sc.user, limit)
	if err != nil {
		return c, fmt.Errorf("convert %s rows: %w", table, err)
	}
	var parents []parent
	for found.Next() {
		var p parent
		if err := found.Scan(&p.id, &p.provider, &p.start); err != nil {
			found.Close()
			return c, fmt.Errorf("convert %s rows: %w", table, err)
		}
		parents = append(parents, p)
	}
	found.Close()
	if err := found.Err(); err != nil {
		return c, fmt.Errorf("convert %s rows: %w", table, err)
	}
	slices.SortFunc(parents, func(a, b parent) int {
		if a.provider != b.provider {
			if a.provider < b.provider {
				return -1
			}
			return 1
		}
		switch {
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		}
		return 0
	})
	for _, p := range parents {
		var start time.Time
		if p.start != nil {
			start = *p.start
		}
		n, err := convertParentSeries(ctx, tx, sc, seriesRef{pp.kind, p.provider, p.id}, start)
		if err != nil {
			return c, err
		}
		c.Rows += n
	}
	return c, nil
}

// sealClearSeries seals the series blobs a process without keys stored
// clear.
func sealClearSeries(ctx context.Context, tx TxLike, sc scope, limit int) (vault.SealCount, error) {
	var c vault.SealCount
	rows, err := collectRows(openRows(ctx, tx, nil, tSeries, `SELECT * FROM ohs_data.series_blobs
		WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NULL ORDER BY start_at LIMIT $3`,
		[]any{sc.tenant, sc.user, limit}))
	if err != nil {
		return c, fmt.Errorf("seal %s rows: %w", tSeries, err)
	}
	t := vault.Schema[tSeries]
	for _, r := range rows {
		id, err := rowIdentity(t, func(col string) any { return r[col] })
		if err != nil {
			return c, err
		}
		sealed, err := sc.uv.SealRow(t, id, []any{r["points"]})
		if err != nil {
			return c, fmt.Errorf("seal %s row: %w", tSeries, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE ohs_data.series_blobs SET sealed = $6, points = NULL
			WHERE tenant_id = $1 AND user_id = $2 AND kind = $3 AND provider = $4 AND parent = $5 AND sealed IS NULL`,
			sc.tenant, sc.user, r["kind"], r["provider"], r["parent"], sealed); err != nil {
			return c, fmt.Errorf("seal %s row: %w", tSeries, err)
		}
		c.Rows++
	}
	return c, nil
}
