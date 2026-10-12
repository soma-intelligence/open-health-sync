// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapBody turns one body measurement snapshot into body_mass and height
// samples dated the snapshot day (midnight UTC). WHOOP keeps only the current
// values, with no date, so a sample says "this was the value on that day". A
// change during the day replaces the day's sample; an unchanged value read on
// a later day gives that day its own sample.
//
// The maximum heart rate WHOOP holds for the user is a profile setting, not a
// reading, so it is kept in the metadata of both samples rather than stored
// as a quantity.
func mapBody(line []byte, userID string) (*model.MapResult, error) {
	var b Body
	if err := decode(DataTypeBody, line, &b); err != nil {
		return nil, err
	}
	if b.SnapshotDate == "" {
		return nil, fmt.Errorf("whoop: body: missing snapshot_date")
	}
	day, err := time.Parse("2006-01-02", b.SnapshotDate)
	if err != nil {
		return nil, fmt.Errorf("whoop: body: snapshot_date %q: %w", b.SnapshotDate, err)
	}
	md := func() map[string]any {
		m := map[string]any{"snapshot_date": b.SnapshotDate}
		setIf(m, "max_heart_rate", b.MaxHeartRate)
		return m
	}
	var samples []model.QuantitySample
	if b.WeightKilogram != nil && *b.WeightKilogram > 0 {
		samples = append(samples, quantity(userID, b.SnapshotDate, "body_mass", *b.WeightKilogram, "kg", day, day, md()))
	}
	if b.HeightMeter != nil && *b.HeightMeter > 0 {
		// Metres: the unit pass stores height in centimetres.
		samples = append(samples, quantity(userID, b.SnapshotDate, "height", *b.HeightMeter, "m", day, day, md()))
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}
