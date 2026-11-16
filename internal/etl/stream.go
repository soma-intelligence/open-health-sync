// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"hash/maphash"
	"slices"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/consent"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// Bounded memory for large objects.
//
// A raw object is one write transaction, and several steps of that write need
// the object as a whole: the deletions are applied before any record is
// written, the locks are taken up front in one order, a parent is resolved
// with its children, and of several copies of one Replaceable record the last
// one wins. Holding every record for that made the memory of a job grow with
// its object: about 2 KiB of heap per quantity sample, over 500 MiB for a
// 64 MiB part.
//
// Most of a large object does not need the whole: a quantity sample, a
// category sample, a heartbeat and an observation are each written on their
// own (the bulk records). So an object with more bulk records than one chunk
// (WriterConfig.ChunkRecords) is read twice:
//
//  1. The first read (objectPlan) maps and validates every line, as for any
//     object, but keeps only what the write needs as a whole: the deletions,
//     the parent and child families, the nutrition logs and the provider
//     summaries (the held records), plus a 16 byte mark or two per
//     Replaceable quantity sample, until the end of that read, to find the
//     ones that have another copy in the object.
//  2. The write transaction takes the locks and applies the deletions
//     (Writer.begin), then reads the object again and writes its bulk records
//     chunk by chunk (batchWrite.writeChunk), then writes the held records
//     and recomputes the hours and days the whole write touched
//     (batchWrite.finish).
//
// The result is the one a single batch gives. Each chunk goes through the
// steps of the whole batch (tombstones, conversion of clear rows, keyed
// hashes, dedup, aliases), and those steps treat each bulk record on its own:
// a duplicate in a later chunk meets the stored row of the earlier one and is
// counted skipped, as the in-batch dedup counted it. The one exception is a
// Replaceable quantity sample with another copy in the object (same identity,
// or same provider record under another start): which copy wins depends on
// the others, so those samples are left out of the chunks and written with
// the held records, in the order the object gave them.
//
// What still grows with the object, and why:
//
//   - the held records: a session needs its phases and a workout its route,
//     and the last copy of a Replaceable row wins;
//   - the hours and days touched: eight bytes an hour (hourSet) and one map
//     entry a day, recomputed once at the end from the stored rows;
//   - the diverted samples above: none in an object that names each record
//     once;
//   - coverage: one counter per metric and day.
//
// A mapper that gave the same content hash and start to a sample that is
// Replaceable and to one that is not would see a difference (the first to
// arrive would keep its columns): no connector does, since the two hash
// differently on purpose.

// lineMapper turns one raw line of a job's object into canonical records, the
// same way on every read of the object: the connector, then the consent,
// attribution, unit and validation choke points.
type lineMapper struct {
	conn        connector.Connector
	job         queue.ETLJob
	sc          scope
	dataType    string
	policy      consent.Policy
	validatedAt time.Time
}

// passState is what one read of an object counts besides its records.
type passState struct {
	details  map[string]*metricDetail
	suspects int
	withheld int
	// failures are the record failures kept for the write; failed counts
	// every one, kept or not, and recoverable says a code fix may rescue one
	// (anyRecoverable).
	failures    []FailedRecord
	failed      int
	recoverable bool
	keep        bool
}

func newPassState() *passState {
	return &passState{details: map[string]*metricDetail{}, keep: true}
}

// release stops keeping the failures: they are counted only.
func (st *passState) release() {
	st.keep, st.failures = false, nil
}

func (st *passState) fail(i int, record map[string]any, reason, msg string) {
	st.failed++
	if !permanentReason(reason) {
		st.recoverable = true
	}
	detail(st.details, metricTypeFromRecord(record)).failed++
	if st.keep {
		st.failures = append(st.failures, FailedRecord{LineNum: i + 1, RawPayload: record, Error: msg, ReasonCode: reason})
	}
}

