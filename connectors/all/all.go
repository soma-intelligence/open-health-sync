// SPDX-License-Identifier: AGPL-3.0-only

// Package all blank-imports every connector so that their init() functions
// register them. Each connector adds its import line here when it is added.
package all

import (
	_ "github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	_ "github.com/soma-intelligence/open-health-sync/connectors/oura"
	_ "github.com/soma-intelligence/open-health-sync/connectors/withings"
)
