// SPDX-License-Identifier: AGPL-3.0-only

package garminconnect

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
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// maxResponseBytes caps one API answer.
const maxResponseBytes = 32 << 20

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 4

// userAgent names this server to Garmin honestly. It is a single, stable
// value: the connector does not rotate user agents or impersonate a browser
// to get past anti-bot protection.
const userAgent = "open-health-sync"

// errUnauthorized is an HTTP 401: the access token is gone.
var errUnauthorized = errors.New("garmin_connect: access token rejected")

// get sends one GET to path under the API base with a bearer token. It
// returns the body of a 2xx answer, nil for 204 and 404, errUnauthorized for
// 401, a *connector.RateLimitError for 429, and ErrBlocked for 403.
func (c *Connector) get(ctx context.Context, token, path string, q url.Values) ([]byte, error) {
	endpoint := c.apiBase + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	resp, body, err := c.do(ctx, http.MethodGet, endpoint, "Bearer "+token, "", nil)
	if err != nil {
		return nil, err
	}
	switch s := resp.StatusCode; {
	case s == http.StatusUnauthorized:
		return nil, errUnauthorized
	case s == http.StatusForbidden:
		return nil, fmt.Errorf("garmin_connect: GET %s: HTTP 403: %w", path, ErrBlocked)
	case s == http.StatusTooManyRequests:
		return nil, connector.RateLimited(resp.Header, c.now())
	case s == http.StatusNoContent, s == http.StatusNotFound:
		return nil, nil
	case s >= 400:
		return nil, fmt.Errorf("garmin_connect: GET %s: HTTP %d", path, s)
	}
	return body, nil
}

// do sends one request, with an Authorization header when authHeader is set
// and a body of contentType when body is non-nil (contentType defaults to
// application/json). A transport error is retried on a ladder that starts at
// retryBase and doubles, jittered upwards. The response body is read (capped)
// and closed.
func (c *Connector) do(ctx context.Context, method, endpoint, authHeader, contentType string, body []byte) (*http.Response, []byte, error) {
	backoff := c.retryBase
	for attempt := 1; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, rd)
		if err != nil {
			return nil, nil, err
		}
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		if body != nil {
			if contentType == "" {
				contentType = "application/json"
			}
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if attempt == maxAttempts {
				return nil, nil, fmt.Errorf("garmin_connect http: %w", err)
			}
			slog.Warn("garmin_connect: transport error, retrying", "provider", Slug, "attempt", attempt)
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return nil, nil, err
			}
			backoff *= 2
			continue
		}
		b, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("garmin_connect: read response: %w", err)
		}
		return resp, b, nil
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
		return nil, fmt.Errorf("garmin_connect: response exceeds %d bytes", limit)
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
