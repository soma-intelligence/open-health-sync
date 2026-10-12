// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Webhook signatures.
//
// Google signs the raw JSON body of every notification with Tink's
// PublicKeySign (ECDSA P-256, SHA-256) and sends the signature, base64
// encoded, in GOOGLE-HEALTH-API-SIGNATURE. Decoded, it is a 5 byte Tink
// prefix (version byte 0x01, then the 4 byte big-endian key id) followed by
// the DER signature. The public keys are a Tink JSON keyset at
// DefaultKeysetURL, rotated every 30 days.
//
// This verifier follows Google's manual verification steps without the Tink
// library: it reads the keyset, decodes each EcdsaPublicKey (protobuf x and
// y, fields 3 and 4), picks the key by the prefix's key id, and checks the
// signature over the raw body. Not confirmed against a real delivery: which
// base64 alphabet Google uses (all four spellings are accepted) and whether
// a signature can come in IEEE P1363 form (r||s) instead of DER (both are
// accepted). The RAW and LEGACY Tink prefixes are handled too.
//
// The keyset is cached for KeysetTTL and fetched again early when a
// signature names a key id it does not hold (at most once a minute).

// HeaderSignature is the notification signature header.
const HeaderSignature = "GOOGLE-HEALTH-API-SIGNATURE"

// signatureMode is how HandleInbound treats signatures
// (OHS_FITBIT_WEBHOOK_SIGNATURE).
type signatureMode string

const (
	// SignatureRequired: every notification must carry a valid signature;
	// a keyset that cannot be fetched refuses notifications (Google retries
	// them for up to 7 days). The default.
	SignatureRequired signatureMode = "required"
	// SignatureIfAvailable: the signature is checked when the keyset could
	// be fetched; while it cannot, the Authorization secret alone admits a
	// notification. A signature that is present and wrong is always refused.
	SignatureIfAvailable signatureMode = "if_available"
	// SignatureOff: only the Authorization secret is checked. For an
	// instance whose notifications fail the check (the signed format is not
	// confirmed against a real delivery).
	SignatureOff signatureMode = "off"
)

// parseSignatureMode reads OHS_FITBIT_WEBHOOK_SIGNATURE; empty is required.
func parseSignatureMode(s string) (signatureMode, error) {
	switch m := signatureMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return SignatureRequired, nil
	case SignatureRequired, SignatureIfAvailable, SignatureOff:
		return m, nil
	}
	return "", fmt.Errorf("fitbit: OHS_FITBIT_WEBHOOK_SIGNATURE must be required, if_available or off, not %q", clip(s, 32))
}

// Errors of signature checking.
var (
	// ErrBadSignature reports a signature that is missing or does not verify.
	ErrBadSignature = errors.New("fitbit: webhook signature missing or invalid")
	// errKeysetUnavailable reports a keyset that could not be fetched.
	errKeysetUnavailable = errors.New("fitbit: webhook public keyset unavailable")
)

// Tink output prefix types.
const (
	prefixTink   = "TINK"
	prefixLegacy = "LEGACY"
	prefixCrunch = "CRUNCHY"
	prefixRaw    = "RAW"
)

const ecdsaPublicKeyType = "type.googleapis.com/google.crypto.tink.EcdsaPublicKey"

// publicKey is one usable key of the keyset.
type publicKey struct {
	id     uint32
	prefix string
	pub    *ecdsa.PublicKey
}

// tinkKeyset is the Tink JSON keyset format.
type tinkKeyset struct {
	PrimaryKeyID uint32 `json:"primaryKeyId"`
	Key          []struct {
		KeyData struct {
			TypeURL         string `json:"typeUrl"`
			Value           string `json:"value"`
			KeyMaterialType string `json:"keyMaterialType"`
		} `json:"keyData"`
		Status           string `json:"status"`
		KeyID            uint32 `json:"keyId"`
		OutputPrefixType string `json:"outputPrefixType"`
	} `json:"key"`
}

