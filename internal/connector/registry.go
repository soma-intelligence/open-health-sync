// SPDX-License-Identifier: AGPL-3.0-only

package connector

import (
	"fmt"
	"sort"
	"sync"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

// Registry holds connectors by slug. Tests build their own; production code
// uses the package level default through Register, Get and Enabled.
type Registry struct {
	mu   sync.RWMutex
	byID map[string]Connector
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{byID: map[string]Connector{}} }

// Register adds c. It panics on an empty or duplicate slug, as registration
// happens in init() and a clash is a programming error.
func (r *Registry) Register(c Connector) {
	slug := c.Slug()
	if slug == "" {
		panic("connector: empty slug")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[slug]; dup {
		panic(fmt.Sprintf("connector: duplicate slug %q", slug))
	}
	r.byID[slug] = c
}

// Get returns the connector registered under slug.
func (r *Registry) Get(slug string) (Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byID[slug]
	return c, ok
}

// Enabled returns the registered connectors that are usable with cfg, sorted
// by slug. Only registered slugs are looked at; unknown or empty entries in
// cfg are ignored. A KeyedConnector needs every required key non-empty and its
// own Enabled to agree. A DeviceConnector without RequiredKeys is always
// enabled; any other connector without RequiredKeys falls back to its own
// Enabled.
func (r *Registry) Enabled(cfg map[string]config.ProviderKeys) []Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Connector, 0, len(r.byID))
	for slug, c := range r.byID {
		keys := cfg[slug]
		k, keyed := c.(KeyedConnector)
		if !keyed {
			// Device connectors need no operator keys. Anything else
			// decides for itself.
			if _, device := c.(DeviceConnector); device || c.Enabled(keys) {
				out = append(out, c)
			}
			continue
		}
		ok := true
		for _, f := range k.RequiredKeys() {
			if keys[f] == "" {
				ok = false
				break
			}
		}
		if ok && c.Enabled(keys) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug() < out[j].Slug() })
	return out
}

// All returns every registered connector, sorted by slug.
func (r *Registry) All() []Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Connector, 0, len(r.byID))
	for _, c := range r.byID {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug() < out[j].Slug() })
	return out
}

var defaultRegistry = NewRegistry()

// Register adds c to the default registry.
// A slug holding an underscore is declared to the configuration loader, so
// its OHS_<SLUG>_<FIELD> keys are read under the whole slug.
func Register(c Connector) {
	defaultRegistry.Register(c)
	config.RegisterSlug(c.Slug())
}

// Get looks slug up in the default registry.
func Get(slug string) (Connector, bool) { return defaultRegistry.Get(slug) }

// All lists every connector of the default registry.
func All() []Connector { return defaultRegistry.All() }

// Enabled lists the enabled connectors of the default registry.
func Enabled(cfg map[string]config.ProviderKeys) []Connector { return defaultRegistry.Enabled(cfg) }
