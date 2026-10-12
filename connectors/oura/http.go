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
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// maxResponseBytes caps one Oura answer. Its size is provider controlled, so
// a runaway page must fail loudly rather than be buffered whole.
const maxResponseBytes = 32 << 20

// maxAttempts bounds the attempts of one request on transport errors.
const maxAttempts = 5

// maxServerAttempts bounds the attempts of one request Oura answers with a
// server error (HTTP 500, 502, 503, 504). Fewer than for a transport error:
// an API that is down stays down for a while, and the job is retried later.
const maxServerAttempts = 3

// errUnauthorized is an HTTP 401 from a data endpoint: the access token was
// rejected.
var errUnauthorized = errors.New("oura: access token rejected")

// errForbidden is an HTTP 403 from a data endpoint: the user did not grant
// the scope of that endpoint, or their Oura Membership has lapsed.
var errForbidden = errors.New("oura: access to this data type is forbidden")

// errNotFound is an HTTP 404 from a data endpoint.
var errNotFound = errors.New("oura: data endpoint not found")

// errServer is an HTTP 5xx Oura kept answering through the retries.
var errServer = errors.New("oura: server error")

// retryableStatus reports a server error worth asking again.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// get sends one GET request to path with params and decodes the JSON answer
// into out. HTTP 401 gives errUnauthorized, 403 errForbidden, and a rate
// limit (HTTP 429) a *connector.RateLimitError at once, its wait taken from
// Retry-After (one minute without one), so the pull worker defers the job
// instead of a worker sleeping.
//
// A transport error (or a body cut short) is retried on a ladder that starts
// at retryBase and doubles, jittered upwards, maxAttempts in all; a server
// error (500, 502, 503, 504) the same way, maxServerAttempts in all. A server
// error that outlasts its retries while naming a Retry-After is reported as a
// rate limit, so the job waits that long; without one it is errServer.
func (c *Connector) get(ctx context.Context, token, path string, params url.Values, out any) error {
	endpoint := c.apiBase + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	backoff := c.retryBase
	retry := func(attempt, limit int, what string) error {
		if attempt >= limit {
			return errors.New(what)
		}
		slog.Warn("oura: "+what+", retrying", "provider", Slug, "attempt", attempt)
		if err := c.sleep(ctx, jitter(backoff)); err != nil {
			return err
		}
		backoff *= 2
		return nil
	}
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
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
			if rerr := retry(attempt, maxAttempts, "transport error"); rerr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("oura http: %w", err)
			}
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, errTooLarge) {
				return fmt.Errorf("oura: %s: %w", path, err)
			}
			if rerr := retry(attempt, maxAttempts, "response cut short"); rerr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("oura: read response: %w", err)
			}
			continue
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return errUnauthorized
		case resp.StatusCode == http.StatusForbidden:
			return errForbidden
		case resp.StatusCode == http.StatusNotFound && strings.HasPrefix(path, collectionPath):
			return errNotFound
		case resp.StatusCode == http.StatusTooManyRequests:
			return connector.RateLimited(resp.Header, c.now())
		case retryableStatus(resp.StatusCode):
			if rerr := retry(attempt, maxServerAttempts, "server error"); rerr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if resp.Header.Get("Retry-After") != "" {
					return connector.RateLimited(resp.Header, c.now())
				}
				return fmt.Errorf("%w: %s: HTTP %d", errServer, path, resp.StatusCode)
			}
			continue
		case resp.StatusCode >= 400:
			return fmt.Errorf("oura: %s: HTTP %d", path, resp.StatusCode)
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			return fmt.Errorf("oura: %s: unexpected HTTP %d", path, resp.StatusCode)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("oura: %s: decode answer: %w", path, err)
		}
		return nil
	}
}

// errTooLarge reports an answer over the size cap.
var errTooLarge = errors.New("oura: response too large")

// readBody reads at most limit bytes. Past the cap it reports errTooLarge
// rather than handing a truncated body to the decoder.
func readBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: over %d bytes", errTooLarge, limit)
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
