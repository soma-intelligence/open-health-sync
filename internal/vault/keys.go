// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// UserKeys is the secret key set of one user: the data key seals rows, the
// index key makes keyed hashes, and the ML-KEM seed and X25519 private key
// open sealed objects. The value is secret: it is stored only wrapped by the
// tenant key, and it never goes to a log.
type UserKeys struct {
	Version   uint32
	Data      [32]byte
	Index     [32]byte
	MLKEMSeed [64]byte
	X25519    [32]byte
}

// userKeysFormat is the first byte of a marshalled UserKeys. The layout is
// fixed: format (1) || version (uint32 BE) || data (32) || index (32) ||
// ML-KEM seed (64) || X25519 private key (32), 165 bytes.
const (
	userKeysFormat = 0x01
	userKeysSize   = 1 + 4 + 32 + 32 + 64 + 32
)

// publicKeyFormat is the first byte of a marshalled PublicKey. The layout is
// fixed: format (1) || ML-KEM-1024 encapsulation key (1568) || X25519 public
// key (32), 1601 bytes.
const (
	publicKeyFormat = 0x01
	publicKeySize   = 1 + mlkem.EncapsulationKeySize1024 + 32
)

// GenerateUserKeys draws a fresh key set from crypto/rand.
func GenerateUserKeys(version uint32) (*UserKeys, error) {
	k := &UserKeys{Version: version}
	for _, b := range [][]byte{k.Data[:], k.Index[:], k.MLKEMSeed[:]} {
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("vault: generate user keys: %w", err)
		}
	}
	x, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("vault: generate user keys: %w", err)
	}
	copy(k.X25519[:], x.Bytes())
	return k, nil
}

// Marshal returns the fixed layout described at userKeysFormat. The result is
// as secret as the keys: the caller wraps it and zeroes it.
func (k *UserKeys) Marshal() []byte {
	b := make([]byte, 0, userKeysSize)
	b = append(b, userKeysFormat)
	b = binary.BigEndian.AppendUint32(b, k.Version)
	b = append(b, k.Data[:]...)
	b = append(b, k.Index[:]...)
	b = append(b, k.MLKEMSeed[:]...)
	b = append(b, k.X25519[:]...)
	return b
}

// UnmarshalUserKeys reads the layout written by Marshal. It rejects any other
// length and any format byte it does not know.
func UnmarshalUserKeys(b []byte) (*UserKeys, error) {
	if len(b) != userKeysSize {
		return nil, fmt.Errorf("vault: user keys are %d bytes, want %d", len(b), userKeysSize)
	}
	if b[0] != userKeysFormat {
		return nil, fmt.Errorf("vault: unknown user keys format %d", b[0])
	}
	k := &UserKeys{Version: binary.BigEndian.Uint32(b[1:5])}
	rest := b[5:]
	rest = rest[copy(k.Data[:], rest):]
	rest = rest[copy(k.Index[:], rest):]
	rest = rest[copy(k.MLKEMSeed[:], rest):]
	copy(k.X25519[:], rest)
	return k, nil
}

// Zero overwrites every field. Go may have copied the arrays elsewhere (a
// value receiver, the garbage collector); Zero is the best effort the language
// allows.
func (k *UserKeys) Zero() {
	clear(k.Data[:])
	clear(k.Index[:])
	clear(k.MLKEMSeed[:])
	clear(k.X25519[:])
	k.Version = 0
}

// PublicKey is the public half of a user's KEM key pair, stored in clear. It
// is enough to seal an object for the user.
type PublicKey struct {
	MLKEM  []byte // ML-KEM-1024 encapsulation key, 1568 bytes
	X25519 []byte // X25519 public key, 32 bytes
}

// Public derives the public key: the ML-KEM encapsulation key from the 64 byte
// seed, the X25519 public key from the private key.
func (k *UserKeys) Public() (PublicKey, error) {
	dk, err := mlkem.NewDecapsulationKey1024(k.MLKEMSeed[:])
	if err != nil {
		return PublicKey{}, fmt.Errorf("vault: ML-KEM key: %w", err)
	}
	x, err := ecdh.X25519().NewPrivateKey(k.X25519[:])
	if err != nil {
		return PublicKey{}, fmt.Errorf("vault: X25519 key: %w", err)
	}
	return PublicKey{MLKEM: dk.EncapsulationKey().Bytes(), X25519: x.PublicKey().Bytes()}, nil
}

// Marshal returns the fixed layout described at publicKeyFormat.
func (p PublicKey) Marshal() []byte {
	b := make([]byte, 0, publicKeySize)
	b = append(b, publicKeyFormat)
	b = append(b, p.MLKEM...)
	b = append(b, p.X25519...)
	return b
}

// UnmarshalPublicKey reads the layout written by Marshal and checks that both
// keys parse. The result does not alias b.
func UnmarshalPublicKey(b []byte) (PublicKey, error) {
	if len(b) != publicKeySize {
		return PublicKey{}, fmt.Errorf("vault: public key is %d bytes, want %d", len(b), publicKeySize)
	}
	if b[0] != publicKeyFormat {
		return PublicKey{}, fmt.Errorf("vault: unknown public key format %d", b[0])
	}
	p := PublicKey{
		MLKEM:  append([]byte(nil), b[1:1+mlkem.EncapsulationKeySize1024]...),
		X25519: append([]byte(nil), b[1+mlkem.EncapsulationKeySize1024:]...),
	}
	if _, err := mlkem.NewEncapsulationKey1024(p.MLKEM); err != nil {
		return PublicKey{}, fmt.Errorf("vault: ML-KEM public key: %w", err)
	}
	if _, err := ecdh.X25519().NewPublicKey(p.X25519); err != nil {
		return PublicKey{}, fmt.Errorf("vault: X25519 public key: %w", err)
	}
	return p, nil
}

// MAC is HMAC-SHA-256 under the index key over the label and then each part.
// The label and every part are each preceded by their length as a uint32 BE,
// so no two different (label, parts) inputs give the same byte stream:
// ("a", "xy") differs from ("a", "x", "y"), and a label never runs into a part.
func (k *UserKeys) MAC(label string, parts ...[]byte) [32]byte {
	m := hmac.New(sha256.New, k.Index[:])
	var n [4]byte
	writeLen := func(l int) {
		if uint64(l) > 0xffffffff {
			panic(errors.New("vault: MAC input longer than 4 GiB"))
		}
		binary.BigEndian.PutUint32(n[:], uint32(l))
		m.Write(n[:])
	}
	writeLen(len(label))
	m.Write([]byte(label))
	for _, p := range parts {
		writeLen(len(p))
		m.Write(p)
	}
	var out [32]byte
	m.Sum(out[:0])
	return out
}
