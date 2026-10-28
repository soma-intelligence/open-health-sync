// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// hourMap maps one line to one quantity sample with its attribution (source,
// product, model, slug, external id), or, with kind "delete", to the deletion
// of the record id.
func hourMap(r connector.RawRecord) (model.MapResult, error) {
	str := func(k string) string { s, _ := r.Fields[k].(string); return s }
	if str("kind") == "delete" {
		return model.MapResult{Deletions: []model.Deletion{{ExternalID: str("id")}}}, nil
	}
	start, err := time.Parse(time.RFC3339, str("start"))
	if err != nil {
		return model.MapResult{}, err
	}
	v := r.Fields["value"].(float64)
	s := model.QuantitySample{
		Provider: r.Provider, QuantityType: str("type"), Value: v, Unit: str("unit"),
		StartDate: start.UTC(), EndDate: start.UTC(), ExternalID: str("id"),
		SourceName: str("source"), SourceBundleID: str("bundle"), DeviceID: str("device"),
		ProductType: str("product"), DeviceModel: str("model"), SourceSlug: str("slug"),
		ContentHash: model.ComputeContentHash(map[string]string{
			"user_id": r.UserID, "type": str("type"), "start": start.UTC().Format(time.RFC3339Nano),
			"value": model.CanonicalFloat(v), "source": str("source"), "id": str("id"), "product": str("product"),
		}),
	}
	return model.MapResult{QuantitySamples: []model.QuantitySample{s}}, nil
}

// hs is one line of hourMap.
func hs(qt string, v float64, unit string, at time.Time, attrs ...string) string {
	f := map[string]any{"type": qt, "value": v, "start": at.UTC().Format(time.RFC3339)}
	if unit != "" {
		f["unit"] = unit
	}
	for i := 0; i+1 < len(attrs); i += 2 {
		f[attrs[i]] = attrs[i+1]
	}
	return line(f)
}

// bucket is one opened hourly bucket, or one group the continuous aggregate
// would compute, by its key; source is the clear source, for the expected
// side, or the hex of the keyed source, mapped back by the test.
type bucket struct {
	quantityType, provider, source, slug string
	worn                                 bool
	unit                                 string
	at                                   time.Time
}

type bucketStats struct {
	n             int64
	sum, min, max float64
}

// expectedBuckets groups the user's opened samples as migration 0035 does:
// the same source, slug, worn and unit expressions, worn true where the
// aggregate's rank is 0, a NULL unit as the empty string.
func (e *env) expectedBuckets() map[bucket]bucketStats {
	e.t.Helper()
	ctx := context.Background()
	rows, err := e.read().Query(ctx, `
		SELECT quantity_type, provider,
		       COALESCE(NULLIF(source_bundle_id, ''), NULLIF(source_name, ''), NULLIF(device_id, ''), '') AS source,
		       coalesce(source_slug, provider) AS slug,
		       (coalesce(product_type, '') || ' ' || coalesce(device_model, '') || ' ' ||
		        coalesce(source_name, '')) ILIKE '%watch%' AS worn,
		       coalesce(unit, '') AS unit,
		       time_bucket(INTERVAL '1 hour', start_date) AS bucket,
		       count(*), sum(value), min(value), max(value)
		FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2
		GROUP BY 1, 2, 3, 4, 5, 6, 7`, e.tenant, e.user)
	require.NoError(e.t, err)
	defer rows.Close()
	out := map[bucket]bucketStats{}
	for rows.Next() {
		var b bucket
		var s bucketStats
		require.NoError(e.t, rows.Scan(&b.quantityType, &b.provider, &b.source, &b.slug, &b.worn, &b.unit, &b.at,
			&s.n, &s.sum, &s.min, &s.max))
		b.at = b.at.UTC()
		out[b] = s
	}
	require.NoError(e.t, rows.Err())
	return out
}

