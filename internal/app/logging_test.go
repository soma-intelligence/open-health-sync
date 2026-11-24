// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

func TestStartupLogLine(t *testing.T) {
	t.Setenv("LOG_FORMAT", "json")
	var buf bytes.Buffer
	cfg := config.Config{LogLevel: "info", Listen: ":8080", Roles: map[string]bool{"api": true}}
	_, err := startLogging(&buf, cfg)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 1)
	var line map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &line))
	assert.Equal(t, "info", line["level"])
	assert.Equal(t, "service_starting", line["message"])
	assert.Equal(t, "ohs", line["service"])
	assert.Equal(t, "ohs", line["module"])
	for _, k := range []string{"env", "version", "time"} {
		assert.Contains(t, line, k)
	}
}

func TestStartLoggingRejectsUnknownLevel(t *testing.T) {
	_, err := startLogging(&bytes.Buffer{}, config.Config{LogLevel: "loud"})
	require.Error(t, err)
	assert.ErrorContains(t, err, "OHS_LOG_LEVEL")
}
