// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// enqueueWebhooks announces what one object's write changed, inside its
// transaction: data.written when records were written or updated, and
// data.deleted when provider deletions removed records. A write that only
// met duplicates announces nothing.
func enqueueWebhooks(ctx context.Context, tx TxLike, sc scope, counts Counts, cov []coverageEntry) error {
	records := map[string]int{}
	for table, tc := range counts {
		if table != tDeleted && tc.Written > 0 {
			records[table] = tc.Written
		}
	}
	user := sc.user
	if len(records) > 0 {
		data := map[string]any{"provider": sc.provider, "records": records}
		addDays(data, cov)
		if _, err := webhook.Enqueue(ctx, tx, webhook.Event{
			Tenant: sc.tenant, User: &user, Type: webhook.DataWritten, Data: data,
		}); err != nil {
			return fmt.Errorf("webhooks: %w", err)
		}
	}
	if n := counts[tDeleted].Written; n > 0 {
		if _, err := webhook.Enqueue(ctx, tx, webhook.Event{
			Tenant: sc.tenant, User: &user, Type: webhook.DataDeleted,
			Data: map[string]any{"provider": sc.provider, "records_deleted": n},
		}); err != nil {
			return fmt.Errorf("webhooks: %w", err)
		}
	}
	return nil
}

// addDays adds the metrics the object held and the first and last data day
// (calendar dates, as coverage counts them), so a receiver knows which
// window to read again.
func addDays(data map[string]any, cov []coverageEntry) {
	if len(cov) == 0 {
		return
	}
	var first, last time.Time
	types := []string{}
	for _, e := range cov {
		if first.IsZero() || e.Date.Before(first) {
			first = e.Date
		}
		if e.Date.After(last) {
			last = e.Date
		}
		if !slices.Contains(types, e.Metric) {
			types = append(types, e.Metric)
		}
	}
	slices.Sort(types)
	data["types"] = types
	data["first_day"] = first.Format(time.DateOnly)
	data["last_day"] = last.Format(time.DateOnly)
}
