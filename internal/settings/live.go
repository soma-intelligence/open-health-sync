// SPDX-License-Identifier: AGPL-3.0-only

package settings

import (
	"context"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/logutil"
)

// ReloadEvery is how often Run reads the settings revision.
const ReloadEvery = 30 * time.Second

// RateLimits are the five token buckets; the zero RateLimit is off.
type RateLimits struct{ API, AuthFailures, Inbound, InboundProvider, OAuth config.RateLimit }

// Snapshot is the live settings at one revision. A reader takes one
// Snapshot and sees one consistent set.
type Snapshot struct {
	Revision                                                 int64
	LogLevel                                                 zerolog.Level
	RetentionDays, AuditRetentionDays, TrackingRetentionDays int
	ConsentRequired                                          bool
	BackfillDays                                             int
	WebhookPauseAfter                                        int
	WebhookRotationGrace                                     time.Duration
	RateLimits                                               RateLimits
}

// liveFields maps each ApplyLive setting to its field of the Snapshot.
var liveFields = map[string]func(Snapshot) any{
	"OHS_LOG_LEVEL":                    func(s Snapshot) any { return s.LogLevel },
	"OHS_RETENTION_DAYS":               func(s Snapshot) any { return s.RetentionDays },
	"OHS_AUDIT_RETENTION_DAYS":         func(s Snapshot) any { return s.AuditRetentionDays },
	"OHS_TRACKING_RETENTION_DAYS":      func(s Snapshot) any { return s.TrackingRetentionDays },
	"OHS_CONSENT_REQUIRED":             func(s Snapshot) any { return s.ConsentRequired },
	"OHS_BACKFILL_DAYS":                func(s Snapshot) any { return s.BackfillDays },
	"OHS_WEBHOOK_PAUSE_AFTER":          func(s Snapshot) any { return s.WebhookPauseAfter },
	"OHS_WEBHOOK_ROTATION_GRACE_HOURS": func(s Snapshot) any { return s.WebhookRotationGrace },
	"OHS_RATE_LIMIT_API":               func(s Snapshot) any { return s.RateLimits.API },
	"OHS_RATE_LIMIT_AUTH_FAILURES":     func(s Snapshot) any { return s.RateLimits.AuthFailures },
	"OHS_RATE_LIMIT_INBOUND":           func(s Snapshot) any { return s.RateLimits.Inbound },
	"OHS_RATE_LIMIT_INBOUND_PROVIDER":  func(s Snapshot) any { return s.RateLimits.InboundProvider },
	"OHS_RATE_LIMIT_OAUTH":             func(s Snapshot) any { return s.RateLimits.OAuth },
}

// SnapshotOf takes the live settings of cfg, at revision.
func SnapshotOf(cfg config.Config, revision int64) Snapshot {
	level, err := config.ParseLogLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	return Snapshot{
		Revision:              revision,
		LogLevel:              level,
		RetentionDays:         cfg.RetentionDays,
		AuditRetentionDays:    cfg.AuditRetentionDays,
		TrackingRetentionDays: cfg.TrackingRetentionDays,
		ConsentRequired:       cfg.ConsentRequired,
		BackfillDays:          cfg.BackfillDays,
		WebhookPauseAfter:     cfg.WebhookPauseAfter,
		WebhookRotationGrace:  cfg.WebhookRotationGrace,
		RateLimits: RateLimits{
			API:             cfg.RateLimitAPI,
			AuthFailures:    cfg.RateLimitAuthFailures,
			Inbound:         cfg.RateLimitInbound,
			InboundProvider: cfg.RateLimitInboundProvider,
			OAuth:           cfg.RateLimitOAuth,
		},
	}
}

// Status is what the console shows of the live settings.
type Status struct {
	// StartRevision is the revision the process resolved at, LiveRevision
	// the one the live snapshot holds.
	StartRevision, LiveRevision int64
	// Skipped are the stored names the last resolve did not use (invalid,
	// unknown or sealed under a key no longer held), never values.
	Skipped []string
	// ReloadError is the last failed reload, empty after a good one.
	ReloadError string
}

// LiveOptions build a Live.
type LiveOptions struct {
	Store Store
	// Env reads the environment; nil means os.LookupEnv.
	Env func(string) (string, bool)
	// Environ is the process environment; nil means os.Environ().
	Environ []string
	// Config is the resolved start configuration.
	Config config.Config
	Start  Start
}

// Live is the live settings of the process. Reads are lock free; Reload
// swaps the snapshot when the stored revision moved.
type Live struct {
	store   Store
	env     func(string) (string, bool)
	environ []string

	reloadMu sync.Mutex // serializes reloads, held across the database reads
	mu       sync.Mutex // guards subs and status, never held across I/O or a callback
	subs     []func(Snapshot)
	status   Status
	cur      atomic.Pointer[Snapshot]
}

// NewLive returns the live settings, holding the start configuration.
func NewLive(o LiveOptions) *Live {
	l := &Live{store: o.Store, env: o.Env, environ: o.Environ}
	if l.env == nil {
		l.env = os.LookupEnv
	}
	if l.environ == nil {
		l.environ = os.Environ()
	}
	snap := SnapshotOf(o.Config, o.Start.Revision)
	l.cur.Store(&snap)
	l.status = Status{StartRevision: o.Start.Revision, LiveRevision: o.Start.Revision}
	for _, inv := range o.Start.Invalid {
		l.status.Skipped = append(l.status.Skipped, inv.Name)
	}
	l.status.Skipped = append(l.status.Skipped, o.Start.Unknown...)
	sort.Strings(l.status.Skipped)
	return l
}

