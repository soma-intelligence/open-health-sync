// SPDX-License-Identifier: AGPL-3.0-only

package demo

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// SeedOptions selects what Seed writes.
type SeedOptions struct {
	// Days and Seed are those of Options.
	Days int
	Seed int64
	// ExternalRef names the user: the tenant's user with this ref, created
	// when the tenant has none.
	ExternalRef string
	// Written, when set, is called after each provider's records are
	// written, with what the write stored.
	Written func(provider string, c etl.Counts)
}

// SeedResult is what Seed did.
type SeedResult struct {
	User tenant.User
	// Created is true when Seed created the user.
	Created bool
	// Written counts the rows stored, Skipped the rows that were already
	// there.
	Written, Skipped int
}

// Seed writes the records of Generate for one user of a tenant, through the
// ETL writer: `ohs seed --demo` and the admin API's demo user. It is safe to
// run again: the days already written add nothing. An unknown tenant is
// tenant.ErrNotFound; a user with that ref being deleted is
// tenant.ErrConflict, or etl.ErrUserNotActive when the deletion starts
// during the write.
func Seed(ctx context.Context, store *tenant.Store, tenantID uuid.UUID, o SeedOptions) (SeedResult, error) {
	// Generated first: options Generate refuses create no user.
	batches, err := Generate(Options{Days: o.Days, Seed: o.Seed})
	if err != nil {
		return SeedResult{}, err
	}
	u, created, err := store.EnsureUser(ctx, tenantID, o.ExternalRef)
	if err != nil {
		return SeedResult{}, err
	}
	res := SeedResult{User: u, Created: created}
	for _, b := range batches {
		c, err := etl.WriteSynthetic(ctx, store.Pool(), tenantID, u.ID, b.Provider, b.Records)
		if err != nil {
			return res, fmt.Errorf("%s: %w", b.Provider, err)
		}
		res.Written += c.TotalWritten()
		res.Skipped += c.TotalSkipped()
		if o.Written != nil {
			o.Written(b.Provider, c)
		}
	}
	return res, nil
}
