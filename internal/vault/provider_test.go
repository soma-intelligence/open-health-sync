// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
)

func rootKey(n byte) []byte { return bytes.Repeat([]byte{n}, 32) }

func TestLocalWrapUnwrap(t *testing.T) {
	ctx := context.Background()
	p, err := newLocalProvider(rootKey(1), nil)
	require.NoError(t, err)
	require.Equal(t, "local", p.Name())
	plain, aad := []byte("tenant key material 32 bytes!!!!"), []byte("tenant-1")
	w, ver, err := p.Wrap(ctx, plain, aad)
	require.NoError(t, err)
	id := crypto.KeyID(rootKey(1))
	require.Equal(t, hex.EncodeToString(id[:]), ver)
	require.Equal(t, byte(0x01), w[0])
	require.Len(t, w, 1+24+len(plain)+16)
	require.False(t, bytes.Contains(w, plain))
	got, err := p.Unwrap(ctx, w, aad, ver)
	require.NoError(t, err)
	require.Equal(t, plain, got)

	_, err = p.Unwrap(ctx, w, []byte("tenant-2"), ver)
	require.Error(t, err, "wrong aad")
	w2, _, _ := p.Wrap(ctx, plain, aad)
	require.NotEqual(t, w, w2, "random nonce")
	bad := append([]byte(nil), w...)
	bad[len(bad)-1] ^= 1
	_, err = p.Unwrap(ctx, bad, aad, ver)
	require.Error(t, err)
	_, err = p.Unwrap(ctx, w[:10], aad, ver)
	require.Error(t, err)
	_, err = p.Unwrap(ctx, w, aad, "deadbeef")
	require.Error(t, err, "unknown version")
}

func TestLocalPreviousKeyUnwrapsActiveWraps(t *testing.T) {
	ctx := context.Background()
	old, _ := newLocalProvider(rootKey(1), nil)
	w, oldVer, _ := old.Wrap(ctx, []byte("k"), []byte("a"))
	rotated, err := newLocalProvider(rootKey(2), [][]byte{rootKey(1)})
	require.NoError(t, err)
	got, err := rotated.Unwrap(ctx, w, []byte("a"), oldVer)
	require.NoError(t, err)
	require.Equal(t, []byte("k"), got)
	_, newVer, _ := rotated.Wrap(ctx, []byte("k"), []byte("a"))
	require.NotEqual(t, oldVer, newVer, "the active key wraps")
	_, err = old.Unwrap(ctx, mustWrap(t, rotated), []byte("a"), newVer)
	require.Error(t, err, "the old process does not know the new key")
}

func mustWrap(t *testing.T, p KeyProvider) []byte {
	t.Helper()
	w, _, err := p.Wrap(context.Background(), []byte("k"), []byte("a"))
	require.NoError(t, err)
	return w
}

func TestLocalRejectsBadKeys(t *testing.T) {
	_, err := newLocalProvider([]byte("short"), nil)
	require.Error(t, err)
	_, err = newLocalProvider(rootKey(1), [][]byte{rootKey(1)})
	require.Error(t, err)
}

func TestOpenProviderLocalFromConfig(t *testing.T) {
	cfg, err := config.Load(func(k string) (string, bool) {
		m := map[string]string{
			"OHS_DATABASE_URL":   "postgres://x",
			"OHS_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(rootKey(9)),
		}
		v, ok := m[k]
		return v, ok
	}, nil)
	require.NoError(t, err)
	require.True(t, cfg.Vault.RootDerived)
	p, err := OpenProvider(context.Background(), cfg.Vault)
	require.NoError(t, err)
	w, ver, err := p.Wrap(context.Background(), []byte("k"), nil)
	require.NoError(t, err)
	// The derived key is the one HKDF gives, not the encryption key.
	want, _ := config.DeriveVaultRootKey(rootKey(9))
	ref, _ := newLocalProvider(want, nil)
	got, err := ref.Unwrap(context.Background(), w, nil, ver)
	require.NoError(t, err)
	require.Equal(t, []byte("k"), got)
}

func TestOpenProviderNotBuiltIn(t *testing.T) {
	for _, name := range []string{"azurekv", "awskms", "nope"} {
		_, err := OpenProvider(context.Background(), config.Vault{Provider: name})
		require.ErrorContains(t, err, "not built into this binary", name)
	}
}

// fakeTransit is a stand-in for the transit routes OpenProvider and the
// provider call. It "encrypts" by prefixing and binds the context.
type fakeTransit struct {
	t       *testing.T
	derived bool
	status  int // forced status for encrypt and decrypt, 0 for none
	mu      sync.Mutex
	tokens  []string
}

