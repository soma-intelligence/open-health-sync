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
		"deadline":      context.DeadlineExceeded,
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
