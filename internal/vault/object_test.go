// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"
)

func objectKeys(t testing.TB) (*UserKeys, PublicKey) {
	t.Helper()
	k, err := GenerateUserKeys(3)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := k.Public()
	if err != nil {
		t.Fatal(err)
	}
	return k, pub
}

func keysForOnly(k *UserKeys) func(uint32) (*UserKeys, error) {
	return func(v uint32) (*UserKeys, error) {
		if v != k.Version {
			return nil, errors.New("unknown version")
		}
		return k, nil
	}
}

func seal(t testing.TB, pub PublicKey, version uint32, body []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := SealObject(&b, pub, version, RawDigest(pub, body), bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// open reads a sealed object to the end and returns the first error, from
// OpenObject or from the body.
func open(sealed []byte, keysFor func(uint32) (*UserKeys, error)) (ObjectHeader, []byte, error) {
	h, r, err := OpenObject(bytes.NewReader(sealed), keysFor)
	if err != nil {
		return h, nil, err
	}
	body, err := io.ReadAll(r)
	return h, body, err
}

func randomBody(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// chunkCount is how many chunks a body of n bytes takes: an empty body is one
// empty final chunk, a body of exactly 64 KiB is one full final chunk.
func chunkCount(n int) int {
	if n == 0 {
		return 1
	}
	return (n + objectChunkSize - 1) / objectChunkSize
}

func TestObjectHeaderLayout(t *testing.T) {
	_, pub := objectKeys(t)
	body := []byte("payload")
	digest := RawDigest(pub, body)
	sealed := seal(t, pub, 0x01020304, body)
	if objectHeaderSize != 4+1+4+32+1568+32 {
		t.Fatalf("header size %d", objectHeaderSize)
	}
	if string(sealed[:4]) != "OHSV" || sealed[4] != 0x01 {
		t.Fatalf("magic and format %x", sealed[:5])
	}
	if got := binary.BigEndian.Uint32(sealed[5:9]); got != 0x01020304 {
		t.Fatalf("version %#x", got)
	}
	if !bytes.Equal(sealed[9:41], digest[:]) {
		t.Fatal("digest not at offset 9")
	}
	if want := objectHeaderSize + chunkCount(len(body))*objectChunkOverhead + len(body); len(sealed) != want {
		t.Fatalf("sealed %d bytes, want %d", len(sealed), want)
	}
}

func TestObjectRoundTrip(t *testing.T) {
	k, pub := objectKeys(t)
	for _, n := range []int{0, 1, 65535, 65536, 65537, 5 << 20} {
		body := randomBody(t, n)
		sealed := seal(t, pub, k.Version, body)
		if want := objectHeaderSize + chunkCount(n)*objectChunkOverhead + n; len(sealed) != want {
			t.Fatalf("size %d: sealed %d bytes, want %d", n, len(sealed), want)
		}
		h, got, err := open(sealed, keysForOnly(k))
		if err != nil {
			t.Fatalf("size %d: %v", n, err)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("size %d: body differs", n)
		}
		if h.Version != k.Version || h.Digest != RawDigest(pub, body) {
			t.Fatalf("size %d: header %+v", n, h)
		}
	}
}

func TestObjectSealTwiceDiffers(t *testing.T) {
	k, pub := objectKeys(t)
	a := seal(t, pub, k.Version, []byte("same"))
	b := seal(t, pub, k.Version, []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of one body are equal")
	}
}

// chunkBounds returns the offset where each chunk of sealed starts, and the
// end of the object.
func chunkBounds(sealed []byte, n int) []int {
	var out []int
	off := objectHeaderSize
	for i := range chunkCount(n) {
		out = append(out, off)
		size := objectChunkSize
		if i == chunkCount(n)-1 {
			size = n - i*objectChunkSize
		}
		off += objectChunkOverhead + size
	}
	return append(out, off)
}

func TestObjectTruncatedFails(t *testing.T) {
	k, pub := objectKeys(t)
	const n = 2*65536 + 100
	sealed := seal(t, pub, k.Version, randomBody(t, n))
	bounds := chunkBounds(sealed, n)
	if bounds[len(bounds)-1] != len(sealed) {
		t.Fatalf("bounds %v, len %d", bounds, len(sealed))
	}
	// Cut after every chunk but the last, inside every chunk, inside the
	// header, and at nothing at all.
	cuts := []int{0, 3, objectHeaderSize - 1, objectHeaderSize}
	for _, b := range bounds[1 : len(bounds)-1] {
		cuts = append(cuts, b, b+1, b+objectChunkOverhead-1)
	}
	cuts = append(cuts, len(sealed)-1)
	for _, c := range cuts {
		if _, _, err := open(sealed[:c], keysForOnly(k)); !errors.Is(err, ErrOpen) {
			t.Fatalf("cut at %d of %d: err %v, want ErrOpen", c, len(sealed), err)
		}
	}
}

func TestObjectEmptyBodyIsOneFinalChunk(t *testing.T) {
	k, pub := objectKeys(t)
	sealed := seal(t, pub, k.Version, nil)
	if len(sealed) != objectHeaderSize+objectChunkOverhead {
		t.Fatalf("empty body sealed to %d bytes", len(sealed))
	}
	// The header alone has no final chunk: it is a truncated object.
	if _, _, err := open(sealed[:objectHeaderSize], keysForOnly(k)); !errors.Is(err, ErrOpen) {
		t.Fatalf("header only: %v", err)
	}
}

func TestObjectSwappedChunksFail(t *testing.T) {
	k, pub := objectKeys(t)
	const n = 3 * 65536
	sealed := seal(t, pub, k.Version, randomBody(t, n))
	b := chunkBounds(sealed, n)
	// Chunks 0 and 1 are both full, so swapping them keeps every length.
	swapped := append([]byte(nil), sealed[:b[0]]...)
	swapped = append(swapped, sealed[b[1]:b[2]]...)
	swapped = append(swapped, sealed[b[0]:b[1]]...)
	swapped = append(swapped, sealed[b[2]:]...)
	if _, _, err := open(swapped, keysForOnly(k)); !errors.Is(err, ErrOpen) {
		t.Fatalf("swapped: %v", err)
	}
}

func TestObjectDroppedLastChunkFails(t *testing.T) {
	k, pub := objectKeys(t)
	// The body fills two chunks exactly, so the first one is full and
	// dropping the second leaves a stream that looks whole.
	const n = 2 * 65536
	sealed := seal(t, pub, k.Version, randomBody(t, n))
	b := chunkBounds(sealed, n)
	if _, _, err := open(sealed[:b[1]], keysForOnly(k)); !errors.Is(err, ErrOpen) {
		t.Fatalf("dropped last: %v", err)
	}
	// Dropping a middle chunk fails too.
	const m = 3*65536 + 7
	sealed = seal(t, pub, k.Version, randomBody(t, m))
	b = chunkBounds(sealed, m)
	cut := append(append([]byte(nil), sealed[:b[1]]...), sealed[b[2]:]...)
	if _, _, err := open(cut, keysForOnly(k)); !errors.Is(err, ErrOpen) {
		t.Fatalf("dropped middle: %v", err)
	}
}

func TestObjectTrailingBytesFail(t *testing.T) {
	k, pub := objectKeys(t)
	for _, n := range []int{0, 10, 65536} {
		sealed := seal(t, pub, k.Version, randomBody(t, n))
		if _, _, err := open(append(sealed, 0), keysForOnly(k)); !errors.Is(err, ErrOpen) {
			t.Fatalf("size %d plus one byte: %v", n, err)
		}
		// A whole second object appended is trailing bytes too.
		if _, _, err := open(append(sealed, seal(t, pub, k.Version, nil)...), keysForOnly(k)); !errors.Is(err, ErrOpen) {
			t.Fatalf("size %d plus an object: %v", n, err)
		}
	}
}

func TestObjectEveryHeaderByteFlipFails(t *testing.T) {
	k, pub := objectKeys(t)
	sealed := seal(t, pub, k.Version, []byte("a short body"))
	// keysFor answers every version, so a flipped version byte reaches the
	// AAD check instead of stopping at the key lookup.
	anyVersion := func(uint32) (*UserKeys, error) { return k, nil }
	for i := range objectHeaderSize {
		bad := append([]byte(nil), sealed...)
		bad[i] ^= 0x01
		if _, _, err := open(bad, anyVersion); !errors.Is(err, ErrOpen) {
			t.Fatalf("flip at header byte %d: %v", i, err)
		}
	}
}

func TestObjectEveryChunkByteFlipFails(t *testing.T) {
	k, pub := objectKeys(t)
	sealed := seal(t, pub, k.Version, []byte("a short body"))
	for i := objectHeaderSize; i < len(sealed); i++ {
		bad := append([]byte(nil), sealed...)
		bad[i] ^= 0x80
		if _, _, err := open(bad, keysForOnly(k)); !errors.Is(err, ErrOpen) {
			t.Fatalf("flip at byte %d: %v", i, err)
		}
	}
}

func TestObjectWrongUserFails(t *testing.T) {
	k, pub := objectKeys(t)
	other, _ := objectKeys(t)
	sealed := seal(t, pub, k.Version, []byte("body"))
	if _, _, err := open(sealed, keysForOnly(other)); !errors.Is(err, ErrOpen) {
		t.Fatalf("other user: %v", err)
	}
}

// A bad chunk yields none of its bytes, and the reader keeps failing.
func TestObjectNoBytesFromBadChunk(t *testing.T) {
	k, pub := objectKeys(t)
	const n = 65536 + 50
	body := randomBody(t, n)
	sealed := seal(t, pub, k.Version, body)
	b := chunkBounds(sealed, n)
	sealed[b[1]+objectChunkOverhead] ^= 1 // inside chunk 1's ciphertext
	_, r, err := OpenObject(bytes.NewReader(sealed), keysForOnly(k))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if !errors.Is(err, ErrOpen) {
		t.Fatalf("err %v", err)
	}
	if !bytes.Equal(got, body[:objectChunkSize]) {
		t.Fatalf("read %d bytes before the error, want exactly chunk 0 (%d)", len(got), objectChunkSize)
	}
	if n, err := r.Read(make([]byte, 10)); n != 0 || !errors.Is(err, ErrOpen) {
		t.Fatalf("read after failure: %d, %v", n, err)
	}
}

func TestOpenObjectKeysForErrorIsWrapped(t *testing.T) {
	_, pub := objectKeys(t)
	sealed := seal(t, pub, 9, []byte("body"))
	outage := errors.New("key provider down")
	_, _, err := OpenObject(bytes.NewReader(sealed), func(uint32) (*UserKeys, error) { return nil, outage })
	if !errors.Is(err, outage) {
		t.Fatalf("err %v does not wrap the keysFor error", err)
	}
	if errors.Is(err, ErrOpen) {
		t.Fatal("a missing key reads as a bad object")
	}
	// A bad object is ErrOpen and never reaches keysFor.
	called := false
	_, _, err = OpenObject(bytes.NewReader([]byte("OHSV\x02")), func(uint32) (*UserKeys, error) {
		called = true
		return nil, outage
	})
	if !errors.Is(err, ErrOpen) || called {
		t.Fatalf("bad format: err %v, keysFor called %v", err, called)
	}
}

func TestOpenObjectSourceErrorPassesThrough(t *testing.T) {
	k, pub := objectKeys(t)
	sealed := seal(t, pub, k.Version, randomBody(t, 70000))
	broken := errors.New("network reset")
	src := io.MultiReader(bytes.NewReader(sealed[:objectHeaderSize+100]), errReader{broken})
	_, r, err := OpenObject(src, keysForOnly(k))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, broken) || errors.Is(err, ErrOpen) {
		t.Fatalf("err %v, want the source error and not ErrOpen", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestReadObjectHeaderGzipIsNotSealed(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("{\"a\":1}\n"))
	_ = zw.Close()
	for _, in := range [][]byte{gz.Bytes(), nil, []byte("OH"), []byte("{\"a\":1}")} {
		_, sealed, err := ReadObjectHeader(bytes.NewReader(in))
		if sealed || err != nil {
			t.Fatalf("%q: sealed %v, err %v", in, sealed, err)
		}
	}
}

func TestReadObjectHeaderNeedsNoKeys(t *testing.T) {
	_, pub := objectKeys(t)
	body := []byte("body")
	sealed := seal(t, pub, 7, body)
	h, ok, err := ReadObjectHeader(bytes.NewReader(sealed))
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	if h.Version != 7 || h.Digest != RawDigest(pub, body) {
		t.Fatalf("header %+v", h)
	}
	// The magic with a bad format byte or a short header is a damaged sealed
	// object, not a legacy one.
	for _, in := range [][]byte{sealed[:objectHeaderSize-1], append([]byte("OHSV\x09"), sealed[5:]...)} {
		if _, ok, err := ReadObjectHeader(bytes.NewReader(in)); !ok || !errors.Is(err, ErrOpen) {
			t.Fatalf("damaged: ok %v, err %v", ok, err)
		}
	}
}

// Through a bufio.Reader, ReadObjectHeader only peeks: the same reader then
// goes to OpenObject or to gzip.
func TestReadObjectHeaderPeeksBufio(t *testing.T) {
	k, pub := objectKeys(t)
	body := []byte("sealed body")
	br := bufio.NewReader(bytes.NewReader(seal(t, pub, k.Version, body)))
	if _, ok, err := ReadObjectHeader(br); !ok || err != nil {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	_, r, err := OpenObject(br, keysForOnly(k))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("got %q, err %v", got, err)
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("legacy"))
	_ = zw.Close()
	br = bufio.NewReader(&gz)
	if _, ok, err := ReadObjectHeader(br); ok || err != nil {
		t.Fatalf("gzip: ok %v, err %v", ok, err)
	}
	zr, err := gzip.NewReader(br)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(zr); string(got) != "legacy" {
		t.Fatalf("gzip after peek: %q", got)
	}
}

// Without a Peek method, ReadObjectHeader reads exactly the header and no
// more, so the rest of the stream is the chunks.
func TestReadObjectHeaderReadsExactlyTheHeader(t *testing.T) {
	_, pub := objectKeys(t)
	sealed := seal(t, pub, 1, []byte("body"))
	r := bytes.NewReader(sealed)
	if _, ok, err := ReadObjectHeader(r); !ok || err != nil {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	if left := r.Len(); left != len(sealed)-objectHeaderSize {
		t.Fatalf("%d bytes left, want %d", left, len(sealed)-objectHeaderSize)
	}
}

func TestRawDigest(t *testing.T) {
	_, pub := objectKeys(t)
	body := []byte("gzipped payload")
	var in []byte
	in = append(in, "ohs.vault.raw.v1"...)
	in = append(in, pub.X25519...)
	in = append(in, pub.MLKEM...)
	in = append(in, body...)
	if RawDigest(pub, body) != sha256.Sum256(in) {
		t.Fatal("digest is not SHA-256 of label, X25519, ML-KEM, body")
	}
	_, other := objectKeys(t)
	if RawDigest(pub, body) == RawDigest(other, body) {
		t.Fatal("digest does not depend on the user")
	}
}

func TestSealObjectRejectsBadPublicKey(t *testing.T) {
	_, pub := objectKeys(t)
	bad := PublicKey{MLKEM: pub.MLKEM[:10], X25519: pub.X25519}
	if err := SealObject(io.Discard, bad, 1, [32]byte{}, bytes.NewReader(nil)); err == nil {
		t.Fatal("sealed for a truncated ML-KEM key")
	}
}

func TestSealObjectSourceErrorPassesThrough(t *testing.T) {
	_, pub := objectKeys(t)
	broken := errors.New("disk gone")
	src := io.MultiReader(bytes.NewReader(make([]byte, 100000)), errReader{broken})
	if err := SealObject(io.Discard, pub, 1, [32]byte{}, src); !errors.Is(err, broken) {
		t.Fatalf("err %v", err)
	}
}

// patternReader yields n bytes of a fixed pattern without holding them.
type patternReader struct{ left int }

func (p *patternReader) Read(b []byte) (int, error) {
	if p.left == 0 {
		return 0, io.EOF
	}
	n := min(len(b), p.left)
	for i := range b[:n] {
		b[i] = byte(p.left - i)
	}
	p.left -= n
	return n, nil
}

// TestObjectStreamsInBoundedMemory seals 64 MiB into a pipe and opens it on
// the other end, copying to io.Discard. TotalAlloc counts every byte
// allocated in the process, freed or not, so the bound does not depend on
// when the collector runs: both ends together allocate their chunk buffers
// plus fixed costs (KEM keys, AEAD state, io.Copy's 32 KiB buffer, the pipe
// goroutine). Under 1 MiB beyond the two chunk buffers is the brief's bound;
// the measured value sits near 210 KiB, both chunk buffers included; the
// rest is margin for the testing runtime's own allocations in that window.
func TestObjectStreamsInBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("64 MiB stream")
	}
	k, pub := objectKeys(t)
	const n = 64 << 20
	pr, pw := io.Pipe()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	done := make(chan error, 1)
	go func() {
		err := SealObject(pw, pub, k.Version, [32]byte{}, &patternReader{left: n})
		pw.CloseWithError(err)
		done <- err
	}()
	_, r, err := OpenObject(pr, keysForOnly(k))
	if err != nil {
		t.Fatal(err)
	}
	copied, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if copied != n {
		t.Fatalf("copied %d bytes, want %d", copied, n)
	}
	extra := after.TotalAlloc - before.TotalAlloc
	bound := uint64(2*objectChunkBufSize + 1<<20)
	t.Logf("allocated %d KiB for a %d MiB stream", extra>>10, n>>20)
	if extra > bound {
		t.Fatalf("allocated %d bytes, bound %d", extra, bound)
	}
}
