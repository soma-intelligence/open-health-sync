// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// The hosted connection page. A tenant's server creates a connect session for
// one user (CreateConnectSession) and sends the user to the link it gets back.
// The page lists the providers the session allows with the user's connection
// status, and each button posts to ConnectStart, which starts the same OAuth
// flow as StartConnection. The random token in the link is the page's only
// credential: only its hash is stored, and it serves several providers until
// it expires.

// Connect session limits.
const (
	// DefaultConnectSessionTTL is how long a connect session lives when the
	// request does not say.
	DefaultConnectSessionTTL = 30 * time.Minute
	minConnectTTLSeconds     = 60
	maxConnectTTLSeconds     = 3600
	maxAppName               = 80
	maxLogoURL               = 2048
	maxConnectProviders      = 64
	// maxConnectForm caps the body of POST /connect/{provider}.
	maxConnectForm = 4 << 10
	// connectCookie carries the session token to the OAuth callback (path
	// /oauth/ only), so the result page can link back to the connection
	// page when the session has no redirect_uri.
	connectCookie = "ohs_connect"
)

var connectPage = template.Must(template.ParseFS(pageFS, "page/connect.html", "page/frame.html"))

// ConnectSessionRequest is the CreateConnectSession body. Every field is
// optional.
type ConnectSessionRequest struct {
	RedirectURI string   `json:"redirect_uri,omitempty"`
	Providers   []string `json:"providers,omitempty"`
	AppName     string   `json:"app_name,omitempty"`
	LogoURL     string   `json:"logo_url,omitempty"`
	TTLSeconds  *int     `json:"ttl_seconds,omitempty"`
}

// ConnectSessionResponse is the CreateConnectSession answer.
type ConnectSessionResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// connectSession is one ohs_core.connect_sessions row.
type connectSession struct {
	tenant, user uuid.UUID
	providers    []string
	redirect     string
	appName      string
	logoURL      string
	expiresAt    time.Time
}

// ConnectURL is the hosted connection page of a session token.
func (d Deps) ConnectURL(token string) string {
	return d.publicBase() + "/connect?session=" + url.QueryEscape(token)
}

