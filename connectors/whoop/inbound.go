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
// A deleted sleep or workout (sleep.deleted, workout.deleted) is handed back
// verbatim as a raw line of DataTypeWebhook: the core stores it for every
// connection of the WHOOP user, and Map turns it into a model.Deletion of
// that sleep or workout id, which removes the stored record and its samples
// and keeps it from coming back. recovery.deleted is acknowledged and not
// applied: WHOOP names the recovery by its sleep id, while the recovery
// figures are stored under their cycle id (see docs/connectors/whoop.md).

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

// appliedDeletion reports a deletion event whose id is the external id of a
// stored record: a sleep or a workout.
func appliedDeletion(eventType string) bool {
	return eventType == EventSleepDeleted || eventType == EventWorkoutDeleted
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
// for an update, the webhook body as a raw line of DataTypeWebhook for a
// deleted sleep or workout. HEAD and GET (a reachability check) give no event
// and no error. A bad, missing or stale signature gives ErrBadSignature.
// recovery.deleted, or an event type this connector does not know, gives no
// event and no error, so the route still answers 200 and WHOOP does not
// retry. A body that is not the documented JSON gives an error.
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
	if appliedDeletion(wh.Type) {
		if wh.ID == "" {
			return nil, errors.New("whoop: deletion webhook has no id")
		}
		return []connector.InboundEvent{{
			ProviderUserID: string(wh.UserID),
			DataType:       DataTypeWebhook,
			Lines:          [][]byte{body},
		}}, nil
	}
	dt := EventToDataType(wh.Type)
	if dt == "" {
		if isDeletion(wh.Type) {
			slog.Info("whoop: recovery deletion not applied, recoveries are stored by cycle",
				"provider", Slug, "event", wh.Type)
		} else {
			slog.Info("whoop: webhook of an unknown type ignored", "provider", Slug, "event", wh.Type)
		}
		return nil, nil
	}
	return []connector.InboundEvent{{
		ProviderUserID: string(wh.UserID),
		DataType:       dt,
		From:           now.Add(-InboundLookback),
		To:             now,
	}}, nil
}

// mapWebhook turns a stored deletion webhook into the deletion of its sleep
// or workout. Any other webhook line gives nothing.
func mapWebhook(line []byte, _ string) (*model.MapResult, error) {
	var wh webhook
	if err := decode(DataTypeWebhook, line, &wh); err != nil {
		return nil, err
	}
	if !appliedDeletion(wh.Type) {
		return nil, nil
	}
	if wh.ID == "" {
		return nil, fmt.Errorf("whoop: webhook: %s without id", wh.Type)
	}
	return &model.MapResult{Deletions: []model.Deletion{{Provider: Slug, ExternalID: string(wh.ID)}}}, nil
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
