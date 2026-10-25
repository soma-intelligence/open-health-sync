// SPDX-License-Identifier: AGPL-3.0-only

package garmin

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
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Notifications.
//
// Garmin calls one URL per summary type, set once per app in Garmin's
// developer portal (not per user); point them all at
// <OHS_PUBLIC_URL>/inbound/garmin. The body is one JSON object whose key
// names the summary type and holds a list of items:
//
//	ping: {"dailies":[{"userId":"..","callbackURL":"https://apis.garmin.com/wellness-api/rest/dailies?uploadStartTimeInSeconds=..&uploadEndTimeInSeconds=.."}]}
//	push: {"dailies":[{"userId":"..","summaryId":"..","calendarDate":"..","steps":..}]}
//
// A ping item becomes a pull of its upload window, for the summary type it
// names, with the user's own token. The callback URL itself is never
// fetched: only its two time parameters are read, so a forged ping can at
// most make the server pull a connected user's own data again.
//
// A push item carries the summary itself, which is stored as received (one
// raw line per item) for every connection of its userId. Garmin signs
// nothing (as far as public sources show), so a push body is trusted on its
// shape alone: whoever knows a connected Garmin user id could store invented
// summaries for that user. So notifications are refused (401) until
// OHS_GARMIN_INBOUND_TOKEN is set, and then must carry ?token=<value>, added
// to every notification URL in Garmin's portal. Pulls do not need it.
//
// "deregistrations" and "userPermissionsChange" are answered 200 and
// otherwise ignored: the next pull with a revoked grant fails and the
// connection is marked for reconnection, and a type no longer granted is
// skipped by Fetch.

// ErrBadToken reports a notification without the configured inbound token.
// The inbound route answers it with 401.
var ErrBadToken = errors.New("garmin: notification token missing or invalid")

// ErrNoInboundToken reports a notification while no inbound token is
// configured. It wraps ErrBadToken, so the route answers 401 as well.
var ErrNoInboundToken = fmt.Errorf("%w: OHS_GARMIN_INBOUND_TOKEN is not set, notifications are refused", ErrBadToken)

// TokenParam is the notification URL query parameter carrying
// OHS_GARMIN_INBOUND_TOKEN.
const TokenParam = "token"

// maxInboundBody caps the body read from a notification (the inbound route
// caps it at 1 MiB too).
const maxInboundBody = 1 << 20

// maxItems bounds the items of one notification.
const maxItems = 5000

// pingFallback is the window pulled for a ping whose callback URL names
// none.
const pingFallback = 24 * time.Hour

// HandleInbound validates a Garmin notification and returns the pulls (ping)
// and raw lines (push) it carries. HEAD and GET give no event and no error.
// A body that is not a Garmin notification, one that lacks the configured
// token, or any notification while no token is configured, is an error
// (401); summary types this connector does not store
// are ignored (200), so Garmin does not retry them.
func (c *Connector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	want := c.inboundToken()
	if want == "" {
		c.warnRefused()
		return nil, ErrNoInboundToken
	}
	if got := r.URL.Query().Get(TokenParam); subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return nil, ErrBadToken
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundBody+1))
	if err != nil {
		return nil, fmt.Errorf("garmin: notification body: %w", err)
	}
	if len(body) > maxInboundBody {
		return nil, fmt.Errorf("garmin: notification body exceeds %d bytes", maxInboundBody)
	}
	return parseNotification(body, c.now().UTC())
}

// refusalWarnEvery spaces the WARN lines of notifications refused for a
// missing inbound token: Garmin retries each one, so a line per request would
// flood the log, and one per interval is enough to be seen.
const refusalWarnEvery = 10 * time.Minute

// warnRefused logs a refusal for a missing inbound token, at most once per
// refusalWarnEvery.
func (c *Connector) warnRefused() {
	now := c.now().UnixNano()
	last := c.refusedWarnAt.Load()
	if last != 0 && now-last < int64(refusalWarnEvery) {
		return
	}
	if !c.refusedWarnAt.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("garmin: notification refused, OHS_GARMIN_INBOUND_TOKEN is not set", "provider", Slug,
		"every", refusalWarnEvery.String())
}

// pingKey groups ping items by user and data type.
type pingKey struct{ user, dataType string }

