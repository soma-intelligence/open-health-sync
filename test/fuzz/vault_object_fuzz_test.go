// SPDX-License-Identifier: AGPL-3.0-only

package fuzz

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// fuzzObjectKeys is a fixed key set, so a saved failing input opens the
// same way on every run.
func fuzzObjectKeys(f *testing.F) *vault.UserKeys {
	b := make([]byte, 1+4+32+32+64+32)
	b[0] = 0x01
	b[4] = 1 // version 1
	for i := 5; i < len(b); i++ {
		b[i] = byte(sha256.Sum256([]byte{byte(i)})[0])
	}
	k, err := vault.UnmarshalUserKeys(b)
	if err != nil {
		f.Fatal(err)
	}
	return k
}

// FuzzOpenObject opens arbitrary bytes as a sealed object. No input may
// panic it; a failure is ErrOpen; an input ReadObjectHeader calls legacy
// never opens; and an object that opens never yields more bytes than it
// holds, nor bytes once the reader has failed.
func FuzzOpenObject(f *testing.F) {
	k := fuzzObjectKeys(f)
	pub, err := k.Public()
	if err != nil {
		f.Fatal(err)
	}
	for _, body := range [][]byte{nil, []byte("x"), bytes.Repeat([]byte{0xab}, 70000)} {
		var b bytes.Buffer
		if err := vault.SealObject(&b, pub, k.Version, vault.RawDigest(pub, body), bytes.NewReader(body)); err != nil {
			f.Fatal(err)
		}
		f.Add(b.Bytes())
	}
	f.Add([]byte("OHSV\x01"))
	f.Add([]byte{0x1f, 0x8b, 0x08, 0x00})
	f.Add([]byte{})
	keysFor := func(v uint32) (*vault.UserKeys, error) {
		if v != k.Version {
			return nil, errors.New("no such version")
		}
		return k, nil
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, sealed, herr := vault.ReadObjectHeader(bytes.NewReader(data))
		_, r, err := vault.OpenObject(bytes.NewReader(data), keysFor)
		if err != nil {
			// ErrOpen, or the version lookup's own error for a header that
			// names another version.
			if !errors.Is(err, vault.ErrOpen) && (!sealed || herr != nil) {
				t.Fatalf("OpenObject error %v on a header ReadObjectHeader refused", err)
			}
			return
		}
		if !sealed || herr != nil {
			t.Fatalf("OpenObject accepted a header ReadObjectHeader refused (sealed %v, err %v)", sealed, herr)
		}
		got, err := io.ReadAll(r)
		if len(got) > len(data) {
			t.Fatalf("read %d bytes from a %d byte object", len(got), len(data))
		}
		if err != nil {
			if !errors.Is(err, vault.ErrOpen) {
				t.Fatalf("body error %v, want ErrOpen", err)
			}
			if n, _ := r.Read(make([]byte, 16)); n != 0 {
				t.Fatalf("read %d bytes after a failure", n)
			}
		}
	})
}
