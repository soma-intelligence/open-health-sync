// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/google/uuid"
)

var (
	testTenant = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	testUser   = uuid.MustParse("22222222-2222-4222-8222-222222222222")
)

func testKeyFor(version uint32, key *[32]byte) func(uint32) (*[32]byte, error) {
	return func(v uint32) (*[32]byte, error) {
		if v != version {
			return nil, errors.New("unknown version")
		}
		return key, nil
	}
}

func randomKey(t *testing.T) *[32]byte {
	t.Helper()
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatal(err)
	}
	return &k
}

func TestRowAADLayout(t *testing.T) {
	got := rowAAD(testTenant, testUser, "samples", []byte{0xaa, 0xbb})
	var want []byte
	want = append(want, "ohs.vault.row.v1"...)
	want = append(want, testTenant[:]...)
	want = append(want, testUser[:]...)
	want = append(want, "samples"...)
	want = append(want, 0x00, 0xaa, 0xbb)
	if !bytes.Equal(got, want) {
		t.Fatalf("aad\n got %x\nwant %x", got, want)
	}
}

func TestRowRoundTrip(t *testing.T) {
	key := randomKey(t)
	aad := rowAAD(testTenant, testUser, "samples", []byte("identity"))
	for _, plain := range [][]byte{{}, []byte("x"), bytes.Repeat([]byte{7}, 4096)} {
		sealed, err := sealRow(key, 5, aad, plain)
		if err != nil {
			t.Fatal(err)
		}
		if len(sealed) != len(plain)+rowOverhead {
			t.Fatalf("sealed %d bytes for %d, want overhead %d", len(sealed), len(plain), rowOverhead)
		}
		if sealed[0] != 0x02 || !bytes.Equal(sealed[1:5], []byte{0, 0, 0, 5}) {
			t.Fatalf("header %x", sealed[:5])
		}
		got, err := openRow(testKeyFor(5, key), aad, sealed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatal("round trip changed the plaintext")
		}
	}
}

func TestRowSealTwiceDiffers(t *testing.T) {
	key := randomKey(t)
	aad := rowAAD(testTenant, testUser, "samples", []byte("id"))
	a, err := sealRow(key, 1, aad, []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := sealRow(key, 1, aad, []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("same plaintext sealed twice gave the same bytes")
	}
}

func TestRowEveryBitFlipFails(t *testing.T) {
	key := randomKey(t)
	aad := rowAAD(testTenant, testUser, "samples", []byte("id"))
	sealed, err := sealRow(key, 1, aad, []byte("heart rate 61"))
	if err != nil {
		t.Fatal(err)
	}
	keyFor := testKeyFor(1, key)
	for i := range len(sealed) * 8 {
		mod := bytes.Clone(sealed)
		mod[i/8] ^= 1 << (i % 8)
		if _, err := openRow(keyFor, aad, mod); !errors.Is(err, ErrOpen) {
			t.Fatalf("bit %d flipped: err %v, want ErrOpen", i, err)
		}
	}
}

func TestRowAADChangeFails(t *testing.T) {
	key := randomKey(t)
	identity := []byte("hash||start")
	sealed, err := sealRow(key, 1, rowAAD(testTenant, testUser, "samples", identity), []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	other := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	cases := map[string][]byte{
		"tenant":   rowAAD(other, testUser, "samples", identity),
		"user":     rowAAD(testTenant, other, "samples", identity),
		"table":    rowAAD(testTenant, testUser, "workouts", identity),
		"identity": rowAAD(testTenant, testUser, "samples", []byte("hash||other")),
		// Moving one byte from the table name into the identity must not open.
		"boundary": rowAAD(testTenant, testUser, "sample", append([]byte("s"), identity...)),
	}
	for name, aad := range cases {
		if _, err := openRow(testKeyFor(1, key), aad, sealed); !errors.Is(err, ErrOpen) {
			t.Errorf("%s changed: err %v, want ErrOpen", name, err)
		}
	}
}

func TestRowUnknownVersionFails(t *testing.T) {
	key := randomKey(t)
	aad := rowAAD(testTenant, testUser, "samples", nil)
	sealed, err := sealRow(key, 7, aad, []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openRow(testKeyFor(6, key), aad, sealed); !errors.Is(err, ErrOpen) {
		t.Fatalf("err %v, want ErrOpen", err)
	}
	nilKey := func(uint32) (*[32]byte, error) { return nil, nil }
	if _, err := openRow(nilKey, aad, sealed); !errors.Is(err, ErrOpen) {
		t.Fatalf("nil key: err %v, want ErrOpen", err)
	}
}

func TestRowWrongKeyFails(t *testing.T) {
	aad := rowAAD(testTenant, testUser, "samples", nil)
	sealed, err := sealRow(randomKey(t), 1, aad, []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openRow(testKeyFor(1, randomKey(t)), aad, sealed); !errors.Is(err, ErrOpen) {
		t.Fatalf("err %v, want ErrOpen", err)
	}
}

func TestRowMalformedFails(t *testing.T) {
	key := randomKey(t)
	aad := rowAAD(testTenant, testUser, "samples", nil)
	sealed, err := sealRow(key, 1, aad, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty":        nil,
		"format only":  {0x02},
		"header only":  sealed[:5],
		"short by one": sealed[:len(sealed)-1],
		"wrong format": append([]byte{0x01}, sealed[1:]...),
	}
	for name, in := range cases {
		got, err := openRow(testKeyFor(1, key), aad, in)
		if !errors.Is(err, ErrOpen) || got != nil {
			t.Errorf("%s: got %x err %v, want nil and ErrOpen", name, got, err)
		}
	}
}
