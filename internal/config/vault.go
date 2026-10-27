// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Vault is the key provider and key cache configuration of the health data
// vault (docs/specs/vault.md).
type Vault struct {
	// Provider is OHS_VAULT_KEY_PROVIDER: local (default), hashicorp,
	// azurekv or awskms.
	Provider string
	// RootKey is the local provider's root key (OHS_VAULT_ROOT_KEY or
	// OHS_VAULT_ROOT_KEY_FILE, base64, 32 bytes), or the key derived from
	// OHS_ENCRYPTION_KEY when neither is set. RootKeyPrevious still opens
	// what earlier root keys wrapped (OHS_VAULT_ROOT_KEY_PREVIOUS, comma
	// separated base64). When the root key is derived, the previous ones are
	// derived from OHS_PREVIOUS_ENCRYPTION_KEYS the same way.
	RootKey         []byte
	RootKeyPrevious [][]byte
	// RootDerived is true when RootKey comes from OHS_ENCRYPTION_KEY.
	RootDerived bool
	// HashiCorpAddr, HashiCorpMount (default transit), HashiCorpKey and
	// HashiCorpTokenFile are the OHS_VAULT_HASHICORP_ settings. The token is
	// never configured here: it comes from the file, else from VAULT_TOKEN.
	HashiCorpAddr, HashiCorpMount, HashiCorpKey, HashiCorpTokenFile string
	// AzureKeyID (OHS_VAULT_AZURE_KEY_ID) and AWSKeyARN
	// (OHS_VAULT_AWS_KEY_ARN) name the key of the cloud providers.
	AzureKeyID, AWSKeyARN string
	// CacheUsers bounds the unwrapped user keys held in memory
	// (OHS_VAULT_CACHE_USERS, default 1024); CacheTTL is how long one stays
	// (OHS_VAULT_CACHE_TTL, default 10m).
	CacheUsers int
	CacheTTL   time.Duration
}

// Defaults of the key cache.
const (
	DefaultVaultCacheUsers = 1024
	DefaultVaultCacheTTL   = 10 * time.Minute
)

// ErrVault is wrapped by every Load error about an OHS_VAULT_ variable.
var ErrVault = errors.New("vault configuration")

// VaultRootInfo is the HKDF info string that derives the local root key from
// OHS_ENCRYPTION_KEY.
const VaultRootInfo = "ohs.vault.root.v1"

// DeriveVaultRootKey returns HKDF-SHA-256(key, info "ohs.vault.root.v1"), 32
// bytes, no salt.
func DeriveVaultRootKey(key []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, key, nil, VaultRootInfo, 32)
}

func loadVault(get func(string, string) string, encKey []byte, prevEnc [][]byte) (Vault, error) {
	v := Vault{
		Provider:           get("OHS_VAULT_KEY_PROVIDER", "local"),
		HashiCorpAddr:      get("OHS_VAULT_HASHICORP_ADDR", ""),
		HashiCorpMount:     get("OHS_VAULT_HASHICORP_MOUNT", "transit"),
		HashiCorpKey:       get("OHS_VAULT_HASHICORP_KEY", ""),
		HashiCorpTokenFile: get("OHS_VAULT_HASHICORP_TOKEN_FILE", ""),
		AzureKeyID:         get("OHS_VAULT_AZURE_KEY_ID", ""),
		AWSKeyARN:          get("OHS_VAULT_AWS_KEY_ARN", ""),
		CacheUsers:         DefaultVaultCacheUsers,
		CacheTTL:           DefaultVaultCacheTTL,
	}
	required := func(name, value string) error {
		if value == "" {
			return fmt.Errorf("%w: %s is required with OHS_VAULT_KEY_PROVIDER=%s", ErrVault, name, v.Provider)
		}
		return nil
	}
	var err error
	switch v.Provider {
	case "local":
		err = loadVaultRoot(&v, get, encKey, prevEnc)
	case "hashicorp":
		if err = required("OHS_VAULT_HASHICORP_ADDR", v.HashiCorpAddr); err == nil {
			err = required("OHS_VAULT_HASHICORP_KEY", v.HashiCorpKey)
		}
	case "azurekv":
		err = required("OHS_VAULT_AZURE_KEY_ID", v.AzureKeyID)
	case "awskms":
		err = required("OHS_VAULT_AWS_KEY_ARN", v.AWSKeyARN)
	default:
		err = fmt.Errorf("%w: OHS_VAULT_KEY_PROVIDER %q is not one of local, hashicorp, azurekv, awskms", ErrVault, v.Provider)
	}
	if err != nil {
		return Vault{}, err
	}
	if s := get("OHS_VAULT_CACHE_USERS", ""); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return Vault{}, fmt.Errorf("%w: OHS_VAULT_CACHE_USERS %q is not a positive integer", ErrVault, s)
		}
		v.CacheUsers = n
	}
	if s := get("OHS_VAULT_CACHE_TTL", ""); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return Vault{}, fmt.Errorf("%w: OHS_VAULT_CACHE_TTL %q is not a positive duration (for example 10m)", ErrVault, s)
		}
		v.CacheTTL = d
	}
	return v, nil
}

// loadVaultRoot sets the local provider's keys: the configured root key, or
// the one derived from the encryption key when none is configured. The
// secret is never generated: a root key file that does not exist is an error,
// because a silently created key would seal data the operator never backed up.
func loadVaultRoot(v *Vault, get func(string, string) string, encKey []byte, prevEnc [][]byte) error {
	noGen := func() (string, error) {
		return "", errors.New("the file does not exist; create it with `openssl rand -base64 32`")
	}
	raw, _, err := secret(get, "OHS_VAULT_ROOT_KEY", noGen)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVault, err)
	}
	if raw == "" {
		if get("OHS_VAULT_ROOT_KEY_PREVIOUS", "") != "" {
			return fmt.Errorf("%w: OHS_VAULT_ROOT_KEY_PREVIOUS needs OHS_VAULT_ROOT_KEY or OHS_VAULT_ROOT_KEY_FILE", ErrVault)
		}
		v.RootDerived = true
		if v.RootKey, err = DeriveVaultRootKey(encKey); err != nil {
			return fmt.Errorf("%w: deriving the root key: %w", ErrVault, err)
		}
		for _, k := range prevEnc {
			d, err := DeriveVaultRootKey(k)
			if err != nil {
				return fmt.Errorf("%w: deriving a previous root key: %w", ErrVault, err)
			}
			v.RootKeyPrevious = append(v.RootKeyPrevious, d)
		}
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("%w: OHS_VAULT_ROOT_KEY is not valid base64 (32 random bytes: `openssl rand -base64 32`)", ErrVault)
	}
	if len(key) != 32 {
		return fmt.Errorf("%w: OHS_VAULT_ROOT_KEY must decode to 32 bytes, got %d", ErrVault, len(key))
	}
	v.RootKey = key
	v.RootKeyPrevious, err = keyList(get("OHS_VAULT_ROOT_KEY_PREVIOUS", ""), key, ErrVault, "OHS_VAULT_ROOT_KEY")
	if err != nil {
		return fmt.Errorf("OHS_VAULT_ROOT_KEY_PREVIOUS: %w", err)
	}
	return nil
}
