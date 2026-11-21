// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/shl"
)

// maxManifestBody caps a manifest request: a name, a passcode, a number.
const maxManifestBody = 4 << 10

// mountShares mounts the share link routes: the management routes behind a
// tenant key, and the public manifest, file and viewer routes, which take no
// credential (the manifest id and the ticket are the secrets) and are rate
// limited per client address in a bucket of their own.
//
// The create route takes no Idempotency-Key: its response holds the link's
// key, which a stored response would keep in the database.
func (s *server) mountShares(mux *http.ServeMux) {
	s.tenantRoute(mux, "POST /v1/users/{id}/shares", s.createShare)
	s.tenantRoute(mux, "GET /v1/users/{id}/shares", s.listShares)
	s.tenantRoute(mux, "GET /v1/users/{id}/shares/{share_id}", s.getShare)
	s.tenantRoute(mux, "DELETE /v1/users/{id}/shares/{share_id}", s.revokeShare)

	limit := s.liveLimiter(func(r RateLimits) (RateLimit, RateLimit) { return r.OAuth, RateLimit{} })
	mux.Handle("POST /shl/{manifest_id}", publicShare(s.rateLimit(groupShare, limit, nil, http.HandlerFunc(s.shareManifest))))
	mux.Handle("OPTIONS /shl/{manifest_id}", publicShare(http.HandlerFunc(sharePreflight)))
	mux.Handle("GET /shl/file/{ticket}", publicShare(s.rateLimit(groupShare, limit, nil, http.HandlerFunc(s.shareFile))))
	mux.Handle("/shl/{manifest_id}", shareMethod("POST, OPTIONS"))
	mux.Handle("/shl/file/{ticket}", shareMethod(http.MethodGet))
	shl.MountViewer(mux)
}

type createShareRequest struct {
	Categories []string `json:"categories"`
	Start      string   `json:"start"`
	End        string   `json:"end"`
	ExpiresAt  string   `json:"expires_at"`
	Passcode   *string  `json:"passcode"`
	Label      string   `json:"label"`
}

// createdShare is the answer to a create: the share, and its link, once.
type createdShare struct {
	shl.Share
	URI       string `json:"uri"`
	ViewerURL string `json:"viewer_url"`
}

// createShare makes a share link: it reads the user's records of the
// categories in the window (merged view), writes them as a FHIR Bundle into
// a DEFLATE stream, encrypts it with a fresh key, stores the ciphertext and
// answers the link. The key is in the link only.
func (s *server) createShare(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	var req createShareRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	cats, err := shl.CheckCategories(req.Categories)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	var start, end, exp time.Time
	for _, f := range []struct {
		name, v string
		dst     *time.Time
	}{{"start", req.Start, &start}, {"end", req.End, &end}, {"expires_at", req.ExpiresAt, &exp}} {
		if f.v == "" {
			badRequest(w, f.name+" is required")
			return
		}
		if *f.dst, err = parseTime(f.v); err != nil {
			badRequest(w, f.name+": "+err.Error())
			return
		}
	}
	now := time.Now()
	for _, err := range []error{shl.CheckWindow(start, end), shl.CheckExpiry(now, exp), shl.CheckLabel(req.Label)} {
		if err != nil {
			badRequest(w, err.Error())
			return
		}
	}
	if req.Passcode != nil {
		if err := shl.CheckPasscodeRules(*req.Passcode); err != nil {
			badRequest(w, err.Error())
			return
		}
	}
	ctx := r.Context()
	q := read.Query{Start: start, End: end, Limit: read.MaxLimit}
	s.mergePolicy(r, p.TenantID, user, &q)
	// The records are read in the transaction that stores the share, which
	// holds the user and their consent: an erasure or a consent change waits
	// for it, then revokes it.
	tx, err := shl.Begin(ctx, s.d.Pool, p.TenantID, user)
	if !shareErr(w, r, err, "api: create share") {
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	types, err := shl.QuantityTypes(ctx, tx, p.TenantID, user, start, end)
	if err != nil {
		internalError(w, r, "api: create share", err)
		return
	}
	refused, err := shl.Refused(ctx, tx, p.TenantID, user)
	if err != nil {
		internalError(w, r, "api: create share", err)
		return
	}
	erasing, err := shl.Erasing(ctx, tx, p.TenantID, user)
	if err != nil {
		internalError(w, r, "api: create share", err)
		return
	}
	key, err := shl.NewKey()
	if err != nil {
		internalError(w, r, "api: create share", err)
		return
	}
	defer clear(key)

	var file bytes.Buffer
	d := shl.NewDeflater(&file)
	snap, err := shl.NewSnapshot(d, shl.SnapshotOptions{User: user, Base: s.fhirBase(r, user),
		At: now.UTC().Truncate(time.Second), Categories: cats,
		Skip: func(provider, category string) bool {
			return erasing[provider] || refused[shl.Source(provider, category)]
		}})
	if err != nil {
		internalError(w, r, "api: create share", err)
		return
	}
	f := shl.Filter(cats, types)
	for {
		pg, err := read.Observations(ctx, tx, p.TenantID, user, q, f)
		if err != nil {
			readError(w, r, err, "user")
			return
		}
		for _, rec := range pg.Records {
			if err := snap.Add(rec); errors.Is(err, shl.ErrTooLarge) {
				badRequest(w, "the share would hold more than "+strconv.Itoa(shl.MaxObservations)+
					" records: narrow the time window or the categories")
				return
			} else if err != nil {
				internalError(w, r, "api: create share", err)
				return
			}
		}
		if pg.NextCursor == "" {
			break
		}
		q.Cursor = pg.NextCursor
	}
	if err := snap.Close(); err != nil {
		internalError(w, r, "api: create share", err)
		return
	}
	if err := d.Close(); err != nil {
		internalError(w, r, "api: create share", err)
		return
	}
	jwe, err := shl.EncryptDeflated(key, file.Bytes(), shl.ContentFHIR)
	if err != nil {
		internalError(w, r, "api: create share", err)
		return
	}
	manifestID, err := shl.NewSecret()
	if err != nil {
		internalError(w, r, "api: create share", err)
		return
	}
	n := shl.NewShare{Label: req.Label, Categories: cats, Start: start, End: end, ExpiresAt: exp,
		ManifestHash: shl.HashSecret(manifestID), JWE: jwe, Observations: snap.Observations(), Sources: snap.Sources()}
	if req.Passcode != nil {
		if n.PasscodeHash, err = shl.HashPasscode(*req.Passcode); err != nil {
			internalError(w, r, "api: create share", err)
			return
		}
	}
	sh, err := shl.Create(ctx, tx, p.TenantID, user, n)
	if !shareErr(w, r, err, "api: create share") {
		return
	}
	base := s.publicBase(r)
	payload := shl.Payload{URL: base + "/shl/" + manifestID, Key: shl.EncodeKey(key), Exp: exp.Unix(), Label: req.Label}
	if req.Passcode != nil {
		payload.Flag = shl.FlagPasscode
	}
	uri, err := payload.URI()
	if err != nil {
		internalError(w, r, "api: create share", err)
		return
	}
	w.Header().Set("Location", "/v1/users/"+user.String()+"/shares/"+sh.ID.String())
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, createdShare{Share: sh, URI: uri, ViewerURL: shl.ViewerURL(base+"/shl/view", uri)})
}

