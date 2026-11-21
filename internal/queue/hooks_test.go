// SPDX-License-Identifier: AGPL-3.0-only

package queue

import (
	"errors"
	"fmt"
	"testing"
)

func TestETLSettledOutcome(t *testing.T) {
	var got []string
	q := &ETL{Hooks: ETLHooks{Settled: func(j ETLJob, outcome string) { got = append(got, fmt.Sprint(j.ID, " ", outcome)) }}}
	q.settled(ETLJob{ID: 1}, "completed", nil)
	q.settled(ETLJob{ID: 2}, "failed", nil)
	q.settled(ETLJob{ID: 3}, "completed", fmt.Errorf("complete job: %w", ErrNotRunning))
	q.settled(ETLJob{ID: 4}, "failed", errors.New("the database is away"))
	want := []string{"1 completed", "2 failed", "3 claim_lost", "4 settle_error"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// No hook: nothing to call.
	(&ETL{}).settled(ETLJob{ID: 5}, "completed", nil)
}

func TestPullTriggerName(t *testing.T) {
	for want, j := range map[string]PullJob{
		TriggerScheduled:    {},
		TriggerNotification: {Inbound: true},
		TriggerBackfill:     {Trigger: TriggerBackfill},
		TriggerManual:       {Trigger: TriggerManual, Inbound: true},
	} {
		if got := j.TriggerName(); got != want {
			t.Errorf("%+v: %q, want %q", j, got, want)
		}
	}
}
