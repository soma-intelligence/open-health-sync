// SPDX-License-Identifier: AGPL-3.0-only

// Package read is the query layer behind every read endpoint: the REST API and
// the MCP server both call it, so tenant isolation lives in one place. Every
// function takes the caller's tenant and user, returns ErrNotFound when the
// user (or the parent workout or sleep session) does not belong to that
// tenant or is being deleted, and filters every row on both ids.
//
// Lists are ordered by (time, id) ascending and paged by an opaque cursor:
// base64url of the last row's time and id. Time windows are half open,
// [Start, End), and a zero bound is open.
package read

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/fusion"
)

// Page limits.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

var (
	// ErrNotFound means the user, or the parent record, is not the tenant's.
	ErrNotFound = errors.New("read: not found")
	// ErrInvalidCursor means the cursor could not be decoded.
	ErrInvalidCursor = errors.New("read: invalid cursor")
)

// QueryError is a caller mistake in a query (the API answers 400 with Msg).
type QueryError struct{ Msg string }

func (e *QueryError) Error() string { return e.Msg }

// Querier is what the queries run on (a *pgxpool.Pool or a pgx.Tx).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Page is one page of records. NextCursor is empty on the last page.
type Page[T any] struct {
	Records    []T
	NextCursor string
}

// Query filters and pages a list.
type Query struct {
	// Type is the canonical type (quantity type, category type or workout
	// activity type). Required for Samples, optional elsewhere.
	Type string
	// Start and End bound the record time, [Start, End). Zero is open.
	Start, End time.Time
	Cursor     string
	// Limit is the page size: 0 means DefaultLimit, at most MaxLimit.
	Limit int
	// View is ViewMerged (the default, also when empty) or ViewAll. It
	// applies to samples, category samples, sleep, workouts, daily summaries
	// and observations.
	View View
	// Policy ranks the user's sources in the merged view; nil reads the
	// user's stored orders over the built-in defaults.
	Policy *fusion.Policy
}

type normalized struct {
	Query
	afterT  time.Time
	afterID int64
	paged   bool
}

func (q Query) normalize(typeRequired bool) (normalized, error) {
	n := normalized{Query: q}
	if typeRequired && strings.TrimSpace(q.Type) == "" {
		return n, &QueryError{Msg: "the type parameter is required"}
	}
	if !q.Start.IsZero() && !q.End.IsZero() && q.Start.After(q.End) {
		return n, &QueryError{Msg: "start must not be after end"}
	}
	switch {
	case q.Limit == 0:
		n.Limit = DefaultLimit
	case q.Limit < 0 || q.Limit > MaxLimit:
		return n, &QueryError{Msg: fmt.Sprintf("limit must be between 1 and %d", MaxLimit)}
	}
	if _, err := ParseView(string(q.View)); err != nil {
		return n, err
	}
	if q.Cursor != "" {
		t, id, err := DecodeCursor(q.Cursor)
		if err != nil {
			return n, err
		}
		n.afterT, n.afterID, n.paged = t, id, true
	}
	return n, nil
}

// EncodeCursor returns the cursor after a row at t with id.
func EncodeCursor(t time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(id, 10)))
}

// DecodeCursor reverses EncodeCursor.
func DecodeCursor(c string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	ts, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, 0, ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	return t, id, nil
}