// Snapshot returns the current live settings.
func (l *Live) Snapshot() Snapshot { return *l.cur.Load() }

// Status returns the revisions and the last reload error.
func (l *Live) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.status
	st.Skipped = slices.Clone(st.Skipped)
	return st
}

// Subscribe registers fn, called after each swap in the goroutine that
// swapped, with the new snapshot.
func (l *Live) Subscribe(fn func(Snapshot)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.subs = append(l.subs, fn)
}

// Reload reads the revision and, when it moved, loads the rows, resolves
// them over the environment and swaps the snapshot. A failure keeps the last
// good snapshot and is recorded in Status. The process that saves calls it
// at once; Run calls it every interval for the other processes' saves.
func (l *Live) Reload(ctx context.Context) error {
	l.reloadMu.Lock()
	defer l.reloadMu.Unlock()
	snap, skipped, moved, err := l.load(ctx)
	if err != nil {
		l.mu.Lock()
		l.status.ReloadError = err.Error()
		l.mu.Unlock()
		return err
	}
	old := *l.cur.Load()
	l.mu.Lock()
	l.status.ReloadError = ""
	if !moved {
		l.mu.Unlock()
		return nil
	}
	l.cur.Store(&snap)
	l.status.LiveRevision = snap.Revision
	l.status.Skipped = skipped
	subs := slices.Clone(l.subs)
	l.mu.Unlock()
	if changed := changedNames(old, snap); len(changed) > 0 {
		modlog().Info().Int64("revision", snap.Revision).Str("settings", strings.Join(changed, ",")).Msg("live_settings_changed")
	}
	for _, fn := range subs {
		fn(snap)
	}
	return nil
}

func (l *Live) load(ctx context.Context) (snap Snapshot, skipped []string, moved bool, err error) {
	rev, err := l.store.Revision(ctx)
	if err != nil {
		return Snapshot{}, nil, false, err
	}
	if rev == l.cur.Load().Revision {
		return Snapshot{}, nil, false, nil
	}
	st, err := l.store.Load(ctx)
	if err != nil {
		return Snapshot{}, nil, false, err
	}
	stored := make(map[string]string, len(st.Rows))
	for name, r := range st.Rows {
		if r.OpenErr != nil {
			if s, ok := config.LookupSetting(name); ok && s.Apply != config.ApplyBootstrap && !config.EnvSet(l.env, name) {
				skipped = append(skipped, name)
				modlog().Warn().Str("setting", name).Str("error", openErrMessage).Msg("stored_setting_skipped")
			}
			continue
		}
		stored[name] = r.Value
	}
	cfg, invalid, unknown, err := Resolve(l.env, l.environ, stored)
	if err != nil {
		return Snapshot{}, nil, false, err
	}
	for _, inv := range invalid {
		skipped = append(skipped, inv.Name)
		modlog().Warn().Str("setting", inv.Name).Str("error", inv.Message).Msg("stored_setting_skipped")
	}
	for _, n := range unknown {
		skipped = append(skipped, n)
		modlog().Warn().Str("setting", n).Msg("stored_setting_unknown_ignored")
	}
	sort.Strings(skipped)
	return SnapshotOf(cfg, st.Revision), skipped, true, nil
}

// changedNames lists the live settings whose value differs, sorted.
func changedNames(a, b Snapshot) []string {
	var out []string
	for name, f := range liveFields {
		if f(a) != f(b) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Run reloads every interval (ReloadEvery when 0) until ctx ends. A failed
// reload is logged and the last good snapshot stays.
func (l *Live) Run(ctx context.Context, every time.Duration) error {
	if every <= 0 {
		every = ReloadEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := l.Reload(ctx); err != nil && ctx.Err() == nil {
				logutil.Err(*modlog(), err, zerolog.WarnLevel).Msg("settings_reload_failed")
			}
		}
	}
}

// LogLevel is OHS_LOG_LEVEL.
func (l *Live) LogLevel() zerolog.Level { return l.cur.Load().LogLevel }

// RetentionDays is OHS_RETENTION_DAYS.
func (l *Live) RetentionDays() int { return l.cur.Load().RetentionDays }

// AuditRetentionDays is OHS_AUDIT_RETENTION_DAYS.
func (l *Live) AuditRetentionDays() int { return l.cur.Load().AuditRetentionDays }

// TrackingRetentionDays is OHS_TRACKING_RETENTION_DAYS.
func (l *Live) TrackingRetentionDays() int { return l.cur.Load().TrackingRetentionDays }

// ConsentRequired is OHS_CONSENT_REQUIRED.
func (l *Live) ConsentRequired() bool { return l.cur.Load().ConsentRequired }

// BackfillDays is OHS_BACKFILL_DAYS.
func (l *Live) BackfillDays() int { return l.cur.Load().BackfillDays }

// WebhookPauseAfter is OHS_WEBHOOK_PAUSE_AFTER.
func (l *Live) WebhookPauseAfter() int { return l.cur.Load().WebhookPauseAfter }

// WebhookRotationGrace is OHS_WEBHOOK_ROTATION_GRACE_HOURS as a duration.
func (l *Live) WebhookRotationGrace() time.Duration { return l.cur.Load().WebhookRotationGrace }

// RateLimits are the five OHS_RATE_LIMIT_* buckets.
func (l *Live) RateLimits() RateLimits { return l.cur.Load().RateLimits }