// parseKeyset reads a Tink JSON keyset of ECDSA P-256 public keys. Disabled
// keys and keys of another type or curve are skipped; a keyset left without
// any key is an error.
func parseKeyset(b []byte) ([]publicKey, error) {
	var ks tinkKeyset
	if err := json.Unmarshal(b, &ks); err != nil {
		return nil, fmt.Errorf("keyset: %w", err)
	}
	var out []publicKey
	for _, k := range ks.Key {
		if k.Status != "" && k.Status != "ENABLED" {
			continue
		}
		if k.KeyData.TypeURL != ecdsaPublicKeyType {
			continue
		}
		raw, err := decodeBase64(k.KeyData.Value)
		if err != nil {
			continue
		}
		pub, err := parseEcdsaPublicKey(raw)
		if err != nil {
			slog.Warn("fitbit: keyset key skipped", "provider", Slug, "key_id", k.KeyID, "error", err)
			continue
		}
		prefix := strings.ToUpper(k.OutputPrefixType)
		if prefix == "" {
			prefix = prefixTink
		}
		out = append(out, publicKey{id: k.KeyID, prefix: prefix, pub: pub})
	}
	if len(out) == 0 {
		return nil, errors.New("keyset holds no usable ECDSA P-256 key")
	}
	return out, nil
}

// parseEcdsaPublicKey decodes a serialized Tink EcdsaPublicKey protobuf:
// version (1), params (2: hash_type 1, curve 2, encoding 3), x (3), y (4),
// the coordinates big-endian, possibly with a leading zero byte. Only the
// NIST P-256 curve (enum 2) is accepted.
func parseEcdsaPublicKey(b []byte) (*ecdsa.PublicKey, error) {
	var x, y []byte
	curve := uint64(0)
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errors.New("bad protobuf tag")
		}
		b = b[n:]
		field, wire := tag>>3, tag&7
		switch wire {
		case 0:
			_, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errors.New("bad protobuf varint")
			}
			b = b[n:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return nil, errors.New("bad protobuf length")
			}
			v := b[n : n+int(l)]
			b = b[n+int(l):]
			switch field {
			case 2:
				curve = paramsCurve(v)
			case 3:
				x = v
			case 4:
				y = v
			}
		default:
			return nil, fmt.Errorf("unexpected protobuf wire type %d", wire)
		}
	}
	if curve != 0 && curve != 2 {
		return nil, fmt.Errorf("curve %d is not NIST P-256", curve)
	}
	pad := func(v []byte) ([]byte, error) {
		for len(v) > 32 && v[0] == 0 {
			v = v[1:]
		}
		if len(v) == 0 || len(v) > 32 {
			return nil, errors.New("coordinate is not 32 bytes")
		}
		return append(make([]byte, 32-len(v)), v...), nil
	}
	px, err := pad(x)
	if err != nil {
		return nil, err
	}
	py, err := pad(y)
	if err != nil {
		return nil, err
	}
	point := append(append([]byte{4}, px...), py...)
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
}

// paramsCurve returns the curve enum (field 2) of an EcdsaParams message, 0
// when absent or unreadable.
func paramsCurve(b []byte) uint64 {
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 || tag&7 != 0 {
			return 0
		}
		b = b[n:]
		v, n := binary.Uvarint(b)
		if n <= 0 {
			return 0
		}
		b = b[n:]
		if tag>>3 == 2 {
			return v
		}
	}
	return 0
}

