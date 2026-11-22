// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/fusion"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// hoisted remembers the lookups every page of a list repeats for one user:
// that the user is active, that a quantity type is known, and the stored
// merge policy. A read of many pages (an export) checks each once.
type hoisted struct {
	mu       sync.Mutex
	tenantID uuid.UUID
	userID   uuid.UUID
	bound    bool
	user     bool
	types    map[string]bool
	policy   *fusion.Policy
	// keys opens the user's sealed rows: the keys are unwrapped once.
	keys *vault.Reader
}

type hoistKey struct{}

// HoistLookups returns ctx under which the pages of one user's lists look
// the user, the quantity type and the stored merge policy up once instead
// of once per page. It is for one request reading many pages in a row: the
// answers are kept for the life of ctx, so a user deleted meanwhile is only
// seen by CheckUser on the caller's own context, run once the last page is
// read.
func HoistLookups(ctx context.Context) context.Context {
	return context.WithValue(ctx, hoistKey{}, &hoisted{types: map[string]bool{}})
}

// hoist returns the lookups kept on ctx for this user, locked, or nil. The
// first user read under ctx is the one they are kept for.
func hoist(ctx context.Context, tenantID, userID uuid.UUID) *hoisted {
	h, _ := ctx.Value(hoistKey{}).(*hoisted)
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if !h.bound {
		h.tenantID, h.userID, h.bound = tenantID, userID, true
	}
	if h.tenantID != tenantID || h.userID != userID {
		h.mu.Unlock()
		return nil
	}
	return h
}
