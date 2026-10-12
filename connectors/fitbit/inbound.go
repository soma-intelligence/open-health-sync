// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"bytes"
	"context"
	"crypto/subtle"
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
// A Google Health API subscriber belongs to the Cloud project, not to a user:
// the operator creates it once (projects.subscribers.create, see
// docs/connectors/fitbit.md) with endpointUri <OHS_PUBLIC_URL>/inbound/fitbit
// and endpointAuthorization equal to OHS_FITBIT_WEBHOOK_SECRET, and with
// AUTOMATIC subscriptions every consenting user is covered. The connector
// therefore implements no connector.Subscriber.
//
// Authentication. Google sends the configured secret verbatim in the
// Authorization header (with whatever scheme the operator put in it, such as
// "Bearer ..."), compared here in constant time, and signs the body (see
// signature.go).
//
// Verification. Creating or updating the subscriber makes Google POST
// {"type": "verification"} twice: with the secret it must get 200, without
// it 401. HandleInbound returns no event and no error for the first (the
// route answers 200) and an error for the second (the route answers 401).
//
// Notifications. A body is one notification or a JSON array of them (up to
// 99), each {"data": {healthUserId, dataType, operation, intervals...}}. A
// notification only says what changed: each one becomes a pull of its data
// type over its intervals (connector.NotifiedFetcher pulls just that type),
// merged per user and data type. UPSERT and DELETE are handled alike:
// deletions are not propagated yet (a pull fetches what is still there and
// cannot tell what is gone), so data deleted at Google stays stored.
//
// The route answers 200 to an accepted notification. Google documents 204;
// any 2xx is expected to count as delivered (unverified).

// HeaderAuthorization carries the subscriber's endpointAuthorization secret.
const HeaderAuthorization = "Authorization"

// maxInboundBody caps a notification body (99 messages are a few tens of KiB).
const maxInboundBody = 1 << 20

// InboundLookback is the window pulled for a notification that names no
// interval, ending now.
const InboundLookback = 72 * time.Hour

// intervalMargin widens a notified physical interval on both sides.
const intervalMargin = time.Minute

// Errors of the webhook route; the route answers them with 401.
var (
	// ErrWebhooksOff reports a notification while OHS_FITBIT_WEBHOOK_SECRET
	// is unset.
	ErrWebhooksOff = errors.New("fitbit: webhooks are off (OHS_FITBIT_WEBHOOK_SECRET is not set)")
	// ErrBadAuthorization reports a missing or wrong Authorization header.
	ErrBadAuthorization = errors.New("fitbit: webhook Authorization header missing or invalid")
)

// notification is one message of a delivery.
type notification struct {
	Version                        string          `json:"version"`
	ClientProvidedSubscriptionName string          `json:"clientProvidedSubscriptionName"`
	HealthUserID                   string          `json:"healthUserId"`
	Operation                      string          `json:"operation"`
	DataType                       string          `json:"dataType"`
	RecordID                       string          `json:"recordId"`
	Intervals                      []notifInterval `json:"intervals"`
}

type notifInterval struct {
	PhysicalTimeInterval *struct {
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	} `json:"physicalTimeInterval"`
	CivilDateTimeInterval *struct {
		StartDateTime *struct {
			Date civilDate `json:"date"`
		} `json:"startDateTime"`
		EndDateTime *struct {
			Date civilDate `json:"date"`
		} `json:"endDateTime"`
	} `json:"civilDateTimeInterval"`
	CivilISO8601TimeInterval *struct {
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	} `json:"civilIso8601TimeInterval"`
}

// HandleInbound authenticates a Google Health API delivery and returns the
// pulls it asks for. HEAD and GET give nothing. Webhooks off, a wrong
// Authorization header or a bad signature is an error; so is a body that is
// neither a verification request nor notifications. A message for a data
// type this connector does not pull, or without a user, is skipped.
func (c *Connector) HandleInbound(ctx context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	secret, mode := c.webhookKeys()
	if secret == "" {
		return nil, ErrWebhooksOff
	}
	got := r.Header.Get(HeaderAuthorization)
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
		return nil, ErrBadAuthorization
	}
	if r.Body == nil {
		return nil, errors.New("fitbit: webhook without a body")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundBody+1))
	if err != nil {
		return nil, fmt.Errorf("fitbit: webhook body: %w", err)
	}
	if len(body) > maxInboundBody {
		return nil, fmt.Errorf("fitbit: webhook body exceeds %d bytes", maxInboundBody)
	}
	if isVerification(body) {
		// The secret matched: the endpoint check succeeds. Nothing to pull.
		return nil, nil
	}
	if err := c.checkSignature(ctx, mode, r.Header.Get(HeaderSignature), body); err != nil {
		return nil, err
	}
	msgs, err := parseNotifications(body)
	if err != nil {
		return nil, err
	}
	return c.events(msgs), nil
}

