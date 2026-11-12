// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// recordingTx records the statements the writer runs and answers every one
// with an empty result.
type recordingTx struct {
	sql    []string
	args   [][]any
	copied [][]any
}

func (r *recordingTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.sql, r.args = append(r.sql, sql), append(r.args, args)
	return pgconn.NewCommandTag("INSERT 0 0"), nil
}

func (r *recordingTx) QueryRow(context.Context, string, ...any) pgx.Row { panic("not used") }

func (r *recordingTx) Query(context.Context, string, ...any) (pgx.Rows, error) { panic("not used") }

func (r *recordingTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, src pgx.CopyFromSource) (int64, error) {
	var n int64
	for src.Next() {
		row, err := src.Values()
		if err != nil {
			return n, err
		}
		r.copied = append(r.copied, row)
		n++
	}
	return n, src.Err()
}

// find returns the arguments of the first recorded statement containing part.
func (r *recordingTx) find(t *testing.T, part string) (string, []any) {
	t.Helper()
	for i, s := range r.sql {
		if strings.Contains(s, part) {
			return s, r.args[i]
		}
	}
	t.Fatalf("no statement contains %q", part)
	return "", nil
}

// A sparse part (years of one type, a day of another) reads the stored rows of
// each type over that type's own window, not every type over the whole span.
func TestInsertMissingReadsStoredRowsPerType(t *testing.T) {
	sc := scope{tenant: uuid.New(), user: uuid.New(), provider: "dev"}
	y2021 := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	y2026 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	steps := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
	sample := func(qt, hash string, at time.Time) model.QuantitySample {
		return model.QuantitySample{QuantityType: qt, StartDate: at, ContentHash: hash, Value: 1}
	}
	tx := &recordingTx{}
	_, _, err := NewWriter(WriterConfig{}).writeQuantitySamples(context.Background(), tx, sc, []model.QuantitySample{
		sample("body_mass", "w1", y2021),
		sample("body_mass", "w2", y2026),
		sample("step_count", "s1", steps),
		sample("step_count", "s2", steps.Add(time.Hour)),
		sample("step_count", "s2", steps.Add(time.Hour)), // in-batch duplicate
	})
	require.NoError(t, err)
	assert.Len(t, tx.copied, 4, "the in-batch duplicate is not staged")

	sql, args := tx.find(t, "WITH stored AS MATERIALIZED")
	assert.Contains(t, sql, `t."quantity_type" = k.kind`)
	assert.Contains(t, sql, `t."start_date" BETWEEN k.lo AND k.hi`)
	require.Len(t, args, 7)
	assert.Equal(t, []string{"body_mass", "step_count"}, args[2])
	assert.Equal(t, []time.Time{y2021, steps}, args[3])
	assert.Equal(t, []time.Time{y2026, steps.Add(time.Hour)}, args[4])
	assert.Equal(t, y2021, args[5], "the batch window still bounds the statement")
	assert.Equal(t, steps.Add(time.Hour), args[6])
}

// A Replaceable sample finds its stored copy through the type index, and is
// held back when the stored copy is newer.
func TestUpdateStoredJoinsOnTypeAndGuardsOnCapture(t *testing.T) {
	sc := scope{tenant: uuid.New(), user: uuid.New(), provider: "dev", captured: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	at := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	tx := &recordingTx{}
	_, _, err := NewWriter(WriterConfig{}).writeQuantitySamples(context.Background(), tx, sc, []model.QuantitySample{
		{QuantityType: "step_count", StartDate: at, ContentHash: "d", Value: 1, Replaceable: true},
	})
	require.NoError(t, err)
	sql, _ := tx.find(t, "UPDATE ohs_data.quantity_samples")
	assert.Contains(t, sql, `t."quantity_type" = s."quantity_type"`)
	assert.Contains(t, sql, "s.captured_at >= t.captured_at")
	assert.Contains(t, sql, "captured_at = s.captured_at")
	require.Len(t, tx.copied, 1)
	assert.Equal(t, sc.captured, tx.copied[0][len(quantitySampleColumns)-1], "the staged row carries the capture time")
}

func TestLatestWinsGuardsOnCapture(t *testing.T) {
	clause := latestWins("daily_summaries", "tenant_id, user_id, content_hash, summary_date", []string{"step_count"}, nil)
	assert.Contains(t, clause, "captured_at = EXCLUDED.captured_at")
	assert.Contains(t, clause,
		"AND (daily_summaries.captured_at IS NULL OR EXCLUDED.captured_at IS NULL OR EXCLUDED.captured_at >= daily_summaries.captured_at)")
}

// A batch without a Replaceable row is passed through, not copied.
func TestReplaceableSplitDoesNotCopyAnInsertOnlyBatch(t *testing.T) {
	in := []model.QuantitySample{{ContentHash: "a"}, {ContentHash: "b"}}
	insertOnly, upsert, superseded := replaceableSplit(in,
		func(s model.QuantitySample) bool { return s.Replaceable },
		func(s model.QuantitySample) copyDedupeKey { return copyDedupeKey{hash: s.ContentHash} })
	assert.Same(t, &in[0], &insertOnly[0])
	assert.Empty(t, upsert)
	assert.Zero(t, superseded)
}

func TestLazyRowsSkipsAndBuildsOnDemand(t *testing.T) {
	built := 0
	src := lazyRows([]int{1, 2, 3, 4}, func(v int) ([]any, error) { built++; return []any{v}, nil },
		func(v int) bool { return v%2 == 0 })
	require.True(t, src.Next())
	assert.Equal(t, 1, built, "rows are built one at a time")
	v, err := src.Values()
	require.NoError(t, err)
	assert.Equal(t, []any{1}, v)
	require.True(t, src.Next())
	v, _ = src.Values()
	assert.Equal(t, []any{3}, v)
	assert.False(t, src.Next())
	assert.NoError(t, src.Err())
}
