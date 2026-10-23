// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"container/heap"
	"context"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/fusion"
)

// streamBatch is how many rows EachSample reads per statement.
const streamBatch = 5000

// EachSample calls fn with every quantity sample of q (every type, or q.Type),
// by start date then id, in batches of at most MaxLimit: the records AllSamples
// returns page by page, for the exports. The merged view is decided in one
// pass over the stored rows instead of one lookup per record: on second by
// second data, where every lookup scanned hundreds of rows of the streams
// around it, a page of 1,000 merged samples took seconds. q takes no cursor
// or limit.
//
// The pass applies the rule of the SQL view (family.covered) to the same
// rows: a record is left out when a better ranked stream of its metric has a
// record that matches it. The one difference is a record longer than its
// metric's longest span, which the paged read compares only with records
// starting before the end of its page's window plus that span: the pass
// compares it with every record it reaches.
func EachSample(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query, fn func([]Sample) error) error {
	if q.Cursor != "" || q.Limit != 0 {
		return &QueryError{Msg: "a stream of every sample takes no cursor or limit"}
	}
	q.Limit = MaxLimit
	n, err := q.normalize(false)
	if err != nil {
		return err
	}
	if !n.merged() {
		for {
			pg, err := AllSamples(ctx, db, tenantID, userID, q)
			if err != nil {
				return err
			}
			if err := fn(pg.Records); err != nil || pg.NextCursor == "" {
				return err
			}
			q.Cursor = pg.NextCursor
		}
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return err
	}
	if n.Type != "" {
		if err := knownQuantityType(ctx, db, tenantID, userID, n.Type); err != nil {
			return err
		}
	}
	p, err := policy(ctx, db, tenantID, userID, n.Query)
	if err != nil {
		return err
	}
	s := newSweep(p, n, fn)
	if err := s.catalog(ctx, db, tenantID, userID); err != nil {
		return err
	}
	return s.run(ctx, db, tenantID, userID)
}

// sweepRule is the rule and order of sources of one metric, as the SQL view
// resolves them: for a read over several metrics (mergeParams.ruleBy and
// orderBy), a metric outside the policy's metrics takes the strict rule and
// the '*' order.
type sweepRule struct {
	tol, span int64 // microseconds
	share     *big.Rat
	order     []string
}

// sweepClass is the records of one metric that rank alike: one source (its
// rank), one wear and one stream key. Records of one class never cover each
// other.
type sweepClass struct {
	metric string
	rank   int
	wear   int
	key    string
	// top is true when no class of the metric ranks before this one.
	top bool
	// hist holds the starts and ends of the class's records, by start,
	// from first on; maxLen is the longest of them.
	hist   []sweepSpan
	first  int
	maxLen int64
}

type sweepSpan struct{ start, end int64 }

// sweepItem is one record waiting for its verdict or its turn.
type sweepItem struct {
	s          Sample
	cls        *sweepClass
	start, end int64
	// deadline is the latest start of a record that can still cover it.
	deadline         int64
	decided, covered bool
}

type sweep struct {
	p        fusion.Policy
	q        normalized
	fn       func([]Sample) error
	metrics  map[string]bool
	reach    time.Duration
	rules    map[string]*sweepRule
	classes  map[string][]*sweepClass // by metric
	keys     map[string]int           // stream key -> position in the database's order
	pending  []*sweepItem             // records in range, by start then id, from the first not sent
	open     map[string][]*sweepItem  // undecided records, by metric
	deadline deadlines
	out      []Sample
}

func newSweep(p fusion.Policy, q normalized, fn func([]Sample) error) *sweep {
	mp := bindMerge(p, nil, func(any) string { return "" })
	metrics := make(map[string]bool, len(mp.metrics))
	for _, m := range mp.metrics {
		metrics[m] = true
	}
	return &sweep{
		p: p, q: q, fn: fn, metrics: metrics, reach: mp.reach(q.Type),
		rules: map[string]*sweepRule{}, classes: map[string][]*sweepClass{}, keys: map[string]int{},
		open: map[string][]*sweepItem{},
	}
}

