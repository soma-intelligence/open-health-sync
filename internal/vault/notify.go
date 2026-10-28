// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// notifyChannel carries the evictions. A payload is "user:" tenant ":" user;
// anything else is ignored.
const notifyChannel = "ohs_vault"

const userPayloadPrefix = "user:"

func userPayload(tenant, user uuid.UUID) string {
	return userPayloadPrefix + tenant.String() + ":" + user.String()
}

func parseUserPayload(s string) (userRef, bool) {
	rest, ok := strings.CutPrefix(s, userPayloadPrefix)
	if !ok {
		return userRef{}, false
	}
	t, u, ok := strings.Cut(rest, ":")
	if !ok {
		return userRef{}, false
	}
	tenant, err1 := uuid.Parse(t)
	user, err2 := uuid.Parse(u)
	if err1 != nil || err2 != nil {
		return userRef{}, false
	}
	return userRef{tenant, user}, true
}

// Listen evicts from this Vault's cache the users another process shredded,
// and drops expired keys from memory, until ctx ends. It holds a connection
// of its own (not one of the pool's) and reconnects after a failure; since
// notifications may have been missed meanwhile, the whole user cache is
// dropped on every reconnect. Run it as a goroutine of the api and etl roles.
func (v *Vault) Listen(ctx context.Context) error {
	sweepEvery := min(max(v.ttl/4, time.Second), time.Minute)
	go func() {
		t := time.NewTicker(sweepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				v.users.sweep()
				v.tenants.sweep()
			}
		}
	}()
	backoff := time.Second
	for {
		listened, err := v.listenOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if listened {
			backoff = time.Second
		}
		slog.WarnContext(ctx, "vault: listening for evictions failed; retrying", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// listenOnce listens until the connection fails or ctx ends, and reports
// whether LISTEN got through.
func (v *Vault) listenOnce(ctx context.Context) (bool, error) {
	conn, err := pgx.ConnectConfig(ctx, v.pool.Config().ConnConfig.Copy())
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return false, fmt.Errorf("listen: %w", err)
	}
	// Whatever was shredded while nobody listened is unknown: start over.
	v.users.purge()
	if v.onListen != nil {
		v.onListen()
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return true, err
		}
		if ref, ok := parseUserPayload(n.Payload); ok {
			v.users.remove(ref)
		}
	}
}
