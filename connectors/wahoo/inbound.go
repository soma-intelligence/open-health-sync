// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Webhooks.
//
// An application turns webhooks on in the developer portal (webhook URL and
// webhook token) and asks for the offline_data scope. Wahoo then POSTs JSON
// for every new workout summary of a user who authorized it:
//
//	{"event_type": "workout_summary", "webhook_token": "...",
//	 "user": {"id": 60462}, "workout_summary": {..., "workout": {...}}}
//
// Wahoo signs nothing: the only proof is the webhook token in the body,
// compared in constant time with OHS_WAHOO_WEBHOOK_TOKEN; a body without the
// right one is refused (401). Without the key every webhook is refused. Wahoo
// retries after 30 minutes, 4, 24 and 72 hours when it does not get a 200,
// and may deliver the same event twice.
//
// A webhook carries the whole summary, so nothing is pulled: the body, its
// token removed, is stored as one raw line of DataTypeWebhook for the user's
// connection, and Map turns it into the same workout a pull gives (same
// hash, so a later pull replaces it rather than adding one). Wahoo's rate
// limits are low, and a pull would read every workout newer than this one.
// Any other event type is acknowledged and ignored.

// ErrWebhookNotConfigured reports a webhook while OHS_WAHOO_WEBHOOK_TOKEN is
// unset.
var ErrWebhookNotConfigured = errors.New("wahoo: webhooks are off (OHS_WAHOO_WEBHOOK_TOKEN is not set)")

// ErrBadWebhookToken reports a webhook whose token is missing or wrong.
var ErrBadWebhookToken = errors.New("wahoo: webhook token missing or invalid")

// maxEventBytes caps one webhook body.
const maxEventBytes = 256 << 10

// HandleInbound reads one webhook POST. HEAD and GET (a reachability probe)
// give no event and no error.
func (c *Connector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	want := c.webhookToken()
	if want == "" {
		return nil, ErrWebhookNotConfigured
	}
	body, err := readBody(r.Body, maxEventBytes)
	if err != nil {
		return nil, fmt.Errorf("wahoo: webhook body: %w", err)
	}
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&fields); err != nil {
		return nil, fmt.Errorf("wahoo: webhook body: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("wahoo: webhook body: trailing data")
	}
	var token string
	if raw, ok := fields["webhook_token"]; !ok || json.Unmarshal(raw, &token) != nil || token == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		return nil, ErrBadWebhookToken
	}
	var b webhookBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, fmt.Errorf("wahoo: webhook body: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(b.EventType), "workout_summary") {
		modlog().Info().Str("event_type", b.EventType).Msg("webhook_event_ignored")
		return nil, nil
	}
	if b.User == nil || b.User.ID.String() == "" {
		return nil, errors.New("wahoo: webhook without user.id")
	}
	if b.WorkoutSummary == nil || b.WorkoutSummary.Workout == nil || b.WorkoutSummary.Workout.ID.String() == "" {
		return nil, errors.New("wahoo: webhook without workout_summary.workout")
	}
	// The token is a credential: it is never stored.
	delete(fields, "webhook_token")
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return []connector.InboundEvent{{
		ProviderUserID: b.User.ID.String(),
		DataType:       DataTypeWebhook,
		Lines:          [][]byte{line},
	}}, nil
}
