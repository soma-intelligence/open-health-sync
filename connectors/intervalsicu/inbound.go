// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

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
// An OAuth app configures one webhook URL on its "Manage App" page, for every
// athlete who authorized it. intervals.icu POSTs JSON:
//
//	{"secret": "...", "events": [{"athlete_id": "...", "type": "ACTIVITY_UPLOADED",
//	  "timestamp": "...", "activity": {...}}, ...]}
//
// The secret is the app's webhook secret; it is compared in constant time
// with OHS_INTERVALS_ICU_WEBHOOK_SECRET, and a body without the right one is
// refused (401). Without the key every webhook is refused. A non-2xx answer
// makes intervals.icu retry with exponential backoff.
//
// An event is only a change signal: it queues a pull, read with the
// athlete's own token, for an athlete who has an active connection here.
//
//	ACTIVITY_*  -> a pull of activities around the activity's start (one day
//	               on each side), or of the last 7 days when the event names
//	               no readable start.
//	WELLNESS_*  -> a pull of wellness around the record's day, or of the last
//	               3 days.
//	anything else (CALENDAR_UPDATED, SPORT_SETTINGS_UPDATED, ...) -> nothing.
//
// Activities synced to intervals.icu from Strava raise no activity webhook.

// ErrWebhookNotConfigured reports a webhook while
// OHS_INTERVALS_ICU_WEBHOOK_SECRET is unset.
var ErrWebhookNotConfigured = errors.New("intervals_icu: webhooks are off (OHS_INTERVALS_ICU_WEBHOOK_SECRET is not set)")

// ErrBadSecret reports a webhook whose secret is missing or wrong.
var ErrBadSecret = errors.New("intervals_icu: webhook secret missing or invalid")

// Pull windows of the events.
const (
	activityMargin   = 24 * time.Hour
	activityLookback = 7 * 24 * time.Hour
	wellnessLookback = 3 * 24 * time.Hour
)

// maxEventBytes caps one webhook body. An activity event carries the whole
// activity, so the cap is generous.
const maxEventBytes = 4 << 20

// HandleInbound reads one webhook POST and returns the pulls it asks for (see
// the table above). HEAD and GET (a reachability probe) give no event and no
// error. Events of one athlete and data type are merged into one pull.
func (c *Connector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	want := c.webhookSecret()
	if want == "" {
		return nil, ErrWebhookNotConfigured
	}
	body, err := readBody(r.Body, maxEventBytes)
	if err != nil {
		return nil, fmt.Errorf("intervals_icu: webhook body: %w", err)
	}
	var wb webhookBody
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&wb); err != nil {
		return nil, fmt.Errorf("intervals_icu: webhook body: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("intervals_icu: webhook body: trailing data")
	}
	if wb.Secret == "" || subtle.ConstantTimeCompare([]byte(wb.Secret), []byte(want)) != 1 {
		return nil, ErrBadSecret
	}

	now := c.now().UTC()
	type key struct{ athlete, dataType string }
	merged := map[key]*connector.InboundEvent{}
	var order []key
	for _, ev := range wb.Events {
		athlete := strings.TrimSpace(string(ev.AthleteID))
		if athlete == "" {
			return nil, errors.New("intervals_icu: webhook event without athlete_id")
		}
		dataType, from, to, ok := eventWindow(ev, now)
		if !ok {
			slog.Info("intervals_icu: webhook event not acted on, ignored", "provider", Slug, "type", ev.Type)
			continue
		}
		k := key{athlete, dataType}
		if m, seen := merged[k]; seen {
			if from.Before(m.From) {
				m.From = from
			}
			if to.After(m.To) {
				m.To = to
			}
			continue
		}
		merged[k] = &connector.InboundEvent{ProviderUserID: athlete, DataType: dataType, From: from, To: to}
		order = append(order, k)
	}
	out := make([]connector.InboundEvent, 0, len(order))
	for _, k := range order {
		out = append(out, *merged[k])
	}
	return out, nil
}

// eventWindow returns the data type and pull window of one event, or false
// for an event this connector does not act on.
func eventWindow(ev webhookEvent, now time.Time) (string, time.Time, time.Time, bool) {
	t := strings.ToUpper(strings.TrimSpace(ev.Type))
	switch {
	case strings.HasPrefix(t, "ACTIVITY_"):
		if ev.Activity != nil {
			if start, err := time.Parse(time.RFC3339, ev.Activity.StartDate); err == nil {
				return DataTypeActivity, start.Add(-activityMargin), start.Add(activityMargin), true
			}
			if local, err := parseLocal(ev.Activity.StartDateLocal); err == nil {
				// Local wall clock: widen by the largest zone offset.
				return DataTypeActivity, local.Add(-activityMargin - 14*time.Hour), local.Add(activityMargin + 14*time.Hour), true
			}
		}
		return DataTypeActivity, now.Add(-activityLookback), now, true
	case strings.HasPrefix(t, "WELLNESS_"):
		if ev.Wellness != nil {
			if d, err := time.Parse(dateLayout, ev.Wellness.ID); err == nil {
				return DataTypeWellness, d.AddDate(0, 0, -1), d.AddDate(0, 0, 2), true
			}
		}
		return DataTypeWellness, utcDay(now.Add(-wellnessLookback)), utcDay(now).AddDate(0, 0, 1), true
	}
	return "", time.Time{}, time.Time{}, false
}
