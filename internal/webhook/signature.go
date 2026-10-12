// SPDX-License-Identifier: AGPL-3.0-only

// Package webhook sends outgoing webhooks: a tenant registers endpoints, and
// each event (new or changed data, provider deletions, a deleted user, a
// connection that needs reauthorization) is posted to every endpoint that
// wants it. Deliveries are written in the transaction that produces the event
// and posted by the etl role with retries. Requests follow the Standard
// Webhooks specification (https://www.standardwebhooks.com): headers
// webhook-id, webhook-timestamp and webhook-signature, HMAC-SHA256 over
// "id.timestamp.body". Payloads are thin: they say what changed, for whom and
// over which days, never a health value; the receiver reads the data through
// the API.
package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SecretPrefix starts every signing secret, as in Standard Webhooks.
const SecretPrefix = "whsec_"

// secretBytes is the length of the random key behind a secret.
const secretBytes = 32

// Headers of a delivery request.
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

// Tolerance is how far a timestamp may be from the receiver's clock before
// Verify refuses it, against replays.
const Tolerance = 5 * time.Minute

// NewSecret returns a fresh signing secret: whsec_ and 32 random bytes in
// standard base64.
func NewSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return SecretPrefix + base64.StdEncoding.EncodeToString(b), nil
}

// ErrSecret is returned for a secret that is not whsec_ followed by base64.
var ErrSecret = errors.New("webhook: malformed secret")

func secretKey(secret string) ([]byte, error) {
	raw, ok := strings.CutPrefix(secret, SecretPrefix)
	if !ok {
		return nil, ErrSecret
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) == 0 {
		return nil, ErrSecret
	}
	return key, nil
}

// Sign returns the webhook-signature header value for one request: "v1,"
// and the base64 HMAC-SHA256 of "id.timestamp.body" keyed by the secret.
func Sign(secret, id string, ts time.Time, body []byte) (string, error) {
	key, err := secretKey(secret)
	if err != nil {
		return "", err
	}
	return "v1," + base64.StdEncoding.EncodeToString(mac(key, id, ts.Unix(), body)), nil
}

func mac(key []byte, id string, ts int64, body []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(id))
	h.Write([]byte{'.'})
	h.Write([]byte(strconv.FormatInt(ts, 10)))
	h.Write([]byte{'.'})
	h.Write(body)
	return h.Sum(nil)
}

// ErrVerify is returned by Verify for a request it does not accept.
var ErrVerify = errors.New("webhook: signature verification failed")

// Verify checks a received request the way a receiver should: the timestamp
// within Tolerance of now, and one of the space-separated v1 signatures
// matching. It is what receivers written in Go can call, and what the tests
// check deliveries with.
func Verify(secret, id, timestamp, signatures string, body []byte, now time.Time) error {
	key, err := secretKey(secret)
	if err != nil {
		return err
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrVerify
	}
	if d := now.Sub(time.Unix(ts, 0)); d > Tolerance || d < -Tolerance {
		return ErrVerify
	}
	want := mac(key, id, ts, body)
	for _, sig := range strings.Fields(signatures) {
		v, b64, ok := strings.Cut(sig, ",")
		if !ok || v != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(b64)
		if err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return ErrVerify
}