// storedBuckets opens the user's hourly buckets; the source of each is found
// back among sources by its keyed form.
func (e *env) storedBuckets(sources ...string) map[bucket]bucketStats {
	e.t.Helper()
	ctx := context.Background()
	uv := e.keys(e.tenant, e.user)
	byKey := map[string]string{}
	for _, s := range append(sources, "") {
		byKey[string(uv.SourceKey(s))] = s
	}
	tx, err := e.pool.Begin(ctx)
	require.NoError(e.t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := collectRows(openRows(ctx, tx, uv, "quantity_hourly_sealed",
		`SELECT * FROM ohs_data.quantity_hourly_sealed WHERE tenant_id = $1 AND user_id = $2`, []any{e.tenant, e.user}))
	require.NoError(e.t, err)
	out := map[bucket]bucketStats{}
	for _, r := range rows {
		key, _ := r["source_key"].([]byte)
		src, ok := byKey[string(key)]
		require.True(e.t, ok, "a bucket's source_key is the keyed form of a known source")
		at, _ := r["bucket"].(time.Time)
		worn, _ := r["worn"].(bool)
		b := bucket{r.text("quantity_type"), r.text("provider"), src, r.text("slug"), worn, r.text("unit"), at.UTC()}
		n, _ := r.int("n")
		s := bucketStats{n: n}
		s.sum, _ = r.float("sum")
		s.min, _ = r.float("min")
		s.max, _ = r.float("max")
		out[b] = s
	}
	return out
}

// bucketXmins is the xmin of every stored bucket, to tell a rewrite.
func (e *env) bucketXmins() map[string]string {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT quantity_type || provider || encode(source_key, 'hex') || slug || worn || unit || bucket, xmin::text
		FROM ohs_data.quantity_hourly_sealed WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user)
	require.NoError(e.t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, x string
		require.NoError(e.t, rows.Scan(&k, &x))
		out[k] = x
	}
	require.NoError(e.t, rows.Err())
	return out
}

var hourSources = []string{"Apple Watch", "iPhone", "com.example.app", "scale-1"}

// After a job, every hour it touched has the buckets the continuous aggregate
// would compute from the opened samples: one per source, slug, worn and unit.
// The same object again changes no bucket.
func TestHourlyBucketsMatchSamples(t *testing.T) {
	e := newEnv(t)
	e.register("dev", hourMap)
	at := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	lines := []string{
		// 08:00, heart rate from a watch and a phone, apart.
		hs("heart_rate", 60, "bpm", at.Add(5*time.Minute), "source", "Apple Watch", "product", "Watch7,2"),
		hs("heart_rate", 70, "bpm", at.Add(10*time.Minute), "source", "Apple Watch", "product", "Watch7,2"),
		hs("heart_rate", 65, "bpm", at.Add(59*time.Minute), "source", "Apple Watch", "product", "Watch7,2"),
		hs("heart_rate", 80, "bpm", at.Add(20*time.Minute), "source", "iPhone", "product", "iPhone17,1"),
		// A bundle id wins over the source name; a watch named by the model.
		hs("heart_rate", 90, "bpm", at.Add(30*time.Minute), "source", "Other", "bundle", "com.example.app", "model", "Watch"),
		// 09:00: another hour, another slug.
		hs("heart_rate", 75, "bpm", at.Add(61*time.Minute), "source", "iPhone", "slug", "polar"),
		// Steps without a source, and a weight without a unit.
		hs(model.QTStepCount, 120, "count", at.Add(15*time.Minute)),
		hs(model.QTStepCount, 80, "count", at.Add(16*time.Minute)),
		hs("body_mass", 70.5, "", at.Add(3*time.Hour), "device", "scale-1"),
	}
	e.process(e.upload(e.tenant, e.user, "dev", "x", lines...))

	want := e.expectedBuckets()
	require.Len(t, want, 6)
	assert.Equal(t, want, e.storedBuckets(hourSources...))
	assert.Equal(t, bucketStats{3, 195, 60, 70},
		want[bucket{"heart_rate", "dev", "Apple Watch", "dev", true, "bpm", at}], "the watch's hour")

	// The same samples again: nothing is rewritten.
	before := e.bucketXmins()
	e.process(e.upload(e.tenant, e.user, "dev", "x", lines...))
	assert.Equal(t, before, e.bucketXmins(), "an unchanged bucket is not rewritten")

	// One more sample in one hour: that bucket only changes.
	e.process(e.upload(e.tenant, e.user, "dev", "x",
		hs("heart_rate", 100, "bpm", at.Add(40*time.Minute), "source", "iPhone", "product", "iPhone17,1")))
	assert.Equal(t, e.expectedBuckets(), e.storedBuckets(hourSources...))
	after := e.bucketXmins()
	changed := 0
	for k, x := range after {
		if before[k] != x {
			changed++
		}
	}
	assert.Equal(t, 1, changed)
}