// mapLine maps and validates record i of the object and returns what is left
// to write, nil when nothing is. Failures and counts go to st.
func (m *lineMapper) mapLine(st *passState, i int, record map[string]any) *model.MapResult {
	mr, mapErr := mapRecord(m.conn, connector.RawRecord{
		TenantID: m.job.TenantID.String(), UserID: m.job.UserID.String(), Provider: m.job.Provider,
		DataType: m.dataType, SourceKey: m.job.Key, Fields: record,
	})
	if mapErr != nil {
		st.fail(i, record, "mapping", fmt.Sprintf("mapping error: %v", mapErr))
		return nil
	}
	if mr.IsEmpty() {
		// The connector skipped the record on purpose (an unknown type, a
		// line without a value): nothing to store, and not a failure. The
		// connector counts and logs what it skips.
		return nil
	}
	// Text choke point: a NUL from the source fails the whole write.
	model.ScrubText(mr)
	stampOwner(mr, m.sc)
	if m.policy.Recorded || m.policy.Required {
		st.withheld += consent.Filter(mr, m.policy.Allows)
		if mr.IsEmpty() {
			return nil
		}
	}

	// Attribution choke point: which upstream produced each sample, not only
	// which integration delivered it.
	model.CanonicalizeSourceAttribution(mr, record, m.job.Provider)

	// Unit choke point, before validation, so the range checks can assume
	// the storage unit. An unconvertible unit is a recoverable failure of
	// its own sample: the record is kept for a replay, and the rest of
	// what it mapped to is written.
	if unitErrs := model.CanonicalizeUnits(mr); len(unitErrs) > 0 {
		st.fail(i, record, "unit", fmt.Sprintf("unit errors: %v", unitErrs))
		pruneUnitErrors(mr, unitErrs)
		if mr.IsEmpty() {
			return nil
		}
	}

	// A reject is a value the metric cannot physically take: parked, never
	// written. A suspect is implausible but possible: written and flagged.
	valErrors := ValidateMapResultAt(mr, m.validatedAt)
	if rejects := Rejections(valErrors); len(rejects) > 0 {
		// Scope the rejection to the offending entries: one record can map to
		// a whole day's series, and dropping all of it over one out-of-band
		// sample would erase the day.
		structural := pruneRejected(mr, rejects)
		st.fail(i, record, "validation", fmt.Sprintf("validation errors: %v", rejects))
		if len(structural) > 0 || mr.IsEmpty() {
			return nil
		}
		// The indices in valErrors point at the pre-prune slices.
		valErrors = ValidateMapResultAt(mr, m.validatedAt)
	}
	if s := Suspicions(valErrors); len(s) > 0 {
		FlagSuspects(mr, s)
		st.suspects += len(s)
		detail(st.details, metricTypeFromRecord(record)).suspect += len(s)
	}

	// Heartbeat series identity: the raw object's data type, unless the
	// connector named the series itself.
	if m.dataType != "" {
		for j := range mr.HeartbeatSamples {
			if mr.HeartbeatSamples[j].SeriesID == "" {
				mr.HeartbeatSamples[j].SeriesID = m.dataType
			}
		}
	}
	return mr
}

// bulkLen is the number of bulk records of mr: the records written on their
// own, which a large object writes chunk by chunk.
func bulkLen(mr *model.MapResult) int {
	return len(mr.QuantitySamples) + len(mr.CategorySamples) + len(mr.HeartbeatSamples) + len(mr.Observations)
}

// bulkCounts counts the bulk records one read of an object gave, so the
// second read can be checked against the first.
type bulkCounts struct {
	quantity, replaceable, category, heartbeat, observation int
}

// copyMark is one Replaceable quantity sample of an object: a digest of what
// makes it a copy of another, and its position among the object's Replaceable
// quantity samples.
type copyMark struct {
	digest uint64
	ord    uint32
}

// copySeed keys the digests of copyMark. Two samples with one digest are
// written with the held records, so a collision costs a little memory and
// changes nothing.
var copySeed = maphash.MakeSeed()

// objectPlan is what the first read of an object keeps for its write.
//
// Until the object proves larger than one chunk, acc holds every record and
// the object is written as one batch. From then on (spilled) acc holds the
// held records only, and the plan notes, for each Replaceable quantity
// sample, the two things that make it a copy of another (identity, moved
// record), from which seal finds the samples to divert.
type objectPlan struct {
	sc      scope
	chunk   int
	acc     *model.MapResult
	spilled bool
	n       bulkCounts
	// same marks the identity (content hash, start) of each Replaceable
	// quantity sample, moved the provider record of each SupersedeMoved one.
	same, moved []copyMark
	// divert holds the positions of the Replaceable quantity samples that
	// have another copy in the object.
	divert map[uint32]struct{}
}

func newObjectPlan(sc scope, key string, chunk int) *objectPlan {
	return &objectPlan{sc: sc, chunk: chunk, acc: &model.MapResult{SourceKey: key}}
}

// add folds one mapped record into the plan and reports whether the object
// just proved larger than one chunk.
func (p *objectPlan) add(mr *model.MapResult) (spilledNow bool) {
	if !p.spilled {
		Accumulate(p.acc, mr)
		if bulkLen(p.acc) <= p.chunk {
			return false
		}
		// Too large to hold: note the bulk records read so far and let go
		// of them. The second read brings them back a chunk at a time.
		p.spilled = true
		p.note(p.acc)
		p.acc.QuantitySamples, p.acc.CategorySamples, p.acc.HeartbeatSamples, p.acc.Observations = nil, nil, nil, nil
		return true
	}
	p.note(mr)
	held := *mr
	held.QuantitySamples, held.CategorySamples, held.HeartbeatSamples, held.Observations = nil, nil, nil, nil
	Accumulate(p.acc, &held)
	return false
}

