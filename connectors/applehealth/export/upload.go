// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// formatVersion names the conversion rules. Bump it when a change alters the
// lines produced, so a re-run after an upgrade uses a fresh batch instead of
// colliding (409) with the parts of the previous one.
const formatVersion = "1"

// maxPartNumber is the largest X-OHS-Part the server accepts.
const maxPartNumber = 1_000_000

// BatchID derives the X-OHS-Batch of an import from the export's ExportDate,
// the size of export.xml and the options, so the same export imported again
// with the same options gets the same batch and the same parts: the server
// answers them as duplicates and stores nothing twice.
func BatchID(exportDate string, xmlSize int64, opts Options) string {
	opts.withDefaults()
	h := sha256.New()
	fmt.Fprintf(h, "v%s\x00%s\x00%d\x00%s\x00%s\x00%d\x00%d",
		formatVersion, exportDate, xmlSize, opts.Since, opts.Zone, opts.MaxPartBytes, opts.MaxBufferedBytes)
	stamp := "undated"
	if t, err := parseExportTime(exportDate); err == nil {
		stamp = t.UTC().Format("20060102T150405Z")
	}
	return "ahx-" + stamp + "-" + hex.EncodeToString(h.Sum(nil))[:12]
}

// ErrAuth reports a 401 or 403: the token is wrong, expired or lacks the
// upload scope. Retrying cannot help.
var ErrAuth = errors.New("the server refused the token")

// Uploader posts parts to POST /v1/ingest/apple-health.
type Uploader struct {
	// Endpoint is the full ingest URL.
	Endpoint string
	// Token is the user token (ohs_ut_...) with the upload scope.
	Token string
	// Batch is the X-OHS-Batch of every part.
	Batch string
	// Client defaults to an http.Client with a 5 minute timeout.
	Client *http.Client
	// MaxAttempts bounds the tries of one part on 429, 5xx and network
	// errors (default 6).
	MaxAttempts int
	// Backoff is the wait before retry n (1, 2, ...) when the server sends
	// no Retry-After. Default: 1s, 2s, 4s... capped at 30s.
	Backoff func(n int) time.Duration
}

func (u *Uploader) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (u *Uploader) backoff(n int) time.Duration {
	if u.Backoff != nil {
		return u.Backoff(n)
	}
	d := time.Second << (n - 1)
	if d > 30*time.Second || d <= 0 {
		d = 30 * time.Second
	}
	return d
}

// address returns the batch and part number a part is sent under. Past the
// server's largest part number, parts move to a numbered batch of their own.
func (u *Uploader) address(n int) (string, int) {
	if n < maxPartNumber {
		return u.Batch, n
	}
	return u.Batch + "-" + strconv.Itoa(n/maxPartNumber), n % maxPartNumber
}

// gzipBytes compresses deterministically: gzip.Writer writes no name and a
// zero time, so the same lines give the same bytes on every run.
func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Upload sends one part, retrying 429, 5xx and network errors. It reports
// whether the server already held the same bytes.
func (u *Uploader) Upload(ctx context.Context, p Part) (duplicate bool, err error) {
	body, err := gzipBytes(p.NDJSON)
	if err != nil {
		return false, err
	}
	batch, num := u.address(p.Number)
	attempts := u.MaxAttempts
	if attempts <= 0 {
		attempts = 6
	}
	var lastErr error
	for try := 1; ; try++ {
		dup, retryAfter, err := u.post(ctx, p, batch, num, body)
		if err == nil {
			return dup, nil
		}
		var re retryable
		if !errors.As(err, &re) {
			return false, err
		}
		lastErr = err
		if try >= attempts {
			return false, fmt.Errorf("part %d (%s, %s): gave up after %d attempts: %w", p.Number, p.Type, p.Day, attempts, lastErr)
		}
		wait := retryAfter
		if wait <= 0 {
			wait = u.backoff(try)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return false, ctx.Err()
		case <-t.C:
		}
	}
}

// retryable marks an error worth another attempt.
type retryable struct{ err error }

func (r retryable) Error() string { return r.err.Error() }
func (r retryable) Unwrap() error { return r.err }

