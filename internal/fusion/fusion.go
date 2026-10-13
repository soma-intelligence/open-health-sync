// SPDX-License-Identifier: AGPL-3.0-only

// Package fusion decides which source a merged read shows when several
// sources recorded the same thing: a ring and a watch both counting the night,
// a phone and a watch both counting steps, one sample arriving directly and
// again through an aggregator.
//
// Nothing here deletes or rewrites a stored row. Every source keeps its raw
// records; the order only ranks them, and the read layer drops a record from
// the merged view when a better ranked source covers the same time (see
// Rule).
//
// A source is a record's source_slug, the vendor that produced it (the
// provider when the record names none). The order of sources is resolved per
// metric, most specific first: the user's order for the metric, for its group,
// for every metric ('*'), then the built-in default of the group. A source
// missing from the order ranks after every listed one.
package fusion

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// All is the key of an order that applies to every metric.
const All = "*"

// Groups of metrics. A user can order sources for a whole group at once.
const (
	GroupActivity = "activity"
	GroupHeart    = "heart"
	GroupRecovery = "recovery"
	GroupBody     = "body"
	GroupSleep    = "sleep"
	GroupWorkouts = "workouts"
)

// Keys of the record families that are not quantity types.
const (
	// MetricSleep ranks sleep sessions.
	MetricSleep = "sleep"
	// MetricWorkouts ranks workouts.
	MetricWorkouts = "workouts"
)

// groupOf maps a metric to its group. A metric outside it belongs to no
// group and falls back to the '*' order.
var groupOf = map[string]string{
	model.QTStepCount:              GroupActivity,
	model.QTActiveEnergyBurned:     GroupActivity,
	model.QTBasalEnergyBurned:      GroupActivity,
	model.QTDistanceWalkingRunning: GroupActivity,
	"distance_cycling":             GroupActivity,
	model.QTFlightsClimbed:         GroupActivity,
	model.QTStepCountIntraday:      GroupActivity,
	model.QTActiveEnergyIntraday:   GroupActivity,
	model.QTDistanceWalkRunIntday:  GroupActivity,
	"apple_exercise_time":          GroupActivity,
	"apple_move_time":              GroupActivity,
	"apple_stand_time":             GroupActivity,

	model.QTHeartRate:            GroupHeart,
	model.QTOxygenSaturation:     GroupHeart,
	model.QTRespiratoryRate:      GroupHeart,
	"walking_heart_rate_average": GroupHeart,
	"blood_pressure_systolic":    GroupHeart,
	"blood_pressure_diastolic":   GroupHeart,
	"vo2_max":                    GroupHeart,

	model.QTRestingHeartRate:         GroupRecovery,
	model.QTHRVSDNN:                  GroupRecovery,
	model.QTHRVRMSSD:                 GroupRecovery,
	model.QTSkinTemperature:          GroupRecovery,
	model.QTBodyTemperatureDeviation: GroupRecovery,
	model.QTReadinessScore:           GroupRecovery,
	model.QTReadinessHRVBalance:      GroupRecovery,
	model.QTSleepScore:               GroupRecovery,
	model.QTStrain:                   GroupRecovery,
	"recovery_score":                 GroupRecovery,
	"activity_score":                 GroupRecovery,

	model.QTBodyTemperature: GroupBody,
	"body_mass":             GroupBody,
	"body_mass_index":       GroupBody,
	"body_fat_percentage":   GroupBody,
	"body_fat_mass":         GroupBody,
	"lean_body_mass":        GroupBody,
	"muscle_mass":           GroupBody,
	"bone_mass":             GroupBody,
	"water_mass":            GroupBody,
	"visceral_fat_rating":   GroupBody,
	"height":                GroupBody,
	"pulse_wave_velocity":   GroupBody,
	"vascular_age":          GroupBody,
	"blood_glucose":         GroupBody,

	MetricSleep:      GroupSleep,
	"sleep_analysis": GroupSleep,

	MetricWorkouts:        GroupWorkouts,
	model.QTWorkoutStrain: GroupWorkouts,
}

// Group returns the group of metric, or "" when it has none.
func Group(metric string) string { return groupOf[metric] }

// Groups lists the group names.
func Groups() []string {
	return []string{GroupActivity, GroupHeart, GroupRecovery, GroupBody, GroupSleep, GroupWorkouts}
}

