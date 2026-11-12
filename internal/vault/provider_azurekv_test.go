// SPDX-License-Identifier: AGPL-3.0-only

//go:build azurekv

package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"
	"github.com/stretchr/testify/require"
)

type staticCred struct{}

func (staticCred) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "test-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// fakeKV speaks the Key Vault REST shapes for one key: GET /keys/{name},
// POST /keys/{name}/{version}/wrapkey and /unwrapkey. A key version is an AES
// key; "wrapping" is AES-GCM, enough to be reversible and version bound.
type fakeKV struct {
	t        *testing.T
	kty      string
	mu       sync.Mutex
	versions map[string]cipher.AEAD
	current  string
	fail     int // status to answer wrap and unwrap with, 0 for none
	lastAlg  string
	lastBody []byte
	empty    bool // answer an empty result
	badKID   string
	srv      *httptest.Server
}

func newFakeKV(t *testing.T, kty string) *fakeKV {
	f := &fakeKV{t: t, kty: kty, versions: map[string]cipher.AEAD{}}
	f.addVersion("00000000000000000000000000000001")
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKV) addVersion(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	b, _ := aes.NewCipher(k)
	g, _ := cipher.NewGCM(b)
	f.versions[v] = g
	f.current = v
}

func (f *fakeKV) keyID() string { return f.srv.URL + "/keys/ohs-root" }

func (f *fakeKV) provider() *azureKVProvider {
	f.t.Helper()
	p, err := newAzureKV(f.keyID(), staticCred{}, &azkeys.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: f.srv.Client(),
			Retry:     policy.RetryOptions{MaxRetries: -1},
		},
		DisableChallengeResourceVerification: true,
	})
	require.NoError(f.t, err)
	return p
}

