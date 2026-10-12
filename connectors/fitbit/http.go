// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

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

// maxResponseBytes caps one API answer. A full page of 10,000 heart rate
// points is a few megabytes.
const maxResponseBytes = 64 << 20

// maxAttempts bounds the attempts of one request on transport errors, and
// maxServerAttempts on server errors (500, 502, 503).
const (
	maxAttempts       = 4
	maxServerAttempts = 3
)

// Errors of the data endpoints.
var (
	// errUnauthorized is an HTTP 401: the access token was rejected.
	errUnauthorized = errors.New("fitbit: access token rejected")
	// errForbidden is an HTTP 403: the scope of that data type was not
	// granted (PERMISSION_DENIED).
	errForbidden = errors.New("fitbit: access to this data type is forbidden")
	// errNotFound is an HTTP 404.
	errNotFound = errors.New("fitbit: not found")
	// errServer is an HTTP 5xx that outlasted the retries.
	errServer = errors.New("fitbit: server error")
	// errTooLarge is an answer over maxResponseBytes.
	errTooLarge = errors.New("fitbit: response too large")
)

// ErrAccountNotLinked reports a Google Account that is not linked to Google
// Health (FAILED_PRECONDITION, reason ACCOUNT_NOT_LINKED): the user must open
// the Fitbit (Google Health) mobile app, link the account there, and connect
// again.
var ErrAccountNotLinked = errors.New("fitbit: this Google Account is not linked to Google Health yet; " +
	"open the Fitbit app, sign in with the Google Account and finish the setup, then connect again")

// apiError is a Google API error answer:
// {"error": {"code", "message", "status", "details": [{"reason"}]}}.
type apiError struct {
	HTTP    int
	Status  string // FAILED_PRECONDITION, PERMISSION_DENIED...
	Reason  string // ErrorInfo reason: ACCOUNT_NOT_LINKED...
	Message string
}

func (e *apiError) Error() string {
	s := fmt.Sprintf("fitbit: HTTP %d", e.HTTP)
	if e.Status != "" {
		s += " " + e.Status
	}
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

// parseAPIError reads a Google error body. A body that is not one gives an
// apiError with the HTTP status only.
func parseAPIError(code int, body []byte) *apiError {
	e := &apiError{HTTP: code}
	var w struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &w) == nil {
		e.Status = clip(w.Error.Status, 64)
		e.Message = clip(w.Error.Message, 200)
		for _, d := range w.Error.Details {
			if d.Reason != "" {
				e.Reason = clip(d.Reason, 64)
				break
			}
		}
	}
	return e
}

// notLinked reports an ACCOUNT_NOT_LINKED answer.
func (e *apiError) notLinked() bool { return e.Reason == "ACCOUNT_NOT_LINKED" }

// retryableStatus reports a server error worth asking again. 504 is not one:
// Google asks for backoff on it, so it is treated as a rate limit.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	}
	return false
}

// get sends one GET to path with params and decodes the JSON answer into out.
//
// HTTP 401 gives errUnauthorized, 403 errForbidden, 404 errNotFound, an
// ACCOUNT_NOT_LINKED answer ErrAccountNotLinked, and 429 or 504 a
// *connector.RateLimitError (Google asks for exponential backoff on both).
// Transport errors and 500, 502, 503 are retried on a doubling, jittered
// ladder from retryBase.
func (c *Connector) get(ctx context.Context, token, path string, params url.Values, out any) error {
	endpoint := c.apiBase + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	backoff := c.retryBase
	wait := func() error {
		err := c.sleep(ctx, jitter(backoff))
		backoff *= 2
		return err
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
			if attempt >= maxAttempts {
				return fmt.Errorf("fitbit http: %w", err)
			}
			slog.Warn("fitbit: transport error, retrying", "provider", Slug, "attempt", attempt)
			if err := wait(); err != nil {
				return err
			}
			continue
		}
		body, err := readBody(resp.Body, maxResponseBytes)
		resp.Body.Close()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, errTooLarge) || attempt >= maxAttempts {
				return fmt.Errorf("fitbit: %s: %w", path, err)
			}
			if err := wait(); err != nil {
				return err
			}
			continue
		}
		code := resp.StatusCode
		switch {
		case code == http.StatusUnauthorized:
			return errUnauthorized
		case code == http.StatusTooManyRequests || code == http.StatusGatewayTimeout:
			return connector.RateLimited(resp.Header, c.now())
		case retryableStatus(code):
			if attempt >= maxServerAttempts {
				if resp.Header.Get("Retry-After") != "" {
					return connector.RateLimited(resp.Header, c.now())
				}
				return fmt.Errorf("%w: %s: HTTP %d", errServer, path, code)
			}
			slog.Warn("fitbit: server error, retrying", "provider", Slug, "status", code, "attempt", attempt)
			if err := wait(); err != nil {
				return err
			}
			continue
		case code >= 400:
			ae := parseAPIError(code, body)
			switch {
			case ae.notLinked():
				return fmt.Errorf("%w: %w", ErrAccountNotLinked, ae)
			case code == http.StatusForbidden:
				return fmt.Errorf("%w: %w", errForbidden, ae)
			case code == http.StatusNotFound:
				return fmt.Errorf("%w: %w", errNotFound, ae)
			}
			return ae
		case code < 200 || code > 299:
			return fmt.Errorf("fitbit: %s: unexpected HTTP %d", path, code)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("fitbit: %s: decode answer: %w", path, err)
		}
		return nil
	}
}

// readBody reads at most limit bytes; past the cap it reports errTooLarge.
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

// clip shortens s to at most n bytes, so a provider value cannot make an
// error message arbitrarily long.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