// parseNotification turns a notification body into inbound events.
func parseNotification(body []byte, now time.Time) ([]connector.InboundEvent, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return nil, errors.New("garmin: notification is not a JSON object")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("garmin: notification: %w", err)
	}
	if len(top) == 0 {
		return nil, errors.New("garmin: notification is empty")
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pings := map[pingKey][2]time.Time{}
	var pingOrder []pingKey
	pushes := map[pingKey][][]byte{}
	var pushOrder []pingKey
	total := 0
	for _, key := range keys {
		var items []json.RawMessage
		if err := json.Unmarshal(top[key], &items); err != nil {
			return nil, fmt.Errorf("garmin: notification %q is not a list", key)
		}
		total += len(items)
		if total > maxItems {
			return nil, fmt.Errorf("garmin: notification holds more than %d items", maxItems)
		}
		if key == keyDeregistrations || key == keyUserPermissionsChange {
			for _, it := range items {
				if _, err := itemUser(it); err != nil {
					return nil, fmt.Errorf("garmin: %s: %w", key, err)
				}
			}
			slog.Info("garmin: notification acknowledged, nothing to do", "provider", Slug, "kind", key, "items", len(items))
			continue
		}
		dt, known := notificationKeys[key]
		if !known {
			slog.Debug("garmin: notification of an unhandled summary type ignored", "provider", Slug, "kind", key)
			continue
		}
		for _, it := range items {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(it, &obj); err != nil || obj == nil {
				return nil, fmt.Errorf("garmin: %s: item is not an object", key)
			}
			user, err := itemUser(it)
			if err != nil {
				return nil, fmt.Errorf("garmin: %s: %w", key, err)
			}
			k := pingKey{user: user, dataType: dt}
			if raw, ok := obj["callbackURL"]; ok {
				var cb string
				if err := json.Unmarshal(raw, &cb); err != nil || cb == "" {
					return nil, fmt.Errorf("garmin: %s: callbackURL is not a string", key)
				}
				from, to, err := pingWindow(cb, now)
				if err != nil {
					return nil, fmt.Errorf("garmin: %s: %w", key, err)
				}
				w, seen := pings[k]
				if !seen {
					pingOrder = append(pingOrder, k)
					w = [2]time.Time{from, to}
				} else {
					if from.Before(w[0]) {
						w[0] = from
					}
					if to.After(w[1]) {
						w[1] = to
					}
				}
				pings[k] = w
				continue
			}
			var sid string
			if err := json.Unmarshal(obj["summaryId"], &sid); err != nil || sid == "" {
				return nil, fmt.Errorf("garmin: %s: pushed summary has no summaryId", key)
			}
			line, _, err := cleanLine(it)
			if err != nil {
				return nil, fmt.Errorf("garmin: %s: %w", key, err)
			}
			if _, seen := pushes[k]; !seen {
				pushOrder = append(pushOrder, k)
			}
			pushes[k] = append(pushes[k], line)
		}
	}
	events := make([]connector.InboundEvent, 0, len(pingOrder)+len(pushOrder))
	for _, k := range pingOrder {
		w := pings[k]
		events = append(events, connector.InboundEvent{ProviderUserID: k.user, DataType: k.dataType, From: w[0], To: w[1]})
	}
	for _, k := range pushOrder {
		events = append(events, connector.InboundEvent{ProviderUserID: k.user, DataType: k.dataType, Lines: pushes[k]})
	}
	return events, nil
}

// itemUser reads the non-empty "userId" string of a notification item.
func itemUser(it json.RawMessage) (string, error) {
	var head struct {
		UserID *string `json:"userId"`
	}
	if err := json.Unmarshal(it, &head); err != nil {
		return "", errors.New("item is not an object with a userId string")
	}
	if head.UserID == nil || *head.UserID == "" || len(*head.UserID) > 128 {
		return "", errors.New("item has no valid userId")
	}
	return *head.UserID, nil
}

// pingWindow reads the upload window of a ping callback URL. A URL without
// one gives the last pingFallback. The window never ends after now; the
// core caps its length.
func pingWindow(callbackURL string, now time.Time) (time.Time, time.Time, error) {
	u, err := url.Parse(callbackURL)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("callbackURL does not parse")
	}
	q := u.Query()
	start, okS := unixParam(q.Get("uploadStartTimeInSeconds"))
	end, okE := unixParam(q.Get("uploadEndTimeInSeconds"))
	if !okS || !okE || !end.After(start) {
		return now.Add(-pingFallback), now, nil
	}
	if end.After(now) {
		end = now
	}
	if !end.After(start) {
		start = end.Add(-time.Second)
	}
	return start, end, nil
}

// unixParam reads a Unix seconds query parameter.
func unixParam(s string) (time.Time, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	t, err := unixTime("upload time", &n)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
