// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bytes"
	"testing"
)

func TestGenerateUserKeysFillsEveryField(t *testing.T) {
	k, err := GenerateUserKeys(3)
	if err != nil {
		t.Fatal(err)
	}
	if k.Version != 3 {
		t.Fatalf("version %d, want 3", k.Version)
	}
	for name, b := range map[string][]byte{"data": k.Data[:], "index": k.Index[:], "seed": k.MLKEMSeed[:], "x25519": k.X25519[:]} {
		if bytes.Equal(b, make([]byte, len(b))) {
			t.Fatalf("%s is all zero", name)
		}
	}
	other, err := GenerateUserKeys(3)
	if err != nil {
		t.Fatal(err)
	}
	if other.Data == k.Data || other.Index == k.Index || other.MLKEMSeed == k.MLKEMSeed || other.X25519 == k.X25519 {
		t.Fatal("two generated key sets share a field")
	}
}

func TestUserKeysMarshalRoundTrip(t *testing.T) {
	k, err := GenerateUserKeys(0xdeadbeef)
	if err != nil {
		t.Fatal(err)
	}
	b := k.Marshal()
	if len(b) != userKeysSize {
		t.Fatalf("marshalled %d bytes, want %d", len(b), userKeysSize)
	}
	got, err := UnmarshalUserKeys(b)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *k {
		t.Fatal("round trip changed the keys")
	}
}

func TestUnmarshalUserKeysRejects(t *testing.T) {
	k, err := GenerateUserKeys(1)
	if err != nil {
		t.Fatal(err)
	}
	b := k.Marshal()
	cases := map[string][]byte{
		"empty":          nil,
		"short":          b[:len(b)-1],
		"long":           append(bytes.Clone(b), 0),
		"unknown format": append([]byte{b[0] + 1}, b[1:]...),
		"format zero":    append([]byte{0}, b[1:]...),
	}
	for name, in := range cases {
		if _, err := UnmarshalUserKeys(in); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestUserKeysZero(t *testing.T) {
	k, err := GenerateUserKeys(9)
	if err != nil {
		t.Fatal(err)
	}
	k.Zero()
	if *k != (UserKeys{}) {
		t.Fatalf("Zero left a field set: %+v", *k)
	}
}

func TestPublicIsDerivedAndStable(t *testing.T) {
	k, err := GenerateUserKeys(1)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := k.Public()
	if err != nil {
		t.Fatal(err)
	}
	p2, err := k.Public()
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.MLKEM) != 1568 || len(p1.X25519) != 32 {
		t.Fatalf("public key sizes %d and %d", len(p1.MLKEM), len(p1.X25519))
	}
	if !bytes.Equal(p1.MLKEM, p2.MLKEM) || !bytes.Equal(p1.X25519, p2.X25519) {
		t.Fatal("Public is not deterministic")
	}
	if bytes.Equal(p1.X25519, k.X25519[:]) {
		t.Fatal("X25519 public key equals the private key")
	}
}

func TestPublicKeyMarshalRoundTrip(t *testing.T) {
	k, err := GenerateUserKeys(1)
	if err != nil {
		t.Fatal(err)
	}
	p, err := k.Public()
	if err != nil {
		t.Fatal(err)
	}
	b := p.Marshal()
	if len(b) != publicKeySize {
		t.Fatalf("marshalled %d bytes, want %d", len(b), publicKeySize)
	}
	got, err := UnmarshalPublicKey(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.MLKEM, p.MLKEM) || !bytes.Equal(got.X25519, p.X25519) {
		t.Fatal("round trip changed the public key")
	}
	// The result must not alias the input.
	b[1] ^= 0xff
	if !bytes.Equal(got.MLKEM, p.MLKEM) {
		t.Fatal("unmarshalled key aliases the input buffer")
	}
	b[1] ^= 0xff

	cases := map[string][]byte{
		"empty":          nil,
		"short":          b[:len(b)-1],
		"long":           append(bytes.Clone(b), 0),
		"unknown format": append([]byte{b[0] + 1}, b[1:]...),
	}
	for name, in := range cases {
		if _, err := UnmarshalPublicKey(in); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestMACLabelSeparation(t *testing.T) {
	k, err := GenerateUserKeys(1)
	if err != nil {
		t.Fatal(err)
	}
	x := []byte("x")
	if k.MAC("a", x) == k.MAC("b", x) {
		t.Fatal(`MAC("a", x) == MAC("b", x)`)
	}
	if k.MAC("a", []byte("xy")) == k.MAC("a", []byte("x"), []byte("y")) {
		t.Fatal(`MAC("a", "xy") == MAC("a", "x", "y")`)
	}
	// A label that ends like a length prefix must not collide with a part.
	if k.MAC("a\x00\x00\x00\x01x") == k.MAC("a", x) {
		t.Fatal("label and part boundaries collide")
	}
	first, second := k.MAC("a", x), k.MAC("a", x)
	if first != second {
		t.Fatal("MAC is not deterministic")
	}
	other, err := GenerateUserKeys(1)
	if err != nil {
		t.Fatal(err)
	}
	if other.MAC("a", x) == k.MAC("a", x) {
		t.Fatal("MAC does not depend on the index key")
	}
}
