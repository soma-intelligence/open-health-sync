// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
	"github.com/soma-intelligence/open-health-sync/internal/tenantkeys"
)

// A tenant brings its own keys for a provider: its users then connect
// through the tenant's OAuth application (their consent screen shows it)
// instead of the instance's, and every other tenant keeps the instance's
// keys. A connection keeps the keys it was made with. No secret is ever
// served back.

// Statuses of a tenant's keys in the answers.
const keysNotSet = "not_set"

// adminTenantKeysJSON is how one tenant's keys for one provider stand.
type adminTenantKeysJSON struct {
	Provider string `json:"provider"`
	// Set is true while the tenant has keys for the provider.
	Set bool `json:"set"`
	// Status is active (new connections use them), error (they do not
	// run: Error says why, and the tenant cannot connect the provider until
	// they are fixed or removed) or not_set.
	Status string  `json:"status"`
	Error  *string `json:"error"`
	// ClientID shows the start and end of the client id, never all of it.
	ClientID *string              `json:"client_id"`
	Keys     []adminTenantKeyJSON `json:"keys"`
	// URLs are the addresses to register in the tenant's application:
	// the instance's, the same for every tenant.
	URLs      adminProviderURL `json:"urls"`
	CreatedAt *time.Time       `json:"created_at"`
	UpdatedAt *time.Time       `json:"updated_at"`
	UpdatedBy *string          `json:"updated_by"`
	// ReauthRequired, only in the answers of PUT and DELETE, counts the
	// connections the change set needs_reauth.
	ReauthRequired *int `json:"reauth_required,omitempty"`
}

// adminTenantKeyJSON is one key the provider takes. Its value is never
// served.
type adminTenantKeyJSON struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
	Set      bool   `json:"set"`
}

type adminTenantKeysPut struct {
	Keys map[string]string `json:"keys"`
}

// mountAdminTenantProviderKeys serves a tenant's own provider keys with
// admin, the admin key middleware of mountAdmin.
func (s *server) mountAdminTenantProviderKeys(admin func(string, http.HandlerFunc)) {
	if s.d.TenantKeys == nil {
		return
	}
	const route = "/v1/admin/tenants/{tenant_id}/providers/{provider}/keys"
	admin("GET "+route, s.getAdminTenantKeys)
	admin("PUT "+route, s.putAdminTenantKeys)
	admin("DELETE "+route, s.deleteAdminTenantKeys)
}

// clientIDHint shows the first and last four characters of a client id (the
// first two of a short one).
func clientIDHint(id string) *string {
	if id == "" {
		return nil
	}
	r := []rune(id)
	var out string
	if len(r) <= 12 {
		out = string(r[:min(2, len(r))]) + "..."
	} else {
		out = string(r[:4]) + "..." + string(r[len(r)-4:])
	}
	return &out
}

func (s *server) toAdminTenantKeys(slug string, st tenantkeys.State, set bool) adminTenantKeysJSON {
	required, optional, _ := s.d.TenantKeys.Supports(slug)
	out := adminTenantKeysJSON{Provider: slug, Status: keysNotSet, Keys: []adminTenantKeyJSON{}}
	key := func(name string, req bool) adminTenantKeyJSON {
		return adminTenantKeyJSON{Name: name, Required: req, Secret: connector.SecretKey(name), Set: set && st.Keys.Keys[name] != ""}
	}
	for _, f := range required {
		out.Keys = append(out.Keys, key(f, true))
	}
	for _, f := range optional {
		if !slices.Contains(required, f) {
			out.Keys = append(out.Keys, key(f, false))
		}
	}
	links := collect.Deps{PublicURL: s.d.PublicURL}
	redirect := links.CallbackURL(slug)
	out.URLs.Redirect = &redirect
	if c, ok := s.d.TenantKeys.Template(slug); ok {
		if _, push := connector.AsPush(c); push {
			u := links.InboundURL(slug)
			out.URLs.Inbound = &u
		}
	}
	if !set {
		return out
	}
	out.Set, out.Status = true, st.Status
	if st.Error != "" {
		msg := st.Error
		out.Error = &msg
	}
	out.ClientID = clientIDHint(st.Keys.Keys["CLIENT_ID"])
	c, u := st.CreatedAt, st.UpdatedAt
	out.CreatedAt, out.UpdatedAt = &c, &u
	if st.UpdatedBy != "" {
		by := st.UpdatedBy
		out.UpdatedBy = &by
	}
	return out
}

