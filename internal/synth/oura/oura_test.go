// SPDX-License-Identifier: AGPL-3.0-only

package oura_test

import (
	"os"
	"testing"

	conn "github.com/soma-intelligence/open-health-sync/connectors/oura"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/logutil/logtest"
	"github.com/soma-intelligence/open-health-sync/internal/synth/oura"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

func TestMain(m *testing.M) {
	logtest.Discard()
	os.Exit(m.Run())
}

func TestSuite(t *testing.T) {
	synthtest.Suite(t, func() connector.Connector { return conn.New(conn.Options{}) }, oura.Lines)
}
