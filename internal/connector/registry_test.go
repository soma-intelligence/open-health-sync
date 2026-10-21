// SPDX-License-Identifier: AGPL-3.0-only

package connector

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

type base struct{ slug string }

func (b base) Slug() string                           { return b.slug }
func (b base) Enabled(cfg Config) bool                { return cfg["CLIENT_ID"] != "" }
func (b base) Map(RawRecord) (model.MapResult, error) { return model.MapResult{}, nil }

// fakeOAuth is keyed and pulls with OAuth.
type fakeOAuth struct{ base }

func (fakeOAuth) RequiredKeys() []string                      { return []string{"CLIENT_ID", "CLIENT_SECRET"} }
func (fakeOAuth) AuthorizeURL(string, string) (string, error) { return "", nil }
func (fakeOAuth) Exchange(context.Context, string, string) (Tokens, error) {
	return Tokens{}, nil
}
func (fakeOAuth) Refresh(context.Context, Tokens) (Tokens, error) { return Tokens{}, ErrNeedsReauth }
func (fakeOAuth) Fetch(context.Context, Connection, time.Time, time.Time, RawSink) error {
	return nil
}

// fakeDevice has no keys.
type fakeDevice struct{ base }

func (fakeDevice) ParseUpload(UploadMeta, io.Reader, RawSink) error { return nil }

// fakeKeyedOff is keyed but its own Enabled always says no.
type fakeKeyedOff struct{ fakeOAuth }

func (fakeKeyedOff) Enabled(Config) bool { return false }

// fakeUnkeyedOAuth pulls with OAuth but declares no RequiredKeys, so its own
// Enabled decides (base: CLIENT_ID present).
type fakeUnkeyedOAuth struct{ base }

func (fakeUnkeyedOAuth) AuthorizeURL(string, string) (string, error) { return "", nil }
func (fakeUnkeyedOAuth) Exchange(context.Context, string, string) (Tokens, error) {
	return Tokens{}, nil
}
func (fakeUnkeyedOAuth) Refresh(context.Context, Tokens) (Tokens, error) { return Tokens{}, nil }
func (fakeUnkeyedOAuth) Fetch(context.Context, Connection, time.Time, time.Time, RawSink) error {
	return nil
}

// fakeOAuthPush is both OAuth and Push.
type fakeOAuthPush struct{ fakeOAuth }

func (fakeOAuthPush) HandleInbound(context.Context, *http.Request) ([]InboundEvent, error) {
	return nil, nil
}

func newReg() *Registry {
	r := NewRegistry()
	r.Register(fakeOAuth{base{"withings"}})
	r.Register(fakeDevice{base{"apple_health"}})
	return r
}

func slugs(cs []Connector) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Slug())
	}
	return out
}

func TestDuplicateRegisterPanics(t *testing.T) {
	r := newReg()
	require.Panics(t, func() { r.Register(fakeDevice{base{"apple_health"}}) })
	require.Panics(t, func() { r.Register(fakeDevice{base{""}}) })
}

func TestGet(t *testing.T) {
	r := newReg()
	c, ok := r.Get("withings")
	require.True(t, ok)
	require.Equal(t, "withings", c.Slug())
	_, ok = r.Get("nope")
	require.False(t, ok)
}

func TestEnabled(t *testing.T) {
	r := newReg()
	r.Register(fakeKeyedOff{fakeOAuth{base{"off"}}})
	r.Register(fakeUnkeyedOAuth{base{"plain"}})
	pk := func(kv ...string) config.ProviderKeys {
		m := config.ProviderKeys{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	cases := []struct {
		name string
		cfg  map[string]config.ProviderKeys
		want []string
	}{
		{"nil config", nil, []string{"apple_health"}},
		{"complete keys", map[string]config.ProviderKeys{"withings": pk("CLIENT_ID", "a", "CLIENT_SECRET", "b")}, []string{"apple_health", "withings"}},
		{"missing secret", map[string]config.ProviderKeys{"withings": pk("CLIENT_ID", "a")}, []string{"apple_health"}},
		{"empty value is missing", map[string]config.ProviderKeys{"withings": pk("CLIENT_ID", "a", "CLIENT_SECRET", "")}, []string{"apple_health"}},
		{"keyed connector own Enabled false is excluded", map[string]config.ProviderKeys{"off": pk("CLIENT_ID", "a", "CLIENT_SECRET", "b"), "withings": pk("CLIENT_ID", "a", "CLIENT_SECRET", "b")}, []string{"apple_health", "withings"}},
		{"unkeyed oauth without keys excluded", nil, []string{"apple_health"}},
		{"unkeyed oauth with keys included", map[string]config.ProviderKeys{"plain": pk("CLIENT_ID", "a")}, []string{"apple_health", "plain"}},
		{"unknown entries ignored", map[string]config.ProviderKeys{"terms": pk("X", "y"), "ghost": pk("CLIENT_ID", "a")}, []string{"apple_health"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, slugs(r.Enabled(tc.cfg)))
		})
	}
}

func TestEnabledSortedBySlug(t *testing.T) {
	r := NewRegistry()
	order := []string{"zeta", "mike", "alpha", "tango", "bravo", "kilo"}
	for _, s := range order {
		r.Register(fakeDevice{base{s}})
	}
	want := []string{"alpha", "bravo", "kilo", "mike", "tango", "zeta"}
	for i := 0; i < 20; i++ { // map iteration order is random
		require.Equal(t, want, slugs(r.Enabled(nil)))
	}
}

func TestCapabilities(t *testing.T) {
	var o Connector = fakeOAuth{base{"withings"}}
	var d Connector = fakeDevice{base{"apple_health"}}

	_, ok := AsOAuth(o)
	require.True(t, ok)
	_, ok = AsOAuth(d)
	require.False(t, ok)
	_, ok = AsDevice(d)
	require.True(t, ok)
	_, ok = AsDevice(o)
	require.False(t, ok)
	_, ok = AsPush(o)
	require.False(t, ok)
	_, ok = AsPush(d)
	require.False(t, ok)

	var op Connector = fakeOAuthPush{fakeOAuth{base{"both"}}}
	_, ok = AsOAuth(op)
	require.True(t, ok)
	_, ok = AsPush(op)
	require.True(t, ok)
	_, ok = AsDevice(op)
	require.False(t, ok)
}

func TestDefaultRegistryDelegates(t *testing.T) {
	_, ok := Get("definitely-not-registered")
	require.False(t, ok)
	require.Empty(t, Enabled(nil))
}

func TestSecretKey(t *testing.T) {
	for _, k := range []string{"CLIENT_ID", "REGION", "ENVIRONMENT", "WEBHOOK_SIGNATURE", "SUBSCRIPTION_ID"} {
		require.False(t, SecretKey(k), k)
	}
	for _, k := range []string{"CLIENT_SECRET", "WEBHOOK_SECRET", "VERIFICATION_TOKEN", "VERIFY_TOKEN",
		"SUBSCRIPTION_KEY", "NOTIFICATION_SECRET", "SOMETHING_NEW", ""} {
		require.True(t, SecretKey(k), "%q is a secret unless known otherwise", k)
	}
}
