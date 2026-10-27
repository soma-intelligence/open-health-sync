// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/soma-intelligence/open-health-sync/internal/crypto"
)

// localWrapVersion is the first byte of a locally wrapped value:
// version || nonce (24) || ciphertext || tag (16).
const localWrapVersion = 0x01

type localKey struct {
	version string
	aead    cipher.AEAD
}

// localProvider wraps under a root key held in this process: the active key
// wraps, the previous ones only open.
type localProvider struct {
	active localKey
	all    map[string]localKey
}

func newLocalKey(key []byte) (localKey, error) {
	if len(key) != 32 {
		return localKey{}, fmt.Errorf("vault: local root key must be 32 bytes, got %d", len(key))
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return localKey{}, err
	}
	id := crypto.KeyID(key)
	return localKey{version: hex.EncodeToString(id[:]), aead: aead}, nil
}

func newLocalProvider(root []byte, previous [][]byte) (*localProvider, error) {
	active, err := newLocalKey(root)
	if err != nil {
		return nil, err
	}
	p := &localProvider{active: active, all: map[string]localKey{active.version: active}}
	for _, k := range previous {
		lk, err := newLocalKey(k)
		if err != nil {
			return nil, err
		}
		if _, dup := p.all[lk.version]; dup {
			return nil, errors.New("vault: a previous root key repeats another root key")
		}
		p.all[lk.version] = lk
	}
	return p, nil
}

func (p *localProvider) Name() string { return "local" }

func (p *localProvider) Wrap(_ context.Context, plain, aad []byte) ([]byte, string, error) {
	out := make([]byte, 1+chacha20poly1305.NonceSizeX, 1+chacha20poly1305.NonceSizeX+len(plain)+chacha20poly1305.Overhead)
	out[0] = localWrapVersion
	nonce := out[1:]
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", fmt.Errorf("vault: nonce: %w", err)
	}
	return p.active.aead.Seal(out, nonce, plain, aad), p.active.version, nil
}

func (p *localProvider) Unwrap(_ context.Context, wrapped, aad []byte, version string) ([]byte, error) {
	k, ok := p.all[version]
	if !ok {
		return nil, fmt.Errorf("vault: no local root key has version %q", version)
	}
	if len(wrapped) < 1+chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead || wrapped[0] != localWrapVersion {
		return nil, errors.New("vault: wrapped key is malformed")
	}
	nonce := wrapped[1 : 1+chacha20poly1305.NonceSizeX]
	plain, err := k.aead.Open(nil, nonce, wrapped[1+chacha20poly1305.NonceSizeX:], aad)
	if err != nil {
		return nil, errors.New("vault: wrapped key does not open (wrong root key, wrong aad or altered value)")
	}
	return plain, nil
}
