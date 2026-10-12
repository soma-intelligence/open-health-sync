// SPDX-License-Identifier: AGPL-3.0-only

package strava

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// maxResponseBytes caps one Strava answer. Its size is provider controlled,
// so a runaway page must fail loudly rather than be buffered whole.
const maxResponseBytes = 32 << 20

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 5

// errUnauthorized is an HTTP 401 from a data endpoint: the access token was
// rejected (expired, revoked, or the athlete deauthorized the app).
var errUnauthorized = errors.New("strava: access token rejected")

// get sends one GET to the API and returns the body. HTTP 401 gives
// errUnauthorized, HTTP 429 a *connector.RateLimitError at once (see
// rateLimited), so the pull worker defers the job instead of a worker
// sleeping. A transport error is retried on a ladder that starts at retryBase
// and doubles, jittered upwards, five attempts in all.
func (c *Connector) get(ctx context.Context, token, path string, query url.Values) ([]byte, error) {
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
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == maxAttempts {
				return nil, fmt.Errorf("strava http: %w", err)
			}
			slog.Warn("strava: transport error, retrying", "provider", Slug, "attempt", attempt)
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("strava: read response: %w", err)
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, errUnauthorized
		case resp.StatusCode == http.StatusTooManyRequests:
			return nil, rateLimited(resp.Header, c.now())
		case resp.StatusCode >= 400:
			return nil, fmt.Errorf("strava: %s: HTTP %d", path, resp.StatusCode)
		}
		return body, nil
	}
}

// rateLimited builds the RateLimitError of a 429. A Retry-After header wins.
// Strava usually sends none, only its usage headers, each "15-minute,daily":
// X-RateLimit-Limit and -Usage (all requests) and X-ReadRateLimit-Limit and
// -Usage (non-upload requests). When a daily figure is spent the wait runs to
// the next midnight UTC, when Strava resets it; otherwise to the next quarter
// hour (:00, :15, :30, :45), when the short window resets.
func rateLimited(h http.Header, now time.Time) *connector.RateLimitError {
	if usableRetryAfter(h.Get("Retry-After"), now) {
		return connector.RateLimited(h, now)
	}
	now = now.UTC()
	until := now.Truncate(15 * time.Minute).Add(15 * time.Minute)
	if dailySpent(h, "X-RateLimit") || dailySpent(h, "X-ReadRateLimit") {
		until = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	}
	wait := until.Sub(now)
	if wait < time.Second {
		wait = time.Second
	}
	return &connector.RateLimitError{RetryAfter: min(wait, connector.MaxRetryAfter)}
}

// usableRetryAfter reports whether v is a wait connector.RateLimited reads:
// positive delay seconds or an HTTP date after now.
func usableRetryAfter(v string, now time.Time) bool {
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		return secs > 0
	}
	at, err := http.ParseTime(v)
	return err == nil && at.After(now)
}

// dailySpent reports whether the daily usage of the prefix-Limit and
// prefix-Usage header pair has reached its limit.
func dailySpent(h http.Header, prefix string) bool {
	limit, ok1 := dailyFigure(h.Get(prefix + "-Limit"))
	usage, ok2 := dailyFigure(h.Get(prefix + "-Usage"))
	return ok1 && ok2 && limit > 0 && usage >= limit
}

// dailyFigure reads the second value of a "15-minute,daily" header.
func dailyFigure(v string) (int, bool) {
	_, daily, ok := strings.Cut(v, ",")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(daily))
	return n, err == nil
}

// readBody reads at most limit bytes. Past the cap it reports an error rather
// than handing a truncated body to the decoder.
func readBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("strava: response exceeds %d bytes", limit)
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