// knownQuantityType refuses a type that is not in the quantity type catalog,
// so a typo is a 400 rather than an empty page. A type outside the catalog
// that this user does have samples of (a connector may emit a metric the
// catalog does not declare yet) is still served.
func knownQuantityType(ctx context.Context, db Querier, tenantID, userID uuid.UUID, qt string) error {
	var ok bool
	if err := db.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM ohs_data.quantity_codes WHERE canonical_code = $3)
			OR EXISTS (SELECT 1 FROM ohs_data.quantity_samples
				WHERE tenant_id = $1 AND user_id = $2 AND quantity_type = $3)`,
		tenantID, userID, qt).Scan(&ok); err != nil {
		return fmt.Errorf("read: quantity type: %w", err)
	}
	if !ok {
		return &QueryError{Msg: fmt.Sprintf("unknown type %q: not a quantity type of the catalog (snake_case names such as heart_rate or step_count; listed in docs/data-model.md and by the MCP tool list_quantity_types)", qt)}
	}
	return nil
}

// userExists reports whether user is an active user of tenant. A user being
// deleted reads as absent.
func userExists(ctx context.Context, db Querier, tenantID, userID uuid.UUID) error {
	var ok bool
	if err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 AND status = 'active')`,
		tenantID, userID).Scan(&ok); err != nil {
		return fmt.Errorf("read: user: %w", err)
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// list is one paged query over table. timeCol orders and bounds it; it is a
// date column when date is true (bounds and cursor compare as dates).
type list struct {
	table, cols, timeCol, typeCol string
	date                          bool
	// parentCol, parentID add "AND parentCol = parentID" when parentCol is set.
	parentCol string
	parentID  int64
	// where adds conditions over the row (alias r), binding parameters
	// through arg, for a read of q. Set, it is the merged view's, and an
	// open-ended read goes through windows (see windowed).
	where func(arg func(any) string, q normalized) []string
}

func run[T any](ctx context.Context, db Querier, tenantID, userID uuid.UUID, l list, q normalized,
	scan func(pgx.Rows) (T, time.Time, int64, error)) (Page[T], error) {
	if l.where == nil || !q.End.IsZero() || l.date {
		return runOnce(ctx, db, tenantID, userID, l, q, scan)
	}
	since := q.Start
	if q.paged && q.afterT.After(since) {
		since = q.afterT
	}
	first := func(from time.Time) (time.Time, error) {
		where, args := "tenant_id = $1 AND user_id = $2", []any{tenantID, userID}
		if l.typeCol != "" && q.Type != "" {
			args = append(args, q.Type)
			where += " AND " + l.typeCol + " = $3"
		}
		return firstFrom(ctx, db, []timed{{l.table + " WHERE " + where, l.timeCol}}, args, from)
	}
	return windowed(since, first, func(end time.Time) (Page[T], error) {
		wq := q
		wq.End = end
		return runOnce(ctx, db, tenantID, userID, l, wq, scan)
	})
}

// mergedWindows are the spans an open-ended read of the merged view tries
// before reading without an end: a read with an end bounds the lookups of a
// better ranked record on both sides, so TimescaleDB plans the chunks of
// the window only instead of every chunk after the page's start (seconds
// per page on a year of data).
var mergedWindows = []time.Duration{2 * 24 * time.Hour, 16 * 24 * time.Hour, 128 * 24 * time.Hour}

// windowed reads a page that starts at since and has no end through
// mergedWindows: read(end) is the page up to end. A window that fills the
// page gives it (its records are the first ones whatever comes after the
// window), as does one after which no record starts. first(t) is the start
// of the earliest record at or after t (zero when none), the start of the
// first window when since is zero.
func windowed[T any](since time.Time, first func(time.Time) (time.Time, error), read func(end time.Time) (Page[T], error)) (Page[T], error) {
	if since.IsZero() {
		t, err := first(time.Time{})
		if err != nil || t.IsZero() {
			if err != nil {
				return Page[T]{}, err
			}
			return read(time.Time{})
		}
		since = t
	}
	for _, w := range mergedWindows {
		end := since.Add(w)
		pg, err := read(end)
		if err != nil || pg.NextCursor != "" {
			return pg, err
		}
		next, err := first(end)
		if err != nil {
			return Page[T]{}, err
		}
		if next.IsZero() {
			return pg, nil
		}
	}
	return read(time.Time{})
}

// timed is a set of rows ("table WHERE conditions") and the SQL time of a
// row.
type timed struct{ rows, time string }

// firstFrom is the earliest time of the rows of sets (args bound as $1...)
// at or after from, or the zero time.
func firstFrom(ctx context.Context, db Querier, sets []timed, args []any, from time.Time) (time.Time, error) {
	parts := make([]string, len(sets))
	n := len(args)
	if !from.IsZero() {
		args = append(args, from)
	}
	for i, s := range sets {
		q := "SELECT " + s.time + " AS t FROM " + s.rows
		if !from.IsZero() {
			q += fmt.Sprintf(" AND %s >= $%d::timestamptz", s.time, n+1)
		}
		parts[i] = "(" + q + " ORDER BY 1 LIMIT 1)"
	}
	var t *time.Time
	if err := db.QueryRow(ctx, "SELECT min(t) FROM ("+strings.Join(parts, " UNION ALL ")+") f", args...).Scan(&t); err != nil {
		return time.Time{}, fmt.Errorf("read: first record: %w", err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

func runOnce[T any](ctx context.Context, db Querier, tenantID, userID uuid.UUID, l list, q normalized,
	scan func(pgx.Rows) (T, time.Time, int64, error)) (Page[T], error) {
	args := []any{tenantID, userID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	bound := func(t time.Time) string {
		if l.date {
			return arg(t.UTC().Format(time.DateOnly)) + "::date"
		}
		return arg(t)
	}
	where := []string{"tenant_id = $1", "user_id = $2"}
	if l.parentCol != "" {
		where = append(where, l.parentCol+" = "+arg(l.parentID))
	}
	if l.typeCol != "" && q.Type != "" {
		where = append(where, l.typeCol+" = "+arg(q.Type))
	}
	if l.where != nil {
		where = append(where, l.where(arg, q)...)
	}
	if !q.Start.IsZero() {
		where = append(where, l.timeCol+" >= "+bound(q.Start))
	}
	if !q.End.IsZero() {
		where = append(where, l.timeCol+" < "+bound(q.End))
	}
	if q.paged {
		// The scalar bound lets TimescaleDB exclude chunks before the cursor;
		// the row comparison breaks ties on time by id.
		after := bound(q.afterT)
		where = append(where, l.timeCol+" >= "+after,
			"("+l.timeCol+", id) > ("+after+", "+arg(q.afterID)+")")
	}
	sql := "SELECT " + l.cols + " FROM " + l.table + " r WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + l.timeCol + ", id LIMIT " + arg(q.Limit+1)

	if l.where != nil {
		args = append([]any{customPlan}, args...)
	}
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return Page[T]{}, fmt.Errorf("read: %s: %w", l.table, err)
	}
	defer rows.Close()
	out := Page[T]{Records: []T{}}
	var lastT time.Time
	var lastID int64
	for rows.Next() {
		if len(out.Records) == q.Limit {
			out.NextCursor = EncodeCursor(lastT, lastID)
			break
		}
		rec, t, id, err := scan(rows)
		if err != nil {
			return Page[T]{}, fmt.Errorf("read: %s: scan: %w", l.table, err)
		}
		out.Records = append(out.Records, rec)
		lastT, lastID = t, id
	}
	if err := rows.Err(); err != nil {
		return Page[T]{}, fmt.Errorf("read: %s: %w", l.table, err)
	}
	return out, nil
}

// mergeWhere sets l.where to leave out the records of f a better ranked
// source covers, when q reads the merged view.
func mergeWhere(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q normalized, l *list, f family) error {
	if !q.merged() {
		return nil
	}
	p, err := policy(ctx, db, tenantID, userID, q.Query)
	if err != nil {
		return err
	}
	metric := f.key
	if metric == "" {
		metric = q.Type // "" for category samples of every type
	}
	l.where = func(arg func(any) string, q normalized) []string {
		since := q.Start
		if q.paged && q.afterT.After(since) {
			since = q.afterT
		}
		return []string{"NOT " + f.covered(bindMerge(p, nil, arg).window(since, q.End), "r", metric)}
	}
	return nil
}

func utc(t *time.Time) {
	if t != nil {
		*t = t.UTC()
	}
}

// Provenance is carried by every record.
type Provenance struct {
	Provider   string    `json:"provider"`
	Source     *string   `json:"source"` // the vendor that produced the record
	Device     *string   `json:"device"`
	IngestedAt time.Time `json:"ingested_at"`
}

// Sample is one quantity sample.
type Sample struct {
	ID              int64           `json:"id"`
	Type            string          `json:"type"`
	Value           float64         `json:"value"`
	Unit            *string         `json:"unit"`
	StartDate       time.Time       `json:"start_date"`
	EndDate         *time.Time      `json:"end_date"`
	SourceName      *string         `json:"source_name"`
	SourceBundleID  *string         `json:"source_bundle_id"`
	DeviceModel     *string         `json:"device_model"`
	ProductType     *string         `json:"product_type"`
	ExternalID      *string         `json:"external_id"`
	IsPrimarySource *bool           `json:"is_primary_source"`
	WasUserEntered  *bool           `json:"was_user_entered"`
	Metadata        json.RawMessage `json:"metadata"`
	Provenance
}

// Samples lists quantity samples of one type (q.Type is required).
func Samples(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[Sample], error) {
	return samples(ctx, db, tenantID, userID, q, true)
}

// AllSamples lists quantity samples of every type, or of one when q.Type is
// set, by start date then id (the exports read it).
func AllSamples(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[Sample], error) {
	return samples(ctx, db, tenantID, userID, q, false)
}

func samples(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query, typeRequired bool) (Page[Sample], error) {
	n, err := q.normalize(typeRequired)
	if err != nil {
		return Page[Sample]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[Sample]{}, err
	}
	if n.Type != "" {
		if err := knownQuantityType(ctx, db, tenantID, userID, n.Type); err != nil {
			return Page[Sample]{}, err
		}
	}
	l := list{table: "ohs_data.quantity_samples", timeCol: "start_date", typeCol: "quantity_type", cols: sampleCols}
	if err := mergeWhere(ctx, db, tenantID, userID, n, &l, quantityFamily); err != nil {
		return Page[Sample]{}, err
	}
	return run(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (Sample, time.Time, int64, error) {
		var s Sample
		err := r.Scan(s.dest()...)
		s.utc()
		return s, s.StartDate, s.ID, err
	})
}

// sampleCols are the columns of a Sample, in the order of Sample.dest.
const sampleCols = `id, quantity_type, value, unit, start_date, end_date, source_name, source_bundle_id,
	device_model, product_type, external_id, is_primary_source, was_user_entered, metadata,
	provider, source_slug, device_id, ingested_at`

func (s *Sample) dest() []any {
	return []any{&s.ID, &s.Type, &s.Value, &s.Unit, &s.StartDate, &s.EndDate, &s.SourceName, &s.SourceBundleID,
		&s.DeviceModel, &s.ProductType, &s.ExternalID, &s.IsPrimarySource, &s.WasUserEntered, &s.Metadata,
		&s.Provider, &s.Source, &s.Device, &s.IngestedAt}
}

func (s *Sample) utc() {
	utc(&s.StartDate)
	utc(s.EndDate)
	utc(&s.IngestedAt)
}

// CategorySample is one categorical event.
type CategorySample struct {
	ID         int64           `json:"id"`
	Type       string          `json:"type"`
	ValueInt   *int            `json:"value_int"`
	ValueLabel *string         `json:"value_label"`
	StartDate  time.Time       `json:"start_date"`
	EndDate    *time.Time      `json:"end_date"`
	SourceName *string         `json:"source_name"`
	ExternalID *string         `json:"external_id"`
	Metadata   json.RawMessage `json:"metadata"`
	Provenance
}

// CategorySamples lists category samples, of one type when q.Type is set.
func CategorySamples(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[CategorySample], error) {
	n, err := q.normalize(false)
	if err != nil {
		return Page[CategorySample]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[CategorySample]{}, err
	}
	l := list{table: "ohs_data.category_samples", timeCol: "start_date", typeCol: "category_type",
		cols: `id, category_type, value_int, value_label, start_date, end_date, source_name, external_id, metadata,
			provider, source_slug, device_id, ingested_at`}
	if err := mergeWhere(ctx, db, tenantID, userID, n, &l, categoryFamily); err != nil {
		return Page[CategorySample]{}, err
	}
	return run(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (CategorySample, time.Time, int64, error) {
		var s CategorySample
		err := r.Scan(&s.ID, &s.Type, &s.ValueInt, &s.ValueLabel, &s.StartDate, &s.EndDate, &s.SourceName,
			&s.ExternalID, &s.Metadata, &s.Provider, &s.Source, &s.Device, &s.IngestedAt)
		utc(&s.StartDate)
		utc(s.EndDate)
		utc(&s.IngestedAt)
		return s, s.StartDate, s.ID, err
	})
}

// SleepSession is one night (or nap).
type SleepSession struct {
	ID                int64           `json:"id"`
	BedtimeStart      time.Time       `json:"bedtime_start"`
	BedtimeEnd        *time.Time      `json:"bedtime_end"`
	TotalSleepSeconds *int            `json:"total_sleep_seconds"`
	DeepSleepSeconds  *int            `json:"deep_sleep_seconds"`
	REMSleepSeconds   *int            `json:"rem_sleep_seconds"`
	LightSleepSeconds *int            `json:"light_sleep_seconds"`
	AwakeSeconds      *int            `json:"awake_seconds"`
	SleepScore        *float64        `json:"sleep_score"`
	HRVAvgMs          *float64        `json:"hrv_avg_ms"`
	RestingHR         *float64        `json:"resting_hr"`
	RespiratoryRate   *float64        `json:"respiratory_rate"`
	SpO2Avg           *float64        `json:"spo2_avg"`
	ExternalID        *string         `json:"external_id"`
	Metadata          json.RawMessage `json:"metadata"`
	Provenance
}

// Sleep lists sleep sessions by bedtime start.
func Sleep(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[SleepSession], error) {
	n, err := q.normalize(false)
	if err != nil {
		return Page[SleepSession]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[SleepSession]{}, err
	}
	l := list{table: "ohs_data.sleep_sessions", timeCol: "bedtime_start", cols: sleepCols}
	if err := mergeWhere(ctx, db, tenantID, userID, n, &l, sleepFamily); err != nil {
		return Page[SleepSession]{}, err
	}
	return run(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (SleepSession, time.Time, int64, error) {
		var s SleepSession
		err := r.Scan(s.dest()...)
		s.utc()
		return s, s.BedtimeStart, s.ID, err
	})
}

// sleepCols are the columns of a SleepSession, in the order of its dest.
const sleepCols = `id, bedtime_start, bedtime_end, total_sleep_seconds, deep_sleep_seconds, rem_sleep_seconds,
	light_sleep_seconds, awake_seconds, sleep_score, hrv_avg_ms, resting_hr, respiratory_rate, spo2_avg,
	external_id, metadata, provider, source_slug, device_id, ingested_at`

func (s *SleepSession) dest() []any {
	return []any{&s.ID, &s.BedtimeStart, &s.BedtimeEnd, &s.TotalSleepSeconds, &s.DeepSleepSeconds,
		&s.REMSleepSeconds, &s.LightSleepSeconds, &s.AwakeSeconds, &s.SleepScore, &s.HRVAvgMs, &s.RestingHR,
		&s.RespiratoryRate, &s.SpO2Avg, &s.ExternalID, &s.Metadata, &s.Provider, &s.Source, &s.Device, &s.IngestedAt}
}

func (s *SleepSession) utc() {
	utc(&s.BedtimeStart)
	utc(s.BedtimeEnd)
	utc(&s.IngestedAt)
}

// SleepPhase is one stage of a sleep session. Its provenance is the session's.
type SleepPhase struct {
	ID        int64      `json:"id"`
	SessionID int64      `json:"session_id"`
	Phase     string     `json:"phase"`
	StartDate time.Time  `json:"start_date"`
	EndDate   *time.Time `json:"end_date"`
	DurationS int        `json:"duration_s"`
	Provenance
}

// SleepPhases lists the phases of one session of the user.
func SleepPhases(ctx context.Context, db Querier, tenantID, userID uuid.UUID, sessionID int64, q Query) (Page[SleepPhase], error) {
	n, err := q.normalize(false)
	if err != nil {
		return Page[SleepPhase]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[SleepPhase]{}, err
	}
	var prov Provenance
	err = db.QueryRow(ctx, `SELECT provider, source_slug, device_id FROM ohs_data.sleep_sessions
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3 LIMIT 1`, tenantID, userID, sessionID).
		Scan(&prov.Provider, &prov.Source, &prov.Device)
	if errors.Is(err, pgx.ErrNoRows) {
		return Page[SleepPhase]{}, ErrNotFound
	}
	if err != nil {
		return Page[SleepPhase]{}, fmt.Errorf("read: sleep session: %w", err)
	}
	l := list{table: "ohs_data.sleep_phases", timeCol: "start_date", parentCol: "session_id", parentID: sessionID,
		cols: `id, session_id, phase, start_date, end_date, duration_s, ingested_at`}
	return run(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (SleepPhase, time.Time, int64, error) {
		s := SleepPhase{Provenance: prov}
		err := r.Scan(&s.ID, &s.SessionID, &s.Phase, &s.StartDate, &s.EndDate, &s.DurationS, &s.IngestedAt)
		utc(&s.StartDate)
		utc(s.EndDate)
		utc(&s.IngestedAt)
		return s, s.StartDate, s.ID, err
	})
}

// Workout is one workout session.
type Workout struct {
	ID               int64           `json:"id"`
	ActivityType     string          `json:"activity_type"`
	StartDate        time.Time       `json:"start_date"`
	EndDate          *time.Time      `json:"end_date"`
	DurationS        *int            `json:"duration_s"`
	ActiveEnergyKcal *float64        `json:"active_energy_kcal"`
	DistanceM        *float64        `json:"distance_m"`
	AvgHeartRate     *float64        `json:"avg_heart_rate"`
	MaxHeartRate     *float64        `json:"max_heart_rate"`
	AvgSpeedMps      *float64        `json:"avg_speed_mps"`
	ElevationGainM   *float64        `json:"elevation_gain_m"`
	VO2Max           *float64        `json:"vo2_max"`
	ExternalID       *string         `json:"external_id"`
	Metadata         json.RawMessage `json:"metadata"`
	Provenance
}

// Workouts lists workouts, of one activity type when q.Type is set.
func Workouts(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[Workout], error) {
	n, err := q.normalize(false)
	if err != nil {
		return Page[Workout]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[Workout]{}, err
	}
	l := list{table: "ohs_data.workouts", timeCol: "start_date", typeCol: "activity_type", cols: workoutCols}
	if err := mergeWhere(ctx, db, tenantID, userID, n, &l, workoutFamily); err != nil {
		return Page[Workout]{}, err
	}
	return run(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (Workout, time.Time, int64, error) {
		var w Workout
		err := r.Scan(w.dest()...)
		w.utc()
		return w, w.StartDate, w.ID, err
	})
}

// workoutCols are the columns of a Workout, in the order of its dest.
const workoutCols = `id, activity_type, start_date, end_date, duration_s, active_energy_kcal, distance_m, avg_heart_rate,
	max_heart_rate, avg_speed_mps, elevation_gain_m, vo2_max, external_id, metadata,
	provider, source_slug, device_id, ingested_at`

func (w *Workout) dest() []any {
	return []any{&w.ID, &w.ActivityType, &w.StartDate, &w.EndDate, &w.DurationS, &w.ActiveEnergyKcal,
		&w.DistanceM, &w.AvgHeartRate, &w.MaxHeartRate, &w.AvgSpeedMps, &w.ElevationGainM, &w.VO2Max,
		&w.ExternalID, &w.Metadata, &w.Provider, &w.Source, &w.Device, &w.IngestedAt}
}

func (w *Workout) utc() {
	utc(&w.StartDate)
	utc(w.EndDate)
	utc(&w.IngestedAt)
}

// RoutePoint is one GPS fix of a workout. Its provenance is the workout's.
type RoutePoint struct {
	ID        int64     `json:"id"`
	WorkoutID int64     `json:"workout_id"`
	Timestamp time.Time `json:"timestamp"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	AltitudeM *float64  `json:"altitude_m"`
	SpeedMps  *float64  `json:"speed_mps"`
	CourseDeg *float64  `json:"course_deg"`
	Provenance
}

// WorkoutRoute lists the route points of one workout of the user.
func WorkoutRoute(ctx context.Context, db Querier, tenantID, userID uuid.UUID, workoutID int64, q Query) (Page[RoutePoint], error) {
	n, err := q.normalize(false)
	if err != nil {
		return Page[RoutePoint]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[RoutePoint]{}, err
	}
	var prov Provenance
	err = db.QueryRow(ctx, `SELECT provider, source_slug, device_id FROM ohs_data.workouts
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3 LIMIT 1`, tenantID, userID, workoutID).
		Scan(&prov.Provider, &prov.Source, &prov.Device)
	if errors.Is(err, pgx.ErrNoRows) {
		return Page[RoutePoint]{}, ErrNotFound
	}
	if err != nil {
		return Page[RoutePoint]{}, fmt.Errorf("read: workout: %w", err)
	}
	l := list{table: "ohs_data.route_points", timeCol: `"timestamp"`, parentCol: "workout_id", parentID: workoutID,
		cols: `id, workout_id, "timestamp", latitude, longitude, altitude_m, speed_mps, course_deg, ingested_at`}
	return run(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (RoutePoint, time.Time, int64, error) {
		p := RoutePoint{Provenance: prov}
		err := r.Scan(&p.ID, &p.WorkoutID, &p.Timestamp, &p.Latitude, &p.Longitude, &p.AltitudeM, &p.SpeedMps,
			&p.CourseDeg, &p.IngestedAt)
		utc(&p.Timestamp)
		utc(&p.IngestedAt)
		return p, p.Timestamp, p.ID, err
	})
}

// NutritionLog is one meal or food entry.
type NutritionLog struct {
	ID         int64           `json:"id"`
	StartDate  time.Time       `json:"start_date"`
	EndDate    *time.Time      `json:"end_date"`
	MealType   *string         `json:"meal_type"`
	EnergyKcal *float64        `json:"energy_kcal"`
	ProteinG   *float64        `json:"protein_g"`
	CarbsG     *float64        `json:"carbs_g"`
	FatG       *float64        `json:"fat_g"`
	FiberG     *float64        `json:"fiber_g"`
	SugarG     *float64        `json:"sugar_g"`
	SodiumMg   *float64        `json:"sodium_mg"`
	Nutrients  json.RawMessage `json:"nutrients"`
	ExternalID *string         `json:"external_id"`
	Metadata   json.RawMessage `json:"metadata"`
	Provenance
}

const nutritionCols = `id, start_date, end_date, meal_type, energy_kcal, protein_g, carbs_g, fat_g, fiber_g, sugar_g,
	sodium_mg, coalesce(nutrients_json, 'null'::jsonb), external_id, metadata,
	provider, source_slug, device_id, ingested_at`

func (x *NutritionLog) dest() []any {
	return []any{&x.ID, &x.StartDate, &x.EndDate, &x.MealType, &x.EnergyKcal, &x.ProteinG, &x.CarbsG,
		&x.FatG, &x.FiberG, &x.SugarG, &x.SodiumMg, &x.Nutrients, &x.ExternalID, &x.Metadata,
		&x.Provider, &x.Source, &x.Device, &x.IngestedAt}
}

func (x *NutritionLog) utc() {
	utc(&x.StartDate)
	utc(x.EndDate)
	utc(&x.IngestedAt)
}

// Nutrition lists nutrition logs.
func Nutrition(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[NutritionLog], error) {
	n, err := q.normalize(false)
	if err != nil {
		return Page[NutritionLog]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[NutritionLog]{}, err
	}
	l := list{table: "ohs_data.nutrition_logs", timeCol: "start_date", cols: nutritionCols}
	return run(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (NutritionLog, time.Time, int64, error) {
		var x NutritionLog
		err := r.Scan(x.dest()...)
		x.utc()
		return x, x.StartDate, x.ID, err
	})
}

// DailySummary is one provider's figures for one local calendar day.
type DailySummary struct {
	ID             int64           `json:"id"`
	Date           string          `json:"date"` // YYYY-MM-DD, the user's local day
	ReadinessScore *float64        `json:"readiness_score"`
	ActivityScore  *float64        `json:"activity_score"`
	SleepScore     *float64        `json:"sleep_score"`
	RecoveryScore  *float64        `json:"recovery_score"`
	StrainScore    *float64        `json:"strain_score"`
	StepCount      *int            `json:"step_count"`
	ActiveKcal     *float64        `json:"active_kcal"`
	TotalKcal      *float64        `json:"total_kcal"`
	DistanceM      *float64        `json:"distance_m"`
	FloorsClimbed  *int            `json:"floors_climbed"`
	RestingHR      *float64        `json:"resting_hr"`
	HRVAvgMs       *float64        `json:"hrv_avg_ms"`
	Metadata       json.RawMessage `json:"metadata"`
	// Sources names, in the merged view, the source each figure comes from
	// (keyed by the figure's JSON name). Absent in the all view, where the
	// row is one source's.
	Sources map[string]string `json:"sources,omitempty"`
	Provenance
}

// Daily lists daily summaries. Start and End compare as calendar dates. The
// merged view (the default) has one summary per day, each figure from the
// best ranked source that has it; the all view has one per source per day.
func Daily(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[DailySummary], error) {
	n, err := q.normalize(false)
	if err != nil {
		return Page[DailySummary]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[DailySummary]{}, err
	}
	if n.merged() {
		p, err := policy(ctx, db, tenantID, userID, n.Query)
		if err != nil {
			return Page[DailySummary]{}, err
		}
		return dailyMerged(ctx, db, tenantID, userID, n, p)
	}
	l := list{table: "ohs_data.daily_summaries", timeCol: "summary_date", date: true, cols: dailyCols}
	return run(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (DailySummary, time.Time, int64, error) {
		var d DailySummary
		var day time.Time
		err := r.Scan(d.dest(&day)...)
		day = d.finish(day)
		return d, day, d.ID, err
	})
}

// dailyCols are the columns of a DailySummary, in the order of its dest.
const dailyCols = `id, summary_date, readiness_score, activity_score, sleep_score, recovery_score, strain_score,
	step_count, active_kcal, total_kcal, distance_m, floors_climbed, resting_hr, hrv_avg_ms, metadata,
	provider, source_slug, NULL::text, ingested_at`

// dest scans the date into day; finish sets Date from it.
func (d *DailySummary) dest(day *time.Time) []any {
	return []any{&d.ID, day, &d.ReadinessScore, &d.ActivityScore, &d.SleepScore, &d.RecoveryScore,
		&d.StrainScore, &d.StepCount, &d.ActiveKcal, &d.TotalKcal, &d.DistanceM, &d.FloorsClimbed,
		&d.RestingHR, &d.HRVAvgMs, &d.Metadata, &d.Provider, &d.Source, &d.Device, &d.IngestedAt}
}

// finish sets Date from the scanned day and returns the day at midnight UTC.
func (d *DailySummary) finish(day time.Time) time.Time {
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	d.Date = day.Format(time.DateOnly)
	utc(&d.IngestedAt)
	return day
}

// CoverageQuery bounds a coverage report: [Start, End) as calendar dates,
// zero is open; Provider filters when set.
type CoverageQuery struct {
	Provider   string
	Start, End time.Time
}

// CoverageDay counts the records one provider landed for one local day.
type CoverageDay struct {
	Provider     string           `json:"provider"`
	Date         string           `json:"date"`
	Records      int64            `json:"records"`
	Metrics      map[string]int64 `json:"metrics"`
	LastSyncedAt time.Time        `json:"last_synced_at"`
}

// Coverage is what the ETL has landed for a user and what it still has to do.
type Coverage struct {
	// PendingJobs counts the user's work not finished yet: ETL jobs and
	// provider pulls (backfill, scheduled or notified) pending or running.
	// Zero means every accepted upload and every queued pull has been
	// processed.
	PendingJobs int64 `json:"pending_jobs"`
	// ParkedJobs counts ETL jobs that exhausted their attempts.
	ParkedJobs int64 `json:"parked_jobs"`
	// RejectedRecords counts the user's lines the ETL could not store (a bad
	// value, a missing field, an unknown unit) and has not recovered since,
	// each line once; an object that failed as a whole counts as one.
	// A job can finish with some of its lines rejected: pending_jobs 0 does
	// not mean every line landed.
	RejectedRecords int64         `json:"rejected_records"`
	Days            []CoverageDay `json:"days"`
	// Truncated is true when more than MaxCoverageDays rows matched: Days
	// holds the earliest ones.
	Truncated bool `json:"truncated"`
}

// MaxCoverageDays bounds one coverage report; past it the report is
// truncated (Coverage.Truncated) and the caller narrows start and end. A
// variable so tests can lower it.
var MaxCoverageDays = 10000

// UserCoverage reports per provider per local day record counts (from
// ohs_sync.pull_coverage), the user's unfinished ETL jobs and pulls, and the
// lines the ETL rejected.
func UserCoverage(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q CoverageQuery) (Coverage, error) {
	if !q.Start.IsZero() && !q.End.IsZero() && q.Start.After(q.End) {
		return Coverage{}, &QueryError{Msg: "start must not be after end"}
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Coverage{}, err
	}
	c := Coverage{Days: []CoverageDay{}}
	// A pull lands its data through ETL jobs it queues when it ends, so a
	// connection's backfill counts as pending until it is pulled. Parked pulls
	// are not counted: a pull is parked when its connection is disconnected
	// or needs reauthorization, which freshness reports.
	if err := db.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status IN ('pending', 'running')) + (SELECT count(*) FROM ohs_sync.pull_jobs p
				WHERE p.tenant_id = $1 AND p.user_id = $2 AND p.status IN ('pending', 'running')),
			count(*) FILTER (WHERE status = 'parked')
		FROM ohs_sync.etl_jobs WHERE tenant_id = $1 AND user_id = $2`, tenantID, userID).
		Scan(&c.PendingJobs, &c.ParkedJobs); err != nil {
		return Coverage{}, fmt.Errorf("read: etl jobs: %w", err)
	}
	// One per rejected line (raw_key, line_number): a replay of an object can
	// leave several rows for one line. A job-level row (no line number)
	// stands for a whole object and counts only when that object has no line
	// rows of its own.
	if err := db.QueryRow(ctx, `SELECT count(*) FROM (
			SELECT DISTINCT f.raw_key, f.line_number FROM ohs_data.failed_records f
			WHERE f.tenant_id = $1 AND f.user_id = $2 AND f.status <> 'recovered'
			  AND (f.line_number IS NOT NULL OR NOT EXISTS (
				SELECT 1 FROM ohs_data.failed_records l
				WHERE l.tenant_id = f.tenant_id AND l.user_id = f.user_id AND l.raw_key = f.raw_key
				  AND l.line_number IS NOT NULL AND l.status <> 'recovered'))
		) rejected`, tenantID, userID).
		Scan(&c.RejectedRecords); err != nil {
		return Coverage{}, fmt.Errorf("read: failed records: %w", err)
	}

	args := []any{tenantID, userID}
	where := "tenant_id = $1 AND user_id = $2"
	if q.Provider != "" {
		args = append(args, q.Provider)
		where += " AND provider = $" + strconv.Itoa(len(args))
	}
	if !q.Start.IsZero() {
		args = append(args, q.Start.UTC().Format(time.DateOnly))
		where += " AND date >= $" + strconv.Itoa(len(args)) + "::date"
	}
	if !q.End.IsZero() {
		args = append(args, q.End.UTC().Format(time.DateOnly))
		where += " AND date < $" + strconv.Itoa(len(args)) + "::date"
	}
	rows, err := db.Query(ctx, `SELECT provider, date, sum(records_count)::bigint,
			jsonb_object_agg(metric, records_count), max(last_synced_at)
		FROM ohs_sync.pull_coverage WHERE `+where+`
		GROUP BY provider, date ORDER BY date, provider LIMIT `+strconv.Itoa(MaxCoverageDays+1), args...)
	if err != nil {
		return Coverage{}, fmt.Errorf("read: coverage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if len(c.Days) == MaxCoverageDays {
			c.Truncated = true
			break
		}
		var d CoverageDay
		var day time.Time
		if err := rows.Scan(&d.Provider, &day, &d.Records, &d.Metrics, &d.LastSyncedAt); err != nil {
			return Coverage{}, fmt.Errorf("read: coverage: scan: %w", err)
		}
		d.Date = day.Format(time.DateOnly)
		utc(&d.LastSyncedAt)
		c.Days = append(c.Days, d)
	}
	if err := rows.Err(); err != nil {
		return Coverage{}, fmt.Errorf("read: coverage: %w", err)
	}
	return c, nil
}
