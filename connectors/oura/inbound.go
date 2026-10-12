// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Webhooks.
//
// Oura subscriptions belong to the application, not to a user: the operator
// creates one per data type and event type, once, with the client id and
// secret (POST /v2/webhook/subscription, see docs/connectors/oura.md), and
// every user of the application is notified at <OHS_PUBLIC_URL>/inbound/oura.
// The connector therefore implements neither connector.Subscriber nor any
// per-connection registration.
//
// Verification. Creating a subscription makes Oura send a GET with
// verification_token and challenge; AnswerChallenge echoes
// {"challenge": "..."} when the token matches OHS_OURA_VERIFICATION_TOKEN.
// Without that key webhooks are off: challenges and notifications are
// refused, and the 6 hour pull still collects everything.
//
// Signature. A notification carries x-oura-signature, the hex HMAC-SHA256 of
// the x-oura-timestamp header value followed by the raw body, keyed by the
// client secret. HandleInbound recomputes it (the case of the hex digits does
// not matter) and refuses a missing or wrong signature, and a timestamp it can
// read that is more than SignatureMaxAge old or SignatureMaxSkew ahead.
//
// Events. The body is {event_type, data_type, object_id, event_time,
// user_id}; user_id is the Oura user id read at connect (personal_info):
//
//	create, update            -> a pull of the last InboundLookback for that
//	                             user (a pull fetches every data type).
//	delete of a sleep, workout -> the body, verbatim, as a raw line of
//	or session                    DataTypeWebhook; Map turns it into a
//	                             model.Deletion of object_id.
//	delete of anything else   -> nothing: a daily document is keyed by its
//	                             day and replaced by the next copy, and the
//	                             other types (tags...) are not stored.

// Key names of the webhook settings (OHS_OURA_<KEY>).
const (
	KeyVerificationToken = "VERIFICATION_TOKEN"
)

// Webhook headers.
const (
	HeaderSignature = "x-oura-signature"
	HeaderTimestamp = "x-oura-timestamp"
)

// SignatureMaxAge is how old a signed timestamp may be. Oura retries a
// delivery that failed, so this leaves room for a retry or two while a
// captured delivery cannot be replayed later.
const SignatureMaxAge = time.Hour

// SignatureMaxSkew is how far ahead of the server clock a signed timestamp
// may be.
const SignatureMaxSkew = 5 * time.Minute

// InboundLookback is the window a create or update event pulls, ending now.
// Oura documents name a day (a sleep its wake-up day), so three days cover a
// late ring sync and a recent edit; the scheduled pull covers the rest.
const InboundLookback = 72 * time.Hour

// maxInboundBody caps a notification body; a real one is a few hundred bytes.
const maxInboundBody = 64 << 10

// Errors of the webhook routes; the inbound routes answer them with 401.
var (
	// ErrWebhooksOff reports a challenge or notification while
	// OHS_OURA_VERIFICATION_TOKEN is unset.
	ErrWebhooksOff = errors.New("oura: webhooks are off (OHS_OURA_VERIFICATION_TOKEN is not set)")
	// ErrBadVerificationToken reports a subscription challenge whose
	// verification token is missing or wrong.
	ErrBadVerificationToken = errors.New("oura: subscription challenge verification token missing or invalid")
	// ErrBadSignature reports a notification whose signature is missing,
	// wrong or stale.
	ErrBadSignature = errors.New("oura: webhook signature missing, invalid or stale")
)

// Oura webhook event types.
const (
	EventCreate = "create"
	EventUpdate = "update"
	EventDelete = "delete"
)

// deletableTypes are the data types whose delete event removes a stored
// record: the ones stored under their Oura id.
var deletableTypes = map[string]bool{
	DataTypeSleep:   true,
	DataTypeWorkout: true,
	DataTypeSession: true,
}

// notification is the body of an Oura webhook.
type notification struct {
	EventType string `json:"event_type"`
	DataType  string `json:"data_type"`
	ObjectID  string `json:"object_id"`
	EventTime string `json:"event_time"`
	UserID    string `json:"user_id"`
}

// Sign returns the x-oura-signature of body sent with timestamp: the upper
// case hex HMAC-SHA256 of timestamp followed by body, keyed by secret.
func Sign(secret, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp))
	m.Write(body)
	return strings.ToUpper(hex.EncodeToString(m.Sum(nil)))
}

// webhookKeys returns the verification token and the client secret.
func (c *Connector) webhookKeys() (verificationToken, secret string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeyVerificationToken], c.keys[KeyClientSecret]
}

// AnswerChallenge answers the GET Oura sends when a subscription is created.
// A GET without challenge (a plain reachability probe) gets 200 with no body.
func (c *Connector) AnswerChallenge(r *http.Request) (string, []byte, error) {
	q := r.URL.Query()
	challenge := q.Get("challenge")
	if challenge == "" {
		return "", nil, nil
	}
	want, _ := c.webhookKeys()
	if want == "" {
		return "", nil, ErrWebhooksOff
	}
	got := q.Get("verification_token")
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return "", nil, ErrBadVerificationToken
	}
	body, err := json.Marshal(map[string]string{"challenge": challenge})
	if err != nil {
		return "", nil, err
	}
	return "application/json", body, nil
}

