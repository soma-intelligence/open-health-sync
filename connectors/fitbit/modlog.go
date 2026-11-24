// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
)

// modlog is the connector's process logger, tagged with its provider. It is built per use so it
// follows logging.Init.
func modlog() *zerolog.Logger {
	l := logging.Module("connector").With().Str("provider", Slug).Logger()
	return &l
}
