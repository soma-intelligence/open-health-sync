package httplog

import (
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
)

// Middleware returns the one HTTP chain of the monolith, for the named module.
//
// Order, outermost first:
//
//	hlog.NewHandler      put the module logger on the request context
//	  requestIDMiddleware  read or mint the id, tag the logger, echo the header
//	    holderMiddleware   install the pointer each module's auth writes through
//	      hlog.AccessHandler wrap the writer, time it, emit the line, count it
//	        recoverMiddleware turn a panic into a 500 the access handler sees
//	          next             the mux
//
// hlog.AccessHandler does the ResponseWriter wrapping, and that is not a detail
// to reimplement: its mutil.WrapWriter re-exposes http.Flusher, http.Hijacker
// and io.ReaderFrom on the wrapped writer. A hand-rolled wrapper answers false
// to the first two, which silently breaks text/event-stream responses and
// WebSocket upgraders.
func Middleware(logger zerolog.Logger, module string, opts ...Option) func(http.Handler) http.Handler {
	cfg := config{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return func(next http.Handler) http.Handler {
		// Resolved once, at wrap time. A handler that is not a ServeMux has no
		// pattern to report and every one of its requests is labelled
		// routeUnmatched.
		mux, _ := next.(*http.ServeMux)

		access := hlog.AccessHandler(func(r *http.Request, status, size int, d time.Duration) {
			// mutil.WrapWriter reports 0, never 200, for a handler that calls
			// neither WriteHeader nor Write: the implicit 200 net/http sends
			// for a silent handler is written on the real ResponseWriter
			// after this callback has already run, so the wrapper never sees
			// it. 0 is not a status any client receives, so it is always the
			// implicit 200 and is recorded as such.
			if status == 0 {
				status = http.StatusOK
			}
			route := routeOf(mux, r)
			if cfg.observe != nil {
				cfg.observe(module, metricMethod(r.Method), route, status, d)
			}
			accessLine(zerolog.Ctx(r.Context()), route, r, status, size, d)
		})

		h := recoverMiddleware(next)
		h = access(h)
		h = holderMiddleware(h)
		h = requestIDMiddleware(h)
		h = hlog.NewHandler(logger)(h)

		return inFlightMiddleware(cfg.inFlight, module, h)
	}
}

// Option customises Middleware. The shared package knows nothing about any
// metrics library: a repo that wants counters supplies the hooks, see
// WithMetrics in the canonical repo's metrics.go.
type Option func(*config)

// config holds the optional hooks. Both are nil-safe: Middleware never calls
// a hook that is nil.
type config struct {
	// observe is called once per finished request with the folded method, the
	// matched route (never the raw path) and the status.
	observe func(module, method, route string, status int, d time.Duration)
	// inFlight is called with +1 when a request starts and -1 when it ends.
	inFlight func(module string, delta float64)
}

// requestIDMiddleware reuses a valid incoming X-Request-Id or mints one, tags
// the context logger with it, and echoes it on the response.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestIDFromRequest(r)
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}

// holderMiddleware installs the pointer that each module's auth writes the
// authenticated subject through. See holder.go for why a pointer is needed.
func holderMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := withHolder(r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// inFlightMiddleware is outermost so the gauge covers the whole chain, panics
// included. With no hook it is a pass-through.
func inFlightMiddleware(inFlight func(string, float64), module string, next http.Handler) http.Handler {
	if inFlight == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight(module, 1)
		defer inFlight(module, -1)
		next.ServeHTTP(w, r)
	})
}