// HandleInbound verifies an Oura notification and returns what it asks for
// (see the table above). HEAD and GET give no event and no error
// (AnswerChallenge serves them). Webhooks off, or a bad signature, is an
// error; so is a body that is not a notification. An event this connector
// does not act on (a data type it does not store, an event type it does not
// know) gives no event and no error, so the route answers 200 and Oura does
// not retry. Event and data types are compared without regard to case.
//
// Replays. A delivery is accepted as often as it arrives within
// SignatureMaxAge: Oura retries one it thinks failed, and only the core knows
// whether it was stored. Processing one twice is harmless: a pull is
// idempotent, the raw store keeps identical bytes once, and a deletion
// applied twice changes nothing.
func (c *Connector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	token, secret := c.webhookKeys()
	if token == "" {
		return nil, ErrWebhooksOff
	}
	if secret == "" {
		return nil, errNotConfigured
	}
	if r.Body == nil {
		return nil, errors.New("oura: webhook without a body")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundBody+1))
	if err != nil {
		return nil, fmt.Errorf("oura: webhook body: %w", err)
	}
	if len(body) > maxInboundBody {
		return nil, fmt.Errorf("oura: webhook body exceeds %d bytes", maxInboundBody)
	}
	now := c.now().UTC()
	if err := verifySignature(secret, r.Header.Get(HeaderTimestamp), r.Header.Get(HeaderSignature), body, now); err != nil {
		return nil, err
	}

	n, err := parseNotification(body)
	if err != nil {
		return nil, err
	}
	if n.UserID == "" || n.EventType == "" || n.DataType == "" {
		return nil, errors.New("oura: webhook body has no user_id, event_type or data_type")
	}

	switch n.EventType {
	case EventCreate, EventUpdate:
		if !pulledTypes[n.DataType] {
			slog.Info("oura: webhook for a data type not stored, ignored", "provider", Slug, "data_type", clip(n.DataType, 64))
			return nil, nil
		}
		return []connector.InboundEvent{{
			ProviderUserID: n.UserID, DataType: n.DataType, From: now.Add(-InboundLookback), To: now,
		}}, nil
	case EventDelete:
		if !deletableTypes[n.DataType] {
			slog.Info("oura: delete not applied for this data type", "provider", Slug, "data_type", clip(n.DataType, 64))
			return nil, nil
		}
		if n.ObjectID == "" {
			return nil, errors.New("oura: delete webhook has no object_id")
		}
		return []connector.InboundEvent{{
			ProviderUserID: n.UserID, DataType: DataTypeWebhook, Lines: [][]byte{bytes.TrimSpace(body)},
		}}, nil
	}
	slog.Info("oura: webhook of an unknown event type ignored", "provider", Slug, "event_type", clip(n.EventType, 64))
	return nil, nil
}

// pulledTypes are the webhook data types whose create or update queues a
// pull: the ones Fetch stores. Oura's webhook spelling of VO2 max is
// "vo2_max", the data type's own name.
var pulledTypes = func() map[string]bool {
	m := map[string]bool{}
	for _, dt := range AllDataTypes {
		m[dt] = true
	}
	return m
}()

// parseNotification decodes one notification body: exactly one JSON object,
// its event and data types in lower case.
func parseNotification(body []byte) (notification, error) {
	var n notification
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&n); err != nil {
		return notification{}, fmt.Errorf("oura: webhook body: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return notification{}, errors.New("oura: webhook body: trailing data")
	}
	n.normalize()
	return n, nil
}

// normalize lower cases the event and data types.
func (n *notification) normalize() {
	n.EventType = strings.ToLower(strings.TrimSpace(n.EventType))
	n.DataType = strings.ToLower(strings.TrimSpace(n.DataType))
}

// verifySignature checks the signature of body and, when the timestamp reads
// as a time, that it is recent. Oura documents the signature as the HMAC of
// the timestamp followed by the JSON body as its sender serializes it; the
// bytes received are that serialization, and their compact form is accepted
// too, so a body re-indented on the way still verifies.
func verifySignature(secret, timestamp, signature string, body []byte, now time.Time) error {
	timestamp, signature = strings.TrimSpace(timestamp), strings.TrimSpace(signature)
	if timestamp == "" || signature == "" {
		return fmt.Errorf("%w: missing header", ErrBadSignature)
	}
	got := []byte(strings.ToUpper(signature))
	ok := hmac.Equal(got, []byte(Sign(secret, timestamp, body)))
	if !ok {
		var b bytes.Buffer
		if json.Compact(&b, body) == nil && !bytes.Equal(b.Bytes(), body) {
			ok = hmac.Equal(got, []byte(Sign(secret, timestamp, b.Bytes())))
		}
	}
	if !ok {
		return ErrBadSignature
	}
	if at, ok := signedTime(timestamp); ok {
		if d := now.Sub(at); d > SignatureMaxAge || d < -SignatureMaxSkew {
			return fmt.Errorf("%w: timestamp outside the tolerance", ErrBadSignature)
		}
	}
	return nil
}

// signedTime reads the x-oura-timestamp value: Unix seconds (what Oura's
// documentation shows) or milliseconds, or an RFC 3339 time. A value that
// reads as none of them is accepted on the strength of the signature alone.
func signedTime(v string) (time.Time, bool) {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		if n > 1e12 {
			return time.UnixMilli(n), true
		}
		return time.Unix(n, 0), true
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// mapWebhook turns a stored delete notification into the deletion of its
// sleep period, workout or session. Any other notification gives nothing.
func mapWebhook(line []byte, _ string) (*model.MapResult, error) {
	var n notification
	if err := decode(DataTypeWebhook, line, &n); err != nil {
		return nil, err
	}
	n.normalize()
	if n.EventType != EventDelete || !deletableTypes[n.DataType] {
		return nil, nil
	}
	if n.ObjectID == "" {
		return nil, fmt.Errorf("oura: webhook: delete without object_id")
	}
	return &model.MapResult{Deletions: []model.Deletion{{Provider: Slug, ExternalID: n.ObjectID}}}, nil
}
