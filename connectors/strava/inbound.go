// SPDX-License-Identifier: AGPL-3.0-only

package strava

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
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Push subscription events.
//
// Strava keeps one subscription per application. Creating it (POST
// /push_subscriptions with a verify_token) makes Strava send a GET to the
// callback with hub.mode=subscribe, hub.verify_token and hub.challenge; the
// callback must answer 200 within two seconds with {"hub.challenge": "..."}.
// AnswerChallenge does that when the verify token matches
// OHS_STRAVA_VERIFY_TOKEN.
//
// Events are JSON POSTs: {object_type, object_id, aspect_type, updates,
// owner_id, subscription_id, event_time}. Strava signs nothing. When
// OHS_STRAVA_SUBSCRIPTION_ID is set an event naming another subscription is
// refused; otherwise any caller can post one. That is tolerable because an
// event is only a change signal: it queues a pull of a bounded recent window
// for an athlete who already has an active connection (collect checks), and
// the pull reads Strava with that athlete's own token.
//
//	activity create, update  -> pull the recent window: an update lands as a
//	                            new copy of the same activity, which replaces
//	                            the stored workout (Map marks it Replaceable).
//	activity delete          -> nothing: the core cannot delete one record.
//	athlete updates.authorized = "false" (deauthorization)
//	                         -> a short pull: Strava has revoked the tokens,
//	                            so the pull is refused (401, then the refresh
//	                            too) and the connection becomes needs_reauth.
//	anything else            -> nothing.

// ErrBadVerifyToken reports a subscription challenge whose verify token is
// missing or wrong, or a challenge while no verify token is configured. The
// probe route answers it with 401.
var ErrBadVerifyToken = errors.New("strava: subscription challenge verify token missing or invalid")

// ErrWebhookNotConfigured reports an event while OHS_STRAVA_VERIFY_TOKEN is
// unset: no subscription can have been created through this instance.
var ErrWebhookNotConfigured = errors.New("strava: push events are off (OHS_STRAVA_VERIFY_TOKEN is not set)")

// ErrWrongSubscription reports an event for another subscription than
// OHS_STRAVA_SUBSCRIPTION_ID.
var ErrWrongSubscription = errors.New("strava: event for another push subscription")

// InboundLookback is how far before the event an activity event pulls.
// Strava filters activities on their start, and a device may upload an
// activity days after it was recorded; an older activity is still collected
// by the 6 hour pull only if it started inside that pull's window.
const InboundLookback = 7 * 24 * time.Hour

// deauthWindow is the pull a deauthorization queues: any request will do, as
// it only has to be refused.
const deauthWindow = time.Hour

// maxEventBytes caps one event body.
const maxEventBytes = 64 << 10

// AnswerChallenge answers the GET Strava sends when the push subscription is
// created. A GET without hub.challenge (a plain reachability probe) gets 200
// with no body.
func (c *Connector) AnswerChallenge(r *http.Request) (string, []byte, error) {
	q := r.URL.Query()
	challenge := q.Get("hub.challenge")
	if challenge == "" {
		return "", nil, nil
	}
	want, _ := c.webhookKeys()
	got := q.Get("hub.verify_token")
	if want == "" || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return "", nil, ErrBadVerifyToken
	}
	if mode := q.Get("hub.mode"); mode != "" && mode != "subscribe" {
		return "", nil, fmt.Errorf("strava: hub.mode %q is not subscribe", mode)
	}
	body, err := json.Marshal(map[string]string{"hub.challenge": challenge})
	if err != nil {
		return "", nil, err
	}
	return "application/json", body, nil
}

// HandleInbound reads one Strava event and returns the pull it asks for (see
// the table above). HEAD and GET give no event and no error (AnswerChallenge
// serves them). A body that is not an event is an error; an event for an
// object or aspect this connector does not act on gives no event and no
// error, so the route answers 200 and Strava does not retry.
func (c *Connector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	verify, subscription := c.webhookKeys()
	if verify == "" {
		return nil, ErrWebhookNotConfigured
	}
	body, err := readBody(r.Body, maxEventBytes)
	if err != nil {
		return nil, fmt.Errorf("strava: event body: %w", err)
	}
	var ev event
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&ev); err != nil {
		return nil, fmt.Errorf("strava: event body: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("strava: event body: trailing data")
	}
	if subscription != "" && ev.SubscriptionID.String() != subscription {
		return nil, ErrWrongSubscription
	}
	if ev.ObjectType == "" || ev.AspectType == "" {
		return nil, errors.New("strava: event: missing object_type or aspect_type")
	}
	if _, err := ev.OwnerID.Int64(); err != nil {
		return nil, errors.New("strava: event: owner_id is not a number")
	}
	owner := ev.OwnerID.String()
	now := c.now().UTC()

	switch ev.ObjectType {
	case "activity":
		switch ev.AspectType {
		case "create", "update":
			return []connector.InboundEvent{{ProviderUserID: owner, DataType: DataTypeActivity, From: now.Add(-InboundLookback), To: now}}, nil
		case "delete":
			slog.Info("strava: activity deleted at Strava, not removed here", "provider", Slug)
			return nil, nil
		}
	case "athlete":
		if ev.AspectType == "update" && deauthorized(ev.Updates) {
			return []connector.InboundEvent{{ProviderUserID: owner, DataType: DataTypeActivity, From: now.Add(-deauthWindow), To: now}}, nil
		}
		return nil, nil
	}
	slog.Info("strava: event not acted on, ignored", "provider", Slug, "object_type", ev.ObjectType, "aspect_type", ev.AspectType)
	return nil, nil
}

// deauthorized reports updates {"authorized": "false"}. Strava sends the
// value as a string; a boolean false is accepted too.
func deauthorized(updates json.RawMessage) bool {
	var u map[string]any
	if len(updates) == 0 || json.Unmarshal(updates, &u) != nil {
		return false
	}
	switch v := u["authorized"].(type) {
	case string:
		return v == "false"
	case bool:
		return !v
	}
	return false
}
