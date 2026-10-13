// SPDX-License-Identifier: AGPL-3.0-only

package omron

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// maxResponseBytes caps one API answer.
const maxResponseBytes = 32 << 20

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 5

// errUnauthorized is an HTTP 401 or 403: the token is gone or the user
// withdrew consent. Only a new connect fixes it.
var errUnauthorized = errors.New("omron: access token rejected")

// post sends one form POST to the measurement endpoint and returns the body
// of a 2xx answer. 401 and 403 give errUnauthorized, 429 a
// *connector.RateLimitError.
func (c *Connector) post(ctx context.Context, token string, form url.Values) ([]byte, error) {
	endpoint := c.endpoints().Measurement
	backoff := c.retryBase
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == maxAttempts {
				return nil, fmt.Errorf("omron http: %w", err)
			}
			slog.Warn("omron: transport error, retrying", "provider", Slug, "attempt", attempt)
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
			return nil, fmt.Errorf("omron: POST measurement: HTTP %d", resp.StatusCode)
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
		return nil, fmt.Errorf("omron: response exceeds %d bytes", limit)
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
