// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// call posts params to path, checks the inner status and decodes the body into
// out.
func (c *Connector) call(ctx context.Context, token, path string, params url.Values, out any) error {
	var env envelope
	if err := c.post(ctx, token, c.apiBase+path, params, &env); err != nil {
		return err
	}
	if env.Status == 401 {
		return errUnauthorized
	}
	if env.Status != 0 {
		return fmt.Errorf("withings: %s %s: API status %d", path, params.Get("action"), env.Status)
	}
	if len(env.Body) == 0 || string(env.Body) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Body, out); err != nil {
		return fmt.Errorf("withings: %s %s: decode body: %w", path, params.Get("action"), err)
	}
	return nil
}

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 5

// post sends one form request. A rate limit (HTTP 429, or inner status 601 on
// a 200) returns a *connector.RateLimitError at once, its wait taken from
// Retry-After (one minute without one), so the pull worker defers the job
// instead of a worker sleeping. A transport error is retried on a ladder that
// starts at retryBase and doubles, jittered upwards, five attempts in all.
func (c *Connector) post(ctx context.Context, token, endpoint string, params url.Values, out any) error {
	backoff := c.retryBase
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(params.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt == maxAttempts {
				return fmt.Errorf("withings http: %w", err)
			}
			modlog().Warn().Int("attempt", attempt).Msg("transport_error_retrying")
			if err := c.sleep(ctx, jitter(backoff)); err != nil {
				return err
			}
			backoff *= 2
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("withings: read response: %w", err)
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return errUnauthorized
		case resp.StatusCode == http.StatusTooManyRequests:
			return connector.RateLimited(resp.Header, c.now())
		case resp.StatusCode >= 400:
			return fmt.Errorf("withings: HTTP %d", resp.StatusCode)
		}
		var status struct {
			Status int `json:"status"`
		}
		if json.Unmarshal(body, &status) == nil && status.Status == 601 {
			return connector.RateLimited(resp.Header, c.now())
		}
		return json.Unmarshal(body, out)
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
		return nil, fmt.Errorf("withings: response exceeds %d bytes", limit)
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
