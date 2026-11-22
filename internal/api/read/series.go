// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
	"github.com/soma-intelligence/open-health-sync/internal/vault/series"
)

// Series blobs (migration 0053). The points of an ECG recording, of a
// workout route and of a heartbeat series are stored as one compressed blob
// per series in ohs_data.series_blobs, sealed like any sealed row. A series
// is held either by its blob or, until `ohs vault seal` converts it, by its
// per-point rows in ecg_samples, route_points or heartbeat_samples, never by
// both: the ETL moves the rows into the blob whenever it writes the series.
// The reads here decode the blob and serve its points as the rows served
// them, and fall back to the rows for a series not converted yet.

// blobSel reads one series blob: its provider, parent, first point time,
// the points (sealed on a sealed row) and when it was stored.
var blobSel = vault.Select(vault.SeriesTable, `provider, parent, start_at, points, ingested_at`)

// seriesBlob is one stored series.
type seriesBlob struct {
	provider, parent string
	start, ingested  time.Time
	points           []series.Point
}

// eachBlob reads the blobs of kind k selected by where (over the table's
// columns, after the tenant and user: $1 and $2, then args from $3), in the
// order of order, and passes each one to fn. stop, when not nil, is asked
// with each blob's first point time before the blob is opened and decoded:
// true ends the read there.
func eachBlob(ctx context.Context, db Querier, tenantID, userID uuid.UUID, k series.Kind,
	where, order string, args []any, stop func(start time.Time) bool, fn func(seriesBlob) error) error {
	sql := `SELECT ` + blobSel.List("") + ` FROM ohs_data.series_blobs
		WHERE tenant_id = $1 AND user_id = $2 AND kind = ` + fmt.Sprint(int16(k))
	if where != "" {
		sql += " AND " + where
	}
	if order != "" {
		sql += " ORDER BY " + order
	}
	rows, err := db.Query(ctx, sql, append([]any{tenantID, userID}, args...)...)
	if err != nil {
		return fmt.Errorf("read: series blobs: %w", err)
	}
	defer rows.Close()
	keys := keysFor(ctx, tenantID, userID)
	for rows.Next() {
		var start time.Time
		if stop != nil {
			// The first point time is a clear column, read before opening.
			vals, err := rows.Values()
			if err != nil {
				return fmt.Errorf("read: series blobs: %w", err)
			}
			start, _ = vals[2].(time.Time)
			if stop(start.UTC()) {
				return nil
			}
		}
		var b seriesBlob
		var blob []byte
		if err := blobSel.Scan(ctx, keys, rows, []any{&b.provider, &b.parent, &b.start, &blob, &b.ingested}); err != nil {
			return fmt.Errorf("read: series blobs: scan: %w", err)
		}
		if b.points, err = series.Decode(k, blob); err != nil {
			return fmt.Errorf("read: series blob %s %s: %w", k, b.parent, err)
		}
		b.start, b.ingested = b.start.UTC(), b.ingested.UTC()
		if err := fn(b); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read: series blobs: %w", err)
	}
	return nil
}

// readBlobs reads every blob eachBlob would pass.
func readBlobs(ctx context.Context, db Querier, tenantID, userID uuid.UUID, k series.Kind,
	where, order string, args ...any) ([]seriesBlob, error) {
	var out []seriesBlob
	err := eachBlob(ctx, db, tenantID, userID, k, where, order, args, nil, func(b seriesBlob) error {
		out = append(out, b)
		return nil
	})
	return out, err
}
