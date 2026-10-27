// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bytes"
	"crypto/cipher"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// A sealed object is a header and then a STREAM of chunks:
//
//	"OHSV" || 0x01 || user key version (uint32 BE) || digest (32) || ct_m (1568) || ct_x (32) || chunks
//	chunk: nonce (24) || ciphertext of up to 64 KiB || tag (16)
//
// Every chunk is XChaCha20-Poly1305 under the KEM output k of the header,
// used as the key directly: k is a fresh uniform 32 byte key per object, so a
// derivation step would add nothing. Each chunk's AAD is
//
//	header (all 1641 bytes, "OHSV" through ct_x) || chunk index (uint64 BE) || final flag (1 byte)
//
// so chunks cannot be reordered, dropped, truncated or moved to another
// object. Every chunk but the last holds exactly 64 KiB of plaintext; the
// last one has the final flag set and holds 0 to 64 KiB. An empty body is one
// final chunk with an empty ciphertext. No byte may follow the final chunk.
const (
	objectMagic         = "OHSV"
	objectFormat        = 0x01
	objectHeaderSize    = len(objectMagic) + 1 + 4 + 32 + mlkem.CiphertextSize1024 + 32
	objectChunkSize     = 64 << 10
	objectChunkOverhead = chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead
	// objectChunkBufSize is the one buffer each end of a stream holds: a
	// whole sealed chunk, plus one byte to look ahead for the end.
	objectChunkBufSize = objectChunkOverhead + objectChunkSize + 1
)

const rawDigestLabel = "ohs.vault.raw.v1"

// ObjectHeader is the part of a sealed object readable without keys.
type ObjectHeader struct {
	Version uint32
	Digest  [32]byte
}

// RawDigest is SHA-256("ohs.vault.raw.v1" || X25519 public key || ML-KEM
// public key || body): the content hash of a raw object for its owner. It
// needs only the public key, so a process that cannot open objects still
// recognizes a retried upload.
func RawDigest(pub PublicKey, body []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte(rawDigestLabel))
	h.Write(pub.X25519)
	h.Write(pub.MLKEM)
	h.Write(body)
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// SealObject streams src into dst as a sealed object for the owner of pub,
// under a fresh KEM key. It holds one chunk in memory, whatever the size of
// src. digest goes into the header as is; the caller computes it with
// RawDigest over the same bytes. On an error, what dst already holds is a
// partial object that never opens; the caller discards it.
func SealObject(dst io.Writer, pub PublicKey, version uint32, digest [32]byte, src io.Reader) error {
	k, ctM, ctX, err := Encapsulate(pub)
	if err != nil {
		return err
	}
	defer clear(k[:])
	if len(ctM) != mlkem.CiphertextSize1024 || len(ctX) != 32 {
		return fmt.Errorf("vault: KEM ciphertexts of %d and %d bytes", len(ctM), len(ctX))
	}
	aead, err := chacha20poly1305.NewX(k[:])
	if err != nil {
		return fmt.Errorf("vault: object cipher: %w", err)
	}
	header := make([]byte, 0, objectHeaderSize)
	header = append(header, objectMagic...)
	header = append(header, objectFormat)
	header = binary.BigEndian.AppendUint32(header, version)
	header = append(header, digest[:]...)
	header = append(header, ctM...)
	header = append(header, ctX...)
	if _, err := dst.Write(header); err != nil {
		return fmt.Errorf("vault: write object header: %w", err)
	}
	aad := chunkAAD(header)

	// buf is nonce || plaintext || room for the tag. The plaintext is read
	// one byte past a full chunk, to know whether another chunk follows;
	// that byte sits where the tag goes, so it is saved before sealing.
	const pt = chacha20poly1305.NonceSizeX
	buf := make([]byte, objectChunkBufSize)
	have := 0
	for index := uint64(0); ; index++ {
		n, err := io.ReadFull(src, buf[pt+have:pt+objectChunkSize+1])
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("vault: read object body: %w", err)
		}
		have += n
		final := have <= objectChunkSize
		size := min(have, objectChunkSize)
		carry := buf[pt+objectChunkSize]
		if _, err := rand.Read(buf[:pt]); err != nil {
			return fmt.Errorf("vault: object nonce: %w", err)
		}
		setChunkAAD(aad, index, final)
		sealed := aead.Seal(buf[pt:pt], buf[:pt], buf[pt:pt+size], aad)
		if _, err := dst.Write(buf[:pt+len(sealed)]); err != nil {
			return fmt.Errorf("vault: write object chunk: %w", err)
		}
		if final {
			return nil
		}
		buf[pt] = carry
		have = 1
	}
}

// chunkAAD returns the header followed by room for the chunk index and the
// final flag, which setChunkAAD fills in for each chunk.
func chunkAAD(header []byte) []byte {
	aad := make([]byte, len(header)+8+1)
	copy(aad, header)
	return aad
}

func setChunkAAD(aad []byte, index uint64, final bool) {
	binary.BigEndian.PutUint64(aad[len(aad)-9:], index)
	aad[len(aad)-1] = 0
	if final {
		aad[len(aad)-1] = 1
	}
}

