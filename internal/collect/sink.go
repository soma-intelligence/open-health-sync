// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/logutil"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// pullSink stores what one pull job fetched. Every Emit is one raw object of
// one data type (R16): key raw/<tenant>/<user>/<provider>/<day>/<batch>_<part>
// with batch naming the job and its claim and part the emit sequence. The
// raw_objects row and its standard tier ETL job are inserted in one
// transaction (R18). Bytes already registered for this user (an overlapping
// pull) are skipped, so a re-pull of the same data stores nothing new.
type pullSink struct {
	d     Deps
	job   queue.PullJob
	batch string

	mu sync.Mutex
	// fetched is when the fetch in progress started (startFetch): the
	// capture time of every raw object it emits.
	fetched time.Time
	part    int
	stored  int
	skipped int
}

var _ connector.RawSink = (*pullSink)(nil)

func newPullSink(d Deps, job queue.PullJob) *pullSink {
	// The claim time keeps keys of a retried or deferred job apart from the
	// files an earlier attempt already wrote (raw files are never rewritten).
	return &pullSink{d: d, job: job, batch: fmt.Sprintf("pull%d-%d", job.ID, job.ClaimedAt.UnixMicro())}
}

// startFetch notes that a fetch starts now. What the fetch emits was read
// from the provider after this moment, however long the objects then take to
// be stored and registered, so this is the time that orders them against the
// objects of other pulls. It is the database's clock, as the receive time of
// an upload is.
func (s *pullSink) startFetch(ctx context.Context) error {
	var at time.Time
	if err := s.d.Pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return fmt.Errorf("read fetch start time: %w", err)
	}
	s.mu.Lock()
	s.fetched = at
	s.mu.Unlock()
	return nil
}

// Emit stores lines (NDJSON, one record each, verbatim) as one raw object.
func (s *pullSink) Emit(ctx context.Context, dataType string, day time.Time, lines [][]byte) error {
	if dataType == "" {
		return errors.New("collect: emit without a data type")
	}
	if len(lines) == 0 {
		return nil
	}
	s.mu.Lock()
	part := s.part
	s.part++
	fetched := s.fetched
	s.mu.Unlock()

	j := s.job
	stored, err := storeRaw(ctx, s.d, rawTarget{
		tenant: j.TenantID, user: j.UserID, provider: j.Provider, dataType: dataType,
		day: day, batch: s.batch, part: part, captured: fetched,
	}, lines)
	if err != nil {
		return err
	}
	s.count(stored)
	return nil
}

// rawTarget is where one raw object goes: its owner, data type and key parts.
type rawTarget struct {
	tenant, user uuid.UUID
	provider     string
	dataType     string
	day          time.Time
	batch        string
	part         int
	// captured is when the data was fetched (rawObject.captured); zero when
	// it was received rather than fetched.
	captured time.Time
}

// storeRaw stores lines as one raw object of t and queues its standard tier
// ETL job: the file first, then the raw_objects row and the job in one
// transaction. It returns false, storing nothing, when this user already has
// these bytes registered (or a concurrent writer registered them first), and
// errInactive when the user is gone or being deleted.
func storeRaw(ctx context.Context, d Deps, t rawTarget, lines [][]byte) (bool, error) {
	body, err := gzipLines(lines)
	if err != nil {
		return false, err
	}
	rb, err := newRawBody(ctx, d.Pool, d.Vault, t.tenant, t.user, body)
	if err != nil {
		return false, err
	}
	hash := rb.hash
	key := rawstore.Key(t.tenant, t.user, t.provider, t.day, t.batch, t.part)

	if _, found, err := findRaw(ctx, d.Pool, t.tenant, t.user, key, rb); err != nil {
		return false, err
	} else if found {
		return false, nil
	}
	if err := userActive(ctx, d.Pool, t.tenant, t.user); err != nil {
		return false, err
	}
	size, err := putRaw(ctx, d.Raw, key, rb)
	if err != nil {
		if errors.Is(err, errOtherBytes) {
			return false, fmt.Errorf("collect: raw key %s already holds other bytes", key)
		}
		return false, err
	}
	inserted, err := registerRaw(ctx, d.Pool, rawObject{
		tenant: t.tenant, user: t.user, provider: t.provider, key: key, hash: hash, size: size,
		batch: t.batch, part: t.part, dataType: t.dataType, tier: queue.TierStandard, captured: t.captured,
	})
	if errors.Is(err, errInactive) || errors.Is(err, errErasing) {
		// The user, or their data from this provider, started being deleted
		// between the check and the registration: the file just written is
		// removed again, or it could outlive the deletion's own raw sweep.
		// Either way nothing more is pulled.
		if derr := d.Raw.DeletePrefix(context.WithoutCancel(ctx), key); derr != nil {
			logutil.ErrCtx(ctx, *logging.Ctx(ctx), derr, zerolog.WarnLevel).Stringer("tenant_id", t.tenant).Stringer("user_id", t.user).Str("provider", t.provider).Msg("refused_write_file_not_removed")
		}
		return false, errInactive
	}
	if err != nil {
		return false, err
	}
	// Not inserted: a concurrent writer registered the same bytes first. The
	// file just written is then an orphan the daily scan reports.
	return inserted, nil
}

func (s *pullSink) count(stored bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored {
		s.stored++
	} else {
		s.skipped++
	}
}

func gzipLines(lines [][]byte) ([]byte, error) {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	for _, l := range lines {
		l = bytes.TrimRight(l, "\r\n")
		if bytes.ContainsAny(l, "\n") {
			return nil, errors.New("collect: emitted line contains a newline")
		}
		if _, err := zw.Write(l); err != nil {
			return nil, err
		}
		if _, err := zw.Write([]byte{'\n'}); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
