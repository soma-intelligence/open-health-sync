// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Webhooks.
//
// WHOOP posts a JSON body {"user_id", "id", "type", "trace_id"} to the one
// webhook URL set in the developer dashboard (<OHS_PUBLIC_URL>/inbound/whoop),
// for every user of the application: there is nothing to subscribe per user.
// It signs each delivery: X-WHOOP-Signature is the base64 HMAC-SHA256 of the
// X-WHOOP-Signature-Timestamp header value followed by the raw body, keyed by
// the client secret. HandleInbound recomputes it, and refuses a timestamp
// further than SignatureTolerance from now, so a captured delivery cannot be
// replayed later. The caller (collect) must still check that the returned
// ProviderUserID belongs to an active connection.
//
// A webhook names a record, not a time window. An updated record becomes a
// pull of the last InboundLookback, which covers the record in all but the
// rarest cases (an edit to an old night); the scheduled pull covers the rest.
// A deletion (sleep.deleted, workout.deleted, recovery.deleted) is handed back
// verbatim as a raw line of DataTypeWebhook: the core stores it for every
// connection of the WHOOP user, and Map turns it into a model.Deletion, which
// removes the stored record and its samples and keeps it from coming back.
// A v2 webhook names a sleep or workout by its UUID, and a recovery by the
// UUID of its sleep: recovery samples are stored under "<sleep id>_recovery"
// (see recovery.go), so recovery.deleted is the deletion of that id.
//
// A delivery replayed within SignatureTolerance is accepted again: WHOOP
// retries a delivery it thinks failed, and refusing a copy could drop the
// only one the core stored. A replay is harmless: an update queues one more
// pull of a few days, and a deletion is stored once (same bytes) and applied
// once.

// ErrBadSignature reports a webhook whose signature is missing, wrong or
// stale. The inbound route answers it with 401.
var ErrBadSignature = errors.New("whoop: webhook signature missing, invalid or stale")

// Webhook headers.
const (
	HeaderSignature = "X-WHOOP-Signature"
	HeaderTimestamp = "X-WHOOP-Signature-Timestamp"
)

// SignatureTolerance is how far the signed timestamp may lie from now.
const SignatureTolerance = 5 * time.Minute

// InboundLookback is the window a webhook pulls, ending now. WHOOP scores a
// night or a workout within hours, so three days covers late scoring and a
// recent edit.
const InboundLookback = 72 * time.Hour

// maxInboundBody caps a webhook body; a real one is a few hundred bytes.
const maxInboundBody = 64 << 10

// WHOOP webhook event types (v2).
// https://developer.whoop.com/docs/developing/webhooks
const (
	EventRecoveryUpdated = "recovery.updated"
	EventRecoveryDeleted = "recovery.deleted"
	EventSleepUpdated    = "sleep.updated"
	EventSleepDeleted    = "sleep.deleted"
	EventWorkoutUpdated  = "workout.updated"
	EventWorkoutDeleted  = "workout.deleted"
)

// EventToDataType maps an update event to the data type to pull, or "" for
// a deletion or an event this connector does not know.
//
// Deletions are handled by HandleInbound and mapWebhook instead.
func EventToDataType(eventType string) string {
	switch eventType {
	case EventRecoveryUpdated:
		return DataTypeRecovery
	case EventSleepUpdated:
		return DataTypeSleep
	case EventWorkoutUpdated:
		return DataTypeWorkout
	default:
		return ""
	}
}

// isDeletion reports a deletion event.
func isDeletion(eventType string) bool {
	return eventType == EventRecoveryDeleted || eventType == EventSleepDeleted || eventType == EventWorkoutDeleted
}

// deletedRecordID is the stored record id a deletion event removes: the sleep
// or workout id itself, or "<sleep id>_recovery" for a recovery (WHOOP names
// a recovery by its sleep).
func deletedRecordID(eventType, id string) string {
	if eventType == EventRecoveryDeleted {
		return recoveryRecordID(id, "")
	}
	return id
}

// webhook is the body of a WHOOP webhook.
type webhook struct {
	UserID  FlexString `json:"user_id"`
	ID      FlexString `json:"id"`
	Type    string     `json:"type"`
	TraceID string     `json:"trace_id"`
}

