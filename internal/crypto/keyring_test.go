// SPDX-License-Identifier: AGPL-3.0-only

package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func keyN(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

// legacySeal writes the form used before key ids, nonce || ciphertext || tag,
// with the given nonce (nil: a random one).
func legacySeal(t *testing.T, key, nonce, plain, aad []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	if nonce == nil {
		nonce = make([]byte, aead.NonceSize())
		_, err = rand.Read(nonce)
		require.NoError(t, err)
	}
	n := bytes.Clone(nonce)
	return aead.Seal(n, n, plain, aad)
}

func mustSeal(t *testing.T, b *Box) []byte {
	t.Helper()
	s, err := b.Seal([]byte("secret"))
	require.NoError(t, err)
	return s
}

func TestSealWritesVersionedHeader(t *testing.T) {
	b, err := NewKeyring(keyN(1), keyN(2))
	require.NoError(t, err)
	sealed, err := b.SealFor([]byte("tok"), []byte("row"))
	require.NoError(t, err)
	require.Equal(t, byte(Version1), sealed[0])
	id := KeyID(keyN(1))
	require.Equal(t, id[:], sealed[1:5])
	require.Len(t, sealed, 1+4+12+len("tok")+16)
	require.False(t, b.NeedsRekey(sealed))
	require.Equal(t, StatusCurrent, b.Status(sealed, []byte("row")))
}

func TestKeyIDDiffersPerKey(t *testing.T) {
	require.NotEqual(t, KeyID(keyN(1)), KeyID(keyN(2)))
	require.Equal(t, KeyID(keyN(1)), KeyID(keyN(1)))
}

func TestOpenLegacyValues(t *testing.T) {
	aad := []byte("tenant|user|provider")
	b, err := NewKeyring(keyN(1), keyN(2), keyN(3))
	require.NoError(t, err)
	for i, k := range [][]byte{keyN(1), keyN(2), keyN(3)} {
		old := legacySeal(t, k, nil, []byte("legacy"), aad)
		got, err := b.OpenFor(old, aad)
		require.NoError(t, err, "key %d", i)
		require.Equal(t, []byte("legacy"), got)
		require.True(t, b.NeedsRekey(old))
		_, err = b.OpenFor(old, []byte("other"))
		require.Error(t, err, "key %d with another aad", i)
	}
	require.Equal(t, StatusLegacy, b.Status(legacySeal(t, keyN(1), nil, []byte("x"), aad), aad))
	require.Equal(t, StatusPrevious, b.Status(legacySeal(t, keyN(3), nil, []byte("x"), aad), aad))

	// A value sealed by the Box of an earlier release opens with NewBox.
	single, err := NewBox(keyN(1))
	require.NoError(t, err)
	got, err := single.OpenFor(legacySeal(t, keyN(1), nil, []byte("legacy"), aad), aad)
	require.NoError(t, err)
	require.Equal(t, []byte("legacy"), got)
}

func TestOpenWithPreviousKey(t *testing.T) {
	aad := []byte("row")
	old, err := NewBox(keyN(1))
	require.NoError(t, err)
	sealed, err := old.SealFor([]byte("tok"), aad)
	require.NoError(t, err)

	rotated, err := NewKeyring(keyN(2), keyN(1))
	require.NoError(t, err)
	got, err := rotated.OpenFor(sealed, aad)
	require.NoError(t, err)
	require.Equal(t, []byte("tok"), got)
	require.True(t, rotated.NeedsRekey(sealed))
	require.Equal(t, StatusPrevious, rotated.Status(sealed, aad))

	// Without the previous key the value is lost.
	alone, err := NewBox(keyN(2))
	require.NoError(t, err)
	_, err = alone.OpenFor(sealed, aad)
	require.Error(t, err)
	require.Equal(t, StatusUnknown, alone.Status(sealed, aad))
}

func TestRekey(t *testing.T) {
	aad := []byte("row")
	old, _ := NewBox(keyN(1))
	sealed, _ := old.SealFor([]byte("tok"), aad)
	b, _ := NewKeyring(keyN(2), keyN(1))

	fresh, changed, err := b.Rekey(sealed, aad)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, b.NeedsRekey(fresh))
	// Only the new key is needed from now on.
	alone, _ := NewBox(keyN(2))
	got, err := alone.OpenFor(fresh, aad)
	require.NoError(t, err)
	require.Equal(t, []byte("tok"), got)

	same, changed, err := b.Rekey(fresh, aad)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, fresh, same)

	// A legacy value under the active key gets its header.
	out, changed, err := b.Rekey(legacySeal(t, keyN(2), nil, []byte("tok"), aad), aad)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, StatusCurrent, b.Status(out, aad))

	// The same aad is required: rekeying cannot move a value to another row.
	_, _, err = b.Rekey(sealed, []byte("other row"))
	require.Error(t, err)
	unknown, _ := NewBox(keyN(9))
	_, _, err = unknown.Rekey(sealed, aad)
	require.Error(t, err)
}

