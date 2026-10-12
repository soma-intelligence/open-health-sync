// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Push notifications.
//
// Withings signs nothing: a notification is a form POST
// (userid=..&appli=..&startdate=..&enddate=..) to the callback URL given at
// subscription time, and every field of it is caller controlled. The only
// secret is the callback URL itself, so that is where the proof lives:
// Subscribe registers <callback>?sig=<hex HMAC-SHA256 of the Withings user id,
// keyed by the client secret>, and HandleInbound recomputes it from the body's
// userid. A caller without the client secret cannot forge a notification for
// any user, and a leaked callback URL only speaks for the one user it was
// minted for. The caller (collect) must still check that the returned
// ProviderUserID belongs to an active connection. Rotating the client secret
// invalidates every registered URL: every user must be subscribed again.
//
// The body's window is not trusted either: a notification is a change signal.
// The event window always ends now and starts at most InboundLookback ago
// (startdate minus InboundOverlap when the body gives a later one), so a crafted
// startdate=0 cannot ask for a full history pull.

// ErrBadSignature reports a notification whose callback signature is missing
// or wrong. The inbound route answers it with 401.
var ErrBadSignature = errors.New("withings: notification signature missing or invalid")

// InboundLookback bounds how far back a notification may pull.
const InboundLookback = 7 * 24 * time.Hour

// InboundOverlap re-covers a little before the notified start, so a reading
// that landed on the boundary is not missed.
const InboundOverlap = 2 * time.Hour

// sigParam is the callback URL query parameter carrying the signature.
const sigParam = "sig"

// Withings notification categories (appli).
// https://developer.withings.com/api-reference/#tag/notify/operation/notify-subscribe
const (
	AppliMeasurement = 1  // new measurement (weight, BP, HR, ...) -> measure
	AppliActivity    = 16 // new activity (steps, calories) -> activity
	AppliSleep       = 44 // new sleep session -> sleep
	AppliWorkout     = 46 // new workout -> workout
)

// subscribedApplis are the categories Subscribe registers.
var subscribedApplis = []int{AppliMeasurement, AppliActivity, AppliSleep, AppliWorkout}

// AppliToDataType maps a notification category to the data type to pull, or
// "" for a category this connector does not subscribe to.
func AppliToDataType(appli int) string {
	switch appli {
	case AppliMeasurement:
		return DataTypeMeasure
	case AppliActivity:
		return DataTypeActivity
	case AppliSleep:
		return DataTypeSleep
	case AppliWorkout:
		return DataTypeWorkout
	default:
		return ""
	}
}

// notifiedTypes are the data types a notification of one type can concern.
// ECG recordings and intraday samples have no notification category of their
// own: they come with a measurement and with activity.
var notifiedTypes = map[string][]string{
	DataTypeMeasure:  {DataTypeMeasure, DataTypeECG},
	DataTypeActivity: {DataTypeActivity, DataTypeIntraday},
	DataTypeSleep:    {DataTypeSleep},
	DataTypeWorkout:  {DataTypeWorkout},
}

// FetchNotified pulls what a notification of dataType concerns (see
// notifiedTypes): one or two data types instead of all six. An unknown
// dataType pulls everything.
func (c *Connector) FetchNotified(ctx context.Context, conn connector.Connection, from, to time.Time, dataType string, emit connector.RawSink) error {
	types, ok := notifiedTypes[dataType]
	if !ok {
		types = AllDataTypes
	}
	return c.FetchTypes(ctx, conn, from, to, types, emit)
}

// signature is the hex HMAC-SHA256 of the Withings user id, keyed by the
// client secret.
func signature(secret, providerUserID string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("withings-inbound:" + providerUserID))
	return hex.EncodeToString(m.Sum(nil))
}

// SignedCallbackURL returns callbackURL (normally <OHS_PUBLIC_URL>/inbound/withings)
// with the signature of providerUserID added. It is the exact URL Subscribe
// and Unsubscribe register, since Withings matches subscriptions on it.
func (c *Connector) SignedCallbackURL(callbackURL, providerUserID string) (string, error) {
	_, secret := c.credentials()
	if secret == "" {
		return "", errNotConfigured
	}
	if providerUserID == "" {
		return "", errors.New("withings: no Withings user id to sign")
	}
	u, err := url.Parse(callbackURL)
	if err != nil {
		return "", fmt.Errorf("withings: callback URL: %w", err)
	}
	q := u.Query()
	q.Set(sigParam, signature(secret, providerUserID))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// HandleInbound verifies a Withings notification and returns the pull it asks
// for. HEAD and GET (the reachability check Withings makes when a
// subscription is created) give no event and no error. A bad or missing
// signature gives ErrBadSignature. A category this connector does not pull
// gives no event and no error, so the route still answers 200 and Withings
// does not retry.
func (c *Connector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	_, secret := c.credentials()
	if secret == "" {
		return nil, errNotConfigured
	}
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("withings: notification body: %w", err)
	}
	userID := r.PostForm.Get("userid")
	if userID == "" {
		return nil, fmt.Errorf("%w: no userid", ErrBadSignature)
	}
	got := r.URL.Query().Get(sigParam)
	want := signature(secret, userID)
	if got == "" || !hmac.Equal([]byte(got), []byte(want)) {
		return nil, ErrBadSignature
	}

	appliStr := r.PostForm.Get("appli")
	appli, err := strconv.Atoi(appliStr)
	if err != nil {
		return nil, fmt.Errorf("withings: notification appli %q is not a number", appliStr)
	}
	dt := AppliToDataType(appli)
	if dt == "" {
		slog.Info("withings: notification for a category not pulled, ignored", "provider", Slug, "appli", appli)
		return nil, nil
	}

	now := c.now().UTC()
	from, to := inboundWindow(r.PostForm.Get("startdate"), now)
	return []connector.InboundEvent{{ProviderUserID: userID, DataType: dt, From: from, To: to}}, nil
}

// inboundWindow derives the pull window of a notification: it ends now and
// starts at the notified start minus InboundOverlap, never earlier than
// InboundLookback ago and never later than InboundOverlap ago.
func inboundWindow(startdate string, now time.Time) (time.Time, time.Time) {
	floor := now.Add(-InboundLookback)
	epoch, err := strconv.ParseInt(startdate, 10, 64)
	if err != nil {
		return floor, now
	}
	start := time.Unix(epoch, 0).UTC().Add(-InboundOverlap)
	if start.Before(floor) {
		return floor, now
	}
	if start.After(now) {
		// Clock skew or a start in the future: fall back to the overlap.
		return now.Add(-InboundOverlap), now
	}
	return start, now
}
