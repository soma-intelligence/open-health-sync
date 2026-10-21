// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

func TestRunStatusFromJob(t *testing.T) {
	outcomes := []string{"", queue.OutcomeCompleted, queue.OutcomeFailed, queue.OutcomeRateLimited, queue.OutcomeInactive}
	want := map[string]map[string]string{
		// A job a worker holds, or one that finished, says so whatever its
		// last outcome was.
		"running": {"": RunRunning, queue.OutcomeCompleted: RunRunning, queue.OutcomeFailed: RunRunning,
			queue.OutcomeRateLimited: RunRunning, queue.OutcomeInactive: RunRunning},
		"done": {"": RunSucceeded, queue.OutcomeCompleted: RunSucceeded, queue.OutcomeFailed: RunSucceeded,
			queue.OutcomeRateLimited: RunSucceeded, queue.OutcomeInactive: RunSucceeded},
		// A parked job was stopped by the connection going away, or gave up.
		"parked": {"": RunFailed, queue.OutcomeCompleted: RunFailed, queue.OutcomeFailed: RunFailed,
			queue.OutcomeRateLimited: RunFailed, queue.OutcomeInactive: RunCancelled},
		// A waiting job tells why it waits.
		"queued": {"": RunQueued, queue.OutcomeCompleted: RunQueued, queue.OutcomeFailed: RunRetrying,
			queue.OutcomeRateLimited: RunRateLimited, queue.OutcomeInactive: RunQueued},
	}
	for status, byOutcome := range want {
		for _, o := range outcomes {
			var outcome *string
			if o != "" {
				outcome = &o
			}
			assert.Equal(t, byOutcome[o], runStatus(status, outcome), "status %q, outcome %q", status, o)
		}
	}
}

func TestNeedsRefreshAroundTheMargin(t *testing.T) {
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		expiry time.Time
		want   bool
	}{
		{time.Time{}, false}, // no expiry: the token never expires
		{now.Add(-time.Hour), true},
		{now, true},
		{now.Add(RefreshMargin - time.Nanosecond), true},
		{now.Add(RefreshMargin), false},
		{now.Add(time.Hour), false},
	} {
		assert.Equal(t, tc.want, needsRefresh(connector.Tokens{Expiry: tc.expiry}, now), "expiry %v", tc.expiry)
	}
}

func TestReauthReasonAndSeverity(t *testing.T) {
	assert.Equal(t, BrokenMissingScope, reauthReason(connector.ErrMissingScope))
	assert.Equal(t, BrokenMissingScope, reauthReason(fmt.Errorf("oura: %w", connector.ErrMissingScope)))
	assert.Equal(t, BrokenNeedsReauth, reauthReason(connector.ErrNeedsReauth))
	assert.Equal(t, BrokenNeedsReauth, reauthReason(io.ErrUnexpectedEOF))

	for reason, want := range map[string]bool{
		BrokenNeedsReauth:  true,
		BrokenMissingScope: true,
		BrokenFailing:      false,
		BrokenRateLimited:  false,
		"":                 false,
	} {
		assert.Equal(t, want, severe(reason), "reason %q", reason)
	}
}

// uploadOnly is a device connector: an app uploads its data.
type uploadOnly struct{ slug string }

func (u uploadOnly) Slug() string                                   { return u.slug }
func (uploadOnly) Enabled(connector.Config) bool                    { return true }
func (uploadOnly) Map(connector.RawRecord) (model.MapResult, error) { return model.MapResult{}, nil }
func (uploadOnly) ParseUpload(connector.UploadMeta, io.Reader, connector.RawSink) error {
	return nil
}

// typedOAuth is an OAuth connector that can pull only what a notification
// names.
type typedOAuth struct{ pageOAuth }

func (typedOAuth) FetchNotified(context.Context, connector.Connection, time.Time, time.Time, string, connector.RawSink) error {
	return nil
}

func TestConnectorCapabilities(t *testing.T) {
	registry := map[string]connector.Connector{
		"pushy":   probePush{"pushy"},
		"puller":  pageOAuth{"puller"},
		"typed":   typedOAuth{pageOAuth{"typed"}},
		"devices": uploadOnly{"devices"},
	}
	d := Deps{Lookup: func(slug string) (connector.Connector, bool) {
		c, ok := registry[slug]
		return c, ok
	}}
	for _, tc := range []struct {
		slug                    string
		push, typedPull, device bool
	}{
		{"pushy", true, false, false},
		{"puller", false, false, false},
		{"typed", false, true, false},
		{"devices", false, false, true},
		{"unknown", false, false, false},
	} {
		assert.Equal(t, tc.push, d.isPush(tc.slug), "%s isPush", tc.slug)
		assert.Equal(t, tc.typedPull, d.typed(tc.slug), "%s typed", tc.slug)
		assert.Equal(t, tc.device, d.isDevice(tc.slug), "%s isDevice", tc.slug)
	}
}