func (u *Uploader) post(ctx context.Context, p Part, batch string, num int, body []byte) (bool, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.Endpoint, bytes.NewReader(body))
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+u.Token)
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("X-OHS-Type", p.Type)
	req.Header.Set("X-OHS-Day", p.Day)
	req.Header.Set("X-OHS-Batch", batch)
	req.Header.Set("X-OHS-Part", strconv.Itoa(num))
	resp, err := u.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, 0, ctx.Err()
		}
		// The URL is ours and holds no secret; the error may name it.
		return false, 0, retryable{fmt.Errorf("part %d: %w", p.Number, err)}
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	switch {
	case resp.StatusCode == http.StatusOK:
		var out struct {
			Duplicate bool `json:"duplicate"`
		}
		if err := json.Unmarshal(msg, &out); err != nil {
			return false, 0, fmt.Errorf("part %d: unreadable answer from the server: %w", p.Number, err)
		}
		return out.Duplicate, 0, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, 0, fmt.Errorf("%w (HTTP %d%s): check the token and that it has the upload scope",
			ErrAuth, resp.StatusCode, problemDetail(msg))
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return false, retryAfter(resp.Header.Get("Retry-After")), retryable{
			fmt.Errorf("part %d: HTTP %d%s", p.Number, resp.StatusCode, problemDetail(msg))}
	case resp.StatusCode == http.StatusConflict:
		return false, 0, fmt.Errorf("part %d (%s, %s): HTTP 409, the server holds other bytes under batch %s part %d: "+
			"run again with a new --batch", p.Number, p.Type, p.Day, batch, num)
	default:
		return false, 0, fmt.Errorf("part %d (%s, %s): HTTP %d%s", p.Number, p.Type, p.Day, resp.StatusCode, problemDetail(msg))
	}
}

// problemDetail extracts the title and detail of an RFC 9457 problem body.
// Server messages name headers and limits, never a health value.
func problemDetail(body []byte) string {
	var pr struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &pr) != nil || (pr.Title == "" && pr.Detail == "") {
		return ""
	}
	s := strings.TrimSpace(strings.Join([]string{pr.Title, pr.Detail}, ": "))
	s = strings.Trim(s, ": ")
	if len(s) > 300 {
		s = s[:300]
	}
	return ": " + s
}

func retryAfter(v string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	return 0
}

// Summary is the outcome of Run.
type Summary struct {
	Stats
	Batch      string
	Uploaded   int // parts the server accepted, duplicates included
	Duplicates int // parts the server already held
}

// Run converts the export and, unless up is nil (a dry run), uploads the
// parts with at most concurrency requests in flight. progress, when not
// nil, is called after each part with the running totals (Stats.Records and
// Stats.Parts are then progress counts, the rest is filled at the end). It
// stops at the first error: a refused token, a part that keeps failing.
func Run(ctx context.Context, e *Export, opts Options, up *Uploader, concurrency int, progress func(Summary)) (Summary, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if concurrency <= 0 {
		concurrency = 4
	}
	var (
		mu       sync.Mutex
		sum      Summary
		firstErr error
	)
	if up != nil {
		sum.Batch = up.Batch
	}
	report := func(p Part) {
		sum.Records = max(sum.Records, p.RecordsRead) // parts finish out of order
		sum.Parts++
		if progress != nil {
			progress(sum)
		}
	}

	if up == nil {
		stats, err := Convert(ctx, e, opts, func(p Part) error {
			report(p)
			return nil
		})
		sum.Stats = stats
		return sum, err
	}

	parts := make(chan Part, concurrency)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range parts {
				dup, err := up.Upload(ctx, p)
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
						cancel()
					}
				} else {
					sum.Uploaded++
					if dup {
						sum.Duplicates++
					}
					report(p)
				}
				mu.Unlock()
			}
		}()
	}
	stats, convErr := Convert(ctx, e, opts, func(p Part) error {
		select {
		case parts <- p:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	close(parts)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	uploaded, dups := sum.Uploaded, sum.Duplicates
	sum.Stats = stats
	sum.Uploaded, sum.Duplicates = uploaded, dups
	if firstErr != nil {
		return sum, firstErr
	}
	return sum, convErr
}
