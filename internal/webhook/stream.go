// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StreamChannel is the Postgres NOTIFY channel of the event stream. A
// payload is tenant id ":" seq, the row of ohs_core.stream_events to load;
// it never carries the event itself.
const StreamChannel = "ohs_events"

// StreamRetention is how long an event stays in ohs_core.stream_events: the
// window a stream resumes in with Last-Event-ID.
const StreamRetention = 24 * time.Hour

// streamOff, when set, leaves the event stream out of Enqueue: an instance
// with OHS_STREAM_MAX=0 writes no stream rows and sends no NOTIFY.
var streamOff atomic.Bool

// SetStreamRecording turns the writing of stream events on (the default) or
// off for this process.
func SetStreamRecording(on bool) { streamOff.Store(!on) }

// streamSQL writes the event for the stream, with the same data the
// deliveries carry (the user's id and external_ref read in the same
// statement), and notifies the listening api processes of its row. NOTIFY is
// sent at commit, so an event that rolls back is never announced.
const streamSQL = `
	WITH ev AS (
		INSERT INTO ohs_core.stream_events (tenant_id, user_id, event_id, event_type, payload)
		SELECT $1, $2, $3, $4,
			jsonb_set($5::jsonb, '{data}', ($5::jsonb -> 'data') || COALESCE(
				(SELECT jsonb_build_object('user_id', u.id, 'external_ref', u.external_ref)
				 FROM ohs_core.end_users u WHERE u.tenant_id = $1 AND u.id = $2),
				CASE WHEN $2::uuid IS NULL THEN '{}'::jsonb
					ELSE jsonb_build_object('user_id', $2::uuid, 'external_ref', NULL) END))
		ON CONFLICT (tenant_id, event_id) DO NOTHING
		RETURNING tenant_id, seq)
	SELECT pg_notify('` + StreamChannel + `', ev.tenant_id::text || ':' || ev.seq::text) FROM ev`

// recordStream writes ev for the event stream in the caller's transaction.
// A user's erasure first removes the stream rows about them: user.deleted is
// the only one left.
func recordStream(ctx context.Context, db Execer, ev Event, id uuid.UUID, payload []byte) error {
	if streamOff.Load() {
		return nil
	}
	if ev.Type == UserDeleted && ev.User != nil {
		if _, err := db.Exec(ctx, `DELETE FROM ohs_core.stream_events WHERE tenant_id = $1 AND user_id = $2`,
			ev.Tenant, *ev.User); err != nil {
			return fmt.Errorf("stream %s: drop the user's events: %w", ev.Type, err)
		}
	}
	if _, err := db.Exec(ctx, streamSQL, ev.Tenant, ev.User, id, ev.Type, payload); err != nil {
		return fmt.Errorf("stream %s: %w", ev.Type, err)
	}
	return nil
}

// ParseStreamNotification reads a payload of StreamChannel.
func ParseStreamNotification(s string) (tenant uuid.UUID, seq int64, ok bool) {
	t, n, found := strings.Cut(s, ":")
	if !found {
		return uuid.Nil, 0, false
	}
	tenant, err := uuid.Parse(t)
	if err != nil {
		return uuid.Nil, 0, false
	}
	seq, err = strconv.ParseInt(n, 10, 64)
	if err != nil || seq <= 0 {
		return uuid.Nil, 0, false
	}
	return tenant, seq, true
}

// PruneStream deletes the stream events older than StreamRetention.
func PruneStream(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `DELETE FROM ohs_core.stream_events
		WHERE created_at < now() - make_interval(secs => $1::double precision)`, StreamRetention.Seconds())
	if err != nil {
		return fmt.Errorf("prune stream events: %w", err)
	}
	return nil
}
