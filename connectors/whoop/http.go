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
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// maxResponseBytes caps one WHOOP answer. Its size is provider controlled,
// so a runaway page must fail loudly rather than be buffered whole.
const maxResponseBytes = 8 << 20

// maxAttempts bounds the attempts of one request on transport errors and
// server errors (HTTP 500, 502, 503, 504).
const maxAttempts = 5

// maxPaceReset is the longest X-RateLimit-Reset the pacer waits out when
// WHOOP says no request is left in the window. A longer one is the daily
// limit: the next request gets its 429 and the job is deferred instead of a
// worker sleeping for hours.
const maxPaceReset = time.Minute

// errUnauthorized is an HTTP 401 from a data endpoint.
var errUnauthorized = errors.New("whoop: access token rejected")

// errNotFound is an HTTP 404 from a data endpoint (a record removed since it
// was listed).
var errNotFound = errors.New("whoop: record not found")

// errForbidden is an HTTP 403 from a data endpoint: the grant lacks the
// endpoint's scope.
var errForbidden = errors.New("whoop: access to this data was not granted")

// statusError is an HTTP error status a data endpoint answered after the
// retries. It names the path and the status, never the query (it holds the
// page token) or the body.
type statusError struct {
	path   string
	status int
}

func (e *statusError) Error() string { return fmt.Sprintf("whoop: %s: HTTP %d", e.path, e.status) }

// retryable reports a server error worth another attempt.
func retryable(status int) bool {
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// get sends one GET to path (under the API base) and decodes the JSON answer
// into out. See send for the errors and retries.
func (c *Connector) get(ctx context.Context, token, path string, q url.Values, out any) error {
	body, err := c.send(ctx, http.MethodGet, token, path, q)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("whoop: %s: decode answer: %w", path, err)
	}
	return nil
}

// send sends one request to path (under the API base) and returns the body of
// a 2xx answer.
//
// Every request first waits for its slot (see pace). A rate limit (HTTP 429)
// returns a *connector.RateLimitError at once, its wait taken from
// Retry-After or, failing that, X-RateLimit-Reset (one minute without either),
// so the pull worker defers the job instead of a worker sleeping. 401 gives
// errUnauthorized, 403 errForbidden, 404 errNotFound. A transport error or a
// server error (500, 502, 503, 504) is retried on a ladder that starts at
// retryBase and doubles, jittered upwards, maxAttempts in all; any other
// status gives a *statusError.
func (c *Connector) send(ctx context.Context, method, token, path string, q url.Values) ([]byte, error) {
	endpoint := c.apiBase + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	backoff := c.retryBase
	for attempt := 1; ; attempt++ {
		if err := c.pace(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpc.Do(req)
		var retry error
		var body []byte
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// The URL carries the page token: keep it out of the error.
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			retry = fmt.Errorf("whoop http: %w", err)
		} else {
			body, err = readBody(resp.Body, maxResponseBytes)
			resp.Body.Close()
			c.observeLimit(resp.Header)
			switch {
			case err != nil && ctx.Err() != nil:
				return nil, ctx.Err()
			case err != nil:
				return nil, fmt.Errorf("whoop: %s: read response: %w", path, err)
			case resp.StatusCode == http.StatusUnauthorized:
				return nil, errUnauthorized
			case resp.StatusCode == http.StatusTooManyRequests:
				return nil, c.rateLimited(resp.Header)
			case resp.StatusCode == http.StatusForbidden:
				return nil, errForbidden
			case resp.StatusCode == http.StatusNotFound:
				return nil, errNotFound
			case retryable(resp.StatusCode):
				retry = &statusError{path: path, status: resp.StatusCode}
			case resp.StatusCode >= 300:
				return nil, &statusError{path: path, status: resp.StatusCode}
			default:
				return body, nil
			}
		}
		if attempt == maxAttempts {
			return nil, retry
		}
		slog.Warn("whoop: request failed, retrying", "provider", Slug, "attempt", attempt, "error", retry)
		if err := c.sleep(ctx, jitter(backoff)); err != nil {
			return nil, err
		}
		backoff *= 2
	}
}

// pace waits for this request's slot. WHOOP allows 100 requests a minute per
// application, so the requests of every pull of this process (the scheduler
// runs several at once) are spaced pageDelay apart: one shared schedule, not
// one per pull. A negative pageDelay disables it.
func (c *Connector) pace(ctx context.Context) error {
	if c.pageDelay < 0 {
		return ctx.Err()
	}
	c.paceMu.Lock()
	now := time.Now()
	slot := c.nextSlot
	if slot.Before(now) {
		slot = now
	}
	c.nextSlot = slot.Add(c.pageDelay)
	if c.onSlot != nil {
		c.onSlot(slot)
	}
	c.paceMu.Unlock()
	return pause(ctx, slot.Sub(now))
}

// observeLimit reads the rate limit headers of an answer. When WHOOP says no
// request is left in the current window and the window resets within
// maxPaceReset, the next request waits for the reset instead of drawing a
// 429.
func (c *Connector) observeLimit(h http.Header) {
	if c.pageDelay < 0 {
		return
	}
	rem, err := strconv.Atoi(strings.TrimSpace(h.Get("X-RateLimit-Remaining")))
	if err != nil || rem > 0 {
		return
	}
	secs, err := strconv.Atoi(strings.TrimSpace(h.Get("X-RateLimit-Reset")))
	if err != nil || secs <= 0 {
		return
	}
	wait := time.Duration(secs) * time.Second
	if wait > maxPaceReset {
		return
	}
	c.paceMu.Lock()
	defer c.paceMu.Unlock()
	if at := time.Now().Add(wait); at.After(c.nextSlot) {
		c.nextSlot = at
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
