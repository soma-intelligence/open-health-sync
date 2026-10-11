// SPDX-License-Identifier: AGPL-3.0-only

package oura

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

// maxResponseBytes caps one Oura answer. Its size is provider controlled, so
// a runaway page must fail loudly rather than be buffered whole.
const maxResponseBytes = 32 << 20

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 5

// errUnauthorized is an HTTP 401 from a data endpoint: the access token was
// rejected.
var errUnauthorized = errors.New("oura: access token rejected")

// errForbidden is an HTTP 403 from a data endpoint: the user did not grant
// the scope of that endpoint, or their Oura Membership has lapsed.
var errForbidden = errors.New("oura: access to this data type is forbidden")

// get sends one GET request to path with params and decodes the JSON answer
// into out. HTTP 401 gives errUnauthorized, 403 errForbidden, and a rate
// limit (HTTP 429) a *connector.RateLimitError at once, its wait taken from
// Retry-After (one minute without one), so the pull worker defers the job
// instead of a worker sleeping. A transport error is retried on a ladder that
// starts at retryBase and doubles, jittered upwards, five attempts in all.
func (c *Connector) get(ctx context.Context, token, path string, params url.Values, out any) error {
	endpoint := c.apiBase + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
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
				return fmt.Errorf("oura http: %w", err)
			}
			slog.Warn("oura: transport error, retrying", "provider", Slug, "attempt", attempt)
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return err
			}
			backoff *= 2
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("oura: read response: %w", err)
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return errUnauthorized
		case resp.StatusCode == http.StatusForbidden:
			return errForbidden
		case resp.StatusCode == http.StatusTooManyRequests:
			return connector.RateLimited(resp.Header, c.now())
		case resp.StatusCode >= 400:
			return fmt.Errorf("oura: %s: HTTP %d", path, resp.StatusCode)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("oura: %s: decode answer: %w", path, err)
		}
		return nil
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
		return nil, fmt.Errorf("oura: response exceeds %d bytes", limit)
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
