// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
)

// adminProviderJSON is how one registered connector stands on the instance.
type adminProviderJSON struct {
	Slug  string   `json:"slug"`
	Name  string   `json:"name"`
	Kinds []string `json:"kinds"`
	// Status is active, off, not_configured, error or built_in.
	Status  string `json:"status"`
	Enabled bool   `json:"enabled"`
	// Source is env or console, null when no key is set.
	Source *string          `json:"source"`
	Keys   []adminKeyJSON   `json:"keys"`
	Error  *string          `json:"error"`
	URLs   adminProviderURL `json:"urls"`
	// Sync is how the collect role pulls the provider, null for one it
	// never pulls (an upload from an app).
	Sync *adminSyncJSON `json:"sync"`
	// UpdatedAt and UpdatedBy (an API key id) are of the last change saved
	// from the console, null when none was.
	UpdatedAt *time.Time `json:"updated_at"`
	UpdatedBy *string    `json:"updated_by"`
}

// adminKeyJSON is one key of a provider. Value is only served for a key that
// is not a secret.
type adminKeyJSON struct {
	Name     string  `json:"name"`
	Env      string  `json:"env"`
	Required bool    `json:"required"`
	Secret   bool    `json:"secret"`
	Set      bool    `json:"set"`
	Value    *string `json:"value"`
}

// adminProviderURL are the addresses to register at the provider.
type adminProviderURL struct {
	// Redirect is the OAuth redirect URI, for a provider users connect to.
	Redirect *string `json:"redirect"`
	// Inbound receives the provider's notifications, for a push provider.
	Inbound *string `json:"inbound"`
}

// adminSyncJSON is a provider's sync policy (connector.SyncPolicy), in
// seconds. ReconcileSeconds is 0 when the daily reconciliation is off.
type adminSyncJSON struct {
	PullEverySeconds            int64 `json:"pull_every_seconds"`
	PullEveryWithoutPushSeconds int64 `json:"pull_every_without_push_seconds"`
	LookbackSeconds             int64 `json:"lookback_seconds"`
	MaxCatchUpSeconds           int64 `json:"max_catch_up_seconds"`
	BackfillSeconds             int64 `json:"backfill_seconds"`
	BackfillChunkSeconds        int64 `json:"backfill_chunk_seconds"`
	ReconcileSeconds            int64 `json:"reconcile_seconds"`
}

// syncJSON is the sync policy the scheduler runs c on, nil when the server
// never pulls c.
func syncJSON(c connector.Connector) *adminSyncJSON {
	if _, ok := connector.AsOAuth(c); !ok {
		return nil
	}
	p := connector.SyncPolicyOf(c)
	sec := func(d time.Duration) int64 { return int64(d / time.Second) }
	return &adminSyncJSON{
		PullEverySeconds: sec(p.PullEvery), PullEveryWithoutPushSeconds: sec(p.PullEveryWithoutPush),
		LookbackSeconds: sec(p.Lookback), MaxCatchUpSeconds: sec(p.MaxCatchUp),
		BackfillSeconds: sec(p.Backfill), BackfillChunkSeconds: sec(p.BackfillChunk),
		ReconcileSeconds: sec(p.Reconcile),
	}
}

type adminProviderPatch struct {
	Keys    map[string]*string `json:"keys"`
	Enabled *bool              `json:"enabled"`
}

// mountAdmin serves the instance settings and the tenants to a key of the
// instance admin tenant. Provider keys are instance wide: every tenant's
// users connect through the same provider applications.
func (s *server) mountAdmin(mux *http.ServeMux) {
	admin := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, auth.Middleware(s.lookup, auth.TenantAdmin)(h))
	}
	s.mountAdminTenants(admin)
	if s.d.Providers == nil {
		return
	}
	admin("GET /v1/admin/providers", s.listAdminProviders)
	admin("GET /v1/admin/providers/{provider}", s.getAdminProvider)
	admin("PATCH /v1/admin/providers/{provider}", s.updateAdminProvider)
	admin("DELETE /v1/admin/providers/{provider}", s.forgetAdminProvider)
}

