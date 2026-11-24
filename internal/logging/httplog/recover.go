package httplog

import (
	"net/http"
	"runtime/debug"

	"github.com/rs/zerolog"
)

// recoverMiddleware turns a panic into one structured error line and a clean
// 500.
//
// Without it, net/http recovers the panic itself and prints an unstructured,
// uncorrelated stack through log.Default(), which says nothing about which
// route, which request or which member was being served.
//
// It sits INSIDE the access middleware on purpose: it returns normally after
// writing the 500, so the access handler observes a real status and logs it,
// rather than being skipped by an unwinding stack.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// A client that hangs up mid-response makes net/http panic with
			// ErrAbortHandler, and that is not an application fault. Re-panic
			// so net/http handles it as it always has.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			zerolog.Ctx(r.Context()).Error().
				Interface("panic", rec).
				Bytes("stack", debug.Stack()).
				Msg("http_panic")
			w.WriteHeader(http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}
