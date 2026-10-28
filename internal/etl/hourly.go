// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// Sealed hourly buckets (docs/specs/vault.md, "Aggregates move to Go").
//
// ohs_data.quantity_hourly_sealed holds, per user, quantity type and hour, the
// count, sum, minimum and maximum of the samples, grouped as the continuous
// aggregate of migration 0035 groups them: provider, source, slug, worn and
// unit. The source is the keyed source_key (vault.UserVault.SourceKey of the
// same stream string), worn is true where the aggregate's rank is 0 (a watch),
// and a missing unit is ''. n, sum, min and max are sealed together.
//
// The writer recomputes, inside the job's transaction, every hour the job
// touched: the hours of the samples it wrote or met again, and of the samples
// a deletion or a supersede removed. An hour is recomputed whole from its
// opened samples, so the bucket is exact whatever happened before; an hour
// left without samples loses its buckets, and a bucket whose plaintext did
// not change is not rewritten (content_mac). A provider erasure deletes the
// provider's buckets with its other rows (deletion.planProvider: the table
// has a provider column), and retention prunes them by bucket.
//
// Without keys nothing is written: the continuous aggregate still serves the
// users who are not sealed yet.

// HourKey is one hour of one quantity type of a user. Bucket is truncated to
// the hour, in UTC, as time_bucket('1 hour', start_date) is.
type HourKey struct {
	QuantityType string
	Bucket       time.Time
}

// hourBatch bounds the hours one read covers, so a year of imported history
// is read in slices.
const hourBatch = 500

// hourSet collects the hours a write touches. A nil set collects nothing.
type hourSet map[HourKey]struct{}

// add records the hour of a sample of quantityType starting at start.
func (h hourSet) add(quantityType string, start time.Time) {
	if h == nil {
		return
	}
	h[HourKey{QuantityType: quantityType, Bucket: hourOf(start)}] = struct{}{}
}

// keys returns the hours of the set.
func (h hourSet) keys() []HourKey {
	out := make([]HourKey, 0, len(h))
	for k := range h {
		out = append(out, k)
	}
	return out
}

// hourOf is the hour a sample starting at t falls in.
func hourOf(t time.Time) time.Time { return t.UTC().Truncate(time.Hour) }

// hourGroup is the identity of one bucket within a user: the primary key of
// quantity_hourly_sealed after user_id.
type hourGroup struct {
	quantityType, provider string
	sourceKey              string // bytes
	slug                   string
	worn                   bool
	unit                   string
	bucket                 int64 // UnixMicro
}

// hourStats are the sealed statistics of one bucket.
type hourStats struct {
	n             int64
	sum, min, max float64
	seen          bool // a value was folded in (min and max are set)
}

func (s *hourStats) fold(v float64, ok bool) {
	s.n++
	if !ok {
		return
	}
	if !s.seen {
		s.min, s.max, s.seen = v, v, true
	} else {
		s.min, s.max = math.Min(s.min, v), math.Max(s.max, v)
	}
	s.sum += v
}

// hourSamples reads the samples of the hours ($3 types, $4 buckets, one
// element per hour) of a user, bounded by $5 and $6 so a hypertable reads
// only those chunks. It selects the identity, the sealed value and the clear
// columns a row not converted yet holds.
const hourSamples = `SELECT q.content_hash, q.start_date, q.sealed, q.quantity_type, q.provider, q.source_slug, q.unit,
		q.value, q.source_name, q.source_bundle_id, q.device_id, q.device_model, q.product_type
	FROM unnest($3::text[], $4::timestamptz[]) AS h(quantity_type, bucket)
	JOIN ohs_data.quantity_samples q
	  ON q.tenant_id = $1 AND q.user_id = $2 AND q.quantity_type = h.quantity_type
	 AND q.start_date >= h.bucket AND q.start_date < h.bucket + INTERVAL '1 hour'
	WHERE q.start_date >= $5 AND q.start_date < $6`

// hourStale deletes the buckets of the hours ($3, $4) that are not among the
// groups ($7 to $13) the samples gave, and hourUpsert writes those groups,
// leaving a bucket whose content_mac did not change as it is.
const (
	hourStale = `DELETE FROM ohs_data.quantity_hourly_sealed b
	USING unnest($3::text[], $4::timestamptz[]) AS h(quantity_type, bucket)
	WHERE b.tenant_id = $1 AND b.user_id = $2 AND b.bucket >= $5 AND b.bucket < $6
	  AND b.quantity_type = h.quantity_type AND b.bucket = h.bucket
	  AND NOT EXISTS (SELECT 1 FROM unnest($7::text[], $8::text[], $9::bytea[], $10::text[], $11::bool[], $12::text[], $13::timestamptz[])
	      AS g(quantity_type, provider, source_key, slug, worn, unit, bucket)
	      WHERE g.quantity_type = b.quantity_type AND g.provider = b.provider AND g.source_key = b.source_key
	        AND g.slug = b.slug AND g.worn = b.worn AND g.unit = b.unit AND g.bucket = b.bucket)`
	hourUpsert = `INSERT INTO ohs_data.quantity_hourly_sealed AS b
		(tenant_id, user_id, quantity_type, provider, source_key, slug, worn, unit, bucket, sealed, content_mac)
	SELECT $1, $2, g.* FROM unnest($3::text[], $4::text[], $5::bytea[], $6::text[], $7::bool[], $8::text[], $9::timestamptz[], $10::bytea[], $11::bytea[])
	    AS g(quantity_type, provider, source_key, slug, worn, unit, bucket, sealed, content_mac)
	ON CONFLICT (tenant_id, user_id, quantity_type, provider, source_key, slug, worn, unit, bucket)
	DO UPDATE SET sealed = EXCLUDED.sealed, content_mac = EXCLUDED.content_mac
	WHERE b.content_mac IS DISTINCT FROM EXCLUDED.content_mac`
)

