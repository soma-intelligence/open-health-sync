// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func TestPrepareSynthetic(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	at := now.Add(-24 * time.Hour)
	sc := scope{tenant: uuid.New(), user: uuid.New(), provider: "fitbit"}
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{QuantityType: model.QTStepCount, Value: 500, Unit: "steps", StartDate: at, EndDate: at.Add(time.Hour),
				ContentHash: "a", Metadata: map[string]any{"timezone": "Europe/Paris"}},
			{QuantityType: "body_mass", Value: 160, Unit: "lb", StartDate: at, EndDate: at, ContentHash: "b"},
			// Possible but implausible: written and flagged.
			{QuantityType: model.QTHeartRate, Value: 25, Unit: "bpm", StartDate: at, EndDate: at, ContentHash: "c"},
		},
		HeartbeatSamples: []model.HeartbeatSample{{Timestamp: at, RRMS: 900}},
	}
	cov, err := prepareSynthetic(mr, sc, now)
	if err != nil {
		t.Fatal(err)
	}
	s := mr.QuantitySamples
	if s[0].TenantID != sc.tenant.String() || s[0].UserID != sc.user.String() || s[0].SourceSlug != "fitbit" {
		t.Fatalf("owner or attribution not stamped: %+v", s[0])
	}
	if s[0].Unit != "count" || s[1].Unit != "kg" || s[1].Value < 72 || s[1].Value > 73 {
		t.Fatalf("units not canonicalized: %s, %v %s", s[0].Unit, s[1].Value, s[1].Unit)
	}
	if s[2].Metadata["quality_flag"] != string(SeveritySuspect) {
		t.Fatalf("suspect value not flagged: %v", s[2].Metadata)
	}
	if mr.HeartbeatSamples[0].SeriesID != "fitbit" {
		t.Fatalf("heartbeat series: %q", mr.HeartbeatSamples[0].SeriesID)
	}
	day := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	found := false
	for _, e := range cov {
		if e.Metric == model.QTStepCount && e.Date.Equal(day) && e.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("coverage lacks the step day: %+v", cov)
	}
}

func TestPrepareSyntheticRefuses(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	sc := scope{tenant: uuid.New(), user: uuid.New(), provider: "oura"}
	for name, s := range map[string]model.QuantitySample{
		"reject":    {QuantityType: model.QTHeartRate, Value: 400, Unit: "bpm", StartDate: now, EndDate: now},
		"unit":      {QuantityType: "body_mass", Value: 70, Unit: "furlong", StartDate: now, EndDate: now},
		"in future": {QuantityType: model.QTHeartRate, Value: 60, Unit: "bpm", StartDate: now.Add(72 * time.Hour)},
	} {
		_, err := prepareSynthetic(&model.MapResult{QuantitySamples: []model.QuantitySample{s}}, sc, now)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		// Never a health value in an error.
		if strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "70") {
			t.Errorf("%s: the error carries the value: %v", name, err)
		}
	}
}

func TestWriteSyntheticArguments(t *testing.T) {
	if _, err := WriteSynthetic(context.Background(), nil, uuid.New(), uuid.New(), "", &model.MapResult{}); err == nil {
		t.Fatal("an empty provider must be refused")
	}
	c, err := WriteSynthetic(context.Background(), nil, uuid.New(), uuid.New(), "oura", &model.MapResult{})
	if err != nil || c.TotalWritten() != 0 {
		t.Fatalf("empty records: %v %v", c, err)
	}
}
