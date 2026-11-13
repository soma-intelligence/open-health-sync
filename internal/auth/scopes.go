// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"errors"
	"slices"
	"strings"
)

// The scopes of an API key: what the key may do in its tenant. Every route
// behind a credential needs exactly one of them (internal/api maps each
// route to its scope). A scope ending in :write also grants the :read of
// the same resource; ScopeAll grants every scope.
const (
	// ScopeAll is every scope, present and future: what a key created
	// without scopes holds.
	ScopeAll = "*"
	// ScopeTenantRead reads the tenant of the key and the providers of the
	// instance.
	ScopeTenantRead = "tenant:read"
	// ScopeUsersRead and ScopeUsersWrite read and change users and their
	// consents.
	ScopeUsersRead  = "users:read"
	ScopeUsersWrite = "users:write"
	// ScopeTokensWrite mints and revokes user tokens. A token never does
	// more than the key that minted it (TokenNeeds).
	ScopeTokensWrite = "tokens:write"
	// ScopeDataRead reads health data, in every format, serves /mcp and
	// streams one user's events (GET /v1/users/{id}/events/stream).
	ScopeDataRead = "data:read"
	// ScopeDataWrite uploads data: ingest, self reports, tags and source
	// priorities.
	ScopeDataWrite = "data:write"
	// ScopeDataDelete erases a user or one provider's data of a user.
	ScopeDataDelete = "data:delete"
	// ScopeConnectionsRead and ScopeConnectionsWrite read and run provider
	// connections, their sync runs and the pipeline.
	ScopeConnectionsRead  = "connections:read"
	ScopeConnectionsWrite = "connections:write"
	// ScopeWebhooksRead and ScopeWebhooksWrite read and change webhook
	// endpoints and their deliveries. ScopeWebhooksRead also streams the
	// tenant's events (GET /v1/events/stream), the payloads deliveries carry.
	ScopeWebhooksRead  = "webhooks:read"
	ScopeWebhooksWrite = "webhooks:write"
	// ScopeExportsRead downloads export files and user archives and reads
	// share links; ScopeExportsWrite creates and revokes share links.
	ScopeExportsRead  = "exports:read"
	ScopeExportsWrite = "exports:write"
	// ScopeKeysRead and ScopeKeysWrite list, create and revoke the API keys
	// of the tenant.
	ScopeKeysRead  = "keys:read"
	ScopeKeysWrite = "keys:write"
	// ScopeAuditRead reads the audit log of the tenant.
	ScopeAuditRead = "audit:read"
	// ScopeAdminRead and ScopeAdminWrite read and change the instance
	// (/v1/admin). They count only on a key of an instance admin tenant.
	ScopeAdminRead  = "admin:read"
	ScopeAdminWrite = "admin:write"
)

// KeyScopes lists every scope a key can hold, in the order of the docs.
var KeyScopes = []string{
	ScopeAll,
	ScopeTenantRead,
	ScopeUsersRead, ScopeUsersWrite,
	ScopeTokensWrite,
	ScopeDataRead, ScopeDataWrite, ScopeDataDelete,
	ScopeConnectionsRead, ScopeConnectionsWrite,
	ScopeWebhooksRead, ScopeWebhooksWrite,
	ScopeExportsRead, ScopeExportsWrite,
	ScopeKeysRead, ScopeKeysWrite,
	ScopeAuditRead,
	ScopeAdminRead, ScopeAdminWrite,
}

// ErrInvalidKeyScopes means a list of key scopes names a scope that does
// not exist (KeyScopes).
var ErrInvalidKeyScopes = errors.New("auth: unknown API key scope")

// NormalizeKeyScopes checks scopes and returns them without duplicates, in
// the order of KeyScopes. An empty list, and any list holding ScopeAll, is
// ScopeAll alone. A scope that is not in KeyScopes is ErrInvalidKeyScopes.
func NormalizeKeyScopes(scopes []string) ([]string, error) {
	for _, sc := range scopes {
		if !slices.Contains(KeyScopes, sc) {
			return nil, ErrInvalidKeyScopes
		}
	}
	if len(scopes) == 0 || slices.Contains(scopes, ScopeAll) {
		return []string{ScopeAll}, nil
	}
	out := make([]string, 0, len(scopes))
	for _, sc := range KeyScopes {
		if slices.Contains(scopes, sc) {
			out = append(out, sc)
		}
	}
	return out, nil
}

// Grants reports whether a key holding held may do what scope names: held
// is nil (a credential tied to no scoped key), or holds scope, ScopeAll, or
// the :write of a :read scope.
func Grants(held []string, scope string) bool {
	if held == nil || slices.Contains(held, ScopeAll) || slices.Contains(held, scope) {
		return true
	}
	if resource, ok := strings.CutSuffix(scope, ":read"); ok {
		return slices.Contains(held, resource+":write")
	}
	return false
}

// Can reports whether the caller's key scopes grant scope (Grants). For a
// user token they are the scopes of the key that minted it.
func (p Principal) Can(scope string) bool { return Grants(p.KeyScopes, scope) }

// TokenNeeds is the key scope a key must hold to mint a user token with the
// given token scope: a token never does more than its key. Empty for a
// token scope that does not exist.
func TokenNeeds(tokenScope Scope) string {
	switch tokenScope {
	case UserUpload:
		return ScopeDataWrite
	case UserConnect:
		return ScopeConnectionsWrite
	case UserRead:
		return ScopeDataRead
	}
	return ""
}