// Defaults is the built-in order of each group and of '*'. The order is a
// starting point, not a verdict on any vendor: a device worn all day counts
// steps and daytime heart rate better than one worn at night, and the
// reverse for sleep and overnight recovery figures.
func Defaults() map[string][]string {
	return map[string][]string{
		All:           {"apple_health", "oura", "whoop", "polar", "withings", "strava"},
		GroupActivity: {"apple_health", "polar", "withings", "whoop", "oura", "strava"},
		GroupHeart:    {"apple_health", "polar", "whoop", "oura", "withings"},
		GroupRecovery: {"oura", "whoop", "polar", "apple_health", "withings"},
		GroupBody:     {"withings", "apple_health", "oura", "whoop", "polar"},
		GroupSleep:    {"oura", "whoop", "withings", "polar", "apple_health"},
		GroupWorkouts: {"polar", "strava", "apple_health", "whoop", "oura", "withings"},
	}
}

// Priorities is a set of source orders keyed by metric, group or '*'.
type Priorities map[string][]string

// MaxSources bounds one order.
const MaxSources = 50

// ValidKey reports whether key may carry an order: '*', a group, a metric of
// a group, or any other snake_case metric name (a connector may emit a
// metric no group lists yet).
func ValidKey(key string) bool {
	if key == All || slices.Contains(Groups(), key) {
		return true
	}
	if key == "" || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// Normalize checks p and returns it with every source slug normalized. The
// error message is fit for a 400.
func (p Priorities) Normalize() (Priorities, error) {
	out := make(Priorities, len(p))
	for key, sources := range p {
		if !ValidKey(key) {
			return nil, fmt.Errorf("%q is not '*', a group (%s) or a snake_case metric name", key, strings.Join(Groups(), ", "))
		}
		if len(sources) == 0 || len(sources) > MaxSources {
			return nil, fmt.Errorf("the order of %q must list between 1 and %d sources", key, MaxSources)
		}
		seen := make(map[string]bool, len(sources))
		list := make([]string, 0, len(sources))
		for _, s := range sources {
			slug := model.Slug(s)
			if slug == "" {
				return nil, fmt.Errorf("the order of %q has an empty source", key)
			}
			if seen[slug] {
				return nil, fmt.Errorf("the order of %q lists %q twice", key, slug)
			}
			seen[slug] = true
			list = append(list, slug)
		}
		out[key] = list
	}
	return out, nil
}

// Scorer is the hook for a quality layer that measures how much each source
// of a user can be trusted, per metric. Scores rank the sources of a metric
// the user has not ordered (higher first), ahead of the built-in default; an
// order the user set always wins. Nothing in this repository implements it.
type Scorer interface {
	// SourceScores returns metric -> source slug -> score.
	SourceScores(ctx context.Context, tenantID, userID uuid.UUID) (map[string]map[string]float64, error)
}

// Policy is the resolved ranking of one user's sources.
type Policy struct {
	user     Priorities
	defaults Priorities
	scores   map[string]map[string]float64
}

// NewPolicy builds a policy from a user's orders and optional scores.
func NewPolicy(user Priorities, scores map[string]map[string]float64) Policy {
	return Policy{user: user, defaults: Defaults(), scores: scores}
}

// Order returns the sources of metric, best first.
func (p Policy) Order(metric string) []string {
	g := Group(metric)
	for _, k := range []string{metric, g, All} {
		if k == "" {
			continue
		}
		if o, ok := p.user[k]; ok {
			return o
		}
	}
	base, ok := p.defaults[g]
	if !ok {
		base = p.defaults[All]
	}
	sc := p.scores[metric]
	if len(sc) == 0 {
		return base
	}
	// Scored sources first, best score first; then the rest in default order.
	order := slices.Clone(base)
	for s := range sc {
		if !slices.Contains(order, s) {
			order = append(order, s)
		}
	}
	pos := make(map[string]int, len(order))
	for i, s := range order {
		pos[s] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		si, iok := sc[order[i]]
		sj, jok := sc[order[j]]
		switch {
		case iok && jok:
			return si > sj
		case iok != jok:
			return iok
		}
		return pos[order[i]] < pos[order[j]]
	})
	return order
}