// oauthSlugs are the OAuth providers a tenant can connect on this instance:
// Deps.Slugs (every registered connector when nil) that the instance's keys
// or the tenant's own run as an OAuth connector (forTenant), less those
// turned off for the tenant.
func (d Deps) oauthSlugs(tenantID uuid.UUID) []string {
	cands := d.Slugs
	if cands == nil {
		for _, c := range connector.All() {
			cands = append(cands, c.Slug())
		}
	}
	var out []string
	for _, s := range cands {
		if _, _, ok := d.forTenant(tenantID, s); ok && d.allowed(tenantID, s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// validLogo accepts an absolute https URL without credentials.
func validLogo(s string) bool {
	if len(s) > maxLogoURL {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// newConnectSession checks a request of the tenant and returns the session
// it describes (without tenant and user), or the status and problem detail
// of the refusal: 403 for a provider turned off for the tenant, 400
// otherwise. The status is 0 when the request is accepted.
func (d Deps) newConnectSession(tenantID uuid.UUID, req ConnectSessionRequest, now time.Time) (connectSession, int, string) {
	var s connectSession
	bad := func(detail string) (connectSession, int, string) { return s, http.StatusBadRequest, detail }
	if req.RedirectURI != "" && !validRedirect(req.RedirectURI) {
		return bad("redirect_uri must be an absolute https URL (http only for localhost)")
	}
	s.redirect = req.RedirectURI

	s.appName = strings.TrimSpace(req.AppName)
	if utf8.RuneCountInString(s.appName) > maxAppName || !utf8.ValidString(s.appName) {
		return bad(fmt.Sprintf("app_name must be at most %d characters", maxAppName))
	}
	if strings.ContainsFunc(s.appName, unicode.IsControl) {
		return bad("app_name must not contain control characters")
	}

	if req.LogoURL != "" && !validLogo(req.LogoURL) {
		return bad(fmt.Sprintf("logo_url must be an absolute https URL of at most %d characters", maxLogoURL))
	}
	s.logoURL = req.LogoURL

	ttl := DefaultConnectSessionTTL
	if req.TTLSeconds != nil {
		if *req.TTLSeconds < minConnectTTLSeconds || *req.TTLSeconds > maxConnectTTLSeconds {
			return bad(fmt.Sprintf("ttl_seconds must be between %d and %d", minConnectTTLSeconds, maxConnectTTLSeconds))
		}
		ttl = time.Duration(*req.TTLSeconds) * time.Second
	}
	s.expiresAt = now.Add(ttl).UTC().Truncate(time.Second)

	if len(req.Providers) > maxConnectProviders {
		return bad(fmt.Sprintf("providers lists at most %d providers", maxConnectProviders))
	}
	for _, p := range req.Providers {
		if _, _, ok := d.forTenant(tenantID, p); !ok {
			if len(p) > 64 {
				p = p[:64]
			}
			return bad(fmt.Sprintf("providers: %q is not an OAuth provider enabled on this instance", strings.ToValidUTF8(p, "?")))
		}
		if !d.allowed(tenantID, p) {
			return s, http.StatusForbidden, fmt.Sprintf("providers: %q: %s", p, ProviderOffDetail)
		}
		if !slices.Contains(s.providers, p) {
			s.providers = append(s.providers, p)
		}
	}
	if len(s.providers) == 0 {
		s.providers = d.oauthSlugs(tenantID)
	}
	if len(s.providers) == 0 {
		return bad("no OAuth provider is enabled on this instance for this tenant")
	}
	return s, 0, ""
}

// CreateConnectSession serves POST /v1/users/{id}/connect-sessions. Mount
// behind auth.Middleware with auth.TenantKey. Body: redirect_uri, providers,
// app_name, logo_url, ttl_seconds, all optional. Answers 201 with the page URL
// and its expiry.
func (h *Handlers) CreateConnectSession() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID, ok := h.caller(w, r)
		if !ok {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStartBody))
		if err != nil {
			auth.WriteProblem(w, http.StatusBadRequest, "Bad Request", "the body could not be read")
			return
		}
		var req ConnectSessionRequest
		if len(strings.TrimSpace(string(body))) > 0 {
			dec := json.NewDecoder(strings.NewReader(string(body)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil || dec.More() {
				auth.WriteProblem(w, http.StatusBadRequest, "Bad Request",
					"the body must be a JSON object with redirect_uri, providers, app_name, logo_url or ttl_seconds")
				return
			}
		}
		sess, code, problem := h.d.newConnectSession(tenantID, req, h.d.now())
		if code != 0 {
			auth.WriteProblem(w, code, http.StatusText(code), problem)
			return
		}
		token, err := newState()
		if err != nil {
			slog.ErrorContext(r.Context(), "collect: connect session token", "error", err)
			writeInternal(w)
			return
		}
		if _, err := h.d.Pool.Exec(r.Context(), `
			INSERT INTO ohs_core.connect_sessions (token_hash, tenant_id, user_id, providers, redirect_uri,
				app_name, logo_url, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			hashState(token), tenantID, userID, sess.providers, nullable(sess.redirect),
			nullable(sess.appName), nullable(sess.logoURL), sess.expiresAt); err != nil {
			slog.ErrorContext(r.Context(), "collect: store connect session", "tenant_id", tenantID, "user_id", userID, "error", err)
			writeInternal(w)
			return
		}
		writeJSON(w, http.StatusCreated, ConnectSessionResponse{URL: h.d.ConnectURL(token), ExpiresAt: sess.expiresAt})
	})
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// sessionLookup is what loadConnectSession found.
type sessionLookup int

const (
	sessionValid sessionLookup = iota
	sessionUnknown
	sessionExpired
)

// tokenShape is the shape of a token newState mints.
var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// loadConnectSession finds the session of a token. A session whose user no
// longer exists or is being deleted is unknown.
func (h *Handlers) loadConnectSession(ctx context.Context, token string) (connectSession, sessionLookup, error) {
	var s connectSession
	if !tokenShape.MatchString(token) {
		return s, sessionUnknown, nil
	}
	err := h.d.Pool.QueryRow(ctx, `
		SELECT tenant_id, user_id, providers, coalesce(redirect_uri, ''), coalesce(app_name, ''),
			coalesce(logo_url, ''), expires_at
		FROM ohs_core.connect_sessions WHERE token_hash = $1`, hashState(token)).
		Scan(&s.tenant, &s.user, &s.providers, &s.redirect, &s.appName, &s.logoURL, &s.expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, sessionUnknown, nil
	}
	if err != nil {
		return s, sessionUnknown, fmt.Errorf("load connect session: %w", err)
	}
	if !h.d.now().Before(s.expiresAt) {
		return s, sessionExpired, nil
	}
	u, err := h.d.Tenants.GetUser(ctx, s.tenant, s.user)
	if errors.Is(err, tenant.ErrNotFound) || (err == nil && u.Status != "active") {
		return s, sessionUnknown, nil
	}
	if err != nil {
		return s, sessionUnknown, fmt.Errorf("load connect session user: %w", err)
	}
	return s, sessionValid, nil
}

// session loads the session of token and, when it is not usable, answers
// with the matching page. ok is false when an answer was written.
func (h *Handlers) session(w http.ResponseWriter, r *http.Request, token string) (connectSession, bool) {
	s, found, err := h.loadConnectSession(r.Context(), token)
	switch {
	case err != nil:
		slog.ErrorContext(r.Context(), "collect: connect page", "error", err)
		renderConnectProblem(w, http.StatusInternalServerError, "Something went wrong",
			"Try again in a moment.")
		return s, false
	case found == sessionExpired:
		renderConnectProblem(w, http.StatusGone, "This link has expired",
			"Go back to the app and open the connection page again.")
		return s, false
	case found != sessionValid:
		renderConnectProblem(w, http.StatusNotFound, "This link is not valid",
			"Go back to the app and open the connection page again.")
		return s, false
	}
	return s, true
}

// sessionProviders returns the session's providers still enabled as OAuth
// providers and not turned off for its tenant, or only provider when it is
// one of them. found is false when provider is not one of them; off tells
// that it is, but turned off for the tenant.
func (h *Handlers) sessionProviders(s connectSession, provider string) (slugs []string, found, off bool) {
	for _, p := range s.providers {
		if provider != "" && p != provider {
			continue
		}
		if _, _, ok := h.d.forTenant(s.tenant, p); !ok {
			continue
		}
		if !h.d.allowed(s.tenant, p) {
			off = off || p == provider
			continue
		}
		slugs = append(slugs, p)
	}
	return slugs, provider == "" || len(slugs) == 1, off
}

// renderSourceRefused renders the page of a provider the session cannot
// connect: turned off for the tenant (403), or not one of the session's
// (404).
func renderSourceRefused(w http.ResponseWriter, off bool) {
	if off {
		renderConnectProblem(w, http.StatusForbidden, "This source is turned off",
			"This app has turned it off. Go back to the app and pick another source.")
		return
	}
	renderConnectProblem(w, http.StatusNotFound, "This source is not available",
		"Go back to the app and open the connection page again.")
}

// providerView is one provider row of the page.
type providerView struct {
	Slug, Name         string
	Initial            string // the first letter of Name, on the row's tile
	Detail             string // what the source shares, when known
	Status, StatusText string
	Button             string
	Action             string
}

type connectView struct {
	Problem, Detail string
	AppName         string
	LogoURL         string
	Session         string
	Single          bool
	Expires         string
	ExpiresMinutes  int // the minutes left, at least 1, for the page's large figure
	Providers       []providerView
	Connected       int // providers the user has an active connection to
	// PasswordSources names the listed providers the user signs in to on
	// this server's page (connector.PasswordConnector), for the trust line.
	PasswordSources string
}

// ConnectPage serves GET /connect?session=T and GET /connect/{provider}?session=T,
// with no bearer: the session token is the credential. It renders the
// session's providers (or the one in the path) with the user's connection
// status, each with a button that posts to ConnectStart. An unknown token is
// 404 and an expired one 410, as a page that says nothing more.
func (h *Handlers) ConnectPage() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("session")
		provider := r.PathValue("provider")
		s, ok := h.session(w, r, token)
		if !ok {
			return
		}
		slugs, found, off := h.sessionProviders(s, provider)
		if !found {
			renderSourceRefused(w, off)
			return
		}
		status, err := h.connectionStatuses(r.Context(), s.tenant, s.user, slugs)
		if err != nil {
			slog.ErrorContext(r.Context(), "collect: connect page", "tenant_id", s.tenant, "user_id", s.user, "error", err)
			renderConnectProblem(w, http.StatusInternalServerError, "Something went wrong", "Try again in a moment.")
			return
		}
		v := connectView{
			AppName: s.appName, LogoURL: s.logoURL, Session: token, Single: provider != "",
			Expires: expiresIn(s.expiresAt.Sub(h.d.now())), ExpiresMinutes: minutesLeft(s.expiresAt.Sub(h.d.now())),
		}
		var passwords []string
		for _, p := range slugs {
			pv := providerView{Slug: p, Name: providerName(p), Detail: providerDetails[p]}
			if r, _ := utf8.DecodeRuneInString(pv.Name); r != utf8.RuneError {
				pv.Initial = string(unicode.ToUpper(r))
			}
			// The form posts to /connect/{provider}, relative to the page so
			// an instance served under a path prefix works too.
			if provider == "" {
				pv.Action = "connect/" + url.PathEscape(p)
			} else {
				pv.Action = url.PathEscape(p)
			}
			switch status[p] {
			case StatusActive:
				pv.Status, pv.StatusText, pv.Button = "connected", "Connected", "Reconnect"
				v.Connected++
			case StatusNeedsReauth:
				pv.Status, pv.StatusText, pv.Button = "needs_reauth", "Needs reconnecting", "Reconnect"
			default:
				pv.Status, pv.StatusText, pv.Button = "not_connected", "Not connected", "Connect"
			}
			v.Providers = append(v.Providers, pv)
			if oc, ok := h.d.capableOAuth(p); ok {
				if _, pw := connector.AsPassword(oc); pw {
					passwords = append(passwords, pv.Name)
				}
			}
		}
		v.PasswordSources = joinNames(passwords)
		setPageHeaders(w, h.pageCSP(s.tenant, slugs))
		w.WriteHeader(http.StatusOK)
		if err := connectPage.Execute(w, v); err != nil {
			slog.ErrorContext(r.Context(), "collect: render connect page", "error", err)
		}
	})
}

// ConnectStart serves POST /connect/{provider} with the form field session.
// It checks the session and the provider, mints an OAuth state exactly as
// StartConnection does (the session's redirect_uri is the redirect), and
// answers 303 to the provider's consent URL. The session stays usable.
func (h *Handlers) ConnectStart() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxConnectForm)
		if err := r.ParseForm(); err != nil {
			renderConnectProblem(w, http.StatusBadRequest, "This request could not be read",
				"Go back to the app and open the connection page again.")
			return
		}
		token := r.PostForm.Get("session")
		provider := r.PathValue("provider")
		s, ok := h.session(w, r, token)
		if !ok {
			return
		}
		if _, found, off := h.sessionProviders(s, provider); !found {
			renderSourceRefused(w, off)
			return
		}
		oc, keys, _ := h.d.forTenant(s.tenant, provider)
		authorize, err := h.authorize(r.Context(), s.tenant, s.user, provider, oc, keys, s.redirect)
		if err != nil {
			renderConnectProblem(w, http.StatusInternalServerError, "Something went wrong", "Try again in a moment.")
			return
		}
		if s.redirect == "" {
			http.SetCookie(w, h.backCookie(token, s.expiresAt))
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, authorize, http.StatusSeeOther)
	})
}

// backCookie carries the session token to the OAuth callback only, for the
// link back on the result page.
func (h *Handlers) backCookie(token string, expires time.Time) *http.Cookie {
	path := "/oauth/"
	secure := false
	if u, err := url.Parse(h.d.publicBase()); err == nil {
		path = strings.TrimRight(u.Path, "/") + "/oauth/"
		secure = u.Scheme == "https"
	}
	return &http.Cookie{
		Name: connectCookie, Value: token, Path: path, Expires: expires,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	}
}

// connectBackLink is the connection page to offer on the result page of a
// callback whose state had no redirect: the session in the cookie set by
// ConnectStart, when it is still valid and for the same user.
func (h *Handlers) connectBackLink(r *http.Request, st oauthState) string {
	c, err := r.Cookie(connectCookie)
	if err != nil {
		return ""
	}
	s, found, err := h.loadConnectSession(r.Context(), c.Value)
	if err != nil || found != sessionValid || s.tenant != st.tenant || s.user != st.user {
		return ""
	}
	return h.d.ConnectURL(c.Value)
}

// connectionStatuses maps each provider of slugs the user has a connection
// to onto its status.
func (h *Handlers) connectionStatuses(ctx context.Context, tenantID, userID uuid.UUID, slugs []string) (map[string]string, error) {
	rows, err := h.d.Pool.Query(ctx, `
		SELECT provider, status FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id = $2 AND provider = ANY($3)`, tenantID, userID, slugs)
	if err != nil {
		return nil, fmt.Errorf("connection statuses: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, s string
		if err := rows.Scan(&p, &s); err != nil {
			return nil, fmt.Errorf("connection statuses: %w", err)
		}
		out[p] = s
	}
	return out, rows.Err()
}

// cspSource matches a scheme://host[:port] safe to put in a CSP header.
var cspSource = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]+)?$`)

// pageCSP is the page's Content-Security-Policy. Browsers apply form-action
// to the redirects that follow a form submission too, so the origins of the
// providers' consent pages are listed next to 'self'.
func (h *Handlers) pageCSP(tenantID uuid.UUID, slugs []string) string {
	sources := []string{"'self'"}
	for _, p := range slugs {
		oc, _, ok := h.d.forTenant(tenantID, p)
		if !ok {
			continue
		}
		// A placeholder state: only the origin is kept.
		raw, err := oc.AuthorizeURL("csp", h.d.CallbackURL(p))
		if err != nil {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		origin := u.Scheme + "://" + u.Host
		if cspSource.MatchString(origin) && !slices.Contains(sources, origin) {
			sources = append(sources, origin)
		}
	}
	return "default-src 'none'; style-src 'unsafe-inline'; img-src https:; form-action " +
		strings.Join(sources, " ") + "; frame-ancestors 'none'; base-uri 'none'"
}

func setPageHeaders(w http.ResponseWriter, csp string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", csp)
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
}

// renderConnectProblem renders the page's error state: a title and one line,
// nothing about the session.
func renderConnectProblem(w http.ResponseWriter, code int, title, detail string) {
	setPageHeaders(w, "default-src 'none'; style-src 'unsafe-inline'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(code)
	_ = connectPage.Execute(w, connectView{Problem: title, Detail: detail})
}

// expiresIn says when the link expires, for the page footer.
func expiresIn(d time.Duration) string {
	m := minutesLeft(d)
	if m == 1 {
		return "in about a minute"
	}
	return fmt.Sprintf("in %d minutes", m)
}

// minutesLeft is d rounded up to whole minutes, at least 1.
func minutesLeft(d time.Duration) int {
	return max(1, int(math.Ceil(d.Minutes())))
}

// providerNames are the display names of the providers whose name is not
// their slug capitalized.
var providerNames = map[string]string{
	"activity_files": "Activity files",
	"apple_health":   "Apple Health",
	"bluetooth":      "Bluetooth devices",
	"dexcom":         "Dexcom",
	"eight_sleep":    "Eight Sleep",
	"fitbit":         "Fitbit",
	"garmin":         "Garmin",
	"garmin_connect": "Garmin (account)",
	"health_connect": "Health Connect",
	"intervals_icu":  "intervals.icu",
	"omron":          "OMRON",
	"oura":           "Oura",
	"polar":          "Polar",
	"self_report":    "Self reports",
	"strava":         "Strava",
	"suunto":         "Suunto",
	"ultrahuman":     "Ultrahuman",
	"whoop":          "WHOOP",
	"withings":       "Withings",
	"xiaomi":         "Xiaomi",
}

// ProviderName is how the pages and the console name a provider: its display
// name when known, else its slug.
func ProviderName(slug string) string { return providerName(slug) }

// providerDetails say on the connection page what each source shares, from
// the data its connector reads.
var providerDetails = map[string]string{
	"dexcom":         "Continuous glucose readings",
	"eight_sleep":    "Sleep stages, heart rate, HRV, breathing and bed temperature",
	"fitbit":         "Sleep, activity, workouts and heart rate",
	"garmin":         "Daily activity, sleep, heart rate, HRV and workouts",
	"garmin_connect": "Sleep, heart rate, HRV, stress, steps and activities from your Garmin account",
	"intervals_icu":  "Workouts, HRV, sleep score and weight",
	"omron":          "Blood pressure, heart rate and weight",
	"oura":           "Sleep, readiness, activity and heart rate",
	"polar":          "Workouts, sleep, activity and heart rate",
	"strava":         "Workouts with distance, pace and heart rate",
	"suunto":         "Workouts with distance, pace and heart rate",
	"ultrahuman":     "Sleep, heart rate, HRV, steps and temperature",
	"whoop":          "Recovery, sleep, strain and workouts",
	"withings":       "Weight, body composition, blood pressure and sleep",
}

// joinNames lists names in prose: "A", "A and B", "A, B and C".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// providerName is how the pages name a provider: its display name when
// known, else its slug.
func providerName(slug string) string {
	if n, ok := providerNames[slug]; ok {
		return n
	}
	return slug
}
