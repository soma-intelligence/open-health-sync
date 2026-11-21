// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/fitbit"
	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// A Fitbit point sent again without its optional dataSource cannot hash like
// the copy that had one, so both are stored; the copy without a source is a
// stream of its own, and the merged read counts the reading once.
func TestFitbitPointWithoutSourceCountsOnce(t *testing.T) {
	e := newEnv(t)
	e.register(fitbit.Slug, func(r connector.RawRecord) (model.MapResult, error) {
		return fitbit.New(fitbit.Options{}).Map(r)
	})
	weight := `"weight":{"sampleTime":{"physicalTime":"2026-03-09T06:30:00Z","utcOffset":"3600s"},"weightGrams":"72500"}`
	sourced := `{"dataSource":{"platform":"FITBIT","recordingMethod":"AUTOMATICALLY_MEASURED","device":{"formFactor":"SCALE","displayName":"Aria Air","manufacturer":"Fitbit"}},` + weight + `}`
	sourceless := `{` + weight + `}`
	e.process(e.upload(e.tenant, e.user, fitbit.Slug, fitbit.DataTypeWeight, sourced))
	e.process(e.upload(e.tenant, e.user, fitbit.Slug, fitbit.DataTypeWeight, sourceless))

	ctx := context.Background()
	all, err := read.Samples(ctx, e.pool, e.tenant, e.user, read.Query{Type: "body_mass", View: read.ViewAll})
	require.NoError(t, err)
	require.Len(t, all.Records, 2, "both copies are stored")
	merged, err := read.Samples(ctx, e.pool, e.tenant, e.user, read.Query{Type: "body_mass"})
	require.NoError(t, err)
	require.Len(t, merged.Records, 1, "the merged read counts the reading once")
	assert.InDelta(t, 72.5, merged.Records[0].Value, 1e-9)
}
