// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CodedSample is a quantity sample with the standard codes the quantity type
// catalog gives its type: nil when the catalog has none (or does not know
// the type).
type CodedSample struct {
	Sample
	LOINCCode *string
	// UCUMUnit is the UCUM spelling of StorageUnit.
	UCUMUnit    *string
	StorageUnit *string
}

// codedCols adds the catalog codes of the row's type to sampleCols. Scalar
// subqueries on the catalog's primary key keep run's single-table query.
const codedCols = sampleCols + `,
	(SELECT loinc_code FROM ohs_data.quantity_codes WHERE canonical_code = quantity_type),
	(SELECT ucum_unit FROM ohs_data.quantity_codes WHERE canonical_code = quantity_type),
	(SELECT storage_unit FROM ohs_data.quantity_codes WHERE canonical_code = quantity_type)`

func (s *CodedSample) dest() []any {
	return append(s.Sample.dest(), &s.LOINCCode, &s.UCUMUnit, &s.StorageUnit)
}

// CodedSamples lists quantity samples of every type with their catalog
// codes. q.Type, when set, keeps one type (a type outside the catalog is a
// QueryError, as for Samples); loinc, when set, keeps the types the catalog
// codes with that LOINC code (none is an empty page, not an error).
func CodedSamples(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query, loinc string) (Page[CodedSample], error) {
	n, err := q.normalize(false)
	if err != nil {
		return Page[CodedSample]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[CodedSample]{}, err
	}
	if n.Type != "" {
		if err := knownQuantityType(ctx, db, tenantID, userID, n.Type); err != nil {
			return Page[CodedSample]{}, err
		}
	}
	l := list{table: "ohs_data.quantity_samples", timeCol: "start_date", typeCol: "quantity_type",
		cols: codedCols, loinc: strings.TrimSpace(loinc)}
	return run(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (CodedSample, time.Time, int64, error) {
		var s CodedSample
		err := r.Scan(s.dest()...)
		s.utc()
		return s, s.StartDate, s.ID, err
	})
}

// CodedSampleByID reads one quantity sample of the user by its id, with its
// catalog codes. ErrNotFound when the user or the sample is not the tenant's.
func CodedSampleByID(ctx context.Context, db Querier, tenantID, userID uuid.UUID, id int64) (CodedSample, error) {
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return CodedSample{}, err
	}
	var s CodedSample
	err := db.QueryRow(ctx, `SELECT `+codedCols+` FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3 LIMIT 1`, tenantID, userID, id).Scan(s.dest()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return CodedSample{}, ErrNotFound
	}
	if err != nil {
		return CodedSample{}, fmt.Errorf("read: quantity sample: %w", err)
	}
	s.utc()
	return s, nil
}

// CheckUser returns ErrNotFound unless user is an active user of tenant.
func CheckUser(ctx context.Context, db Querier, tenantID, userID uuid.UUID) error {
	return userExists(ctx, db, tenantID, userID)
}

// Check reports a mistake in q (a QueryError or ErrInvalidCursor) without
// running it.
func (q Query) Check() error {
	_, err := q.normalize(false)
	return err
}
