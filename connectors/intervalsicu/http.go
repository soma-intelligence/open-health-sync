// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// maxResponseBytes caps one answer. Its size is provider controlled, so a
// runaway list must fail loudly rather than be buffered whole.
const maxResponseBytes = 32 << 20

// maxAttempts bounds the attempts of one request on transport errors and
// server errors (5xx).
const maxAttempts = 4

// errUnauthorized is an HTTP 401 from a data endpoint: the access token was
// rejected (the athlete revoked the app, or connected it again, which
// replaces the token).
var errUnauthorized = errors.New("intervals_icu: access token rejected")

// get sends one GET to the API and returns the body. HTTP 401 gives
// errUnauthorized, HTTP 429 a *connector.RateLimitError at once, so the pull
// worker defers the job instead of a worker sleeping. Transport errors and
// 5xx answers are retried on a ladder that starts at retryBase and doubles,
// jittered upwards.
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
		var body []byte
		status := 0
		if err == nil {
			status = resp.StatusCode
			body, err = readBody(resp.Body, maxResponseBytes)
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("intervals_icu: read response: %w", err)
			}
		}
		retryable := err != nil || status >= 500
		if retryable {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == maxAttempts {
				if err != nil {
					return nil, fmt.Errorf("intervals_icu http: %w", err)
				}
				return nil, fmt.Errorf("intervals_icu: %s: HTTP %d", path, status)
			}
			slog.Warn("intervals_icu: request failed, retrying", "provider", Slug, "attempt", attempt, "status", status)
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		switch {
		case status == http.StatusUnauthorized:
			return nil, errUnauthorized
		case status == http.StatusTooManyRequests:
			return nil, connector.RateLimited(resp.Header, c.now())
		case status >= 400:
			return nil, fmt.Errorf("intervals_icu: %s: HTTP %d", path, status)
		}
		return body, nil
	}
}

// readBody reads at most limit bytes. Past the cap it reports an error rather
// than handing a truncated body to the decoder.
func readBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("intervals_icu: response exceeds %d bytes", limit)
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
