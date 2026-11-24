// SPDX-License-Identifier: AGPL-3.0-only

package tenantproviders

import (
	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
)

// modlog is the package's process logger, tagged with its module. It is built per use so it
// follows logging.Init.
func modlog() *zerolog.Logger {
	l := logging.Module("tenantproviders")
	return &l
}