// note counts the bulk records of mr and marks its Replaceable quantity
// samples.
func (p *objectPlan) note(mr *model.MapResult) {
	for _, s := range mr.QuantitySamples {
		p.n.quantity++
		if !s.Replaceable {
			continue
		}
		ord := uint32(p.n.replaceable)
		p.n.replaceable++
		p.same = append(p.same, copyMark{sameDigest(s), ord})
		if supersedes(s) {
			p.moved = append(p.moved, copyMark{movedDigest(p.sc, s), ord})
		}
	}
	p.n.category += len(mr.CategorySamples)
	p.n.heartbeat += len(mr.HeartbeatSamples)
	p.n.observation += len(mr.Observations)
}

// sameDigest digests the identity of a Replaceable sample: what
// replaceableSplit keys on.
func sameDigest(s model.QuantitySample) uint64 {
	var h maphash.Hash
	h.SetSeed(copySeed)
	h.WriteString(s.ContentHash)
	h.WriteByte(0)
	maphash.WriteComparable(&h, s.StartDate.UnixNano())
	return h.Sum64()
}

// movedDigest digests the provider record figure of a SupersedeMoved sample:
// what supersedeMovedSplit keys on.
func movedDigest(sc scope, s model.QuantitySample) uint64 {
	var h maphash.Hash
	h.SetSeed(copySeed)
	for _, part := range []string{sc.providerOr(s.Provider), s.ExternalID, s.QuantityType} {
		h.WriteString(part)
		h.WriteByte(0)
	}
	return h.Sum64()
}

// seal ends the first read: it finds the Replaceable quantity samples that
// share a digest with another one, and lets go of the marks.
func (p *objectPlan) seal() {
	for _, marks := range [][]copyMark{p.same, p.moved} {
		slices.SortFunc(marks, func(a, b copyMark) int {
			switch {
			case a.digest < b.digest:
				return -1
			case a.digest > b.digest:
				return 1
			}
			return 0
		})
		for i := 0; i < len(marks); {
			j := i + 1
			for j < len(marks) && marks[j].digest == marks[i].digest {
				j++
			}
			if j-i > 1 {
				if p.divert == nil {
					p.divert = map[uint32]struct{}{}
				}
				for _, m := range marks[i:j] {
					p.divert[m.ord] = struct{}{}
				}
			}
			i = j
		}
	}
	p.same, p.moved = nil, nil
}

// failedFlush is how many record failures the second read of a streamed
// object holds before it writes them.
const failedFlush = 500

