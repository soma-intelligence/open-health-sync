// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

// UserVault is the unwrapped keys of one user, every version still stored,
// as ForUser returned them. It is the caller's own copy: the cache evicting
// the user does not change it. New seals and keyed hashes use the newest
// version; opens take the version a sealed value names.
type UserVault struct {
	tenant, user uuid.UUID
	keys         map[uint32]*UserKeys
	newest       *UserKeys
	metrics      *Metrics
}

// SealRow seals vals (in t.Sealed order, nil for NULL) for the row of t whose
// identity is identity, under the newest data key.
func (u *UserVault) SealRow(t Table, identity []byte, vals []any) ([]byte, error) {
	plain, err := t.Encode(vals)
	if err != nil {
		u.metrics.seal(false)
		return nil, err
	}
	defer clear(plain)
	sealed, err := sealRow(&u.newest.Data, u.newest.Version, rowAAD(u.tenant, u.user, t.Name, identity), plain)
	u.metrics.seal(err == nil)
	return sealed, err
}

// OpenRow opens what SealRow returned for the same table and identity. Any
// failure is ErrOpen, counted in ohs_vault_open_failures_total.
func (u *UserVault) OpenRow(t Table, identity []byte, sealed []byte) ([]any, error) {
	plain, err := openRow(func(version uint32) (*[32]byte, error) {
		k, err := u.Keys(version)
		if err != nil {
			return nil, err
		}
		return &k.Data, nil
	}, rowAAD(u.tenant, u.user, t.Name, identity), sealed)
	if err == nil {
		defer clear(plain)
		var vals []any
		if vals, err = t.Decode(plain); err == nil {
			u.metrics.open(true)
			return vals, nil
		}
	}
	u.metrics.open(false)
	u.metrics.openFailure(failRow)
	return nil, ErrOpen
}

// ContentHash is the keyed content_hash of a record: hex HMAC under the
// newest index key, label "content", over the same canonical bytes the plain
// SHA-256 hashed.
func (u *UserVault) ContentHash(canonical []byte) string {
	m := u.newest.MAC("content", canonical)
	return hex.EncodeToString(m[:])
}

// ContentMAC is the content_mac of a row of t: the HMAC, label "row:" and the
// table name, of the row's plaintext. It is nil when vals do not encode;
// SealRow then fails on the same values.
func (u *UserVault) ContentMAC(t Table, vals []any) []byte {
	plain, err := t.Encode(vals)
	if err != nil {
		return nil
	}
	defer clear(plain)
	m := u.newest.MAC("row:"+t.Name, plain)
	return m[:]
}

// SourceKey is the keyed group key of a source: the HMAC, label "source", of
// the source string.
func (u *UserVault) SourceKey(source string) []byte {
	m := u.newest.MAC("source", []byte(source))
	return m[:]
}

// Keys returns the key set of a version. A version the user does not have
// is ErrOpen: what names it will never open.
func (u *UserVault) Keys(version uint32) (*UserKeys, error) {
	k, ok := u.keys[version]
	if !ok {
		return nil, fmt.Errorf("%w: the user has no key version %d", ErrOpen, version)
	}
	return k, nil
}