func (s *sweep) rule(metric string) *sweepRule {
	if r, ok := s.rules[metric]; ok {
		return r
	}
	// A read of one type inlines that type's rule and order.
	m, orderOf := metric, metric
	if !s.metrics[metric] && s.q.Type == "" {
		m, orderOf = "", fusion.All
	}
	fr := fusion.RuleFor(m)
	r := &sweepRule{tol: fr.Tolerance.Microseconds(), span: fr.MaxSpan.Microseconds(), order: s.p.Order(orderOf)}
	if fr.MinShare != 0 {
		// The literal the SQL view compares with, as an exact fraction.
		r.share, _ = new(big.Rat).SetString(strconv.FormatFloat(fr.MinShare, 'f', -1, 64))
	}
	s.rules[metric] = r
	return r
}

// streamCols are the wear and the stream key of a sample (alias r), as the
// SQL view ranks them.
var streamCols = quantityFamily.wear("r") + ", r.provider || '|' || " + quantityFamily.stream("r")

// bounds are the conditions on the rows the pass reads: those of q's range
// and those within reach of it, as candidates to cover a record in range.
func (s *sweep) bounds(arg func(any) string) []string {
	where := []string{"tenant_id = $1", "user_id = $2"}
	if s.q.Type != "" {
		where = append(where, "quantity_type = "+arg(s.q.Type))
	}
	if !s.q.Start.IsZero() {
		where = append(where, "start_date >= "+arg(s.q.Start.Add(-s.reach)))
	}
	if !s.q.End.IsZero() {
		where = append(where, "start_date <= "+arg(s.q.End.Add(s.reach)))
	}
	return where
}

// catalog reads the classes of the rows the pass will read, so a record of a
// class that nothing ranks before is sent without waiting for its deadline.
func (s *sweep) catalog(ctx context.Context, db Querier, tenantID, userID uuid.UUID) error {
	args := []any{tenantID, userID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	sql := "SELECT DISTINCT quantity_type, coalesce(source_slug, provider), " + streamCols +
		" FROM ohs_data.quantity_samples r WHERE " + strings.Join(s.bounds(arg), " AND ")
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("read: samples: classes: %w", err)
	}
	type found struct {
		metric, slug, key string
		wear              int
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (found, error) {
		var f found
		err := r.Scan(&f.metric, &f.slug, &f.wear, &f.key)
		return f, err
	})
	if err != nil {
		return fmt.Errorf("read: samples: classes: %w", err)
	}
	var keys []string
	for _, f := range all {
		keys = append(keys, f.key)
	}
	if err := s.sortKeys(ctx, db, keys); err != nil {
		return err
	}
	for _, f := range all {
		s.class(f.metric, f.slug, f.wear, f.key, false)
	}
	for m := range s.classes {
		s.markTop(m)
	}
	return nil
}

// sortKeys places keys, with the ones known, in the database's order of
// text: the SQL view compares stream keys with its collation.
func (s *sweep) sortKeys(ctx context.Context, db Querier, keys []string) error {
	fresh := false
	for _, k := range keys {
		if _, ok := s.keys[k]; !ok {
			s.keys[k], fresh = -1, true
		}
	}
	if !fresh {
		return nil
	}
	all := make([]string, 0, len(s.keys))
	for k := range s.keys {
		all = append(all, k)
	}
	var sorted []string
	if err := db.QueryRow(ctx, "SELECT array_agg(k ORDER BY k) FROM unnest($1::text[]) k", all).Scan(&sorted); err != nil {
		return fmt.Errorf("read: samples: stream order: %w", err)
	}
	for i, k := range sorted {
		s.keys[k] = i
	}
	return nil
}