// decodeBase64 accepts standard and URL-safe base64, padded or not.
func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	var lastErr error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		b, err := enc.DecodeString(s)
		if err == nil {
			return b, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// verifyWith checks sig (decoded, prefix included) over body with keys. It
// reports whether a key matched the signature's key id, so the caller can
// fetch a newer keyset when none did.
func verifyWith(keys []publicKey, sig, body []byte) (ok, known bool) {
	digest := sha256.Sum256(body)
	for _, k := range keys {
		var rest []byte
		data := digest
		switch k.prefix {
		case prefixRaw:
			rest = sig
		case prefixTink, prefixLegacy, prefixCrunch:
			if len(sig) < 5 || binary.BigEndian.Uint32(sig[1:5]) != k.id {
				continue
			}
			if (k.prefix == prefixTink && sig[0] != 1) || (k.prefix != prefixTink && sig[0] != 0) {
				continue
			}
			rest = sig[5:]
			if k.prefix == prefixLegacy {
				// Tink LEGACY signs the data followed by one zero byte.
				data = sha256.Sum256(append(append([]byte(nil), body...), 0))
			}
		default:
			continue
		}
		known = true
		if verifyECDSA(k.pub, data[:], rest) {
			return true, true
		}
	}
	return false, known
}

// verifyECDSA accepts a DER signature or an IEEE P1363 one (r||s, 64 bytes).
func verifyECDSA(pub *ecdsa.PublicKey, digest, sig []byte) bool {
	if ecdsa.VerifyASN1(pub, digest, sig) {
		return true
	}
	if len(sig) == 64 {
		return ecdsa.VerifyASN1(pub, digest, p1363ToDER(sig))
	}
	return false
}

// p1363ToDER re-encodes r||s as an ASN.1 SEQUENCE of two INTEGERs.
func p1363ToDER(sig []byte) []byte {
	integer := func(v []byte) []byte {
		for len(v) > 1 && v[0] == 0 {
			v = v[1:]
		}
		if v[0]&0x80 != 0 {
			v = append([]byte{0}, v...)
		}
		return append([]byte{0x02, byte(len(v))}, v...)
	}
	r, s := integer(sig[:32]), integer(sig[32:])
	body := append(r, s...)
	return append([]byte{0x30, byte(len(body))}, body...)
}

// keysetCache fetches and caches the public keyset.
type keysetCache struct {
	url   string
	ttl   time.Duration
	httpc *http.Client
	now   func() time.Time

	mu          sync.Mutex
	keys        []publicKey
	fetchedAt   time.Time
	lastAttempt time.Time
}

// minRefetch is the least time between two fetches of the keyset outside
// its TTL (a signature naming an unknown key, a fetch that failed).
const minRefetch = time.Minute

func newKeysetCache(url string, ttl time.Duration, httpc *http.Client, now func() time.Time) *keysetCache {
	return &keysetCache{url: url, ttl: ttl, httpc: httpc, now: now}
}

// get returns the cached keys, fetching them when older than the TTL, or
// when refresh is set and the last attempt is more than minRefetch old. A
// failed fetch keeps serving the previous keys while there are some.
func (k *keysetCache) get(ctx context.Context, refresh bool) ([]publicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	stale := k.keys == nil || now.Sub(k.fetchedAt) > k.ttl
	due := now.Sub(k.lastAttempt) >= minRefetch
	if (stale || refresh) && (due || k.lastAttempt.IsZero()) {
		k.lastAttempt = now
		keys, err := k.fetch(ctx)
		if err == nil {
			k.keys, k.fetchedAt = keys, now
		} else {
			slog.Warn("fitbit: fetching the webhook keyset failed", "provider", Slug, "error", err)
		}
	}
	if k.keys == nil {
		return nil, errKeysetUnavailable
	}
	return k.keys, nil
}

func (k *keysetCache) fetch(ctx context.Context) ([]publicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := k.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp.Body, 1<<20)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keyset HTTP %d", resp.StatusCode)
	}
	return parseKeyset(body)
}

// checkSignature verifies the signature header of body under mode.
func (c *Connector) checkSignature(ctx context.Context, mode signatureMode, header string, body []byte) error {
	if mode == SignatureOff {
		return nil
	}
	header = strings.TrimSpace(header)
	keys, err := c.keyset.get(ctx, false)
	if err != nil {
		if mode == SignatureIfAvailable {
			return nil
		}
		return fmt.Errorf("%w: %w", ErrBadSignature, err)
	}
	if header == "" {
		return fmt.Errorf("%w: no %s header", ErrBadSignature, HeaderSignature)
	}
	sig, err := decodeBase64(header)
	if err != nil || len(sig) == 0 {
		return fmt.Errorf("%w: not base64", ErrBadSignature)
	}
	ok, known := verifyWith(keys, sig, body)
	if !ok && !known {
		// A key id the cached keyset does not hold: Google rotated keys.
		if fresh, err := c.keyset.get(ctx, true); err == nil {
			ok, _ = verifyWith(fresh, sig, body)
		}
	}
	if !ok {
		return ErrBadSignature
	}
	return nil
}
