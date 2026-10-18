// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
	"github.com/soma-intelligence/open-health-sync/internal/tenantproviders"
)

// An instance admin turns a provider off, or back on, for one tenant. The
// instance runs a provider for every tenant (its keys are instance wide);
// this switch narrows it to the tenants that should have it. While it is off
// the tenant's users cannot connect, sync or upload to it and no pull runs;
// the data already stored stays readable and the connections are kept, so
// they resume when it is turned back on.

// adminTenantProviderJSON is how one provider stands for one tenant.
type adminTenantProviderJSON struct {
	Slug  string   `json:"slug"`
	Name  string   `json:"name"`
	Kinds []string `json:"kinds"`
	// InstanceStatus is the provider's status on the instance: active, off,
	// not_configured, error or built_in (GET /v1/admin/providers).
	InstanceStatus string `json:"instance_status"`
	// Enabled is the tenant's switch, true unless an admin turned it off.
	Enabled bool `json:"enabled"`
	// Available is true when the tenant's users can use it now: the
	// instance runs it (active or built_in) and the switch is on.
	Available bool `json:"available"`
	// UpdatedAt is the last change of the switch, null when never set.
	UpdatedAt *time.Time `json:"updated_at"`
}

type adminTenantProviderPatch struct {
	Enabled *bool `json:"enabled"`
}

// mountAdminTenantProviders serves the switches with admin, the admin key
// middleware of mountAdmin. Without the provider set or the switches the
// routes are left out.
func (s *server) mountAdminTenantProviders(admin func(string, http.HandlerFunc)) {
	if s.d.Providers == nil || s.d.TenantProviders == nil {
		return
	}
	admin("GET /v1/admin/tenants/{tenant_id}/providers", s.listAdminTenantProviders)
	admin("PATCH /v1/admin/tenants/{tenant_id}/providers/{provider}", s.updateAdminTenantProvider)
}

func toAdminTenantProvider(st providerset.State, set tenantproviders.Setting, saved bool) adminTenantProviderJSON {
	out := adminTenantProviderJSON{Slug: st.Slug, Name: collect.ProviderName(st.Slug), Kinds: kinds(st.Connector),
		InstanceStatus: st.Status, Enabled: true}
	if saved {
		out.Enabled = set.Enabled
		t := set.UpdatedAt.UTC()
		out.UpdatedAt = &t
	}
	runs := st.Status == providerset.StatusActive || st.Status == providerset.StatusBuiltIn
	out.Available = runs && out.Enabled
	return out
}

// listAdminTenantProviders lists every provider the instance knows, in the
// order of GET /v1/admin/providers, with the tenant's switch.
func (s *server) listAdminTenantProviders(w http.ResponseWriter, r *http.Request) {
	t, ok := s.pathTenant(w, r)
	if !ok {
		return
	}
	saved, err := s.d.TenantProviders.Tenant(r.Context(), t.ID)
	if err != nil {
		internalError(w, r, "api: tenant providers", err)
		return
	}
	states := s.d.Providers.States()
	out := make([]adminTenantProviderJSON, 0, len(states))
	for _, st := range states {
		set, ok := saved[st.Slug]
		out = append(out, toAdminTenantProvider(st, set, ok))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// updateAdminTenantProvider turns a provider off or back on for the tenant.
// The change and its event on the tenant's audit log are written together,
// and the switch applies in this process at once, in the others within
// tenantproviders.ReloadEvery.
func (s *server) updateAdminTenantProvider(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	t, ok := s.pathTenant(w, r)
	if !ok {
		return
	}
	slug := r.PathValue("provider")
	st, ok := s.d.Providers.State(slug)
	if !ok {
		notFound(w, "provider")
		return
	}
	var req adminTenantProviderPatch
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.Enabled == nil {
		badRequest(w, "enabled is required: true turns the provider on for the tenant, false turns it off")
		return
	}
	ev := adminEvent(r, p, audit.TenantProvider, http.StatusOK, map[string]any{"provider": slug, "enabled": *req.Enabled})
	set, err := s.d.TenantProviders.Update(r.Context(), t.ID, slug, *req.Enabled, p.KeyID, &ev)
	switch {
	case errors.Is(err, tenantproviders.ErrUnknownTenant):
		notFound(w, "tenant")
		return
	case err != nil:
		internalError(w, r, "api: tenant provider", err)
		return
	}
	writeJSON(w, http.StatusOK, toAdminTenantProvider(st, set, true))
}
