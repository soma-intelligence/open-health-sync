// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// Sealed rows (docs/specs/vault.md, "Data flow", step 3).
//
// A health row is either clear (no vault configured, or a row written before
// the vault and not converted yet) or sealed: its sensitive columns are NULL
// and held together in its sealed column. Every query of this package
// filters, orders and pages on clear columns only, selects the sealed column
// with the identity of the row, and scans through a vault.Columns, which
// fills the same structs from either form. What SQL used to compute on a
// value (the merged daily summary, the statistics, the list of devices) is
// computed here after opening.
//
// The keys come from the Vault the request carries (WithVault). They are
// unwrapped when the first sealed row is met, once per read, or once for a
// read of many pages under HoistLookups. A row that does not open fails the
// read (vault.OpenError, a 500 with the audit event vault.open_failed): a
// page never comes back shorter for it. A sealed row read without a vault
// fails the same way (vault.ErrNoVault). A user whose keys are gone (erased)
// reads as an absent user (ErrNotFound), and a key provider that is down as
// vault.ErrProviderUnavailable, which the API answers with 503.

// WithVault returns ctx under which the reads of this package open sealed
// rows with the keys v holds. The API puts it on every request.
func WithVault(ctx context.Context, v *vault.Vault) context.Context {
	return vault.NewContext(ctx, v)
}

// keysFor returns the reader of the user's sealed rows for a read under ctx.
func keysFor(ctx context.Context, tenantID, userID uuid.UUID) *vault.Reader {
	h := hoist(ctx, tenantID, userID)
	if h == nil {
		return vault.ReaderFor(ctx, tenantID, userID).NoKeysAs(ErrNotFound)
	}
	defer h.mu.Unlock()
	if h.keys == nil {
		h.keys = vault.ReaderFor(ctx, tenantID, userID).NoKeysAs(ErrNotFound)
	}
	return h.keys
}

// The select lists of the sealed tables, each in the order of the
// destinations its scan passes.
var (
	sampleSel    = vault.Select("quantity_samples", sampleCols)
	categorySel  = vault.Select("category_samples", categoryCols)
	sleepSel     = vault.Select("sleep_sessions", sleepCols)
	phaseSel     = vault.Select("sleep_phases", `id, session_id, phase, start_date, end_date, duration_s, ingested_at`)
	workoutSel   = vault.Select("workouts", workoutCols)
	routeSel     = vault.Select("route_points", `id, workout_id, "timestamp", latitude, longitude, altitude_m, speed_mps, course_deg, ingested_at`)
	nutritionSel = vault.Select("nutrition_logs", nutritionCols)
	dailySel     = vault.Select("daily_summaries", dailyCols)
	ecgSel       = vault.Select("ecg_recordings", ecgCols)
)

// parentProvenance reads the provenance a child list repeats on each of its
// records: the provider, source and device of the sleep session or workout
// id, ErrNotFound when the user has none.
func parentProvenance(ctx context.Context, db Querier, keys *vault.Reader, table string, tenantID, userID uuid.UUID, id int64) (Provenance, error) {
	sel := vault.Select(table, `provider, source_slug, device_id`)
	var prov Provenance
	err := sel.Scan(ctx, keys, db.QueryRow(ctx, `SELECT `+sel.List("")+` FROM ohs_data.`+table+`
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3 LIMIT 1`, tenantID, userID, id),
		[]any{&prov.Provider, &prov.Source, &prov.Device})
	if err != nil {
		return Provenance{}, fmt.Errorf("read: %s: %w", table, err)
	}
	return prov, nil
}
