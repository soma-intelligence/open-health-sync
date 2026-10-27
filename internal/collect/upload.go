// SPDX-License-Identifier: AGPL-3.0-only

// Package collect brings data in: device uploads, and (later) OAuth pulls and
// provider notifications.
package collect

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
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// MaxUploadBytes caps one uploaded part (gzip bytes). Larger parts get 413,
// never a truncated store.
const MaxUploadBytes = 64 << 20

// DefaultMaxDecompressedBytes caps how far one part may expand while it is
// validated, unless UploadDeps.MaxDecompressedBytes says otherwise
// (OHS_INGEST_MAX_DECOMPRESSED_MIB). The clients write parts of at most
// 8 MiB of NDJSON (or one line, at most 32 MiB, on its own), so 64 MiB
// leaves room; a gzip bomb costs at most this much decompression while it
// holds an ingest slot. It stays at or under the ETL reader's cap
// (etl.DefaultMaxDecompressedBytes), so an accepted part is one the ETL can
// read to the end.
const DefaultMaxDecompressedBytes int64 = 64 << 20

// MaxMaxDecompressedBytes is the largest UploadDeps.MaxDecompressedBytes:
// the ETL reader's cap.
const MaxMaxDecompressedBytes int64 = 512 << 20

// storageFullRetryAfter is the Retry-After, in seconds, of an upload refused
// because the raw store is full or read-only: freeing room takes an
// operator, so clients wait longer than for a restart.
const storageFullRetryAfter = 60

// uploadReadTimeout bounds how long one part may take to arrive. The server
// has no global read timeout (it would cut long requests), so a stalled client
// cannot pin a goroutine and up to 64 MiB of buffer indefinitely.
const uploadReadTimeout = 5 * time.Minute

// Upload headers.
const (
	HeaderType  = "X-OHS-Type"
	HeaderDay   = "X-OHS-Day"
	HeaderBatch = "X-OHS-Batch"
	HeaderPart  = "X-OHS-Part"
)

// maxPart bounds X-OHS-Part. A batch of a million parts is not an upload.
const maxPart = 1_000_000

// UploadDeps is what the upload handler needs.
type UploadDeps struct {
	Pool *pgxpool.Pool
	Raw  rawstore.Store
	// Device validates the body and names the provider (its slug).
	Device connector.DeviceConnector
	// Allowed reports whether a tenant may upload to the provider, as
	// Deps.Allowed does. Nil allows every tenant.
	Allowed func(tenant uuid.UUID, slug string) bool
	// MaxDecompressedBytes caps how far one part may expand (413 above).
	// 0 means DefaultMaxDecompressedBytes; above MaxMaxDecompressedBytes it
	// is cut to it.
	MaxDecompressedBytes int64
}

func (d UploadDeps) maxDecompressed() int64 {
	switch {
	case d.MaxDecompressedBytes <= 0:
		return DefaultMaxDecompressedBytes
	case d.MaxDecompressedBytes > MaxMaxDecompressedBytes:
		return MaxMaxDecompressedBytes
	}
	return d.MaxDecompressedBytes
}

func (d UploadDeps) allowed(tenantID uuid.UUID, slug string) bool {
	return d.Allowed == nil || d.Allowed(tenantID, slug)
}

// UploadResponse is the 200 body.
type UploadResponse struct {
	Key       string `json:"key"`
	Duplicate bool   `json:"duplicate"`
}

// NewUploadHandler returns the device upload handler. It expects to run behind
// auth.Middleware with auth.UserUpload, which puts the user's Principal on the
// context. An upload to a provider turned off for the user's tenant is
// refused first (403). One request stores one part:
//
//  1. headers are validated (400 naming the header);
//  2. the body is read up to MaxUploadBytes (413 above);
//  3. it is fully decompressed and every line checked by the connector (400,
//     nothing stored, for a broken gzip or a bad line);
//  4. the gzip bytes are written verbatim to the raw store;
//  5. the raw_objects row and its ETL job are inserted in one transaction.
//
// Re-sending the same part is safe: the answer is 200 with duplicate true and
// nothing new is stored or queued. The same body under other headers is a
// duplicate of the object already holding it. Other bytes under a key that
// is already taken are refused with 409; a raw file is never rewritten.
//
// The raw key is raw/<tenant>/<user>/<provider>/<day>/<batch>_<part>.ndjson.gz:
// X-OHS-Type is recorded on the raw_objects row but is not part of the key.
// A client must therefore keep (batch, part) unique across all the types it
// uploads for one day: two types sent as the same batch and part collide, and
// the second gets 409.
func NewUploadHandler(d UploadDeps) http.Handler {
	if d.Pool == nil || d.Raw == nil || d.Device == nil {
		panic("collect: NewUploadHandler needs Pool, Raw and Device")
	}
	return &uploadHandler{d: d, provider: d.Device.Slug()}
}