// Keys lists the keys the user set an order for.
func (p Policy) Keys() []string {
	out := make([]string, 0, len(p.user))
	for k := range p.user {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// KnownMetrics lists every metric with a group, for Ranks.
func KnownMetrics() []string {
	out := make([]string, 0, len(groupOf))
	for m := range groupOf {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Querier is what Load reads with (a pool or a transaction).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// LoadPriorities reads the orders a user set.
func LoadPriorities(ctx context.Context, db Querier, tenantID, userID uuid.UUID) (Priorities, error) {
	rows, err := db.Query(ctx, `SELECT metric, sources FROM ohs_sync.source_priorities
		WHERE tenant_id = $1 AND user_id = $2`, tenantID, userID)
	if err != nil {
		return nil, fmt.Errorf("fusion: load priorities: %w", err)
	}
	defer rows.Close()
	out := Priorities{}
	for rows.Next() {
		var k string
		var s []string
		if err := rows.Scan(&k, &s); err != nil {
			return nil, fmt.Errorf("fusion: load priorities: %w", err)
		}
		out[k] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fusion: load priorities: %w", err)
	}
	return out, nil
}

// ErrScores wraps a scorer's failure: Load still returns a usable policy.
var ErrScores = errors.New("fusion: source scores")

// Load resolves a user's policy: their orders, and the scorer's scores when
// scorer is not nil. When only the scorer fails, Load returns the policy
// without scores and an error wrapping ErrScores.
func Load(ctx context.Context, db Querier, tenantID, userID uuid.UUID, scorer Scorer) (Policy, error) {
	user, err := LoadPriorities(ctx, db, tenantID, userID)
	if err != nil {
		return Policy{}, err
	}
	var scores map[string]map[string]float64
	var scoreErr error
	if scorer != nil {
		if scores, scoreErr = scorer.SourceScores(ctx, tenantID, userID); scoreErr != nil {
			scores, scoreErr = nil, fmt.Errorf("%w: %w", ErrScores, scoreErr)
		}
	}
	return NewPolicy(user, scores), scoreErr
}

// Beginner is what SavePriorities writes with (a pool).
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// SavePriorities replaces every order of a user with p (normalized by the
// caller). An empty p goes back to the defaults.
func SavePriorities(ctx context.Context, db Beginner, tenantID, userID uuid.UUID, p Priorities) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("fusion: save priorities: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `DELETE FROM ohs_sync.source_priorities WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID); err != nil {
		return fmt.Errorf("fusion: save priorities: %w", err)
	}
	for k, s := range p {
		if _, err := tx.Exec(ctx, `INSERT INTO ohs_sync.source_priorities (tenant_id, user_id, metric, sources)
			VALUES ($1, $2, $3, $4)`, tenantID, userID, k, s); err != nil {
			return fmt.Errorf("fusion: save priorities: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("fusion: save priorities: %w", err)
	}
	return nil
}

// Rule says when two records of one metric from different sources are the
// same thing. They are when the gap between their intervals [start, end]
// (end is start for a point) is under Tolerance, or, with no tolerance, when
// the intervals overlap; an identical interval always matches. With MinShare
// above 0 the shared time must also be at least that share of the shorter
// record. A record is left out of the merged view when a better ranked source
// has a matching record.
type Rule struct {
	// Tolerance is the largest gap between two matching records: a point
	// reading covers the time around it.
	Tolerance time.Duration
	// MinShare, when above 0, asks the shared time to be at least this share
	// of the shorter of the two records (sleep sessions, workouts).
	MinShare float64
	// MaxSpan bounds how long a record of the metric can be, so the lookup of
	// overlapping records stays an index range.
	MaxSpan time.Duration
}

// The rules by kind of metric.
var (
	// Cumulative counts (steps, energy, distance): any overlap of the
	// intervals is the same activity counted twice. A day total spans a day.
	cumulativeRule = Rule{MaxSpan: 25 * time.Hour}
	// Readings taken often (heart rate, SpO2): a reading of a better source
	// within 5 minutes covers it.
	readingRule = Rule{Tolerance: 5 * time.Minute, MaxSpan: 15 * time.Minute}
	// One figure per night or day (resting heart rate, HRV, scores): a figure
	// of a better source within 12 hours covers it.
	dailyFigureRule = Rule{Tolerance: 12 * time.Hour, MaxSpan: 25 * time.Hour}
	// Body measurements (weight, fat): within an hour.
	bodyRule = Rule{Tolerance: time.Hour, MaxSpan: 25 * time.Hour}
	// Sessions (sleep, workouts): the same session when they share at least
	// half of the shorter one, so a nap beside the night stays.
	sessionRule = Rule{MinShare: 0.5, MaxSpan: 36 * time.Hour}
	// Anything else: only overlapping intervals.
	strictRule = Rule{MaxSpan: 25 * time.Hour}
)

// RuleFor returns the overlap rule of metric; a metric of no known kind gets
// the strict rule.
func RuleFor(metric string) Rule {
	switch metric {
	case MetricSleep, MetricWorkouts, "sleep_analysis":
		return sessionRule
	case model.QTHeartRate, model.QTOxygenSaturation, model.QTRespiratoryRate,
		"blood_pressure_systolic", "blood_pressure_diastolic", "blood_glucose":
		return readingRule
	}
	switch Group(metric) {
	case GroupActivity:
		return cumulativeRule
	case GroupRecovery:
		return dailyFigureRule
	case GroupBody:
		return bodyRule
	case GroupHeart:
		return dailyFigureRule // vo2_max, walking heart rate average
	}
	return strictRule
}
