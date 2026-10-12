// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapHeartRate turns one heartrate reading into a heart_rate sample at its
// timestamp. Oura says what the ring was doing ("awake", "rest", "sleep",
// "session", "live", "workout"); that goes into metadata. A reading never
// changes once taken, so the hash carries the value like any measurement. A
// zero or missing bpm is no reading and is skipped.
//
// Oura sends a reading's time in UTC (timestamp, and timestamp_unix in
// milliseconds, read when timestamp is missing), not in the user's offset, so
// the sample claims no time zone: the ETL files it under its UTC day.
func mapHeartRate(line []byte, userID string) (*model.MapResult, error) {
	var h HeartRate
	if err := decode(DataTypeHeartRate, line, &h); err != nil {
		return nil, err
	}
	var ts time.Time
	switch {
	case h.Timestamp != "":
		t, err := parseTime(h.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("oura: heartrate: %w", err)
		}
		ts = t
	case h.TimestampUnix != nil && *h.TimestampUnix > 0:
		ts = time.UnixMilli(*h.TimestampUnix)
		if !plausible(ts) {
			return nil, fmt.Errorf("oura: heartrate: timestamp_unix %d is out of range", *h.TimestampUnix)
		}
	default:
		return nil, fmt.Errorf("oura: heartrate: no timestamp")
	}
	if h.BPM == nil || *h.BPM <= 0 {
		return nil, nil
	}
	md := map[string]any{"oura_source": h.Source}
	at := ts.UTC()
	s := model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		ExternalID:      fmt.Sprintf("heartrate_%d", at.Unix()),
		QuantityType:    model.QTHeartRate,
		Value:           *h.BPM,
		Unit:            "bpm",
		IsPrimarySource: true,
		StartDate:       at,
		EndDate:         at, // point-in-time reading
		Metadata:        md,
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"quantity_type": model.QTHeartRate,
		"start_date":    at.Format(time.RFC3339),
		"value":         model.CanonicalFloat(*h.BPM),
	})
	return &model.MapResult{QuantitySamples: []model.QuantitySample{s}}, nil
}
