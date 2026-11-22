// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha3"
	"fmt"
)

const kemLabel = "ohs.vault.kem.v1"

// Encapsulate draws a fresh object key for the owner of p, with no secret of
// theirs. ctM (1568 bytes) and ctX (32 bytes) go into the sealed object's
// header; only the matching UserKeys turn them back into k.
func Encapsulate(p PublicKey) (k [32]byte, ctM, ctX []byte, err error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return k, nil, nil, fmt.Errorf("vault: X25519 ephemeral key: %w", err)
	}
	return encapsulate(p, func(ek *mlkem.EncapsulationKey1024) ([]byte, []byte, error) {
		ss, ct := ek.Encapsulate()
		return ss, ct, nil
	}, eph)
}

// encapsulate is Encapsulate with its two random inputs passed in, so a test
// can pin the output bytes.
func encapsulate(p PublicKey, mlkemEncaps func(*mlkem.EncapsulationKey1024) ([]byte, []byte, error), eph *ecdh.PrivateKey) (k [32]byte, ctM, ctX []byte, err error) {
	ek, err := mlkem.NewEncapsulationKey1024(p.MLKEM)
	if err != nil {
		return k, nil, nil, fmt.Errorf("vault: ML-KEM public key: %w", err)
	}
	peer, err := ecdh.X25519().NewPublicKey(p.X25519)
	if err != nil {
		return k, nil, nil, fmt.Errorf("vault: X25519 public key: %w", err)
	}
	ssM, ctM, err := mlkemEncaps(ek)
	if err != nil {
		return k, nil, nil, fmt.Errorf("vault: ML-KEM encapsulate: %w", err)
	}
	defer clear(ssM)
	// ECDH fails on a low-order point, whose shared secret would be all zero.
	ssX, err := eph.ECDH(peer)
	if err != nil {
		return k, nil, nil, fmt.Errorf("vault: X25519: %w", err)
	}
	defer clear(ssX)
	ctX = eph.PublicKey().Bytes()
	return combineKEM(ssM, ssX, ctX, p.X25519), ctM, ctX, nil
}

// Decapsulate turns the ciphertexts of an object header back into its key.
// A ct_m made for another user does not fail (ML-KEM's implicit rejection): it
// gives an unrelated key, and the object then fails to open.
func (k *UserKeys) Decapsulate(ctM, ctX []byte) ([32]byte, error) {
	var out [32]byte
	dk, err := mlkem.NewDecapsulationKey1024(k.MLKEMSeed[:])
	if err != nil {
		return out, fmt.Errorf("vault: ML-KEM key: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(k.X25519[:])
	if err != nil {
		return out, fmt.Errorf("vault: X25519 key: %w", err)
	}
	peer, err := ecdh.X25519().NewPublicKey(ctX)
	if err != nil {
		return out, fmt.Errorf("vault: X25519 ciphertext: %w", err)
	}
	ssM, err := dk.Decapsulate(ctM)
	if err != nil {
		return out, fmt.Errorf("vault: ML-KEM decapsulate: %w", err)
	}
	defer clear(ssM)
	ssX, err := priv.ECDH(peer)
	if err != nil {
		return out, fmt.Errorf("vault: X25519: %w", err)
	}
	defer clear(ssX)
	return combineKEM(ssM, ssX, ctX, priv.PublicKey().Bytes()), nil
}

// combineKEM is k = SHA3-256("ohs.vault.kem.v1" || ss_m || ss_x || ct_x ||
// x25519_pub), the X-Wing combiner shape with ML-KEM-1024. ct_m is not hashed:
// ML-KEM binds its ciphertext already; X25519 does not, so ct_x and the
// recipient's public key are.
func combineKEM(ssM, ssX, ctX, xPub []byte) [32]byte {
	b := make([]byte, 0, len(kemLabel)+len(ssM)+len(ssX)+len(ctX)+len(xPub))
	b = append(b, kemLabel...)
	b = append(b, ssM...)
	b = append(b, ssX...)
	b = append(b, ctX...)
	b = append(b, xPub...)
	defer clear(b)
	return sha3.Sum256(b)
}