// RecomputeHours recomputes, in tx, the sealed buckets of the given hours of
// a user from the user's samples of those hours, opened under uv. It takes
// the user's quantity_samples lock (the writer's, so it is already held in an
// ETL job), which serializes it with every other write of the user's samples:
// two jobs cannot each sum an hour without the other's samples. A caller
// outside the writer takes that lock in the writer's order, after the user
// row and the aliases and before the other merged tables. With uv nil (no
// vault configured) it writes nothing.
func RecomputeHours(ctx context.Context, tx TxLike, uv *vault.UserVault, tenant, user uuid.UUID, hours []HourKey) error {
	if uv == nil || len(hours) == 0 {
		return nil
	}
	sc := scope{tenant: tenant, user: user, uv: uv}
	if err := lockUserTable(ctx, tx, tQuantity, sc); err != nil {
		return err
	}
	set := hourSet{}
	for _, h := range hours {
		set.add(h.QuantityType, h.Bucket)
	}
	all := set.keys()
	slices.SortFunc(all, func(a, b HourKey) int {
		return cmp.Or(cmp.Compare(a.QuantityType, b.QuantityType), a.Bucket.Compare(b.Bucket))
	})
	for len(all) > 0 {
		n := min(hourBatch, len(all))
		if err := recomputeBatch(ctx, tx, sc, all[:n]); err != nil {
			return err
		}
		all = all[n:]
	}
	return nil
}

// recomputeBatch is RecomputeHours over one slice of hours.
func recomputeBatch(ctx context.Context, tx TxLike, sc scope, hours []HourKey) error {
	t := vault.Schema["quantity_hourly_sealed"]
	types := make([]string, len(hours))
	buckets := make([]time.Time, len(hours))
	lo, hi := hours[0].Bucket, hours[0].Bucket
	for i, h := range hours {
		types[i], buckets[i] = h.QuantityType, h.Bucket
		lo, hi = minTime(lo, h.Bucket), maxTime(hi, h.Bucket)
	}
	hi = hi.Add(time.Hour)

	stats := map[hourGroup]*hourStats{}
	var order []hourGroup
	for r, err := range openRows(ctx, tx, sc.uv, tQuantity, hourSamples, []any{sc.tenant, sc.user, types, buckets, lo, hi}) {
		if err != nil {
			return fmt.Errorf("hourly buckets: %w", err)
		}
		start, _ := r["start_date"].(time.Time)
		provider := r.text("provider")
		slug := provider
		if s, ok := r["source_slug"].(string); ok {
			slug = s
		}
		// The source and worn expressions of migration 0035.
		source := firstNonEmpty(r["source_bundle_id"], r["source_name"], r["device_id"])
		g := hourGroup{
			quantityType: r.text("quantity_type"),
			provider:     provider,
			sourceKey:    string(sc.uv.SourceKey(source)),
			slug:         slug,
			worn:         namesWatch(r["product_type"], r["device_model"], r["source_name"]),
			unit:         r.text("unit"),
			bucket:       hourOf(start).UnixMicro(),
		}
		s, ok := stats[g]
		if !ok {
			s = &hourStats{}
			stats[g] = s
			order = append(order, g)
		}
		v, ok := r.float("value")
		s.fold(v, ok)
	}

	n := len(order)
	gTypes, gProviders, gSlugs, gUnits := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	gSources, gSealed, gMACs := make([][]byte, n), make([][]byte, n), make([][]byte, n)
	gWorn := make([]bool, n)
	gBuckets := make([]time.Time, n)
	for i, g := range order {
		s := stats[g]
		gTypes[i], gProviders[i], gSlugs[i], gUnits[i] = g.quantityType, g.provider, g.slug, g.unit
		gSources[i], gWorn[i], gBuckets[i] = []byte(g.sourceKey), g.worn, time.UnixMicro(g.bucket).UTC()
		vals := []any{s.n, nil, nil, nil}
		if s.seen {
			vals[1], vals[2], vals[3] = s.sum, s.min, s.max
		}
		id, err := rowIdentity(t, func(c string) any {
			switch c {
			case "quantity_type":
				return g.quantityType
			case "provider":
				return g.provider
			case "source_key":
				return gSources[i]
			case "slug":
				return g.slug
			case "worn":
				return g.worn
			case "unit":
				return g.unit
			case "bucket":
				return gBuckets[i]
			}
			return nil
		})
		if err != nil {
			return err
		}
		if gSealed[i], err = sc.uv.SealRow(t, id, vals); err != nil {
			return fmt.Errorf("seal %s row: %w", t.Name, err)
		}
		gMACs[i] = sc.uv.ContentMAC(t, vals)
	}

	if _, err := tx.Exec(ctx, hourStale, sc.tenant, sc.user, types, buckets, lo, hi,
		gTypes, gProviders, gSources, gSlugs, gWorn, gUnits, gBuckets); err != nil {
		return fmt.Errorf("delete stale hourly buckets: %w", err)
	}
	if n == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, hourUpsert, sc.tenant, sc.user,
		gTypes, gProviders, gSources, gSlugs, gWorn, gUnits, gBuckets, gSealed, gMACs); err != nil {
		return fmt.Errorf("write hourly buckets: %w", err)
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
