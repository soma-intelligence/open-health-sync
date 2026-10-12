// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"fmt"
	"math"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// maxCycleSteps bounds a believable step count of one cycle; a figure past it
// (or negative) is left out rather than stored.
const maxCycleSteps = 1_000_000

// mapCycle turns one cycle into a strain sample, an average heart rate sample
// over the cycle, and the strain, energy and step figures of the day's
// summary.
//
// WHOOP's cycle kilojoules count all the energy spent in the cycle, resting
// metabolism included, so they become the summary's total_kcal, never
// active_energy_burned: written there, they would overstate active energy
// against every other provider. The score of a cycle not scored (yet) gives
// nothing; WHOOP sends it again once scored.
//
// step_count is a figure of the cycle itself, not of its score: it is the
// summary's step_count whatever the score state, and null (no step data) gives
// nothing. A cycle runs from one sleep onset to the next, so its steps are not
// a calendar day's: they stay in the WHOOP summary of the cycle's day rather
// than becoming a step_count sample the ETL would add to the day the cycle
// starts on.
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
	open := cy.End == nil || *cy.End == ""
	if !open {
		if end, err = requireTime(DataTypeCycle, "end", *cy.End); err != nil {
			return nil, err
		}
	}
	var sc *CycleScore
	if cy.ScoreState == scoreScored {
		sc = cy.Score
	}
	if sc == nil && cy.StepCount == nil {
		return nil, nil
	}
	if sc == nil {
		sc = &CycleScore{}
	}
	id := string(cy.ID)

	md := func() map[string]any {
		m := map[string]any{"whoop_cycle_id": id}
		if open {
			m["cycle_open"] = true
		}
		setIf(m, "kilojoule", sc.Kilojoule)
		setIf(m, "max_heart_rate", sc.MaxHeartRate)
		return withTimezone(m, cy.TimezoneOffset, start)
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
	var steps *float64
	if cy.StepCount != nil && *cy.StepCount >= 0 && *cy.StepCount < maxCycleSteps {
		steps = cy.StepCount
	}
	if sc.Strain != nil || sc.Kilojoule != nil || steps != nil {
		d := dailySummary(userID, cycleDay(start, cy.TimezoneOffset), cy.TimezoneOffset, start)
		d.StrainScore = sc.Strain
		if sc.Kilojoule != nil {
			d.TotalKcal = ptrFloat(*sc.Kilojoule * kcalPerKilojoule)
		}
		if steps != nil {
			n := int(math.Round(*steps))
			d.StepCount = &n
		}
		res.DailySummaries = append(res.DailySummaries, d)
	}
	if res.IsEmpty() {
		return nil, nil
	}
	return res, nil
}
