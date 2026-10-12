// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// HeaderSubscriptionKey carries the API Zone subscription key on every data
// API request.
const HeaderSubscriptionKey = "Ocp-Apim-Subscription-Key"

// maxResponseBytes caps one Suunto answer. Its size is provider controlled,
// so a runaway page must fail loudly rather than be buffered whole.
const maxResponseBytes = 32 << 20

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 5

// errUnauthorized is an HTTP 401 from a data endpoint that is about the
// user's access token: expired, revoked, or the user deauthorized the app.
var errUnauthorized = errors.New("suunto: access token rejected")

// ErrSubscriptionKey reports an answer refusing the subscription key
// (missing, invalid or for an inactive subscription). It is an operator
// error: the user's grant is fine, so the connection is not marked
// needs_reauth.
var ErrSubscriptionKey = errors.New("suunto: API Zone subscription key refused (check OHS_SUUNTO_SUBSCRIPTION_KEY)")

// get sends one GET to the API and returns the body. Every request carries
// the user's access token as a bearer token and the subscription key in
// Ocp-Apim-Subscription-Key.
//
// HTTP 401 gives errUnauthorized, or ErrSubscriptionKey when the gateway
// names the subscription key. HTTP 429, and the gateway's 403 "Out of call
// volume quota", give a *connector.RateLimitError at once, so the pull worker
// defers the job instead of a worker sleeping. A transport error is retried
// on a ladder that starts at retryBase and doubles, jittered upwards, five
// attempts in all.
func (c *Connector) get(ctx context.Context, token, path string, query url.Values) ([]byte, error) {
	key := c.subscriptionKey()
	if key == "" {
		return nil, errNotConfigured
	}
	endpoint := c.apiBase + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	backoff := c.retryBase
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(HeaderSubscriptionKey, key)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == maxAttempts {
				return nil, fmt.Errorf("suunto http: %w", err)
			}
			slog.Warn("suunto: transport error, retrying", "provider", Slug, "attempt", attempt)
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("suunto: read response: %w", err)
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized && mentionsSubscriptionKey(body):
			return nil, ErrSubscriptionKey
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, errUnauthorized
		case resp.StatusCode == http.StatusTooManyRequests:
			return nil, connector.RateLimited(resp.Header, c.now())
		case resp.StatusCode == http.StatusForbidden && quotaExceeded(body):
			return nil, quotaLimited(resp.Header, body, c.now())
		case resp.StatusCode >= 400:
			return nil, fmt.Errorf("suunto: %s: HTTP %d", path, resp.StatusCode)
		}
		return body, nil
	}
}

// mentionsSubscriptionKey reports a gateway answer about the subscription
// key ("Access denied due to invalid subscription key...").
func mentionsSubscriptionKey(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "subscription key")
}

// quotaExceeded reports the gateway's quota answer ("Out of call volume
// quota. Quota will be replenished in 02:15:00.").
func quotaExceeded(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "quota")
}

// replenishedIn finds the "[d.]hh:mm:ss" wait of a quota answer.
var replenishedIn = regexp.MustCompile(`replenished in (?:(\d+)\.)?(\d{1,2}):(\d{2}):(\d{2})`)

// quotaLimited builds the RateLimitError of a quota answer: Retry-After when
// usable, else the wait the message names, else the default.
func quotaLimited(h http.Header, body []byte, now time.Time) *connector.RateLimitError {
	if h.Get("Retry-After") != "" {
		if rl := connector.RateLimited(h, now); rl.RetryAfter != connector.DefaultRetryAfter {
			return rl
		}
	}
	m := replenishedIn.FindStringSubmatch(string(body))
	if m == nil {
		return &connector.RateLimitError{RetryAfter: connector.DefaultRetryAfter}
	}
	days, _ := strconv.Atoi(m[1])
	hh, _ := strconv.Atoi(m[2])
	mm, _ := strconv.Atoi(m[3])
	ss, _ := strconv.Atoi(m[4])
	d := time.Duration(days)*24*time.Hour + time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute + time.Duration(ss)*time.Second
	if d < time.Second {
		d = time.Second
	}
	return &connector.RateLimitError{RetryAfter: min(d, connector.MaxRetryAfter)}
}

// readBody reads at most limit bytes. Past the cap it reports an error rather
// than handing a truncated body to the decoder.
func readBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("suunto: response exceeds %d bytes", limit)
	}
	return b, nil
}

// jitter spreads d over [d, 1.5d), so parallel fetches retrying after the
// same failure do not wake together.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d + time.Duration(rand.Int63n(int64(d/2)+1))
}

// pause waits d (skipped when d <= 0), or returns early on cancellation.
func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
