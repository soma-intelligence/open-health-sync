// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func b64(n byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{n}, 32)) }

func withBase(extra map[string]string) map[string]string {
	m := base()
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestVaultDefaultsDeriveRootKey(t *testing.T) {
	c, err := load(t, base())
	if err != nil {
		t.Fatal(err)
	}
	v := c.Vault
	if v.Provider != "local" || !v.RootDerived || v.CacheUsers != 1024 || v.CacheTTL != 10*time.Minute || v.HashiCorpMount != "transit" {
		t.Fatalf("defaults: %+v", v)
	}
	want, _ := DeriveVaultRootKey(c.EncryptionKey)
	if !bytes.Equal(v.RootKey, want) || len(v.RootKey) != 32 || bytes.Equal(v.RootKey, c.EncryptionKey) {
		t.Fatal("root key is not the derived key")
	}
}

// The vector is HKDF-SHA-256 with no salt and info "ohs.vault.root.v1" over
// the bytes 0..31, computed outside Go.
func TestDeriveVaultRootKeyVector(t *testing.T) {
	in := make([]byte, 32)
	for i := range in {
		in[i] = byte(i)
	}
	got, err := DeriveVaultRootKey(in)
	if err != nil {
		t.Fatal(err)
	}
	if h := hex.EncodeToString(got); h != "7237d6504b3271a1a2a1522027ab050ae4afb3a7104779615c516925e618a830" {
		t.Fatalf("derived %s", h)
	}
}

func TestVaultDerivedPreviousFollowEncryptionRotation(t *testing.T) {
	c, err := load(t, withBase(map[string]string{"OHS_PREVIOUS_ENCRYPTION_KEYS": b64(7)}))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := DeriveVaultRootKey(bytes.Repeat([]byte{7}, 32))
	if len(c.Vault.RootKeyPrevious) != 1 || !bytes.Equal(c.Vault.RootKeyPrevious[0], want) {
		t.Fatal("previous root key is not derived from the previous encryption key")
	}
}

func TestVaultExplicitRootKey(t *testing.T) {
	c, err := load(t, withBase(map[string]string{"OHS_VAULT_ROOT_KEY": b64(1), "OHS_VAULT_ROOT_KEY_PREVIOUS": b64(2) + "," + b64(3)}))
	if err != nil {
		t.Fatal(err)
	}
	v := c.Vault
	if v.RootDerived || !bytes.Equal(v.RootKey, bytes.Repeat([]byte{1}, 32)) || len(v.RootKeyPrevious) != 2 {
		t.Fatalf("%+v", v)
	}
}

func TestVaultRootKeyFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "root")
	if err := os.WriteFile(f, []byte(b64(4)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := load(t, withBase(map[string]string{"OHS_VAULT_ROOT_KEY_FILE": f}))
	if err != nil || c.Vault.RootDerived || !bytes.Equal(c.Vault.RootKey, bytes.Repeat([]byte{4}, 32)) {
		t.Fatalf("%v %+v", err, c.Vault)
	}
	// A missing file is an error: the root key is never generated.
	_, err = load(t, withBase(map[string]string{"OHS_VAULT_ROOT_KEY_FILE": f + ".missing"}))
	if !errors.Is(err, ErrVault) {
		t.Fatalf("missing file: %v", err)
	}
	if _, serr := os.Stat(f + ".missing"); serr == nil {
		t.Fatal("the root key file was created")
	}
}

func TestVaultRefusals(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	for name, kv := range map[string]map[string]string{
		"unknown provider":      {"OHS_VAULT_KEY_PROVIDER": "gcp"},
		"short root key":        {"OHS_VAULT_ROOT_KEY": short},
		"root key not base64":   {"OHS_VAULT_ROOT_KEY": "!!!"},
		"both root key sources": {"OHS_VAULT_ROOT_KEY": b64(1), "OHS_VAULT_ROOT_KEY_FILE": "/x"},
		"previous without root": {"OHS_VAULT_ROOT_KEY_PREVIOUS": b64(2)},
		"previous is the root":  {"OHS_VAULT_ROOT_KEY": b64(1), "OHS_VAULT_ROOT_KEY_PREVIOUS": b64(1)},
		"previous short":        {"OHS_VAULT_ROOT_KEY": b64(1), "OHS_VAULT_ROOT_KEY_PREVIOUS": short},
		"hashicorp no addr":     {"OHS_VAULT_KEY_PROVIDER": "hashicorp", "OHS_VAULT_HASHICORP_KEY": "k"},
		"hashicorp no key":      {"OHS_VAULT_KEY_PROVIDER": "hashicorp", "OHS_VAULT_HASHICORP_ADDR": "http://v"},
		"azurekv no key id":     {"OHS_VAULT_KEY_PROVIDER": "azurekv"},
		"awskms no arn":         {"OHS_VAULT_KEY_PROVIDER": "awskms"},
		"cache users zero":      {"OHS_VAULT_CACHE_USERS": "0"},
		"cache users text":      {"OHS_VAULT_CACHE_USERS": "many"},
		"cache ttl bad":         {"OHS_VAULT_CACHE_TTL": "soon"},
		"cache ttl negative":    {"OHS_VAULT_CACHE_TTL": "-1m"},
	} {
		_, err := load(t, withBase(kv))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		for _, secret := range []string{short, "AQEBAQEB"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error carries a key: %v", name, err)
			}
		}
	}
}

func TestVaultSettings(t *testing.T) {
	c, err := load(t, withBase(map[string]string{
		"OHS_VAULT_KEY_PROVIDER": "hashicorp", "OHS_VAULT_HASHICORP_ADDR": "https://v:8200", "OHS_VAULT_HASHICORP_KEY": "ohs",
		"OHS_VAULT_HASHICORP_MOUNT": "t2", "OHS_VAULT_HASHICORP_TOKEN_FILE": "/run/token",
		"OHS_VAULT_CACHE_USERS": "64", "OHS_VAULT_CACHE_TTL": "30s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	v := c.Vault
	if v.HashiCorpAddr != "https://v:8200" || v.HashiCorpMount != "t2" || v.HashiCorpKey != "ohs" || v.HashiCorpTokenFile != "/run/token" ||
		v.CacheUsers != 64 || v.CacheTTL != 30*time.Second {
		t.Fatalf("%+v", v)
	}
	if len(c.Providers) != 0 {
		t.Fatalf("OHS_VAULT_ variables read as provider settings: %v", c.Providers)
	}
}

// A Config built without Load (a test harness, an embedding program) gets the
// vault settings Load would give an environment with only OHS_ENCRYPTION_KEY.
func TestDerivedVaultIsWhatLoadGives(t *testing.T) {
	c, err := load(t, withBase(map[string]string{"OHS_PREVIOUS_ENCRYPTION_KEYS": b64(7)}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := DerivedVault(c.EncryptionKey, c.PreviousEncryptionKeys...)
	if err != nil {
		t.Fatal(err)
	}
	want := c.Vault
	if got.Provider != want.Provider || !got.RootDerived || !bytes.Equal(got.RootKey, want.RootKey) ||
		len(got.RootKeyPrevious) != 1 || !bytes.Equal(got.RootKeyPrevious[0], want.RootKeyPrevious[0]) ||
		got.CacheUsers != want.CacheUsers || got.CacheTTL != want.CacheTTL || got.HashiCorpMount != want.HashiCorpMount {
		t.Fatalf("DerivedVault = %+v, Load gives %+v", got, want)
	}
	if _, err := DerivedVault(nil); !errors.Is(err, ErrVault) {
		t.Fatalf("no encryption key: %v", err)
	}
}
