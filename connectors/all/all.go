// SPDX-License-Identifier: AGPL-3.0-only

// Package all blank-imports every connector so that their init() functions
// register them. Each connector adds its import line here when it is added.
package all

import (
	_ "github.com/soma-intelligence/open-health-sync/connectors/activityfiles"
	_ "github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	_ "github.com/soma-intelligence/open-health-sync/connectors/bluetooth"
	_ "github.com/soma-intelligence/open-health-sync/connectors/dexcom"
	_ "github.com/soma-intelligence/open-health-sync/connectors/eightsleep"
	_ "github.com/soma-intelligence/open-health-sync/connectors/fitbit"
	_ "github.com/soma-intelligence/open-health-sync/connectors/garmin"
	_ "github.com/soma-intelligence/open-health-sync/connectors/garminconnect"
	_ "github.com/soma-intelligence/open-health-sync/connectors/healthconnect"
	_ "github.com/soma-intelligence/open-health-sync/connectors/intervalsicu"
	_ "github.com/soma-intelligence/open-health-sync/connectors/omron"
	_ "github.com/soma-intelligence/open-health-sync/connectors/oura"
	_ "github.com/soma-intelligence/open-health-sync/connectors/polar"
	_ "github.com/soma-intelligence/open-health-sync/connectors/selfreport"
	_ "github.com/soma-intelligence/open-health-sync/connectors/strava"
	_ "github.com/soma-intelligence/open-health-sync/connectors/suunto"
	_ "github.com/soma-intelligence/open-health-sync/connectors/ultrahuman"
	_ "github.com/soma-intelligence/open-health-sync/connectors/whoop"
	_ "github.com/soma-intelligence/open-health-sync/connectors/withings"
	_ "github.com/soma-intelligence/open-health-sync/connectors/xiaomi"
)