func (f *fakeTransit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.tokens = append(f.tokens, r.Header.Get("X-Vault-Token"))
	f.mu.Unlock()
	reply := func(code int, v any) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	data := func(m map[string]any) map[string]any { return map[string]any{"data": m} }
	if r.Header.Get("X-Vault-Token") != "s.token" {
		reply(403, map[string]any{"errors": []string{"permission denied"}})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/transit/keys/ohs":
		reply(200, data(map[string]any{"derived": f.derived, "type": "aes256-gcm96"}))
		return
	case f.status != 0 && r.Method == http.MethodPost:
		reply(f.status, map[string]any{"errors": []string{"boom"}})
		return
	}
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch r.URL.Path {
	case "/v1/transit/encrypt/ohs":
		ct := "vault:v3:" + body["context"] + "." + body["plaintext"]
		reply(200, data(map[string]any{"ciphertext": ct}))
	case "/v1/transit/decrypt/ohs":
		rest := strings.TrimPrefix(body["ciphertext"], "vault:v3:")
		ctxPart, pt, _ := strings.Cut(rest, ".")
		if ctxPart != body["context"] {
			reply(400, map[string]any{"errors": []string{"cipher: message authentication failed"}})
			return
		}
		reply(200, data(map[string]any{"plaintext": pt}))
	default:
		reply(404, map[string]any{"errors": []string{"no handler"}})
	}
}

func hcConfig(srv *httptest.Server, tokenFile string) config.Vault {
	return config.Vault{Provider: "hashicorp", HashiCorpAddr: srv.URL + "/", HashiCorpMount: "transit", HashiCorpKey: "ohs", HashiCorpTokenFile: tokenFile}
}

func TestHashiCorpWrapUnwrap(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "s.token")
	f := &fakeTransit{t: t, derived: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()
	p, err := OpenProvider(ctx, hcConfig(srv, ""))
	require.NoError(t, err)
	require.Equal(t, "hashicorp", p.Name())
	plain, aad := []byte("tenant key"), []byte("tenant-1")
	w, ver, err := p.Wrap(ctx, plain, aad)
	require.NoError(t, err)
	require.Equal(t, "vault:v3", ver)
	got, err := p.Unwrap(ctx, w, aad, ver)
	require.NoError(t, err)
	require.Equal(t, plain, got)

	_, err = p.Unwrap(ctx, w, []byte("tenant-2"), ver)
	require.Error(t, err, "wrong aad")
	require.False(t, errors.Is(err, ErrProviderUnavailable), "a refusal is not an outage")
	require.NotContains(t, err.Error(), "s.token")
	_, err = p.Unwrap(ctx, w, aad, "vault:v1")
	require.Error(t, err, "version mismatch")
}

func TestHashiCorpRefusesKeyThatIsNotDerived(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "s.token")
	srv := httptest.NewServer(&fakeTransit{t: t, derived: false})
	defer srv.Close()
	_, err := OpenProvider(context.Background(), hcConfig(srv, ""))
	require.ErrorContains(t, err, "not derived")
}

func TestHashiCorpOutagesWrapErrProviderUnavailable(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "s.token")
	f := &fakeTransit{t: t, derived: true}
	srv := httptest.NewServer(f)
	ctx := context.Background()
	p, err := OpenProvider(ctx, hcConfig(srv, ""))
	require.NoError(t, err)

	f.status = 503
	_, _, err = p.Wrap(ctx, []byte("k"), []byte("a"))
	require.ErrorIs(t, err, ErrProviderUnavailable)
	_, err = p.Unwrap(ctx, []byte("vault:v3:x"), []byte("a"), "vault:v3")
	require.ErrorIs(t, err, ErrProviderUnavailable)

	srv.Close() // connection refused
	_, _, err = p.Wrap(ctx, []byte("k"), []byte("a"))
	require.ErrorIs(t, err, ErrProviderUnavailable)
	_, err = OpenProvider(ctx, hcConfig(srv, ""))
	require.ErrorIs(t, err, ErrProviderUnavailable)
}

func TestHashiCorpTokenSources(t *testing.T) {
	f := &fakeTransit{t: t, derived: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()

	t.Setenv("VAULT_TOKEN", "")
	_, err := OpenProvider(ctx, hcConfig(srv, ""))
	require.ErrorContains(t, err, "no HashiCorp token")

	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte("s.token\n"), 0o600))
	p, err := OpenProvider(ctx, hcConfig(srv, file))
	require.NoError(t, err)

	// The file is read at each call: a renewed token takes effect, and a
	// token that no longer works is refused (403), not an outage.
	require.NoError(t, os.WriteFile(file, []byte("s.rotated"), 0o600))
	_, _, err = p.Wrap(ctx, []byte("k"), []byte("a"))
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrProviderUnavailable))
	require.NotContains(t, err.Error(), "s.rotated")

	// The file wins over VAULT_TOKEN.
	t.Setenv("VAULT_TOKEN", "s.token")
	_, _, err = p.Wrap(ctx, []byte("k"), []byte("a"))
	require.Error(t, err)

	_, err = OpenProvider(ctx, hcConfig(srv, file+".missing"))
	require.Error(t, err)
}