// keysProvider answers 404 for an unknown provider and 409 for one that
// takes no tenant keys, and reports whether slug takes them.
func (s *server) keysProvider(w http.ResponseWriter, slug string) bool {
	if !s.d.TenantKeys.Known(slug) {
		notFound(w, "provider")
		return false
	}
	if _, _, ok := s.d.TenantKeys.Supports(slug); !ok {
		problem(w, http.StatusConflict, "this provider does not take tenant keys: only OAuth providers do")
		return false
	}
	return true
}

func (s *server) getAdminTenantKeys(w http.ResponseWriter, r *http.Request) {
	t, ok := s.pathTenant(w, r)
	if !ok {
		return
	}
	slug := r.PathValue("provider")
	if !s.keysProvider(w, slug) {
		return
	}
	st, set, err := s.d.TenantKeys.Lookup(r.Context(), t.ID, slug)
	if err != nil {
		internalError(w, r, "api: tenant keys", err)
		return
	}
	writeJSON(w, http.StatusOK, s.toAdminTenantKeys(slug, st, set))
}

// putAdminTenantKeys sets or replaces the tenant's keys. The connector
// checks them before anything is stored. Keys naming another application
// than the ones they replace (another client id, region or environment) set
// the connections made with the old ones needs_reauth. The change and its
// audit event are written together; it applies at once in this process,
// within tenantkeys.ReloadEvery in the others.
func (s *server) putAdminTenantKeys(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	t, ok := s.pathTenant(w, r)
	if !ok {
		return
	}
	slug := r.PathValue("provider")
	if !s.keysProvider(w, slug) {
		return
	}
	var req adminTenantKeysPut
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.Keys == nil {
		badRequest(w, "keys is required: an object of the provider's keys by name")
		return
	}
	ev := adminEvent(r, p, audit.TenantProviderKeys, http.StatusOK, map[string]any{"provider": slug, "set": true})
	st, retired, err := s.d.TenantKeys.Put(r.Context(), t.ID, slug, req.Keys, p.KeyID, &ev)
	var field *providerset.FieldError
	var refused *providerset.RefusedError
	switch {
	case errors.As(err, &field):
		badRequest(w, field.Msg)
		return
	case errors.As(err, &refused):
		problem(w, http.StatusUnprocessableEntity, refused.Msg)
		return
	case errors.Is(err, tenantkeys.ErrUnknownTenant):
		notFound(w, "tenant")
		return
	case err != nil:
		internalError(w, r, "api: tenant keys", err)
		return
	}
	out := s.toAdminTenantKeys(slug, st, true)
	out.ReauthRequired = &retired
	writeJSON(w, http.StatusOK, out)
}

// deleteAdminTenantKeys removes the tenant's keys: its new connections go
// back to the instance's keys, and the connections made with the removed
// keys are set needs_reauth. 404 when the tenant has none.
func (s *server) deleteAdminTenantKeys(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	t, ok := s.pathTenant(w, r)
	if !ok {
		return
	}
	slug := r.PathValue("provider")
	if !s.keysProvider(w, slug) {
		return
	}
	ev := adminEvent(r, p, audit.TenantProviderKeys, http.StatusOK, map[string]any{"provider": slug, "set": false})
	_, retired, found, err := s.d.TenantKeys.Delete(r.Context(), t.ID, slug, &ev)
	if err != nil {
		internalError(w, r, "api: tenant keys", err)
		return
	}
	if !found {
		problem(w, http.StatusNotFound, "the tenant has no keys of its own for this provider")
		return
	}
	out := s.toAdminTenantKeys(slug, tenantkeys.State{}, false)
	out.ReauthRequired = &retired
	writeJSON(w, http.StatusOK, out)
}