// streamChunks is the second read of an object larger than one chunk, inside
// its write transaction, after bw took the locks and applied the deletions.
// It writes the parse failures of the first read (rr, parseFailed), then
// reads the object again and writes its bulk records chunk by chunk and its
// record failures as it meets them. It returns the records left for
// batchWrite.finish: the held records of the plan with the diverted
// Replaceable samples.
//
// The second read must give what the first gave (first is its state): an
// object that reads differently fails the job, which the queue retries.
func streamChunks(ctx context.Context, tx TxLike, bw *batchWrite, reader *Reader, lines *lineMapper,
	plan *objectPlan, rr *ReadResult, first *passState, parseFailed []FailedRecord) (*model.MapResult, error) {
	sc, key := bw.sc, lines.job.Key
	if len(parseFailed) > 0 {
		if err := writeFailedRecords(ctx, tx, sc, key, parseFailed); err != nil {
			return nil, err
		}
	}
	st := newPassState()
	flushFailed := func() error {
		if len(st.failures) == 0 {
			return nil
		}
		err := writeFailedRecords(ctx, tx, sc, key, st.failures)
		clear(st.failures)
		st.failures = st.failures[:0]
		return err
	}
	chunk := &model.MapResult{SourceKey: key}
	flush := func() error {
		if bulkLen(chunk) == 0 {
			return nil
		}
		if err := bw.writeChunk(ctx, tx, chunk); err != nil {
			return fmt.Errorf("write records: %w", err)
		}
		// The writer is done with the chunk: its arrays take the next one.
		for _, reset := range []func(){
			func() { clear(chunk.QuantitySamples); chunk.QuantitySamples = chunk.QuantitySamples[:0] },
			func() { clear(chunk.CategorySamples); chunk.CategorySamples = chunk.CategorySamples[:0] },
			func() { clear(chunk.HeartbeatSamples); chunk.HeartbeatSamples = chunk.HeartbeatSamples[:0] },
			func() { clear(chunk.Observations); chunk.Observations = chunk.Observations[:0] },
		} {
			reset()
		}
		return nil
	}
	var (
		seen     bulkCounts
		diverted []model.QuantitySample
	)
	again, err := reader.StreamNDJSON(ctx, key, func(i int, record map[string]any) error {
		mr := lines.mapLine(st, i, record)
		if len(st.failures) >= failedFlush {
			if err := flushFailed(); err != nil {
				return err
			}
		}
		if mr == nil {
			return nil
		}
		for _, s := range mr.QuantitySamples {
			seen.quantity++
			if s.Replaceable {
				ord := uint32(seen.replaceable)
				seen.replaceable++
				if _, ok := plan.divert[ord]; ok {
					diverted = append(diverted, s)
					continue
				}
			}
			chunk.QuantitySamples = append(chunk.QuantitySamples, s)
		}
		chunk.CategorySamples = append(chunk.CategorySamples, mr.CategorySamples...)
		chunk.HeartbeatSamples = append(chunk.HeartbeatSamples, mr.HeartbeatSamples...)
		chunk.Observations = append(chunk.Observations, mr.Observations...)
		seen.category += len(mr.CategorySamples)
		seen.heartbeat += len(mr.HeartbeatSamples)
		seen.observation += len(mr.Observations)
		if bulkLen(chunk) >= plan.chunk {
			return flush()
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read raw object again: %w", err)
	}
	if again.ScanTruncated != rr.ScanTruncated || again.Truncation != rr.Truncation || again.RecordCount != rr.RecordCount ||
		seen != plan.n || st.failed != first.failed {
		return nil, fmt.Errorf("read raw object again: %d record(s) and %d bulk record(s), the first read gave %d and %d",
			again.RecordCount, seen.quantity+seen.category+seen.heartbeat+seen.observation,
			rr.RecordCount, plan.n.quantity+plan.n.category+plan.n.heartbeat+plan.n.observation)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if err := flushFailed(); err != nil {
		return nil, err
	}
	// The held records are the plan's, which a retried transaction reads
	// again: the writer filters this copy, not them.
	rest := *plan.acc
	rest.QuantitySamples = diverted
	return &rest, nil
}

// zoneSeen is the zone of the latest record of a device upload that names
// one.
type zoneSeen struct {
	name string
	at   time.Time
}

// add looks at the records of mr that carry a time zone. A record dated
// after now is not trusted with it.
func (z *zoneSeen) add(mr *model.MapResult, now time.Time) {
	note := func(md map[string]any, at time.Time) {
		if !at.After(z.at) || at.After(now) {
			return
		}
		name, _ := md[MetadataTimezone].(string)
		if _, ok := loadZone(name); ok {
			z.name, z.at = name, at
		}
	}
	for i := range mr.QuantitySamples {
		note(mr.QuantitySamples[i].Metadata, mr.QuantitySamples[i].StartDate)
	}
	for i := range mr.CategorySamples {
		note(mr.CategorySamples[i].Metadata, mr.CategorySamples[i].StartDate)
	}
	for i := range mr.Workouts {
		note(mr.Workouts[i].Metadata, mr.Workouts[i].StartDate)
	}
	for i := range mr.SleepSessions {
		note(mr.SleepSessions[i].Metadata, mr.SleepSessions[i].BedtimeStart)
	}
}

// learnUserZone records the zone a device upload carried as the user's, when
// the developer set none (timezone_source 'explicit' is never overwritten)
// and no later record taught one already (timezone_seen_at). The user row is
// share locked by every running job of the user, so the update skips a locked
// row instead of waiting: the next upload teaches the zone again.
func learnUserZone(ctx context.Context, db Execer, sc scope, z zoneSeen) error {
	_, err := db.Exec(ctx, `UPDATE ohs_core.end_users u
		SET timezone = $3, timezone_source = 'learnt', timezone_seen_at = $4
		FROM (SELECT id FROM ohs_core.end_users
			WHERE tenant_id = $1 AND id = $2 AND status = 'active'
			  AND timezone_source IS DISTINCT FROM 'explicit'
			  AND (timezone_seen_at IS NULL OR timezone_seen_at < $4)
			FOR NO KEY UPDATE SKIP LOCKED) l
		WHERE u.tenant_id = $1 AND u.id = l.id`, sc.tenant, sc.user, z.name, z.at)
	if err != nil {
		return fmt.Errorf("record user time zone: %w", err)
	}
	return nil
}