// class returns the class of a record, made when new; a class made late,
// while reading (a record stored after the catalog was read), is never top.
func (s *sweep) class(metric, slug string, wear int, key string, late bool) *sweepClass {
	rank := s.rankOf(metric, slug)
	for _, c := range s.classes[metric] {
		if c.wear == wear && c.key == key && c.rank == rank {
			return c
		}
	}
	c := &sweepClass{metric: metric, rank: rank, wear: wear, key: key}
	s.classes[metric] = append(s.classes[metric], c)
	if late {
		s.markTop(metric)
		c.top = false
	}
	return c
}

func (s *sweep) rankOf(metric, slug string) int {
	if i := slices.Index(s.rule(metric).order, slug); i >= 0 {
		return i + 1
	}
	return 1000000
}

// before reports whether class b ranks before class a (both of one metric):
// by source, then worn on the body first, then by stream key.
func (s *sweep) before(b, a *sweepClass) bool {
	if b.rank != a.rank {
		return b.rank < a.rank
	}
	if b.wear != a.wear {
		return b.wear < a.wear
	}
	return s.keys[b.key] < s.keys[a.key]
}

func (s *sweep) markTop(metric string) {
	cs := s.classes[metric]
	for _, a := range cs {
		a.top = true
		for _, b := range cs {
			if b != a && s.before(b, a) {
				a.top = false
				break
			}
		}
	}
}

// matches reports whether b, of a better ranked class, covers a: the
// conditions of family.covered, in microseconds.
func (s *sweep) matches(a *sweepItem, bStart, bEnd int64) bool {
	r := s.rule(a.cls.metric)
	aS, aE := a.start, a.end
	if bStart < aS-r.span-r.tol || bStart > aE+r.tol || bEnd < aS-r.tol {
		return false
	}
	if r.tol == 0 && aS < aE && bStart < bEnd && (bStart == aE || bEnd == aS) {
		return false
	}
	if r.share != nil {
		shared := min(aE, bEnd) - max(aS, bStart)
		shortest := min(aE-aS, bEnd-bStart)
		need := new(big.Rat).Mul(r.share, new(big.Rat).SetInt64(shortest))
		if new(big.Rat).SetInt64(shared).Cmp(need) < 0 {
			return false
		}
	}
	return true
}

// coveredByPast reports whether a record read before a covers it.
func (s *sweep) coveredByPast(a *sweepItem) bool {
	r := s.rule(a.cls.metric)
	for _, c := range s.classes[a.cls.metric] {
		if c == a.cls || !s.before(c, a.cls) {
			continue
		}
		// A record of c reaching a starts at or after this.
		from := max(a.start-r.span-r.tol, a.start-r.tol-c.maxLen)
		h := c.hist[c.first:]
		i := sort.Search(len(h), func(i int) bool { return h[i].start >= from })
		for ; i < len(h); i++ {
			if s.matches(a, h[i].start, h[i].end) {
				return true
			}
		}
	}
	return false
}

func (s *sweep) run(ctx context.Context, db Querier, tenantID, userID uuid.UUID) error {
	var afterT time.Time
	var afterID int64
	paged := false
	for {
		args := []any{tenantID, userID}
		arg := func(v any) string {
			args = append(args, v)
			return "$" + strconv.Itoa(len(args))
		}
		where := s.bounds(arg)
		if paged {
			after := arg(afterT)
			where = append(where, "start_date >= "+after, "(start_date, id) > ("+after+", "+arg(afterID)+")")
		}
		sql := "SELECT " + sampleCols + ", " + streamCols + " FROM ohs_data.quantity_samples r WHERE " +
			strings.Join(where, " AND ") + " ORDER BY start_date, id LIMIT " + arg(streamBatch)
		rows, err := db.Query(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("read: samples: %w", err)
		}
		type row struct {
			s    Sample
			wear int
			key  string
		}
		batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			err := r.Scan(append(x.s.dest(), &x.wear, &x.key)...)
			x.s.utc()
			return x, err
		})
		if err != nil {
			return fmt.Errorf("read: samples: %w", err)
		}
		keys := make([]string, len(batch))
		for i, x := range batch {
			keys[i] = x.key
		}
		if err := s.sortKeys(ctx, db, keys); err != nil {
			return err
		}
		for _, x := range batch {
			slug := x.s.Provider
			if x.s.Source != nil {
				slug = *x.s.Source
			}
			cls := s.class(x.s.Type, slug, x.wear, x.key, true)
			if err := s.add(x.s, cls); err != nil {
				return err
			}
		}
		if len(batch) < streamBatch {
			break
		}
		last := batch[len(batch)-1].s
		afterT, afterID, paged = last.StartDate, last.ID, true
	}
	for _, it := range s.pending {
		it.decided = true
	}
	if err := s.flush(); err != nil {
		return err
	}
	return s.fn(s.out)
}