func TestHashiCorpRejectsBadAddress(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "s.token")
	_, err := OpenProvider(context.Background(), config.Vault{Provider: "hashicorp", HashiCorpAddr: "vault:8200", HashiCorpKey: "ohs"})
	require.Error(t, err)
}

// rawServer answers every transit call with a fixed status and body.
func rawServer(code int, body string, hdr map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
}

func TestHashiCorpUnwrapNeverReturnsEmptyPlaintext(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "s.token")
	ctx := context.Background()
	huge := `{"data":{"plaintext":"` + strings.Repeat("A", 1<<20) + `"}}`
	for name, body := range map[string]string{
		"empty body":        "",
		"malformed":         `{"data":`,
		"no data":           `{}`,
		"null data":         `{"data":null}`,
		"missing plaintext": `{"data":{}}`,
		"empty plaintext":   `{"data":{"plaintext":""}}`,
		"not base64":        `{"data":{"plaintext":"!!"}}`,
		"truncated by cap":  huge,
	} {
		srv := rawServer(200, body, nil)
		p, err := newHashiCorp(config.Vault{HashiCorpAddr: srv.URL, HashiCorpKey: "ohs"}, func() string { return "s.token" })
		require.NoError(t, err)
		got, err := p.Unwrap(ctx, []byte("vault:v1:x"), []byte("a"), "vault:v1")
		require.Error(t, err, name)
		require.Empty(t, got, name)
		_, _, err = p.Wrap(ctx, []byte("k"), []byte("a"))
		require.Error(t, err, name)
		srv.Close()
	}
}

func TestHashiCorpRefusesRedirects(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "s.token")
	var hits int
	var mu sync.Mutex
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer target.Close()
	for _, code := range []int{301, 302, 307, 308} {
		srv := rawServer(code, "", map[string]string{"Location": target.URL + "/v1/transit/encrypt/ohs"})
		p, err := newHashiCorp(config.Vault{HashiCorpAddr: srv.URL, HashiCorpKey: "ohs"}, func() string { return "s.token" })
		require.NoError(t, err)
		_, _, err = p.Wrap(context.Background(), []byte("k"), []byte("a"))
		require.ErrorContains(t, err, "redirects are refused")
		require.False(t, errors.Is(err, ErrProviderUnavailable))
		srv.Close()
	}
	require.Zero(t, hits, "the redirect target was called")
}

func TestHashiCorpRetryableStatuses(t *testing.T) {
	for _, code := range []int{408, 425, 429, 500, 503} {
		srv := rawServer(code, `{"errors":["x"]}`, nil)
		p, _ := newHashiCorp(config.Vault{HashiCorpAddr: srv.URL, HashiCorpKey: "ohs"}, func() string { return "s.token" })
		_, _, err := p.Wrap(context.Background(), []byte("k"), []byte("a"))
		require.ErrorIs(t, err, ErrProviderUnavailable, code)
		srv.Close()
	}
	srv := rawServer(400, `{"errors":["x"]}`, nil)
	defer srv.Close()
	p, _ := newHashiCorp(config.Vault{HashiCorpAddr: srv.URL, HashiCorpKey: "ohs"}, func() string { return "s.token" })
	_, _, err := p.Wrap(context.Background(), []byte("k"), []byte("a"))
	require.False(t, errors.Is(err, ErrProviderUnavailable))
}

func TestHashiCorpRefusesEmptyAAD(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "s.token")
	srv := httptest.NewServer(&fakeTransit{t: t, derived: true})
	defer srv.Close()
	p, err := OpenProvider(context.Background(), hcConfig(srv, ""))
	require.NoError(t, err)
	for _, aad := range [][]byte{nil, {}} {
		_, _, err = p.Wrap(context.Background(), []byte("k"), aad)
		require.Error(t, err)
		_, err = p.Unwrap(context.Background(), []byte("vault:v3:x"), aad, "vault:v3")
		require.Error(t, err)
	}
}

func TestVaultErrorsKeepsUTF8(t *testing.T) {
	// 199 ASCII bytes then a 2 byte character straddling the cut at 200.
	s := vaultErrors([]string{strings.Repeat("a", 199) + "é" + "tail"})
	require.True(t, utf8.ValidString(s))
}