func (s *server) listShares(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	pg, err := shl.List(r.Context(), s.d.Pool, p.TenantID, user, shl.Query{Cursor: r.URL.Query().Get("cursor"), Limit: limit})
	if !shareErr(w, r, err, "api: list shares") {
		return
	}
	writeJSON(w, http.StatusOK, newPage(pg.Records, pg.NextCursor))
}

// pathShare parses {id} and {share_id}; a malformed share id is a share
// that does not exist.
func pathShare(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, uuid.UUID, bool) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return p, uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue("share_id"))
	if err != nil {
		notFound(w, "share")
		return p, uuid.Nil, uuid.Nil, false
	}
	return p, user, id, true
}

func (s *server) getShare(w http.ResponseWriter, r *http.Request) {
	p, user, id, ok := pathShare(w, r)
	if !ok {
		return
	}
	d, err := shl.Get(r.Context(), s.d.Pool, p.TenantID, user, id)
	if !shareErr(w, r, err, "api: get share") {
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// revokeShare stops a share and removes its file. It is idempotent.
func (s *server) revokeShare(w http.ResponseWriter, r *http.Request) {
	p, user, id, ok := pathShare(w, r)
	if !ok {
		return
	}
	if !shareErr(w, r, shl.Revoke(r.Context(), s.d.Pool, p.TenantID, user, id), "api: revoke share") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// shareErr writes the problem for err and returns false; it returns true
// when err is nil.
func shareErr(w http.ResponseWriter, r *http.Request, err error, msg string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, shl.ErrUserNotFound):
		notFound(w, "user")
	case errors.Is(err, shl.ErrNotFound):
		notFound(w, "share")
	case errors.Is(err, shl.ErrInvalidCursor):
		badRequest(w, "the cursor is not valid")
	default:
		internalError(w, r, msg, err)
	}
	return false
}

// publicShare sets the headers of every public share response before the
// rate limiter runs, so a 429 is readable from another origin too: any
// origin may read the answers (no cookie or credential is involved),
// nothing is cached, and no Referer leaves.
func publicShare(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Expose-Headers", "Retry-After, X-Request-Id")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// sharePreflight answers the CORS preflight of a manifest request from a
// viewer on another origin.
func sharePreflight(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Methods", "POST")
	h.Set("Access-Control-Allow-Headers", "Content-Type")
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// shareBusy is the answer when the share or its user stayed locked past
// shl.LockTimeout.
func shareBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(shl.LockTimeout/time.Second)))
	problem(w, http.StatusServiceUnavailable, "this share link is busy, retry later")
}

// shareMethod answers a public share path called with a method it does not
// take, with the public headers like every other answer there.
func shareMethod(allow string) http.Handler {
	return publicShare(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		problem(w, http.StatusMethodNotAllowed, "this route takes "+allow)
	}))
}