type uploadHandler struct {
	d        UploadDeps
	provider string
}

type uploadReq struct {
	tenant, user uuid.UUID
	dataType     string
	day          time.Time
	batch        string
	part         int
}

func (h *uploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.From(r.Context())
	if !ok || p.UserID == nil {
		// Only reachable when mounted without the middleware.
		auth.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "a user token with the upload scope is required")
		return
	}
	if !h.d.allowed(p.TenantID, h.provider) {
		writeProviderOff(w)
		return
	}
	req, msg := parseHeaders(r.Header)
	if msg != "" {
		auth.WriteProblem(w, http.StatusBadRequest, "Bad Request", msg)
		return
	}
	req.tenant, req.user = p.TenantID, *p.UserID

	// Ignored where unsupported (httptest's recorder).
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadReadTimeout))

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxUploadBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			auth.WriteProblem(w, http.StatusRequestEntityTooLarge, "Content Too Large",
				fmt.Sprintf("a part may hold at most %d bytes", MaxUploadBytes))
			return
		}
		auth.WriteProblem(w, http.StatusBadRequest, "Bad Request", "the body could not be read")
		return
	}
	if len(body) == 0 {
		auth.WriteProblem(w, http.StatusBadRequest, "Bad Request", "the body is empty")
		return
	}

	meta := connector.UploadMeta{TenantID: req.tenant, UserID: req.user, Type: req.dataType, Day: req.day, Batch: req.batch, Part: req.part}
	if status, msg := h.validate(meta, body); status != 0 {
		auth.WriteProblem(w, status, http.StatusText(status), msg)
		return
	}

	resp, status, err := h.store(r.Context(), req, body)
	if errors.Is(err, errInactive) {
		auth.WriteProblem(w, http.StatusGone, "Gone", "this user is being deleted")
		return
	}
	if errors.Is(err, errErasing) {
		auth.WriteProblem(w, http.StatusConflict, "Conflict",
			"this user's data from this provider is being deleted; nothing was kept, upload again once the deletion job is done")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "collect: upload failed",
			"tenant_id", req.tenant, "user_id", req.user, "provider", h.provider, "error", err)
		if db.Unavailable(err) {
			// The raw store's network or the database's: say neither.
			auth.WriteUnavailable(w, "storage is unavailable; retry in a few seconds")
			return
		}
		if rawstore.Unwritable(err) {
			// A full or read-only raw store: the client keeps the part and
			// sends it again later, instead of giving up on a 500.
			w.Header().Set("Retry-After", strconv.Itoa(storageFullRetryAfter))
			auth.WriteProblem(w, http.StatusServiceUnavailable, http.StatusText(http.StatusServiceUnavailable),
				"storage is full or not writable; retry later")
			return
		}
		auth.WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "the part could not be stored, retry later")
		return
	}
	if status == http.StatusConflict {
		auth.WriteProblem(w, http.StatusConflict, "Conflict",
			"(batch, part) already used for this day with different content; batch and part must be unique across types")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// Part is one upload part a route builds itself instead of receiving it:
// the self report route turns a JSON body into one.
type Part struct {
	Tenant, User uuid.UUID
	// Type, Day, Batch and Part are what the four upload headers carry.
	Type  string
	Day   time.Time
	Batch string
	Part  int
	// Body is the gzip bytes, stored verbatim.
	Body []byte
}

// Errors of StorePart.
var (
	// ErrUserGone means the user is being deleted (the upload route's 410).
	ErrUserGone = errInactive
	// ErrErasing means this provider's data is being deleted for the user.
	ErrErasing = errErasing
	// ErrKeyTaken means (day, batch, part) already holds other bytes.
	ErrKeyTaken = errors.New("collect: the part's key already holds other bytes")
)

// StorePart stores p exactly as the upload route stores an upload: the
// connector checks every line, the bytes go to the raw store, and the
// raw_objects row and its ETL job are inserted in one transaction. The same
// bytes sent again are a duplicate. A part the connector refuses is an
// error naming the line (the caller should have checked its input first). A
// provider turned off for the tenant is ErrProviderOff, nothing stored.
func StorePart(ctx context.Context, d UploadDeps, p Part) (UploadResponse, error) {
	if d.Pool == nil || d.Raw == nil || d.Device == nil {
		return UploadResponse{}, errors.New("collect: StorePart needs Pool, Raw and Device")
	}
	h := &uploadHandler{d: d, provider: d.Device.Slug()}
	if !d.allowed(p.Tenant, h.provider) {
		return UploadResponse{}, ErrProviderOff
	}
	q := uploadReq{tenant: p.Tenant, user: p.User, dataType: p.Type, day: p.Day, batch: p.Batch, part: p.Part}
	if !safeSegment(q.dataType) || !safeSegment(q.batch) || q.part < 0 || q.part > maxPart {
		return UploadResponse{}, errors.New("collect: StorePart: invalid type, batch or part")
	}
	meta := connector.UploadMeta{TenantID: q.tenant, UserID: q.user, Type: q.dataType, Day: q.day, Batch: q.batch, Part: q.part}
	if status, msg := h.validate(meta, p.Body); status != 0 {
		return UploadResponse{}, fmt.Errorf("collect: StorePart: %s", msg)
	}
	resp, status, err := h.store(ctx, q, p.Body)
	if err != nil {
		return UploadResponse{}, err
	}
	if status == http.StatusConflict {
		return UploadResponse{}, ErrKeyTaken
	}
	return resp, nil
}

// parseHeaders validates the four key headers. The message names the header.
func parseHeaders(hd http.Header) (uploadReq, string) {
	var q uploadReq
	q.dataType = hd.Get(HeaderType)
	if !safeSegment(q.dataType) {
		return q, headerMsg(HeaderType, "a type identifier of letters, digits, '_' or '-' (at most 128)")
	}
	dayStr := hd.Get(HeaderDay)
	day, err := time.Parse(time.DateOnly, dayStr)
	if err != nil || day.Format(time.DateOnly) != dayStr {
		return q, headerMsg(HeaderDay, "a date as YYYY-MM-DD")
	}
	q.day = day
	q.batch = hd.Get(HeaderBatch)
	if !safeSegment(q.batch) {
		return q, headerMsg(HeaderBatch, "a batch id of letters, digits, '_' or '-' (at most 128)")
	}
	partStr := hd.Get(HeaderPart)
	part, err := strconv.Atoi(partStr)
	if err != nil || part < 0 || part > maxPart || strconv.Itoa(part) != partStr {
		return q, headerMsg(HeaderPart, fmt.Sprintf("an integer from 0 to %d", maxPart))
	}
	q.part = part
	return q, ""
}

func headerMsg(name, want string) string {
	return fmt.Sprintf("header %s is missing or invalid: want %s", name, want)
}

// safeSegment keeps a header usable inside a raw key: no separator, no dot.
func safeSegment(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// streamError marks an error of the decompression stream, as opposed to a
// line the connector refused.
type streamError struct{ err error }

func (e *streamError) Error() string { return e.err.Error() }
func (e *streamError) Unwrap() error { return e.err }

var errTooLarge = errors.New("collect: decompressed part exceeds the cap")

// guardedReader reports every read error as a streamError and stops at the
// decompression cap.
type guardedReader struct {
	r    io.Reader
	left int64
}

func (g *guardedReader) Read(p []byte) (int, error) {
	if g.left <= 0 {
		// Probe one byte: exactly at the cap with nothing left is fine.
		var one [1]byte
		n, err := g.r.Read(one[:])
		if n > 0 {
			return 0, &streamError{errTooLarge}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, &streamError{err}
		}
		return 0, err
	}
	if int64(len(p)) > g.left {
		p = p[:g.left]
	}
	n, err := g.r.Read(p)
	g.left -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, &streamError{err}
	}
	return n, err
}

// validate fully decompresses body and has the connector check every line.
// It returns 0 when the part is acceptable, else the status and the message.
func (h *uploadHandler) validate(meta connector.UploadMeta, body []byte) (int, string) {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return http.StatusBadRequest, "the body is not gzip"
	}
	defer zr.Close()
	limit := h.d.maxDecompressed()
	err = h.d.Device.ParseUpload(meta, &guardedReader{r: zr, left: limit}, nil)
	if err == nil {
		return 0, ""
	}
	var se *streamError
	switch {
	case errors.Is(err, errTooLarge):
		return http.StatusRequestEntityTooLarge,
			fmt.Sprintf("a part may expand to at most %d bytes (%d MiB); send smaller parts", limit, limit>>20)
	case errors.As(err, &se):
		return http.StatusBadRequest, "the gzip stream is truncated or corrupt"
	default:
		// The connector's message names the line; it never quotes the data.
		return http.StatusBadRequest, err.Error()
	}
}