func (f *fakeKV) serve(w http.ResponseWriter, r *http.Request) {
	// The challenge policy first sends the call bare and expects a 401 that
	// names the authority.
	if r.Header.Get("Authorization") != "Bearer test-token" {
		w.Header().Set("WWW-Authenticate", `Bearer authorization="https://login.example.test/tenant", resource="https://vault.azure.net"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "keys" || parts[1] != "ohs-root" {
		f.error(w, http.StatusNotFound, "KeyNotFound")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]any{"key": map[string]any{
			"kid": f.srv.URL + "/keys/ohs-root/" + f.current, "kty": f.kty,
		}})
		return
	}
	// The client leaves the version out of the path when it is empty.
	var version, op string
	switch len(parts) {
	case 3:
		version, op = f.current, parts[2]
	case 4:
		version, op = parts[2], parts[3]
	default:
		f.error(w, http.StatusNotFound, "KeyNotFound")
		return
	}
	if f.fail != 0 {
		f.error(w, f.fail, "Failure")
		return
	}
	var in struct {
		Alg   string `json:"alg"`
		Value string `json:"value"`
	}
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
	f.lastAlg = in.Alg
	val, err := base64.RawURLEncoding.DecodeString(in.Value)
	require.NoError(f.t, err)
	if op == "wrapkey" {
		f.lastBody = val
	}
	g, ok := f.versions[version]
	if !ok {
		f.error(w, http.StatusNotFound, "KeyNotFound")
		return
	}
	out := map[string]any{"kid": f.srv.URL + "/keys/ohs-root/" + version}
	if f.badKID != "" {
		out["kid"] = f.badKID
	}
	switch op {
	case "wrapkey":
		nonce := make([]byte, g.NonceSize())
		_, _ = rand.Read(nonce)
		ct := g.Seal(nonce, nonce, val, nil)
		out["kid"] = f.srv.URL + "/keys/ohs-root/" + version
		if f.badKID != "" {
			out["kid"] = f.badKID
		}
		out["value"] = base64.RawURLEncoding.EncodeToString(ct)
		if f.empty {
			out["value"] = ""
		}
	case "unwrapkey":
		if len(val) < g.NonceSize() {
			f.error(w, http.StatusBadRequest, "BadParameter")
			return
		}
		pt, err := g.Open(nil, val[:g.NonceSize()], val[g.NonceSize():], nil)
		if err != nil {
			f.error(w, http.StatusBadRequest, "BadParameter")
			return
		}
		out["value"] = base64.RawURLEncoding.EncodeToString(pt)
		if f.empty {
			out["value"] = ""
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeKV) error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": "secret-looking-detail"}})
}

func TestAzureKVWrapUnwrap(t *testing.T) {
	ctx := context.Background()
	f := newFakeKV(t, "RSA")
	p := f.provider()
	require.NoError(t, p.checkKey(ctx))
	require.Equal(t, "azurekv", p.Name())

	key := rootKey(7)
	wrapped, version, err := p.Wrap(ctx, key, []byte("tenant-1"))
	require.NoError(t, err)
	require.Equal(t, f.current, version)
	require.Equal(t, "RSA-OAEP-256", f.lastAlg)
	require.Len(t, f.lastBody, azureAADSize+len(key), "the wrapped plaintext is SHA-256(aad) || key")
	require.NotContains(t, string(wrapped), string(key))

	got, err := p.Unwrap(ctx, wrapped, []byte("tenant-1"), version)
	require.NoError(t, err)
	require.Equal(t, key, got)

	// Another aad fails on the prefix, even though the service opened it.
	_, err = p.Unwrap(ctx, wrapped, []byte("tenant-2"), version)
	require.ErrorContains(t, err, "does not belong to this aad")
	require.False(t, errors.Is(err, ErrProviderUnavailable))

	// A rotated key still opens what an older version wrapped.
	f.addVersion("00000000000000000000000000000002")
	got, err = p.Unwrap(ctx, wrapped, []byte("tenant-1"), version)
	require.NoError(t, err)
	require.Equal(t, key, got)
	_, v2, err := p.Wrap(ctx, key, []byte("tenant-1"))
	require.NoError(t, err)
	require.Equal(t, "00000000000000000000000000000002", v2)

	// An unknown version and a tampered value are refusals.
	_, err = p.Unwrap(ctx, wrapped, []byte("tenant-1"), "00000000000000000000000000000009")
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrProviderUnavailable))
	bad := append([]byte(nil), wrapped...)
	bad[len(bad)-1] ^= 1
	_, err = p.Unwrap(ctx, bad, []byte("tenant-1"), version)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrProviderUnavailable))
}

func TestAzureKVManagedHSMUsesAESKeyWrap(t *testing.T) {
	ctx := context.Background()
	f := newFakeKV(t, "oct-HSM")
	p := f.provider()
	require.NoError(t, p.checkKey(ctx))
	_, _, err := p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.NoError(t, err)
	require.Equal(t, "A256KW", f.lastAlg)
	// 32 + 5 bytes is not a multiple of 8: refused before the call.
	_, _, err = p.Wrap(ctx, []byte("12345"), []byte("tenant-1"))
	require.ErrorContains(t, err, "multiple of 8")
}

func TestAzureKVRefusesKeyThatCannotWrap(t *testing.T) {
	f := newFakeKV(t, "EC")
	require.ErrorContains(t, f.provider().checkKey(context.Background()), "cannot wrap")
}

func TestAzureKVRefusesEmptyAAD(t *testing.T) {
	ctx := context.Background()
	f := newFakeKV(t, "RSA")
	p := f.provider()
	require.NoError(t, p.checkKey(ctx))
	_, _, err := p.Wrap(ctx, rootKey(1), nil)
	require.Error(t, err)
	_, err = p.Unwrap(ctx, []byte("x"), nil, f.current)
	require.Error(t, err)
}

func TestAzureKVErrorClasses(t *testing.T) {
	ctx := context.Background()
	f := newFakeKV(t, "RSA")
	p := f.provider()
	require.NoError(t, p.checkKey(ctx))
	for status, unavailable := range map[int]bool{
		http.StatusTooManyRequests: true, http.StatusBadGateway: true, http.StatusInternalServerError: true,
		http.StatusForbidden: false, http.StatusNotFound: false, http.StatusBadRequest: false,
	} {
		f.mu.Lock()
		f.fail = status
		f.mu.Unlock()
		_, _, err := p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
		require.Error(t, err, status)
		require.Equal(t, unavailable, errors.Is(err, ErrProviderUnavailable), status)
		require.NotContains(t, err.Error(), "secret-looking-detail", "the body is not echoed")
	}
	// A server that is gone is unavailable.
	f.srv.Close()
	_, _, err := p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.ErrorIs(t, err, ErrProviderUnavailable)
}

func TestAzureKVMalformedAnswers(t *testing.T) {
	ctx := context.Background()
	f := newFakeKV(t, "RSA")
	p := f.provider()
	require.NoError(t, p.checkKey(ctx))
	wrapped, version, err := p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.NoError(t, err)

	f.mu.Lock()
	f.empty = true
	f.mu.Unlock()
	_, _, err = p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.ErrorContains(t, err, "empty wrap")
	_, err = p.Unwrap(ctx, wrapped, []byte("tenant-1"), version)
	require.ErrorContains(t, err, "short plaintext")

	f.mu.Lock()
	f.empty = false
	f.badKID = f.srv.URL + "/keys/another-key/00000000000000000000000000000001"
	f.mu.Unlock()
	_, _, err = p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.ErrorContains(t, err, "unexpected key")
}

func TestParseAzureKeyID(t *testing.T) {
	v, n, ver, err := parseAzureKeyID("https://v.vault.azure.net/keys/root/abc123")
	require.NoError(t, err)
	require.Equal(t, []string{"https://v.vault.azure.net", "root", "abc123"}, []string{v, n, ver})
	_, n, ver, err = parseAzureKeyID("https://v.managedhsm.azure.net/keys/root/")
	require.NoError(t, err)
	require.Equal(t, []string{"root", ""}, []string{n, ver})
	for _, bad := range []string{"", "http://v/keys/root", "https://v/secrets/root", "https://v/keys", "https://v/keys/a/b/c", "https://v/keys/a/b%2Fc", "root"} {
		_, _, _, err = parseAzureKeyID(bad)
		require.Error(t, err, bad)
	}
}

func TestOpenProviderAzureKVRegistered(t *testing.T) {
	_, ok := cloudOpeners["azurekv"]
	require.True(t, ok)
}
