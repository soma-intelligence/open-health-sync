// SPDX-License-Identifier: AGPL-3.0-only

package bluetooth_test

import (
	"os"
	"testing"

	conn "github.com/soma-intelligence/open-health-sync/connectors/bluetooth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/logutil/logtest"
	"github.com/soma-intelligence/open-health-sync/internal/synth/bluetooth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

func TestMain(m *testing.M) {
	logtest.Discard()
	os.Exit(m.Run())
}

func TestSuite(t *testing.T) {
	synthtest.Suite(t, func() connector.Connector { return conn.New() }, bluetooth.Lines)
}
