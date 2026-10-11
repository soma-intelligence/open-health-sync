// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"bytes"
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

// maxResponseBytes caps one AccessLink answer. Its size is provider
// controlled, so a runaway answer must fail loudly rather than be buffered
// whole.
const maxResponseBytes = 32 << 20

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 5

var (
	// errUnauthorized is an HTTP 401 from AccessLink: the token is gone.
	errUnauthorized = errors.New("polar: access token rejected")
	// errForbidden is an HTTP 403 from a user endpoint: the user is not
	// registered with this client or has not accepted Polar's mandatory
	// consents. Either way only a new connect fixes it.
	errForbidden = errors.New("polar: access forbidden, user not registered or consents missing")
)

// isUnauthorized reports a rejection only a new connect can fix.
func isUnauthorized(err error) bool {
	return errors.Is(err, errUnauthorized) || errors.Is(err, errForbidden)
}

// get sends one GET to path under the API base. It returns the body of a 2xx
// answer, nil for 204 and 404 (no data: the status tells which), a
// *connector.RateLimitError for 429, errUnauthorized for 401 and errForbidden
// for 403.
func (c *Connector) get(ctx context.Context, token, path string, q url.Values) ([]byte, int, error) {
	endpoint := c.apiBase + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	resp, body, err := c.do(ctx, http.MethodGet, token, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, resp.StatusCode, errUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return nil, resp.StatusCode, errForbidden
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, resp.StatusCode, rateLimited(resp.Header, c.now())
	case resp.StatusCode == http.StatusNoContent, resp.StatusCode == http.StatusNotFound:
		return nil, resp.StatusCode, nil
	case resp.StatusCode >= 400:
		return nil, resp.StatusCode, fmt.Errorf("polar: GET %s: HTTP %d", path, resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

// send sends one request with an optional JSON body and returns the HTTP
// status, leaving its meaning to the caller, except 429 which gives a
// *connector.RateLimitError.
func (c *Connector) send(ctx context.Context, method, token, path string, payload []byte) (int, error) {
	resp, _, err := c.do(ctx, method, token, c.apiBase+path, payload)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return resp.StatusCode, rateLimited(resp.Header, c.now())
	}
	return resp.StatusCode, nil
}

// do sends one request with a bearer token. A transport error is retried on
// a ladder that starts at retryBase and doubles, jittered upwards, five
// attempts in all. The response body is read (capped) and closed.
func (c *Connector) do(ctx context.Context, method, token, endpoint string, payload []byte) (*http.Response, []byte, error) {
	backoff := c.retryBase
	for attempt := 1; ; attempt++ {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, rd)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if attempt == maxAttempts {
				return nil, nil, fmt.Errorf("polar http: %w", err)
			}
			slog.Warn("polar: transport error, retrying", "provider", Slug, "attempt", attempt)
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return nil, nil, err
			}
			backoff *= 2
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("polar: read response: %w", err)
		}
		return resp, body, nil
	}
}

// rateLimited builds the rate limit error of a 429. AccessLink sends
// RateLimit-Usage, RateLimit-Limit and RateLimit-Reset, each holding the
// short term (15 minute) and long term (24 hour) value, comma separated.
// Retry-After wins when present; otherwise the wait is the reset of the
// longest window that is used up (the short term reset when none reads as
// used up).
func rateLimited(h http.Header, now time.Time) *connector.RateLimitError {
	if h.Get("Retry-After") != "" {
		return connector.RateLimited(h, now)
	}
	resets := headerInts(h.Get("RateLimit-Reset"))
	usage := headerInts(h.Get("RateLimit-Usage"))
	limit := headerInts(h.Get("RateLimit-Limit"))
	wait := int64(-1)
	for i, r := range resets {
		if i < len(usage) && i < len(limit) && usage[i] >= limit[i] && r > wait {
			wait = r
		}
	}
	if wait < 0 && len(resets) > 0 {
		wait = resets[0]
	}
	if wait <= 0 {
		return connector.RateLimited(http.Header{}, now)
	}
	return connector.RateLimited(http.Header{"Retry-After": {strconv.FormatInt(wait, 10)}}, now)
}

// headerInts parses "a, b" into integers, skipping what does not parse.
func headerInts(v string) []int64 {
	var out []int64
	for _, p := range strings.Split(v, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// readBody reads at most limit bytes. Past the cap it reports an error rather
// than handing a truncated body to the decoder.
func readBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("polar: response exceeds %d bytes", limit)
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