// A deletion recomputes the hours of the samples it removes: the hour keeps
// its other samples, and an hour left empty loses its bucket.
func TestHourlyBucketsFollowDeletions(t *testing.T) {
	e := newEnv(t)
	e.register("dev", hourMap)
	at := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	e.process(e.upload(e.tenant, e.user, "dev", "x",
		hs("heart_rate", 60, "bpm", at.Add(5*time.Minute), "source", "iPhone", "id", "r1"),
		hs("heart_rate", 70, "bpm", at.Add(6*time.Minute), "source", "iPhone", "id", "r2"),
		hs("heart_rate", 80, "bpm", at.Add(2*time.Hour), "source", "iPhone", "id", "r3"),
	))
	require.Len(t, e.storedBuckets(hourSources...), 2)

	e.process(e.upload(e.tenant, e.user, "dev", "x",
		line(map[string]any{"kind": "delete", "id": "r1"}),
		line(map[string]any{"kind": "delete", "id": "r3"})))
	got := e.storedBuckets(hourSources...)
	assert.Equal(t, e.expectedBuckets(), got)
	require.Len(t, got, 1, "the 10:00 hour has no sample left")
	assert.Equal(t, bucketStats{1, 70, 70, 70}, got[bucket{"heart_rate", "dev", "iPhone", "dev", false, "bpm", at}])
}

// An import of last year fills last year's hours, rows written before the
// user had keys included.
func TestHourlyBucketsOfAnImportedYear(t *testing.T) {
	e := newEnv(t)
	e.register("dev", hourMap)
	last := time.Date(2025, 3, 14, 22, 0, 0, 0, time.UTC)
	// A clear row of that hour, as an instance not sealed yet stored it.
	e.exec(`INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, source_name, quantity_type, value, unit, start_date, content_hash)
		VALUES ($1, $2, 'dev', 'iPhone', 'heart_rate', 50, 'bpm', $3, 'legacy-1')`, e.tenant, e.user, last.Add(time.Minute))
	var lines []string
	for d := range 3 {
		lines = append(lines, hs("heart_rate", float64(60+d), "bpm", last.AddDate(0, 0, d).Add(10*time.Minute), "source", "iPhone"))
	}
	e.process(e.upload(e.tenant, e.user, "dev", "x", lines...))
	got := e.storedBuckets(hourSources...)
	assert.Equal(t, e.expectedBuckets(), got)
	require.Len(t, got, 3)
	assert.Equal(t, bucketStats{2, 110, 50, 60}, got[bucket{"heart_rate", "dev", "iPhone", "dev", false, "bpm", last}])
}

// Without a vault nothing is written: the continuous aggregate serves the
// users who are not sealed.
func TestHourlyBucketsNeedKeys(t *testing.T) {
	e := newEnv(t)
	e.register("dev", hourMap)
	job := e.upload(e.tenant, e.user, "dev", "x", hs("heart_rate", 60, "bpm", time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)))
	_, err := ProcessWith(context.Background(), e.pool, e.rs, job, e.clearOpts())
	require.NoError(t, err)
	assert.Zero(t, e.count("quantity_hourly_sealed"))
}
