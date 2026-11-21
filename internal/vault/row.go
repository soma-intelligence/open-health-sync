// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/crypto/chacha20poly1305"
)

// ErrOpen is the one error a sealed value that does not open gives, whatever
// the cause: short input, unknown format, unknown key version, wrong key,
// AAD mismatch or a bad tag. It says nothing more, on purpose.
var ErrOpen = errors.New("vault: sealed value does not open")

// A sealed row is
//
//	0x02 || user key version (uint32 BE) || nonce (24) || ciphertext || tag (16)
//
// sealed with XChaCha20-Poly1305 under the user's data key of that version.
const (
	rowFormat     = 0x02
	rowHeaderSize = 1 + 4
	rowOverhead   = rowHeaderSize + chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead
)

const rowAADLabel = "ohs.vault.row.v1"

// rowAAD is the associated data of a sealed row:
//
//	"ohs.vault.row.v1" || tenant_id (16) || user_id (16) || table name || 0x00 || row identity
//
// The identity is the table's dedup key. The 0x00 ends the table name, which
// never holds a zero byte, so the name and the identity cannot trade bytes.
func rowAAD(tenant, user uuid.UUID, table string, identity []byte) []byte {
	b := make([]byte, 0, len(rowAADLabel)+16+16+len(table)+1+len(identity))
	b = append(b, rowAADLabel...)
	b = append(b, tenant[:]...)
	b = append(b, user[:]...)
	b = append(b, table...)
	b = append(b, 0x00)
	b = append(b, identity...)
	return b
}

// sealRow seals plain under key with a fresh random nonce, tagging the result
// with the key's version so a reader can pick the key back.
func sealRow(key *[32]byte, version uint32, aad, plain []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, fmt.Errorf("vault: row cipher: %w", err)
	}
	out := make([]byte, rowHeaderSize+chacha20poly1305.NonceSizeX, rowOverhead+len(plain))
	out[0] = rowFormat
	binary.BigEndian.PutUint32(out[1:rowHeaderSize], version)
	nonce := out[rowHeaderSize:]
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("vault: row nonce: %w", err)
	}
	return aead.Seal(out, nonce, plain, aad), nil
}

// openRow opens a value written by sealRow. keyFor returns the data key of a
// version. Every failure, a keyFor error included, is ErrOpen alone.
func openRow(keyFor func(version uint32) (*[32]byte, error), aad, sealed []byte) ([]byte, error) {
	if len(sealed) < rowOverhead || sealed[0] != rowFormat {
		return nil, ErrOpen
	}
	key, err := keyFor(binary.BigEndian.Uint32(sealed[1:rowHeaderSize]))
	if err != nil || key == nil {
		return nil, ErrOpen
	}
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, ErrOpen
	}
	nonce := sealed[rowHeaderSize : rowHeaderSize+chacha20poly1305.NonceSizeX]
	plain, err := aead.Open(nil, nonce, sealed[rowHeaderSize+chacha20poly1305.NonceSizeX:], aad)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
