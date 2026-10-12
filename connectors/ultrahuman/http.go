// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

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

// maxResponseBytes caps one answer. Its size is provider controlled, so a
// runaway answer must fail loudly rather than be buffered whole.
const maxResponseBytes = 16 << 20

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 4

var (
	// errUnauthorized is an HTTP 401 from a data endpoint: the access token
	// expired or was revoked.
	errUnauthorized = errors.New("ultrahuman: access token rejected")
	// errForbidden is an HTTP 403 from a data endpoint: the grant no longer
	// covers the call.
	errForbidden = errors.New("ultrahuman: access forbidden")
)

// get sends one GET with a bearer token to path under the API base. It
// returns the body of a 2xx answer, nil for 204 and 404 (no data), a
// *connector.RateLimitError for 429, errUnauthorized for 401 and errForbidden
// for 403.
func (c *Connector) get(ctx context.Context, token, path string, q url.Values) ([]byte, int, error) {
	endpoint := c.apiBase + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	resp, body, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		return nil, 0, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, resp.StatusCode, errUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return nil, resp.StatusCode, errForbidden
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, resp.StatusCode, connector.RateLimited(resp.Header, c.now())
	case resp.StatusCode == http.StatusNoContent, resp.StatusCode == http.StatusNotFound:
		return nil, resp.StatusCode, nil
	case resp.StatusCode >= 400:
		return nil, resp.StatusCode, fmt.Errorf("ultrahuman: GET %s: HTTP %d", path, resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

// postForm sends one form POST to path under the API base and leaves the
// status to the caller.
func (c *Connector) postForm(ctx context.Context, path string, form url.Values) (*http.Response, []byte, error) {
	encoded := form.Encode()
	return c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+path, strings.NewReader(encoded))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
}

// do sends the request build makes. A transport error is retried on a ladder
// that starts at retryBase and doubles, jittered upwards. The response body is
// read (capped) and closed.
func (c *Connector) do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, []byte, error) {
	backoff := c.retryBase
	for attempt := 1; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, nil, err
		}
		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if attempt == maxAttempts {
				return nil, nil, fmt.Errorf("ultrahuman http: %w", err)
			}
			slog.Warn("ultrahuman: transport error, retrying", "provider", Slug, "attempt", attempt)
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return nil, nil, err
			}
			backoff *= 2
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("ultrahuman: read response: %w", err)
		}
		return resp, body, nil
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
		return nil, fmt.Errorf("ultrahuman: response exceeds %d bytes", limit)
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
