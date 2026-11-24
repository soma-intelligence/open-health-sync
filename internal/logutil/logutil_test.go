// SPDX-License-Identifier: AGPL-3.0-only

package logutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func TestShutdownErrorsAreDebug(t *testing.T) {
	cases := map[string]error{
		"canceled":      context.Canceled,
		"wrapped":       fmt.Errorf("claim jobs: %w", context.Canceled),
		"closed pool":   errors.New("closed pool: database is closed"),
		"closed pool 2": fmt.Errorf("begin: %w", errors.New("database is closed")),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			log := zerolog.New(&buf).Level(zerolog.WarnLevel)
			Err(log, err, zerolog.ErrorLevel).Msg("claim_jobs_failed")
			assert.Empty(t, buf.String(), "a shutdown error must not reach warn or above")

			buf.Reset()
			log = zerolog.New(&buf).Level(zerolog.DebugLevel)
			Err(log, err, zerolog.ErrorLevel).Msg("claim_jobs_failed")
			assert.Contains(t, buf.String(), `"level":"debug"`)
			assert.Contains(t, buf.String(), `"error":`, "the error stays attached")
		})
	}
}

func TestRealErrorsKeepTheirLevel(t *testing.T) {
	var buf bytes.Buffer
	log := zerolog.New(&buf)
	Err(log, errors.New("connection refused"), zerolog.ErrorLevel).Msg("claim_jobs_failed")
	assert.Contains(t, buf.String(), `"level":"error"`)
	assert.False(t, Shutdown(nil))
}

// A deadline drops to debug only while the context of the work is itself done: a provider that
// timed out under a live request or loop keeps its level.
func TestDeadlineIsDebugOnlyWhenTheContextIsDone(t *testing.T) {
	err := fmt.Errorf("call provider: %w", context.DeadlineExceeded)
	live := context.Background()
	assert.Equal(t, zerolog.WarnLevel, LevelCtx(live, err, zerolog.WarnLevel))
	assert.False(t, ShutdownCtx(live, err))

	done, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Equal(t, zerolog.DebugLevel, LevelCtx(done, err, zerolog.WarnLevel))
	assert.Equal(t, zerolog.DebugLevel, LevelCtx(live, context.Canceled, zerolog.WarnLevel), "a cancellation is always debug")

	var buf bytes.Buffer
	ErrCtx(live, zerolog.New(&buf), err, zerolog.WarnLevel).Msg("call_failed")
	assert.Contains(t, buf.String(), `"level":"warn"`)
}
