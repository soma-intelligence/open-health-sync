// SPDX-License-Identifier: AGPL-3.0-only

package dexcom

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

// maxResponseBytes caps one API answer. 30 days of EGVs at one every 5
// minutes is about 8,600 records, well under this.
const maxResponseBytes = 32 << 20

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 5

// errUnauthorized is an HTTP 401 or 403: the token is gone or the user
// withdrew consent. Only a new connect fixes it.
var errUnauthorized = errors.New("dexcom: access token rejected")

// get sends one GET to path under the API base and returns the body of a 2xx
// answer. 401 and 403 give errUnauthorized, 429 a *connector.RateLimitError.
func (c *Connector) get(ctx context.Context, token, path string, q url.Values) ([]byte, error) {
	endpoint := c.base() + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
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
				return nil, fmt.Errorf("dexcom http: %w", err)
			}
			slog.Warn("dexcom: transport error, retrying", "provider", Slug, "attempt", attempt)
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
			return nil, errUnauthorized
		case resp.StatusCode == http.StatusTooManyRequests:
			return nil, connector.RateLimited(resp.Header, c.now())
		case resp.StatusCode >= 400:
			return nil, fmt.Errorf("dexcom: GET %s: HTTP %d", path, resp.StatusCode)
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
		return nil, fmt.Errorf("dexcom: response exceeds %d bytes", limit)
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
