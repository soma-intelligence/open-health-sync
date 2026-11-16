// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

// sealedReads returns the context of a request with the vault on it, so the
// read layer (internal/api/read, and through it FHIR, Open mHealth, MCP, the
// exports and the share links) and the time tags open the sealed rows of the
// user they read. Without a vault the context is returned as it is: the
// reads then serve clear rows and fail on a sealed one.
//
// instrument calls it where it builds the request's context: the request
// the mux matches must stay the one instrument holds, which reads the
// matched pattern from it.
func (s *server) sealedReads(ctx context.Context) context.Context {
	return read.WithVault(ctx, s.d.Vault)
}
