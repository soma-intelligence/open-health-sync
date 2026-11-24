// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// maxResponseBytes caps one answer. Its size is provider controlled, so a
// runaway page must fail loudly rather than be buffered whole.
const maxResponseBytes = 16 << 20

// maxAttempts bounds the attempts of one request on transport errors and
// server errors (5xx).
const maxAttempts = 4

// Errors of a data request.
var (
	// errUnauthorized is an HTTP 401: the access token was rejected
	// (expired, revoked, or replaced by a refresh).
	errUnauthorized = errors.New("wahoo: access token rejected")
	// errNotFound is an HTTP 404, as for a workout without a summary yet.
	errNotFound = errors.New("wahoo: not found")
	// errForbidden is an HTTP 403: the grant lacks a scope (user_read or
	// workouts_read).
	errForbidden = errors.New("wahoo: the grant lacks a scope")
)

// get sends one GET to the API and returns the body. HTTP 401, 403 and 404
// give errUnauthorized, errForbidden and errNotFound, HTTP 429 a
// *connector.RateLimitError at once, so the pull worker defers the job
// instead of a worker sleeping. Transport errors and 5xx answers are retried
// on a ladder that starts at retryBase and doubles, jittered upwards.
func (c *Connector) get(ctx context.Context, token, path string, query url.Values) ([]byte, error) {
	endpoint := c.base + path
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
		var body []byte
		status := 0
		var header http.Header
		if err == nil {
			status = resp.StatusCode
			header = resp.Header
			body, err = readBody(resp.Body, maxResponseBytes)
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("wahoo: read response: %w", err)
			}
		}
		if err != nil || status >= 500 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == maxAttempts {
				if err != nil {
					return nil, fmt.Errorf("wahoo http: %w", err)
				}
				return nil, fmt.Errorf("wahoo: %s: HTTP %d", path, status)
			}
			modlog().Warn().Int("attempt", attempt).Int("status", status).Msg("request_failed_retrying")
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		switch {
		case status == http.StatusUnauthorized:
			return nil, errUnauthorized
		case status == http.StatusForbidden:
			return nil, errForbidden
		case status == http.StatusNotFound:
			return nil, errNotFound
		case status == http.StatusTooManyRequests:
			return nil, rateLimited(header, c.now())
		case status >= 400:
			return nil, fmt.Errorf("wahoo: %s: HTTP %d", path, status)
		}
		return body, nil
	}
}

// rateLimited builds the RateLimitError of a 429. A usable Retry-After wins;
// otherwise X-RateLimit-Reset, which Wahoo sends as the seconds until the
// window that ran out resets; otherwise connector.DefaultRetryAfter.
func rateLimited(h http.Header, now time.Time) *connector.RateLimitError {
	if h.Get("Retry-After") != "" {
		return connector.RateLimited(h, now)
	}
	reset := strings.TrimSpace(h.Get("X-RateLimit-Reset"))
	if i := strings.IndexByte(reset, ','); i >= 0 {
		reset = strings.TrimSpace(reset[:i])
	}
	if secs, err := strconv.ParseInt(reset, 10, 64); err == nil && secs > 0 {
		wait := time.Duration(min(secs, int64(connector.MaxRetryAfter/time.Second))) * time.Second
		return &connector.RateLimitError{RetryAfter: wait}
	}
	return &connector.RateLimitError{RetryAfter: connector.DefaultRetryAfter}
}

// readBody reads at most limit bytes. Past the cap it reports an error rather
// than handing a truncated body to the decoder.
func readBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("wahoo: response exceeds %d bytes", limit)
	}
	return b, nil
}

// jitter spreads d over [d, 1.5d).
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