// parseObjectHeader reads a whole header. ok is false when b does not start
// with the magic; a header with the magic and an unknown format is ErrOpen.
func parseObjectHeader(b []byte) (h ObjectHeader, ok bool, err error) {
	if len(b) < len(objectMagic) || !bytes.Equal(b[:len(objectMagic)], []byte(objectMagic)) {
		return h, false, nil
	}
	if len(b) < objectHeaderSize || b[4] != objectFormat {
		return h, true, ErrOpen
	}
	h.Version = binary.BigEndian.Uint32(b[5:9])
	copy(h.Digest[:], b[9:41])
	return h, true, nil
}

// ReadObjectHeader reads the header of a sealed object without keys. ok false
// with a nil error means src is not a sealed object (a legacy gzip object);
// ok true with ErrOpen means it starts like one but the header is damaged or
// cut short.
//
// It never consumes more than the header. When src has a Peek method (a
// *bufio.Reader, whose default 4 KiB buffer holds the header), it only peeks
// and consumes nothing: the caller passes the same reader on to OpenObject or
// to gzip.NewReader. Otherwise it reads exactly the header length, or less if
// src ends first; the caller then either has what it needed (the digest) or
// fetches the object again to open it.
func ReadObjectHeader(src io.Reader) (ObjectHeader, bool, error) {
	if p, ok := src.(interface{ Peek(int) ([]byte, error) }); ok {
		b, err := p.Peek(objectHeaderSize)
		if err != nil && !errors.Is(err, io.EOF) {
			return ObjectHeader{}, false, fmt.Errorf("vault: peek object header: %w", err)
		}
		return parseObjectHeader(b)
	}
	b := make([]byte, objectHeaderSize)
	n, err := io.ReadFull(src, b)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return ObjectHeader{}, false, fmt.Errorf("vault: read object header: %w", err)
	}
	return parseObjectHeader(b[:n])
}

// OpenObject reads the header of a sealed object, opens its key with the
// user keys of the header's version and returns a reader of the body. The
// body reader authenticates each chunk whole before it returns any of its
// bytes, and fails with ErrOpen at the first bad, missing, reordered or
// trailing chunk; after that every Read fails the same way.
//
// A damaged object is ErrOpen. An error of keysFor comes back wrapped, not as
// ErrOpen, so the caller can tell a key it could not get (a provider outage:
// retry later) from an object that will never open. An error reading src
// comes back wrapped too, from OpenObject or from the body reader.
func OpenObject(src io.Reader, keysFor func(version uint32) (*UserKeys, error)) (ObjectHeader, io.Reader, error) {
	header := make([]byte, objectHeaderSize)
	n, err := io.ReadFull(src, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return ObjectHeader{}, nil, fmt.Errorf("vault: read object header: %w", err)
	}
	h, ok, err := parseObjectHeader(header[:n])
	if !ok || err != nil {
		return ObjectHeader{}, nil, ErrOpen
	}
	keys, err := keysFor(h.Version)
	if err != nil {
		return ObjectHeader{}, nil, fmt.Errorf("vault: keys of version %d for an object: %w", h.Version, err)
	}
	if keys == nil {
		return ObjectHeader{}, nil, ErrOpen
	}
	ctM := header[41 : 41+mlkem.CiphertextSize1024]
	ctX := header[41+mlkem.CiphertextSize1024:]
	k, err := keys.Decapsulate(ctM, ctX)
	if err != nil {
		return ObjectHeader{}, nil, ErrOpen
	}
	aead, err := chacha20poly1305.NewX(k[:])
	clear(k[:])
	if err != nil {
		return ObjectHeader{}, nil, ErrOpen
	}
	return h, &objectReader{
		src:  src,
		aead: aead,
		aad:  chunkAAD(header),
		buf:  make([]byte, objectChunkBufSize),
	}, nil
}

// objectReader is the body of an open object. buf holds one sealed chunk and
// the look-ahead byte; a chunk is opened in place and its plaintext served
// from buf until it runs out.
type objectReader struct {
	src   io.Reader
	aead  cipher.AEAD
	aad   []byte
	buf   []byte
	have  int    // bytes already in buf for the next chunk (the look-ahead byte)
	plain []byte // the unread rest of the current chunk's plaintext
	index uint64
	done  bool  // the final chunk opened and nothing followed it
	err   error // sticky failure
}

func (r *objectReader) Read(p []byte) (int, error) {
	for len(r.plain) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		r.err = r.next()
	}
	n := copy(p, r.plain)
	r.plain = r.plain[n:]
	return n, nil
}

// next reads, authenticates and decrypts one chunk. A full chunk is final
// when the stream ends right after it; a short one is final by its length.
// A final chunk that does not open under the final flag, or a non-final one
// that does not open without it, is ErrOpen: that is how truncation, a
// dropped chunk and trailing bytes show.
func (r *objectReader) next() error {
	n, err := io.ReadFull(r.src, r.buf[r.have:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("vault: read object chunk: %w", err)
	}
	total := r.have + n
	r.have = 0
	if total < objectChunkOverhead {
		return ErrOpen
	}
	final := total < len(r.buf)
	size := min(total, len(r.buf)-1)
	carry := r.buf[len(r.buf)-1]
	setChunkAAD(r.aad, r.index, final)
	const nonce = chacha20poly1305.NonceSizeX
	plain, err := r.aead.Open(r.buf[nonce:nonce], r.buf[:nonce], r.buf[nonce:size], r.aad)
	if err != nil {
		return ErrOpen
	}
	r.index++
	r.plain = plain
	if final {
		r.done = true
	} else {
		r.buf[0] = carry
		r.have = 1
	}
	return nil
}
