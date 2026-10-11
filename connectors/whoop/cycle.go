// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"fmt"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapCycle turns one scored cycle into a strain sample, an average heart rate
// sample over the cycle, and the strain and energy figures of the day's
// summary.
//
// WHOOP's cycle kilojoules count all the energy spent in the cycle, resting
// metabolism included, so they become the summary's total_kcal, never
// active_energy_burned: written there, they would overstate active energy
// against every other provider. A cycle not scored (yet) gives nothing; WHOOP
// sends it again once scored.
func mapCycle(line []byte, userID string) (*model.MapResult, error) {
	var cy Cycle
	if err := decode(DataTypeCycle, line, &cy); err != nil {
		return nil, err
	}
	if cy.ID == "" {
		return nil, fmt.Errorf("whoop: cycle: missing id")
	}
	start, err := requireTime(DataTypeCycle, "start", cy.Start)
	if err != nil {
		return nil, err
	}
	// The current cycle has no end yet: its samples end where they start.
	end := start
	if cy.End != nil && *cy.End != "" {
		if end, err = requireTime(DataTypeCycle, "end", *cy.End); err != nil {
			return nil, err
		}
	}
	if cy.ScoreState != scoreScored || cy.Score == nil {
		return nil, nil
	}
	sc := cy.Score
	id := string(cy.ID)

	md := func() map[string]any {
		m := map[string]any{"whoop_cycle_id": id}
		if cy.End == nil || *cy.End == "" {
			m["cycle_open"] = true
		}
		setIf(m, "kilojoule", sc.Kilojoule)
		setIf(m, "max_heart_rate", sc.MaxHeartRate)
		return withTimezone(m, cy.TimezoneOffset)
	}

	res := &model.MapResult{}
	if sc.Strain != nil {
		res.QuantitySamples = append(res.QuantitySamples,
			quantity(userID, id, model.QTStrain, *sc.Strain, "index", start, end, md()))
	}
	if sc.AverageHeartRate != nil && *sc.AverageHeartRate > 0 {
		m := md()
		m["aggregate"] = "cycle_average"
		res.QuantitySamples = append(res.QuantitySamples,
			quantity(userID, id, model.QTHeartRate, *sc.AverageHeartRate, "count/min", start, end, m))
	}
	if sc.Strain != nil || sc.Kilojoule != nil {
		d := dailySummary(userID, cycleDay(start, cy.TimezoneOffset), cy.TimezoneOffset)
		d.StrainScore = sc.Strain
		if sc.Kilojoule != nil {
			d.TotalKcal = ptrFloat(*sc.Kilojoule * kcalPerKilojoule)
		}
		res.DailySummaries = append(res.DailySummaries, d)
	}
	if res.IsEmpty() {
		return nil, nil
	}
	return res, nil
}
