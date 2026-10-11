// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"context"
	"encoding/json"
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

// maxResponseBytes caps one WHOOP answer. Its size is provider controlled,
// so a runaway page must fail loudly rather than be buffered whole.
const maxResponseBytes = 8 << 20

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 5

// errUnauthorized is an HTTP 401 from a data endpoint.
var errUnauthorized = errors.New("whoop: access token rejected")

// errNotFound is an HTTP 404 from a data endpoint (a record removed since it
// was listed).
var errNotFound = errors.New("whoop: record not found")

// get sends one GET to path (under the API base) and decodes the JSON answer
// into out. A rate limit (HTTP 429) returns a *connector.RateLimitError at
// once, its wait taken from Retry-After or, failing that, X-RateLimit-Reset
// (one minute without either), so the pull worker defers the job instead of a
// worker sleeping. A transport error is retried on a ladder that starts at
// retryBase and doubles, jittered upwards, five attempts in all.
func (c *Connector) get(ctx context.Context, token, path string, q url.Values, out any) error {
	endpoint := c.apiBase + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	backoff := c.retryBase
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt == maxAttempts {
				// The URL carries the page token: keep it out of the error.
				var ue *url.Error
				if errors.As(err, &ue) {
					err = ue.Err
				}
				return fmt.Errorf("whoop http: %w", err)
			}
			slog.Warn("whoop: transport error, retrying", "provider", Slug, "attempt", attempt)
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return err
			}
			backoff *= 2
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("whoop: read response: %w", err)
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return errUnauthorized
		case resp.StatusCode == http.StatusTooManyRequests:
			return c.rateLimited(resp.Header)
		case resp.StatusCode == http.StatusNotFound:
			return errNotFound
		case resp.StatusCode >= 400:
			return fmt.Errorf("whoop: %s: HTTP %d", path, resp.StatusCode)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("whoop: %s: decode answer: %w", path, err)
		}
		return nil
	}
}

// rateLimited builds the rate limit error of a 429. WHOOP documents the
// X-RateLimit-Reset header (seconds until the window resets) rather than
// Retry-After, so it stands in when Retry-After is absent.
func (c *Connector) rateLimited(h http.Header) *connector.RateLimitError {
	if h.Get("Retry-After") == "" {
		if reset := h.Get("X-RateLimit-Reset"); reset != "" {
			h = h.Clone()
			h.Set("Retry-After", reset)
		}
	}
	return connector.RateLimited(h, c.now())
}

// readBody reads at most limit bytes. Past the cap it reports an error rather
// than handing a truncated body to the decoder.
func readBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("whoop: response exceeds %d bytes", limit)
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