// A legacy value starts with a random nonce, so it can start with the version
// byte and the id of a configured key. The header path then fails to
// authenticate, and Open falls back to trying every key on the whole value.
func TestOpenAmbiguousLegacyValue(t *testing.T) {
	aad := []byte("row")
	for _, tc := range []struct {
		name            string
		sealWith, named []byte
	}{
		{"header names the active key, sealed by a previous one", keyN(2), keyN(1)},
		{"header names the previous key that sealed it", keyN(2), keyN(2)},
		{"header names the active key that sealed it", keyN(1), keyN(1)},
		{"header names a previous key, sealed by the active one", keyN(1), keyN(2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := NewKeyring(keyN(1), keyN(2))
			require.NoError(t, err)
			nonce := make([]byte, 12)
			_, _ = rand.Read(nonce)
			id := KeyID(tc.named)
			nonce[0] = Version1
			copy(nonce[1:5], id[:])
			leg := legacySeal(t, tc.sealWith, nonce, []byte("legacy"), aad)

			got, err := b.OpenFor(leg, aad)
			require.NoError(t, err)
			require.Equal(t, []byte("legacy"), got)
			_, err = b.OpenFor(leg, []byte("other"))
			require.Error(t, err)
			require.NotEqual(t, StatusCurrent, b.Status(leg, aad))
			fresh, changed, err := b.Rekey(leg, aad)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, StatusCurrent, b.Status(fresh, aad))
			got, err = b.OpenFor(fresh, aad)
			require.NoError(t, err)
			require.Equal(t, []byte("legacy"), got)
		})
	}
}

func TestKeyringAADMismatch(t *testing.T) {
	b, _ := NewKeyring(keyN(1), keyN(2))
	sealed, _ := b.SealFor([]byte("tok"), []byte("tenant-a"))
	_, err := b.OpenFor(sealed, []byte("tenant-b"))
	require.Error(t, err)
	_, err = b.Open(sealed)
	require.Error(t, err)
	require.Equal(t, StatusUnknown, b.Status(sealed, []byte("tenant-b")))
	_, _, err = b.Rekey(sealed, []byte("tenant-b"))
	require.Error(t, err)
}

func TestKeyringWrongKey(t *testing.T) {
	b, _ := NewKeyring(keyN(1), keyN(2))
	sealed := mustSeal(t, b)
	other, _ := NewKeyring(keyN(3), keyN(4))
	_, err := other.Open(sealed)
	require.Error(t, err)
	require.Equal(t, StatusUnknown, other.Status(sealed, nil))
}

func TestKeyringTamperDetected(t *testing.T) {
	b, _ := NewKeyring(keyN(1), keyN(2))
	old, _ := NewBox(keyN(2))
	for _, sealed := range [][]byte{mustSeal(t, b), mustSeal(t, old)} {
		for i := range sealed {
			bad := bytes.Clone(sealed)
			bad[i] ^= 1
			_, err := b.Open(bad)
			require.Error(t, err, "flip at byte %d", i)
		}
	}
}

func TestNewKeyringRefusesBadKeys(t *testing.T) {
	_, err := NewKeyring(keyN(1), make([]byte, 16))
	require.Error(t, err)
	_, err = NewKeyring(make([]byte, 31))
	require.Error(t, err)
	_, err = NewKeyring(keyN(1), keyN(1))
	require.Error(t, err, "a previous key equal to the active one")
	_, err = NewKeyring(keyN(1), keyN(2), keyN(2))
	require.Error(t, err, "a duplicate previous key")
	b, err := NewKeyring(keyN(1))
	require.NoError(t, err)
	require.Equal(t, 0, b.PreviousKeys())
	b, _ = NewKeyring(keyN(1), keyN(2), keyN(3))
	require.Equal(t, 2, b.PreviousKeys())
}

func TestNeedsRekeyShortOrForeign(t *testing.T) {
	b, _ := NewKeyring(keyN(1), keyN(2))
	require.True(t, b.NeedsRekey(nil))
	require.True(t, b.NeedsRekey([]byte{Version1}))
	prev, _ := NewBox(keyN(2))
	require.True(t, b.NeedsRekey(mustSeal(t, prev)))
	require.False(t, b.NeedsRekey(mustSeal(t, b)))
}
