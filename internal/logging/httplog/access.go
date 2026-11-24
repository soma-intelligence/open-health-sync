package httplog

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// routeUnmatched labels a request that hit no registered pattern. It keeps the
// raw path out of both the log and the metric labels: an unmatched path is
// attacker-controlled, so using it would let anyone create unbounded label
// cardinality and write arbitrary text into our logs.
const routeUnmatched = "unmatched"

// methodUnknown labels a request whose method is not one of the standard HTTP
// verbs. net/http accepts any RFC 7230 token as r.Method, so it is arbitrary
// client text, exactly like an unmatched path: without folding it, a client
// sending a fresh junk method on every request grows the Prometheus registry
// without bound. Applies to the metric label only; see metricMethod.
const methodUnknown = "unknown"

// standardMethods is the allowlist metricMethod folds against.
var standardMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodPost:    true,
	http.MethodPut:     true,
	http.MethodPatch:   true,
	http.MethodDelete:  true,
	http.MethodConnect: true,
	http.MethodOptions: true,
	http.MethodTrace:   true,
}

// metricMethod folds any method outside standardMethods to methodUnknown, so
// the "method" Prometheus label stays bounded. This must NOT be used for the
// access log line: a log line is not a label, and the real method is useful
// when investigating, so accessLine keeps r.Method as-is.
func metricMethod(method string) string {
	if standardMethods[method] {
		return method
	}
	return methodUnknown
}

// levelFor maps a status onto a level. A served request is not news, a client
// mistake is worth a line but not a look, a failed one is worth waking up for.
// 2xx and 3xx therefore sit at debug, which is silent under the default
// LOG_LEVEL, 4xx at info, and 5xx at error. A metrics hook, when one is
// installed, keeps counting every status.
func levelFor(status int) zerolog.Level {
	switch {
	case status >= 500:
		return zerolog.ErrorLevel
	case status >= 400:
		return zerolog.InfoLevel
	default:
		return zerolog.DebugLevel
	}
}

// routeOf returns the matched ServeMux pattern for r, or routeUnmatched.
//
// It calls mux.Handler(r) rather than reading r.Pattern, because ServeMux sets
// that field on the request it passes DOWNSTREAM: the middleware holds the
// parent and would always read an empty string. The cost is one extra trie
// lookup per request.
func routeOf(mux *http.ServeMux, r *http.Request) string {
	if mux == nil {
		return routeUnmatched
	}
	if _, pattern := mux.Handler(r); pattern != "" {
		return pattern
	}
	return routeUnmatched
}

// clientIP returns the first hop of X-Forwarded-For when present, since Caddy
// fronts the public door and RemoteAddr would otherwise always be the proxy.
// Falls back to RemoteAddr with its port stripped.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// duration_ms is a float in milliseconds with microsecond precision, so a
// sub-millisecond handler reads 0.412 rather than 0.
//
// accessLine emits the one line that describes a finished request.
//
// It does not add "module" itself: hlog.NewHandler already seeded logger with
// the module's own tagged copy (see Middleware), so adding it again here would
// write the field twice on every listener. The real HTTP method goes in the
// log line even where the metric folds it: a log line is not a label, and the
// real value is useful when investigating a junk method (see metricMethod).
func accessLine(
	logger *zerolog.Logger,
	route string,
	r *http.Request,
	status, size int,
	d time.Duration,
) {
	var user string
	if h, ok := holderFrom(r.Context()); ok {
		user = h.userID()
	}
	logger.WithLevel(levelFor(status)).
		Str("method", r.Method).
		Str("route", route).
		Int("status", status).
		Float64("duration_ms", float64(d.Microseconds())/1000).
		Int("bytes", size).
		Str("user_id", user).
		Str("remote_ip", clientIP(r)).
		Str("user_agent", r.UserAgent()).
		Msg("http_request")
}
