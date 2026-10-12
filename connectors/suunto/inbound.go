// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Notifications.
//
// Suunto notifies one URL per application, set by the operator in API Zone,
// for every user who authorized the app: <OHS_PUBLIC_URL>/inbound/suunto. A
// notification is a POST naming the user by Suunto username, the "user" claim
// of the access token this connector stored as the provider user id. Two
// body shapes are accepted:
//
//	{"type":"WORKOUT_CREATED","username":"jane","workout":{"workoutKey":"...","startTime":1772539200000}}
//	username=jane&workoutid=...   (or the same two fields as JSON; the API Zone FAQ shape)
//
// The body is signed: X-HMAC-SHA256-Signature holds the HMAC-SHA256 of the
// raw body keyed by the notification secret. Set it as
// OHS_SUUNTO_NOTIFICATION_SECRET; without it every notification is refused
// (ErrNotificationsOff), since nothing would tell a forged one apart. The
// digest's encoding is not published: hex (either case) and base64 are both
// accepted.
//
// A notification only says what to pull; the data is read back with the
// user's own token:
//
//	WORKOUT_CREATED (or no type, with a workout id)
//	                         -> a workout pull from InboundOverlap before the
//	                            workout's start (the last 24 hours when the
//	                            start is unknown) to now
//	SUUNTO_247_SLEEP_CREATED, _RECOVERY_CREATED, _ACTIVITY_CREATED
//	                         -> a pull of that 24/7 type over the last 48 hours
//	anything else (ROUTE_CREATED, ...) -> nothing, answered 200

// HeaderSignature carries the notification signature.
const HeaderSignature = "X-HMAC-SHA256-Signature"

// Notification types.
const (
	EventWorkoutCreated  = "WORKOUT_CREATED"
	EventSleepCreated    = "SUUNTO_247_SLEEP_CREATED"
	EventRecoveryCreated = "SUUNTO_247_RECOVERY_CREATED"
	EventActivityCreated = "SUUNTO_247_ACTIVITY_CREATED"
)

// ErrBadSignature reports a notification whose signature is missing or
// wrong. The inbound route answers it with 401.
var ErrBadSignature = errors.New("suunto: notification signature missing or invalid")

// ErrNotificationsOff reports a notification while
// OHS_SUUNTO_NOTIFICATION_SECRET is unset.
var ErrNotificationsOff = errors.New("suunto: notifications are off (OHS_SUUNTO_NOTIFICATION_SECRET is not set)")

// InboundLookback bounds how far before now a workout notification pulls.
const InboundLookback = 7 * 24 * time.Hour

// InboundOverlap re-covers a little before the workout start.
const InboundOverlap = 2 * time.Hour

// defaultWindow is the pull of a workout notification without a start.
const defaultWindow = 24 * time.Hour

// window247 is the pull of a 24/7 notification.
const window247 = 48 * time.Hour

// maxInboundBody caps the body read from a notification (24/7 notifications
// carry their samples inline).
const maxInboundBody = 1 << 20

// Signature returns the hex HMAC-SHA256 of body keyed by secret.
func Signature(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// validSignature compares the header with the expected HMAC in constant
// time, reading it as hex (any case) or as base64 (standard or URL alphabet,
// padded or not). An optional "sha256=" prefix is ignored.
func validSignature(secret string, body []byte, header string) bool {
	h := strings.TrimSpace(header)
	if len(h) > 7 && strings.EqualFold(h[:7], "sha256=") {
		h = h[7:]
	}
	if h == "" {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	want := m.Sum(nil)
	for _, dec := range []func(string) ([]byte, error){
		hex.DecodeString,
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	} {
		if got, err := dec(h); err == nil && len(got) == len(want) && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

// HandleInbound verifies a notification and returns the pull it asks for
// (see the table above). HEAD and GET give no event and no error (a
// reachability probe). A wrong signature, a body that is not a notification,
// or a notification without username is an error (401); a type this
// connector does not act on gives no event and no error, so Suunto does not
// retry.
func (c *Connector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	secret := c.notificationSecret()
	if secret == "" {
		return nil, ErrNotificationsOff
	}
	body, err := readBody(r.Body, maxInboundBody)
	if err != nil {
		return nil, fmt.Errorf("suunto: notification body: %w", err)
	}
	if !validSignature(secret, body, r.Header.Get(HeaderSignature)) {
		return nil, ErrBadSignature
	}
	n, err := parseNotification(body)
	if err != nil {
		return nil, err
	}
	user := strings.TrimSpace(n.Username)
	if user == "" {
		return nil, errors.New("suunto: notification names no username")
	}
	now := c.now().UTC()
	typ := strings.ToUpper(strings.TrimSpace(n.Type))
	if typ == "" && n.hasWorkout() {
		typ = EventWorkoutCreated
	}
	switch typ {
	case EventWorkoutCreated:
		from := workoutWindowStart(n, now)
		return []connector.InboundEvent{{ProviderUserID: user, DataType: DataTypeWorkout, From: from, To: now}}, nil
	case EventSleepCreated:
		return []connector.InboundEvent{{ProviderUserID: user, DataType: DataTypeSleep, From: now.Add(-window247), To: now}}, nil
	case EventRecoveryCreated:
		return []connector.InboundEvent{{ProviderUserID: user, DataType: DataTypeRecovery, From: now.Add(-window247), To: now}}, nil
	case EventActivityCreated:
		return []connector.InboundEvent{{ProviderUserID: user, DataType: DataTypeDailyActivity, From: now.Add(-window247), To: now}}, nil
	}
	slog.Info("suunto: notification not acted on, ignored", "provider", Slug, "type", clip(typ, 64))
	return nil, nil
}

// parseNotification reads a JSON object, or a form encoded body
// (username=...&workoutid=...).
func parseNotification(body []byte) (notification, error) {
	var n notification
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if err := json.Unmarshal(trimmed, &n); err != nil {
			return notification{}, fmt.Errorf("suunto: notification body: %w", err)
		}
		return n, nil
	}
	form, err := url.ParseQuery(string(trimmed))
	if err != nil || len(form) == 0 {
		return notification{}, errors.New("suunto: notification body is neither JSON nor a form")
	}
	n.Type = form.Get("type")
	n.Username = form.Get("username")
	n.WorkoutKey = form.Get("workoutKey")
	if id := form.Get("workoutid"); id != "" {
		n.WorkoutID, _ = json.Marshal(id)
	}
	return n, nil
}

// hasWorkout reports a notification naming a workout.
func (n notification) hasWorkout() bool {
	if n.WorkoutKey != "" || present(n.WorkoutID) {
		return true
	}
	return n.Workout != nil && (n.Workout.WorkoutKey != "" || present(n.Workout.WorkoutID))
}

func present(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && !bytes.Equal(t, []byte("null")) && !bytes.Equal(t, []byte(`""`))
}

// workoutWindowStart is InboundOverlap before the workout's start, never
// earlier than InboundLookback ago nor later than defaultWindow ago (a pull
// that also covers a late modification); the last defaultWindow when the
// start is unknown.
func workoutWindowStart(n notification, now time.Time) time.Time {
	floor := now.Add(-InboundLookback)
	ceiling := now.Add(-defaultWindow)
	if n.Workout == nil || n.Workout.StartTime == nil {
		return ceiling
	}
	ms, err := n.Workout.StartTime.Int64()
	if err != nil || ms <= 0 {
		return ceiling
	}
	start := time.UnixMilli(ms).UTC().Add(-InboundOverlap)
	switch {
	case start.Before(floor):
		return floor
	case start.After(ceiling):
		return ceiling
	}
	return start
}