// isVerification reports a {"type": "verification"} body.
func isVerification(body []byte) bool {
	var v struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(body, &v) == nil && strings.EqualFold(v.Type, "verification")
}

// parseNotifications decodes a delivery: one message or an array of them,
// each wrapped in "data" (or not).
func parseNotifications(body []byte) ([]notification, error) {
	body = bytes.TrimSpace(body)
	var items []json.RawMessage
	switch {
	case len(body) > 0 && body[0] == '[':
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("fitbit: webhook body: %w", err)
		}
	case len(body) > 0 && body[0] == '{':
		items = []json.RawMessage{body}
	default:
		return nil, errors.New("fitbit: webhook body is not JSON")
	}
	out := make([]notification, 0, len(items))
	for _, it := range items {
		var w struct {
			Data *notification `json:"data"`
		}
		if err := json.Unmarshal(it, &w); err != nil {
			slog.Info("fitbit: unreadable webhook message skipped", "provider", Slug)
			continue
		}
		if w.Data == nil {
			var n notification
			if json.Unmarshal(it, &n) != nil {
				continue
			}
			w.Data = &n
		}
		out = append(out, *w.Data)
	}
	return out, nil
}

// events turns messages into pulls, one per user and data type spanning all
// their intervals.
func (c *Connector) events(msgs []notification) []connector.InboundEvent {
	now := c.now().UTC()
	type key struct{ user, dt string }
	idx := map[key]int{}
	var out []connector.InboundEvent
	for _, n := range msgs {
		dt := normalizeDataType(n.DataType)
		if n.HealthUserID == "" || dt == "" {
			continue
		}
		if _, ok := lookupDataType(dt); !ok {
			slog.Info("fitbit: webhook for a data type not pulled, ignored", "provider", Slug, "data_type", clip(dt, 64))
			continue
		}
		if strings.EqualFold(n.Operation, "DELETE") {
			slog.Info("fitbit: delete notification queues a pull; deletions are not propagated", "provider", Slug, "data_type", dt)
		}
		from, to := notifiedWindow(n.Intervals, now)
		k := key{n.HealthUserID, dt}
		if i, ok := idx[k]; ok {
			if from.Before(out[i].From) {
				out[i].From = from
			}
			if to.After(out[i].To) {
				out[i].To = to
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, connector.InboundEvent{ProviderUserID: n.HealthUserID, DataType: dt, From: from, To: to})
	}
	return out
}

// notifiedWindow is the span of a message's intervals: physical times widened
// by intervalMargin; civil dates (daily data) from the day before the first
// to two days after the last, since they are the user's local days; civil
// times without offset widened by 14 hours. No usable interval gives the last
// InboundLookback.
func notifiedWindow(ivs []notifInterval, now time.Time) (time.Time, time.Time) {
	var from, to time.Time
	add := func(s, e time.Time) {
		if from.IsZero() || s.Before(from) {
			from = s
		}
		if to.IsZero() || e.After(to) {
			to = e
		}
	}
	for _, iv := range ivs {
		if p := iv.PhysicalTimeInterval; p != nil {
			s, err1 := parseTime(p.StartTime)
			e, err2 := parseTime(p.EndTime)
			if err1 == nil && err2 == nil && !e.Before(s) {
				add(s.Add(-intervalMargin), e.Add(intervalMargin))
				continue
			}
		}
		if c := iv.CivilDateTimeInterval; c != nil && c.StartDateTime != nil {
			s, err := c.StartDateTime.Date.time()
			if err == nil {
				e := s
				if c.EndDateTime != nil {
					if d, err := c.EndDateTime.Date.time(); err == nil && !d.Before(s) {
						e = d
					}
				}
				add(s.AddDate(0, 0, -1), e.AddDate(0, 0, 2))
				continue
			}
		}
		if c := iv.CivilISO8601TimeInterval; c != nil {
			s, err1 := time.Parse("2006-01-02T15:04:05", c.StartTime)
			e, err2 := time.Parse("2006-01-02T15:04:05", c.EndTime)
			if err1 == nil && err2 == nil && !e.Before(s) && plausible(s) {
				add(s.Add(-14*time.Hour), e.Add(14*time.Hour))
			}
		}
	}
	if from.IsZero() {
		return now.Add(-InboundLookback), now
	}
	return from, to
}