// inactiveShare is the one answer for a link that is unknown, revoked,
// disabled or expired: they cannot be told apart.
func inactiveShare(w http.ResponseWriter) {
	problem(w, http.StatusNotFound, "this share link is not active")
}

// notInName reports a character a reader's name may not hold: a control
// character, a line or paragraph separator, or a bidirectional embedding,
// override or isolate, which would make the name show as other text in
// the owner's app. Joiners and marks that scripts need stay allowed.
func notInName(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) ||
		(r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// manifestRequest is the body of a manifest request. Unknown members are
// ignored: the IG may add some.
type manifestRequest struct {
	Recipient         *string `json:"recipient"`
	Passcode          *string `json:"passcode"`
	EmbeddedLengthMax *int    `json:"embeddedLengthMax"`
}

// manifestFile is one file of a manifest.
type manifestFile struct {
	ContentType string `json:"contentType"`
	Embedded    string `json:"embedded,omitempty"`
	Location    string `json:"location,omitempty"`
	LastUpdated string `json:"lastUpdated"`
}

type manifest struct {
	Status string         `json:"status"`
	Files  []manifestFile `json:"files"`
}

// passcodeProblem is the 401 of a wrong or missing passcode, with the
// attempts the share still takes.
type passcodeProblem struct {
	Type              string `json:"type"`
	Title             string `json:"title"`
	Status            int    `json:"status"`
	Detail            string `json:"detail"`
	RequestID         string `json:"request_id,omitempty"`
	RemainingAttempts int    `json:"remainingAttempts"`
}

// shareManifest answers the IG's manifest request: the file, embedded or
// at a single use location, a 401 for a wrong passcode, or a 404.
func (s *server) shareManifest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("manifest_id")
	if !shl.ValidSecret(id) {
		inactiveShare(w)
		return
	}
	var req manifestRequest
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxManifestBody)).Decode(&req)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		problem(w, http.StatusRequestEntityTooLarge, "the request body is too large")
		return
	case err != nil:
		badRequest(w, "the request body must be a JSON object with recipient")
		return
	}
	if req.Recipient == nil || strings.TrimSpace(*req.Recipient) == "" {
		badRequest(w, "recipient is required: the name of who opens the link")
		return
	}
	recipient := strings.TrimSpace(*req.Recipient)
	if !utf8.ValidString(recipient) || strings.IndexFunc(recipient, notInName) >= 0 ||
		utf8.RuneCountInString(recipient) > shl.MaxRecipient {
		badRequest(w, "recipient must be one line of at most "+strconv.Itoa(shl.MaxRecipient)+" characters")
		return
	}
	if req.EmbeddedLengthMax != nil && *req.EmbeddedLengthMax < 0 {
		badRequest(w, "embeddedLengthMax must not be negative")
		return
	}
	res, err := shl.OpenManifest(r.Context(), s.d.Pool, id,
		shl.Request{Recipient: recipient, Passcode: req.Passcode, EmbeddedLengthMax: req.EmbeddedLengthMax},
		shl.AuditInfo{Method: r.Method, Route: r.Pattern, RequestID: reqid.From(r.Context())})
	if errors.Is(err, shl.ErrBusy) {
		shareBusy(w)
		return
	}
	if err != nil {
		internalError(w, r, "api: share manifest", err)
		return
	}
	switch {
	case !res.Known || res.Outcome == shl.OutcomeInactive:
		inactiveShare(w)
	case res.Outcome == shl.OutcomeWrongPasscode:
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(passcodeProblem{Type: "about:blank", Title: http.StatusText(http.StatusUnauthorized),
			Status: http.StatusUnauthorized, Detail: "the passcode is wrong or missing",
			RequestID: w.Header().Get(reqid.Header), RemainingAttempts: res.Remaining})
	default:
		f := manifestFile{ContentType: shl.ContentFHIR, LastUpdated: res.LastUpdated.Format(time.RFC3339)}
		if res.Ticket != "" {
			f.Location = s.publicBase(r) + "/shl/file/" + res.Ticket
		} else {
			f.Embedded = res.JWE
		}
		writeJSON(w, http.StatusOK, manifest{Status: "finalized", Files: []manifestFile{f}})
	}
}

// shareFile serves the file of a manifest's location, once. HEAD is
// refused, so a link checker never spends the ticket.
func (s *server) shareFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		problem(w, http.StatusMethodNotAllowed, "this route takes GET")
		return
	}
	ticket := r.PathValue("ticket")
	if !shl.ValidSecret(ticket) {
		inactiveShare(w)
		return
	}
	res, err := shl.TakeFile(r.Context(), s.d.Pool, ticket,
		shl.AuditInfo{Method: r.Method, Route: r.Pattern, RequestID: reqid.From(r.Context())})
	if errors.Is(err, shl.ErrBusy) {
		shareBusy(w)
		return
	}
	if err != nil {
		internalError(w, r, "api: share file", err)
		return
	}
	if !res.Known || res.Outcome != shl.OutcomeFile {
		inactiveShare(w)
		return
	}
	w.Header().Set("Content-Type", "application/jose")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, res.JWE)
}