// store writes the raw file and registers it. It returns the response, or
// status 409 for a key already holding other bytes.
func (h *uploadHandler) store(ctx context.Context, q uploadReq, body []byte) (UploadResponse, int, error) {
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	key := rawstore.Key(q.tenant, q.user, h.provider, q.day, q.batch, q.part)

	// Already registered, under this key or (same bytes) another one.
	if resp, status, found, err := h.existing(ctx, q, key, hash); err != nil || found {
		return resp, status, err
	}

	if err := userActive(ctx, h.d.Pool, q.tenant, q.user); err != nil {
		return UploadResponse{}, 0, err
	}
	if err := putRaw(ctx, h.d.Raw, key, body); err != nil {
		if errors.Is(err, errOtherBytes) {
			return UploadResponse{}, http.StatusConflict, nil
		}
		return UploadResponse{}, 0, err
	}

	inserted, err := registerRaw(ctx, h.d.Pool, rawObject{
		tenant: q.tenant, user: q.user, provider: h.provider, key: key, hash: hash, size: len(body),
		batch: q.batch, part: q.part, dataType: q.dataType, tier: queue.TierHighFreq,
	})
	if errors.Is(err, errInactive) || errors.Is(err, errErasing) {
		// The user, or their data from this provider, started being deleted
		// between the check and the registration: the file just written is
		// removed again.
		if derr := h.d.Raw.DeletePrefix(context.WithoutCancel(ctx), key); derr != nil {
			slog.WarnContext(ctx, "collect: could not remove the file of a refused part",
				"tenant_id", q.tenant, "user_id", q.user, "error", derr)
		}
		return UploadResponse{}, 0, err
	}
	if err != nil {
		return UploadResponse{}, 0, err
	}
	if !inserted {
		// A concurrent request registered this key or these bytes first.
		// The file just written is the same bytes under the key (confirmed by
		// putRaw) or, for a different key, an orphan the daily scan reports.
		resp, status, found, err := h.existing(ctx, q, key, hash)
		if err != nil {
			return UploadResponse{}, 0, err
		}
		if !found {
			return UploadResponse{}, 0, errors.New("raw object conflict but no row found")
		}
		return resp, status, nil
	}
	slog.InfoContext(ctx, "collect: part stored",
		"tenant_id", q.tenant, "user_id", q.user, "provider", h.provider, "key", key, "bytes", len(body))
	return UploadResponse{Key: key}, http.StatusOK, nil
}

// existing looks for a registered raw object with this key or these bytes.
// Same key, same bytes, or other key, same bytes: a duplicate (200). Same key,
// other bytes: 409.
func (h *uploadHandler) existing(ctx context.Context, q uploadReq, key, hash string) (UploadResponse, int, bool, error) {
	m, found, err := findRaw(ctx, h.d.Pool, q.tenant, q.user, key, hash)
	if err != nil || !found {
		return UploadResponse{}, 0, false, err
	}
	if m.otherBytes {
		return UploadResponse{}, http.StatusConflict, true, nil
	}
	return UploadResponse{Key: m.key, Duplicate: true}, http.StatusOK, true, nil
}
