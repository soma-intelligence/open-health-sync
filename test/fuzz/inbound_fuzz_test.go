// SPDX-License-Identifier: AGPL-3.0-only

package fuzz

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// pushers are the connectors behind POST /inbound/{provider}, in a fixed
// order: the fuzz target picks one by index.
var pushers = []string{"fitbit", "garmin", "intervals_icu", "oura", "polar", "strava", "suunto", "whoop", "withings"}

// inboundHeaders are every provider's notification headers; each request
// carries them all, set to the fuzzed value.
var inboundHeaders = []string{
	"Authorization", "GOOGLE-HEALTH-API-SIGNATURE", "x-oura-signature", "x-oura-timestamp",
	"Polar-Webhook-Event", "Polar-Webhook-Signature", "Ocp-Apim-Subscription-Key",
	"X-HMAC-SHA256-Signature", "X-WHOOP-Signature", "X-WHOOP-Signature-Timestamp",
}

// pushConnector returns the registered connector, or a fresh instance of it
// configured with every key it reads set to a dummy value (so signature
// checks are on).
func pushConnector(t *testing.T, slug string, configured bool) connector.PushConnector {
	t.Helper()
	c := mustConnector(t, slug)
	if configured {
		if in, ok := c.(connector.Instancer); ok {
			if cf, ok := in.NewInstance().(connector.Configurable); ok {
				keys := config.ProviderKeys{}
				if k, ok := c.(connector.KeyedConnector); ok {
					for _, f := range k.RequiredKeys() {
						keys[f] = "fuzz-" + strings.ToLower(f)
					}
				}
				if k, ok := c.(connector.OptionalKeyed); ok {
					for _, f := range k.OptionalKeys() {
						keys[f] = "fuzz-" + strings.ToLower(f)
					}
				}
				if cf.Configure(keys) == nil {
					c = cf.(connector.Connector)
				}
			}
		}
	}
	p, ok := c.(connector.PushConnector)
	if !ok {
		t.Fatalf("%s is not a push connector", slug)
	}
	return p
}

// FuzzInbound posts an arbitrary notification to every push connector, with
// and without its keys configured. No input may panic it or make it read
// for long; what it accepts must name a user, keep a window whose end is not
// before its start, and carry lines that map without a panic.
func FuzzInbound(f *testing.F) {
	ts := time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)
	unix := "1772539200"
	seeds := map[int][]string{
		0: {`{"data":{"healthUserId":"u1","dataType":"steps","operation":"UPSERT","intervals":[{"startTime":"2026-03-03T10:00:00Z","endTime":"2026-03-03T11:00:00Z"}]}}`},
		1: {`{"dailies":[{"userId":"u1","summaryId":"s","calendarDate":"2026-03-03","startTimeInSeconds":` + unix + `,"durationInSeconds":86400}]}`, `{"deregistrations":[{"userId":"u1"}]}`},
		2: {`{"secret":"x","events":[{"athlete_id":"i1","type":"ACTIVITY_UPLOADED","timestamp":"2026-03-03T12:00:00Z","activity":{"id":"a1"}}]}`},
		3: {`{"event_type":"update","data_type":"sleep","object_id":"o","event_time":"2026-03-03T11:59:00+00:00","user_id":"u"}`},
		4: {`{"event":"EXERCISE","user_id":475,"entity_id":"aQlC83","timestamp":"2026-03-03T11:22:24Z","url":"https://www.polaraccesslink.com/v3/exercises/aQlC83"}`, `{"event":"PING"}`},
		5: {`{"aspect_type":"create","event_time":` + unix + `,"object_id":1,"object_type":"activity","owner_id":2,"subscription_id":3,"updates":{}}`},
		6: {`{"type":"WORKOUT_CREATED","username":"u1","workout":{"workoutKey":"k","startTime":1772539200000}}`},
		7: {`{"user_id":10129,"id":"ecdfc41c-1b2a-4c3e-9b6e-1d2e3f405060","type":"sleep.updated","trace_id":"t"}`},
		8: {`userid=12&startdate=` + unix + `&enddate=1772542800&appli=44`, `userid=12&appli=1&date=2026-03-03`},
	}
	for i := range pushers {
		for _, b := range seeds[i] {
			f.Add(uint8(i), false, "", unix, b)
			f.Add(uint8(i), true, "sha256=00", ts.Format(time.RFC3339), b)
		}
	}
	f.Fuzz(func(t *testing.T, pi uint8, configured bool, hdr, stamp, body string) {
		slug := pushers[int(pi)%len(pushers)]
		c := pushConnector(t, slug, configured)
		r := httptest.NewRequest(http.MethodPost, "/inbound/"+slug, strings.NewReader(body))
		if strings.HasPrefix(body, "userid=") {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r.Header.Set("Content-Type", "application/json")
		}
		for _, h := range inboundHeaders {
			v := hdr
			if strings.Contains(strings.ToLower(h), "timestamp") {
				v = stamp
			}
			if v != "" && !strings.ContainsAny(v, "\r\n\x00") {
				r.Header.Set(h, v)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		start := time.Now()
		events, err := c.HandleInbound(ctx, r)
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("%s: HandleInbound took %s", slug, d)
		}
		if err != nil {
			return
		}
		for _, ev := range events {
			if ev.ProviderUserID == "" {
				t.Fatalf("%s: event without a user: %+v", slug, ev)
			}
			if !ev.From.IsZero() && !ev.To.IsZero() && ev.To.Before(ev.From) {
				t.Fatalf("%s: window ends before it starts: %+v", slug, ev)
			}
			var lines []synth.Line
			for _, l := range ev.Lines {
				if !json.Valid(bytes.TrimSpace(l)) {
					t.Fatalf("%s: stored line is not JSON: %q", slug, l)
				}
				lines = append(lines, synth.Line{DataType: ev.DataType, JSON: l})
			}
			for _, res := range synthtest.Run(c, lines, ts.Add(48*time.Hour)) {
				if res.Outcome == synthtest.Panic {
					t.Fatalf("%s: stored notification panics the mapper: %s", slug, res.Detail)
				}
			}
		}
	})
}
