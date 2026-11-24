// Package httplog is the one HTTP middleware chain: a request id, an access
// line whose level follows the status, and panic recovery. Metrics are an
// optional hook (see Option), so the shared files import only the standard
// library and zerolog.
package httplog

import (
	"context"
	"sync"
)

// holder carries what the access line needs but cannot reach.
//
// The access middleware wraps the whole mux, so it holds the PARENT context.
// Routing and authentication both produce their results on CHILD contexts:
// ServeMux sets r.Pattern on the request it passes downstream, and
// RequireAuth calls context.WithValue per route. Neither is visible from the
// parent. A pointer placed in the parent context and written through from a
// child is, which is what this is.
type holder struct {
	mu   sync.Mutex
	user string
}

type holderKey struct{}

// withHolder returns a context carrying a fresh holder, and the holder itself
// so the middleware can read it back after the handler returns.
func withHolder(ctx context.Context) (context.Context, *holder) {
	h := &holder{}
	return context.WithValue(ctx, holderKey{}, h), h
}

func holderFrom(ctx context.Context) (*holder, bool) {
	h, ok := ctx.Value(holderKey{}).(*holder)
	return h, ok
}

// SetUserID records the authenticated subject for the access line. Each
// module's auth middleware calls it once it has resolved the caller.
//
// It is a no-op when the chain is not mounted, so a module can call it before
// its listener is wired without a nil check and without a panic.
func SetUserID(ctx context.Context, id string) {
	h, ok := holderFrom(ctx)
	if !ok {
		return
	}
	h.mu.Lock()
	h.user = id
	h.mu.Unlock()
}

// userID reads the subject back. Guarded because a handler may spawn
// goroutines that write after the access middleware has begun reading.
func (h *holder) userID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.user
}
