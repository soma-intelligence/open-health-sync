// SPDX-License-Identifier: AGPL-3.0-only

package automation

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// maxUnregisteredLogged caps the warnings of one pass; the metric keeps the
// full count.
const maxUnregisteredLogged = 20

// FileScanner finds files in the raw store that have no raw_objects row. Such a
// file cannot be processed (tenant, user and data type live on the row and are
// never guessed from the key), so it is only counted and logged. It walks the
// store page by page and checks each page against the registry in one query.
type FileScanner struct {
	Pool    *pgxpool.Pool
	Store   rawstore.Store
	Metrics *Metrics
}

// Run does one walk and returns the number of unregistered files.
func (s *FileScanner) Run(ctx context.Context) (int, error) {
	missing := 0
	err := s.Store.Walk(ctx, "raw/", func(keys []string) error {
		unreg, err := s.unregistered(ctx, keys)
		if err != nil {
			return err
		}
		for _, k := range unreg {
			if missing < maxUnregisteredLogged {
				logging.Ctx(ctx).Warn().Str("key", k).Msg("raw_file_without_raw_objects_row")
			}
			missing++
		}
		return nil
	})
	if err != nil {
		return missing, fmt.Errorf("walk raw store: %w", err)
	}
	if missing > maxUnregisteredLogged {
		logging.Ctx(ctx).Warn().Int("total", missing).Msg("more_raw_files_without_raw_objects")
	}
	if s.Metrics != nil {
		s.Metrics.UnregisteredFiles.Store(int64(missing))
	}
	return missing, nil
}

func (s *FileScanner) unregistered(ctx context.Context, batch []string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT k.key FROM unnest($1::text[]) AS k(key)
		WHERE NOT EXISTS (SELECT 1 FROM ohs_sync.raw_objects o WHERE o.key = k.key)
		ORDER BY k.key`, batch)
	if err != nil {
		return nil, fmt.Errorf("check registered keys: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scan key: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