// add takes the next row, by start then id.
func (s *sweep) add(rec Sample, cls *sweepClass) error {
	start := rec.StartDate.UnixMicro()
	end := start
	if rec.EndDate != nil {
		end = rec.EndDate.UnixMicro()
	}
	r := s.rule(cls.metric)
	// No record from here on starts early enough to cover these.
	for len(s.deadline) > 0 && s.deadline[0].deadline < start {
		it := heap.Pop(&s.deadline).(*sweepItem)
		it.decided = true
	}
	// The row as a better ranked record of the ones waiting.
	open := s.open[cls.metric][:0]
	for _, a := range s.open[cls.metric] {
		if a.decided {
			continue
		}
		if a.cls != cls && s.before(cls, a.cls) && s.matches(a, start, end) {
			a.decided, a.covered = true, true
			continue
		}
		open = append(open, a)
	}
	s.open[cls.metric] = open

	inRange := (s.q.Start.IsZero() || !rec.StartDate.Before(s.q.Start)) && (s.q.End.IsZero() || rec.StartDate.Before(s.q.End))
	if inRange {
		it := &sweepItem{s: rec, cls: cls, start: start, end: end, deadline: end + r.tol}
		switch {
		case cls.top:
			it.decided = true
		case s.coveredByPast(it):
			it.decided, it.covered = true, true
		default:
			s.open[cls.metric] = append(s.open[cls.metric], it)
			heap.Push(&s.deadline, it)
		}
		s.pending = append(s.pending, it)
	}

	cls.hist = append(cls.hist, sweepSpan{start, end})
	cls.maxLen = max(cls.maxLen, end-start)
	// Records starting before this can cover none of the records to come.
	if keep := start - r.span - r.tol; cls.first < len(cls.hist) && cls.hist[cls.first].start < keep {
		for cls.first < len(cls.hist) && cls.hist[cls.first].start < keep {
			cls.first++
		}
		if cls.first > len(cls.hist)/2 {
			cls.hist = append(cls.hist[:0], cls.hist[cls.first:]...)
			cls.first = 0
		}
	}
	return s.flush()
}

// flush sends the records decided at the head of the queue, by batches of
// MaxLimit.
func (s *sweep) flush() error {
	i := 0
	for ; i < len(s.pending) && s.pending[i].decided; i++ {
		if !s.pending[i].covered {
			s.out = append(s.out, s.pending[i].s)
		}
		if len(s.out) == MaxLimit {
			if err := s.fn(s.out); err != nil {
				return err
			}
			s.out = make([]Sample, 0, MaxLimit)
		}
	}
	if i > 0 {
		clear(s.pending[:i])
		s.pending = s.pending[i:]
	}
	return nil
}

// deadlines is a min-heap of waiting records by deadline.
type deadlines []*sweepItem

func (d deadlines) Len() int           { return len(d) }
func (d deadlines) Less(i, j int) bool { return d[i].deadline < d[j].deadline }
func (d deadlines) Swap(i, j int)      { d[i], d[j] = d[j], d[i] }
func (d *deadlines) Push(x any)        { *d = append(*d, x.(*sweepItem)) }
func (d *deadlines) Pop() any {
	old := *d
	x := old[len(old)-1]
	old[len(old)-1] = nil
	*d = old[:len(old)-1]
	return x
}