// Sign returns the X-WHOOP-Signature of body sent at timestamp (milliseconds
// since the epoch, as sent in X-WHOOP-Signature-Timestamp).
func Sign(secret, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp))
	m.Write(body)
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// HandleInbound verifies a WHOOP webhook and returns what it asks for: a pull
// for an update, the webhook body (compacted to one line) as a raw line of
// DataTypeWebhook for a deleted sleep, workout or recovery. HEAD and GET (a
// reachability check) give no event and no error. A bad, missing or stale
// signature gives ErrBadSignature. An event type this connector does not know
// gives no event and no error, so the route still answers 200 and WHOOP does
// not retry. A body that is not the documented JSON gives an error.
func (c *Connector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	_, secret := c.credentials()
	if secret == "" {
		return nil, errNotConfigured
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundBody+1))
	if err != nil {
		return nil, fmt.Errorf("whoop: webhook body: %w", err)
	}
	if len(body) > maxInboundBody {
		return nil, fmt.Errorf("whoop: webhook body exceeds %d bytes", maxInboundBody)
	}

	now := c.now().UTC()
	if err := verify(secret, r.Header.Get(HeaderTimestamp), r.Header.Get(HeaderSignature), body, now); err != nil {
		return nil, err
	}

	var wh webhook
	if err := json.Unmarshal(body, &wh); err != nil {
		return nil, fmt.Errorf("whoop: webhook body is not JSON: %w", err)
	}
	if wh.UserID == "" || wh.Type == "" {
		return nil, errors.New("whoop: webhook body has no user_id or type")
	}
	if isDeletion(wh.Type) {
		if wh.ID == "" {
			return nil, errors.New("whoop: deletion webhook has no id")
		}
		// One stored line is one JSON object on one line.
		line, err := compact(body)
		if err != nil {
			return nil, fmt.Errorf("whoop: webhook body is not JSON: %w", err)
		}
		return []connector.InboundEvent{{
			ProviderUserID: string(wh.UserID),
			DataType:       DataTypeWebhook,
			Lines:          [][]byte{line},
		}}, nil
	}
	dt := EventToDataType(wh.Type)
	if dt == "" {
		slog.Info("whoop: webhook of an unknown type ignored", "provider", Slug, "event", truncate(wh.Type, 64))
		return nil, nil
	}
	return []connector.InboundEvent{{
		ProviderUserID: string(wh.UserID),
		DataType:       dt,
		From:           now.Add(-InboundLookback),
		To:             now,
	}}, nil
}

// mapWebhook turns a stored deletion webhook into the deletion of its sleep,
// workout or recovery (see deletedRecordID). Any other webhook line gives
// nothing.
func mapWebhook(line []byte, _ string) (*model.MapResult, error) {
	var wh webhook
	if err := decode(DataTypeWebhook, line, &wh); err != nil {
		return nil, err
	}
	if !isDeletion(wh.Type) {
		return nil, nil
	}
	if wh.ID == "" {
		return nil, fmt.Errorf("whoop: webhook: %s without id", wh.Type)
	}
	return &model.MapResult{Deletions: []model.Deletion{{Provider: Slug, ExternalID: deletedRecordID(wh.Type, string(wh.ID))}}}, nil
}

// truncate shortens a provider supplied string before it reaches a log line.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// verify checks the signature of body and that its timestamp lies within
// SignatureTolerance of now.
func verify(secret, timestamp, signature string, body []byte, now time.Time) error {
	if timestamp == "" || signature == "" {
		return fmt.Errorf("%w: missing header", ErrBadSignature)
	}
	ms, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: timestamp is not a number", ErrBadSignature)
	}
	if d := now.Sub(time.UnixMilli(ms)); d > SignatureTolerance || d < -SignatureTolerance {
		return fmt.Errorf("%w: timestamp outside the tolerance", ErrBadSignature)
	}
	want := Sign(secret, timestamp, body)
	if !hmac.Equal([]byte(signature), []byte(want)) {
		return ErrBadSignature
	}
	return nil
}
