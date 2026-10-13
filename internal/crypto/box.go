// SPDX-License-Identifier: AGPL-3.0-only

// Package crypto seals provider OAuth tokens and other small secrets at rest
// with AES-256-GCM, under an active key and any number of previous keys kept
// for rotation.
package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// Version1 is the first byte of a value sealed with a key id. The sealed form
// is Version1 || key id (4 bytes) || nonce || ciphertext || tag. Values
// written before key ids have no header: nonce || ciphertext || tag.
const Version1 = 0x01

// idLen is the length of a key id in the header.
const idLen = 4

// KeyID is the id written in the header of a value sealed with key: the first
// 4 bytes of its SHA-256. It names the key, it does not reveal it.
func KeyID(key []byte) [idLen]byte {
	sum := sha256.Sum256(key)
	return [idLen]byte(sum[:idLen])
}

// Status says which configured key opens a sealed value.
type Status int

// The statuses Box.Status returns.
const (
	// StatusUnknown: no configured key opens the value (or the aad differs).
	StatusUnknown Status = iota
	// StatusCurrent: sealed with a header under the active key.
	StatusCurrent
	// StatusLegacy: opens with the active key but has no header (written
	// before key ids); a rekey adds it.
	StatusLegacy
	// StatusPrevious: opens with a previous key only; a rekey moves it to the
	// active key.
	StatusPrevious
)

type key struct {
	id   [idLen]byte
	aead cipher.AEAD
}

// Box seals and opens small secrets. It seals with its active key and opens
// with the active key or any previous key.
type Box struct {
	keys []key // keys[0] is the active key
}

// NewBox builds a Box from a 32 byte key, with no previous key.
func NewBox(active []byte) (*Box, error) { return NewKeyring(active) }

// NewKeyring builds a Box that seals with active and still opens values
// sealed with any of previous. Every key is 32 bytes; a key given twice is
// refused, so is one whose id another key already has.
func NewKeyring(active []byte, previous ...[]byte) (*Box, error) {
	b := &Box{}
	for i, k := range append([][]byte{active}, previous...) {
		which := "key"
		if i > 0 {
			which = fmt.Sprintf("previous key %d", i)
		}
		if len(k) != 32 {
			return nil, fmt.Errorf("crypto: %s must be 32 bytes, got %d", which, len(k))
		}
		block, err := aes.NewCipher(k)
		if err != nil {
			return nil, fmt.Errorf("crypto: cipher: %w", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("crypto: gcm: %w", err)
		}
		id := KeyID(k)
		for _, o := range b.keys {
			if o.id == id {
				return nil, fmt.Errorf("crypto: %s is given twice", which)
			}
		}
		b.keys = append(b.keys, key{id: id, aead: aead})
	}
	return b, nil
}

// PreviousKeys is how many previous keys the Box holds.
func (b *Box) PreviousKeys() int { return len(b.keys) - 1 }

// Seal encrypts plain with a fresh random nonce and no additional data.
func (b *Box) Seal(plain []byte) ([]byte, error) { return b.SealFor(plain, nil) }

// SealFor is Seal bound to aad (for example tenant, user and provider ids): the
// value only opens with the same aad, so a sealed token cannot be moved to another row.
// It always seals with the active key, in the Version1 form.
func (b *Box) SealFor(plain, aad []byte) ([]byte, error) {
	k := b.keys[0]
	head := 1 + idLen
	n := k.aead.NonceSize()
	out := make([]byte, head+n, head+n+len(plain)+k.aead.Overhead())
	out[0] = Version1
	copy(out[1:head], k.id[:])
	nonce := out[head:]
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: nonce: %w", err)
	}
	return k.aead.Seal(out, nonce, plain, aad), nil
}

// Open decrypts and authenticates a value produced by Seal.
func (b *Box) Open(sealed []byte) ([]byte, error) { return b.OpenFor(sealed, nil) }

// OpenFor opens a value produced by SealFor with the same aad, under the
// active key or a previous one, with or without a header.
func (b *Box) OpenFor(sealed, aad []byte) ([]byte, error) {
	plain, _, err := b.open(sealed, aad)
	return plain, err
}

// Status reports which configured key opens sealed with aad. It decrypts the
// value: unlike NeedsRekey, its answer is authenticated.
func (b *Box) Status(sealed, aad []byte) Status {
	_, st, _ := b.open(sealed, aad)
	return st
}

// NeedsRekey reports whether sealed is not in the Version1 form under the
// active key. It reads the header only, without the aad, so a forged or
// damaged value can pass it: use Status or Rekey to be sure.
func (b *Box) NeedsRekey(sealed []byte) bool {
	id := b.keys[0].id
	return len(sealed) < 1+idLen || sealed[0] != Version1 || !bytes.Equal(sealed[1:1+idLen], id[:])
}

// Rekey returns sealed re-sealed under the active key with the same aad, and
// whether it changed. A value already current is returned as it is. It fails,
// and the caller keeps the value, when no configured key opens it.
func (b *Box) Rekey(sealed, aad []byte) ([]byte, bool, error) {
	plain, st, err := b.open(sealed, aad)
	if err != nil {
		return nil, false, err
	}
	if st == StatusCurrent {
		return sealed, false, nil
	}
	out, err := b.SealFor(plain, aad)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

var errOpen = errors.New("crypto: cannot open sealed value")

// open tries the key the header names, then every key on the whole value: a
// legacy value starts with a random nonce, which can look like a header.
func (b *Box) open(sealed, aad []byte) ([]byte, Status, error) {
	n := b.keys[0].aead.NonceSize()
	overhead := b.keys[0].aead.Overhead()
	if len(sealed) < n+overhead {
		return nil, StatusUnknown, errors.New("crypto: sealed value too short")
	}
	head := 1 + idLen
	if len(sealed) >= head+n+overhead && sealed[0] == Version1 {
		for i, k := range b.keys {
			if !bytes.Equal(sealed[1:head], k.id[:]) {
				continue
			}
			body := sealed[head:]
			if plain, err := k.aead.Open(nil, body[:n], body[n:], aad); err == nil {
				if i == 0 {
					return plain, StatusCurrent, nil
				}
				return plain, StatusPrevious, nil
			}
			break // ids are unique
		}
	}
	for i, k := range b.keys {
		if plain, err := k.aead.Open(nil, sealed[:n], sealed[n:], aad); err == nil {
			if i == 0 {
				return plain, StatusLegacy, nil
			}
			return plain, StatusPrevious, nil
		}
	}
	return nil, StatusUnknown, errOpen
}
