// SPDX-License-Identifier: AGPL-3.0-only

package suunto_test

import (
	"io"
	"log/slog"
	"os"
	"testing"

	conn "github.com/soma-intelligence/open-health-sync/connectors/suunto"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth/suunto"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func TestSuite(t *testing.T) {
	synthtest.Suite(t, func() connector.Connector { return conn.New(conn.Options{}) }, suunto.Lines)
}
