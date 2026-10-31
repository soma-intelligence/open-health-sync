// SPDX-License-Identifier: AGPL-3.0-only

package smartfhir

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// maxResponseBytes caps one answer. A page of a search is a few hundred
// resources; a whole chart in one unpaged answer stays far under this.
const maxResponseBytes = 64 << 20

// maxAttempts bounds the attempts of one request on transport errors and
// 5xx answers.
const maxAttempts = 3

// errUnauthorized is an HTTP 401: the token expired or was revoked.
var errUnauthorized = errors.New("smart_fhir: access token rejected")

// httpError is an HTTP 4xx or 5xx answer other than 401 and 429.
type httpError struct{ status int }

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d", e.status) }

// getJSON sends one GET (with the bearer token when given) and decodes a 2xx
// answer into v. 401 gives errUnauthorized, 429 a *connector.RateLimitError,
// another error status an *httpError.
func (c *Connector) getJSON(ctx context.Context, token, endpoint string, v any) error {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", "application/fhir+json, application/json")
		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt == maxAttempts {
				return fmt.Errorf("transport: %w", err)
			}
			slog.Warn("smart_fhir: transport error, retrying", "provider", Slug, "attempt", attempt)
			if err := pause(ctx, backoff); err != nil {
				return err
			}
			backoff *= 2
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			return err
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return errUnauthorized
		case resp.StatusCode == http.StatusTooManyRequests:
			return connector.RateLimited(resp.Header, c.now())
		case resp.StatusCode >= 500 && attempt < maxAttempts:
			if err := pause(ctx, backoff); err != nil {
				return err
			}
			backoff *= 2
			continue
		case resp.StatusCode >= 400:
			return &httpError{status: resp.StatusCode}
		}
		if err := json.Unmarshal(body, v); err != nil {
			return fmt.Errorf("decode answer: %w", err)
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
		return nil, fmt.Errorf("smart_fhir: response exceeds %d bytes", limit)
	}
	return b, nil
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
