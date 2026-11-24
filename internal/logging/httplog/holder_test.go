package httplog

import (
	"context"
	"sync"
	"testing"
)

func TestSetUserIDIsVisibleToTheParentContext(t *testing.T) {
	// This is the point of the whole holder: the middleware holds the PARENT
	// context, auth writes from a CHILD, and the parent must see it.
	parent, h := withHolder(context.Background())
	child := context.WithValue(parent, struct{ k string }{"unrelated"}, 1)

	SetUserID(child, "user-1")

	if got := h.userID(); got != "user-1" {
		t.Fatalf("userID = %q, want user-1", got)
	}
}

func TestSetUserIDIsANoOpWithoutTheMiddleware(t *testing.T) {
	// A module that never mounts the chain must not panic when its auth calls
	// SetUserID. It degrades to an empty user_id on a line nobody writes.
	SetUserID(context.Background(), "user-1")
}

func TestHolderIsRaceFree(t *testing.T) {
	// Run with -race. A handler may spawn goroutines that outlive the write,
	// and the access line reads the holder after ServeHTTP returns.
	ctx, h := withHolder(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); SetUserID(ctx, "user-1") }()
		go func() { defer wg.Done(); _ = h.userID() }()
	}
	wg.Wait()
}