func (s *server) toAdminProvider(st providerset.State) adminProviderJSON {
	c := st.Connector
	out := adminProviderJSON{Slug: st.Slug, Name: collect.ProviderName(st.Slug), Kinds: kinds(c), Status: st.Status,
		Enabled: st.Enabled, Keys: []adminKeyJSON{}, UpdatedAt: st.UpdatedAt, Sync: syncJSON(c)}
	if st.Source != "" {
		src := st.Source
		out.Source = &src
	}
	if st.Error != "" {
		msg := st.Error
		out.Error = &msg
	}
	if st.UpdatedBy != "" {
		by := st.UpdatedBy
		out.UpdatedBy = &by
	}
	key := func(name string, required bool) adminKeyJSON {
		k := adminKeyJSON{Name: name, Env: providerset.EnvName(st.Slug, name), Required: required,
			Secret: connector.SecretKey(name), Set: st.Keys[name] != ""}
		if k.Set && !k.Secret {
			v := st.Keys[name]
			k.Value = &v
		}
		return k
	}
	for _, f := range st.Required {
		out.Keys = append(out.Keys, key(f, true))
	}
	for _, f := range st.Optional {
		if !slices.Contains(st.Required, f) {
			out.Keys = append(out.Keys, key(f, false))
		}
	}
	links := collect.Deps{PublicURL: s.d.PublicURL}
	// A provider whose users sign in on this server (Eight Sleep) has no
	// callback to register.
	if oc, ok := connector.AsOAuth(c); ok && !isPassword(oc) {
		u := links.CallbackURL(st.Slug)
		out.URLs.Redirect = &u
	}
	if _, ok := connector.AsPush(c); ok {
		u := links.InboundURL(st.Slug)
		out.URLs.Inbound = &u
	}
	return out
}

func (s *server) listAdminProviders(w http.ResponseWriter, r *http.Request) {
	states := s.d.Providers.States()
	out := make([]adminProviderJSON, 0, len(states))
	for _, st := range states {
		out = append(out, s.toAdminProvider(st))
	}
	// public_url is false when OHS_PUBLIC_URL is not set: the URLs above
	// then name localhost, and no provider can reach them.
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "public_url": s.d.PublicURL != ""})
}

func (s *server) getAdminProvider(w http.ResponseWriter, r *http.Request) {
	st, ok := s.d.Providers.State(r.PathValue("provider"))
	if !ok {
		notFound(w, "provider")
		return
	}
	writeJSON(w, http.StatusOK, s.toAdminProvider(st))
}

func (s *server) updateAdminProvider(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	var req adminProviderPatch
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.Keys == nil && req.Enabled == nil {
		badRequest(w, "the body changes nothing: give keys, enabled or both")
		return
	}
	st, err := s.d.Providers.Update(r.Context(), r.PathValue("provider"),
		providerset.Change{Keys: req.Keys, Enabled: req.Enabled}, p.KeyID)
	if !providerErr(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, s.toAdminProvider(st))
}

func (s *server) forgetAdminProvider(w http.ResponseWriter, r *http.Request) {
	st, found, err := s.d.Providers.Forget(r.Context(), r.PathValue("provider"))
	if !providerErr(w, r, err) {
		return
	}
	if !found {
		problem(w, http.StatusNotFound, "the console saved nothing for this provider")
		return
	}
	writeJSON(w, http.StatusOK, s.toAdminProvider(st))
}

// providerErr answers the errors of the provider set and reports whether
// err was nil.
func providerErr(w http.ResponseWriter, r *http.Request, err error) bool {
	var field *providerset.FieldError
	var refused *providerset.RefusedError
	switch {
	case err == nil:
		return true
	case errors.Is(err, providerset.ErrUnknown):
		notFound(w, "provider")
	case errors.Is(err, providerset.ErrBuiltIn):
		problem(w, http.StatusConflict, "this provider needs no key and is always on")
	case errors.Is(err, providerset.ErrEnvKeys):
		problem(w, http.StatusConflict, "the keys of this provider come from its OHS_ environment variables: "+
			"change them there, or remove them to manage the keys here; enabled can still be changed")
	case errors.As(err, &field):
		badRequest(w, field.Msg)
	case errors.As(err, &refused):
		problem(w, http.StatusUnprocessableEntity, refused.Msg)
	default:
		internalError(w, r, "api: provider settings", err)
	}
	return false
}

// isPassword reports whether c signs users in with their provider password.
func isPassword(c connector.Connector) bool { _, ok := connector.AsPassword(c); return ok }
