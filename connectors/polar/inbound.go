// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Webhooks.
//
// AccessLink has one webhook per client, not per user: the operator creates
// it once (POST /v3/webhooks with client credentials, see
// docs/connectors/polar.md) for <OHS_PUBLIC_URL>/inbound/polar. Polar then
// POSTs a JSON event for every user of the client:
//
//	{"event":"EXERCISE","user_id":475,"entity_id":"aQlC83",
//	 "timestamp":"2018-05-15T14:22:24Z","url":"https://www.polaraccesslink.com/v3/exercises/aQlC83"}
//
// with the event type in Polar-Webhook-Event and, in
// Polar-Webhook-Signature, the hex HMAC-SHA256 of the body keyed by the
// signature_secret_key Polar returned when the webhook was created. Set it
// as OHS_POLAR_WEBHOOK_SECRET and every event must carry a valid signature;
// without it events are accepted unverified (a forged one can only trigger a
// pull of the recent window of a connected user).
//
// A PING event (sent when the webhook is created or its URL changes) is
// answered 200 without a signature check, since the operator only learns the
// key from that very creation call; it triggers nothing.
//
// The event window is not trusted either: it ends now and starts at most
// InboundLookback ago (the event's timestamp, or its date when it names
// one, minus InboundOverlap).

// ErrBadSignature reports a webhook event whose signature is missing or
// wrong while a webhook secret is configured. The inbound route answers it
// with 401.
var ErrBadSignature = errors.New("polar: webhook signature missing or invalid")

// InboundLookback bounds how far back an event may pull.
const InboundLookback = 7 * 24 * time.Hour

// InboundOverlap re-covers a little before the event, so data that landed on
// the boundary is not missed.
const InboundOverlap = 2 * time.Hour

// maxInboundBody caps the body read from a webhook request.
const maxInboundBody = 1 << 20

// Header names of AccessLink webhook deliveries.
const (
	HeaderEvent     = "Polar-Webhook-Event"
	HeaderSignature = "Polar-Webhook-Signature"
)

// Webhook event types.
const (
	EventPing                = "PING"
	EventExercise            = "EXERCISE"
	EventSleep               = "SLEEP"
	EventContinuousHeartRate = "CONTINUOUS_HEART_RATE"
	EventActivitySummary     = "ACTIVITY_SUMMARY"
	EventPhysicalInformation = "PHYSICAL_INFORMATION"
)

// WebhookEvents are the event types to subscribe the client's webhook to.
var WebhookEvents = []string{EventExercise, EventSleep, EventContinuousHeartRate, EventActivitySummary, EventPhysicalInformation}

// EventToDataType maps a webhook event type to the data type to pull, or ""
// for an event this connector does not pull (SleepWise events, for example).
func EventToDataType(event string) string {
	switch strings.ToUpper(event) {
	case EventExercise:
		return DataTypeExercise
	case EventSleep:
		return DataTypeSleep
	case EventContinuousHeartRate:
		return DataTypeContinuousHR
	case EventActivitySummary:
		return DataTypeActivity
	case EventPhysicalInformation:
		return DataTypePhysicalInfo
	default:
		return ""
	}
}

// webhookEvent is one AccessLink webhook payload.
type webhookEvent struct {
	Event     string      `json:"event"`
	UserID    json.Number `json:"user_id"`
	EntityID  string      `json:"entity_id"`
	Timestamp string      `json:"timestamp"`
	Date      string      `json:"date"`
	URL       string      `json:"url"`
}

// Signature returns the hex HMAC-SHA256 of body keyed by secret, as Polar
// sends it in Polar-Webhook-Signature.
func Signature(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// validSignature compares the header against the expected HMAC, ignoring
// hex case, in constant time.
func validSignature(secret string, body []byte, header string) bool {
	got, err := hex.DecodeString(strings.TrimSpace(header))
	if err != nil || len(got) == 0 {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}

// HandleInbound verifies an AccessLink webhook event and returns the pull it
// asks for. HEAD and GET give no event and no error. PING and an event type
// this connector does not pull give no event and no error, so the route
// answers 200 and Polar does not retry. A body that is not an event is an
// error; with a webhook secret configured, so is a missing or wrong
// signature (ErrBadSignature).
func (c *Connector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundBody+1))
	if err != nil {
		return nil, fmt.Errorf("polar: webhook body: %w", err)
	}
	if len(body) > maxInboundBody {
		return nil, fmt.Errorf("polar: webhook body exceeds %d bytes", maxInboundBody)
	}
	var ev webhookEvent
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&ev); err != nil {
		return nil, fmt.Errorf("polar: webhook body is not an event: %w", err)
	}
	event := ev.Event
	if event == "" {
		event = r.Header.Get(HeaderEvent)
	}
	if strings.EqualFold(event, EventPing) {
		return nil, nil
	}
	if secret := c.webhookSecret(); secret != "" {
		if !validSignature(secret, body, r.Header.Get(HeaderSignature)) {
			return nil, ErrBadSignature
		}
	}
	if event == "" {
		return nil, errors.New("polar: webhook event has no type")
	}
	dt := EventToDataType(event)
	if dt == "" {
		slog.Info("polar: webhook event not pulled, ignored", "provider", Slug, "event", event)
		return nil, nil
	}
	userID := ev.UserID.String()
	if userID == "" {
		return nil, errors.New("polar: webhook event has no user_id")
	}
	now := c.now().UTC()
	from, to := inboundWindow(ev, now)
	return []connector.InboundEvent{{ProviderUserID: userID, DataType: dt, From: from, To: to}}, nil
}

// inboundWindow derives the pull window of an event: it ends now and starts
// InboundOverlap before the earlier of the event's timestamp and its date,
// never earlier than InboundLookback ago and never later than InboundOverlap
// ago.
func inboundWindow(ev webhookEvent, now time.Time) (time.Time, time.Time) {
	floor := now.Add(-InboundLookback)
	var start time.Time
	if t, _, err := parseTime(ev.Timestamp); err == nil {
		start = t.UTC()
	}
	if d, err := parseDate(ev.Date); err == nil && (start.IsZero() || d.Before(start)) {
		start = d
	}
	if start.IsZero() {
		return floor, now
	}
	start = start.Add(-InboundOverlap)
	if start.Before(floor) {
		return floor, now
	}
	if start.After(now.Add(-InboundOverlap)) {
		return now.Add(-InboundOverlap), now
	}
	return start, now
}
