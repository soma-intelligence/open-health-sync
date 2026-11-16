// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/demo"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// SourceName is the source name of every sandbox sample.
const SourceName = "Sandbox (synthetic)"

// mapRecord maps one stored line of dataType. Every record is marked
// synthetic in its metadata and carries the persona it was generated for.
func mapRecord(dataType string, fields map[string]any, userID string) (*model.MapResult, error) {
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("sandbox: %s: re-encode line: %w", dataType, err)
	}
	switch dataType {
	case DataTypeSleep:
		var s Sleep
		if err := json.Unmarshal(line, &s); err != nil {
			return nil, fmt.Errorf("sandbox: sleep: %w", err)
		}
		return mapSleep(s, userID)
	case DataTypeHeartRate:
		var h HeartRate
		if err := json.Unmarshal(line, &h); err != nil {
			return nil, fmt.Errorf("sandbox: heart_rate: %w", err)
		}
		at, err := parse(h.Time)
		if err != nil {
			return nil, fmt.Errorf("sandbox: heart_rate: %w", err)
		}
		return &model.MapResult{QuantitySamples: []model.QuantitySample{
			sample(userID, model.QTHeartRate, h.BPM, "bpm", at, at, metadata(h.Persona, h.Timezone)),
		}}, nil
	case DataTypeSteps:
		var st Steps
		if err := json.Unmarshal(line, &st); err != nil {
			return nil, fmt.Errorf("sandbox: steps: %w", err)
		}
		start, err := parse(st.Start)
		if err != nil {
			return nil, fmt.Errorf("sandbox: steps: %w", err)
		}
		end, err := parse(st.End)
		if err != nil || end.Before(start) {
			return nil, fmt.Errorf("sandbox: steps: bad end")
		}
		return &model.MapResult{QuantitySamples: []model.QuantitySample{
			sample(userID, model.QTStepCount, st.Count, "count", start, end, metadata(st.Persona, st.Timezone)),
		}}, nil
	case DataTypeWorkouts:
		var w Workout
		if err := json.Unmarshal(line, &w); err != nil {
			return nil, fmt.Errorf("sandbox: workout: %w", err)
		}
		return mapWorkout(w, userID)
	}
	return nil, nil
}

// metadata is the metadata every sandbox record carries.
func metadata(persona, tz string) map[string]any {
	md := map[string]any{demo.MetadataSynthetic: true, "sandbox_persona": persona}
	if tz != "" {
		md["timezone"] = tz
	}
	return md
}

func parse(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not RFC 3339", s)
	}
	return t.UTC(), nil
}

func sample(userID, qt string, v float64, unit string, start, end time.Time, md map[string]any) model.QuantitySample {
	return model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      SourceName,
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        md,
		ContentHash: model.ComputeContentHash(map[string]string{
			"user_id":       userID,
			"provider":      Slug,
			"quantity_type": qt,
			"start_date":    start.Format(time.RFC3339),
			"end_date":      end.Format(time.RFC3339),
			"value":         model.CanonicalFloat(v),
		}),
	}
}

// mapSleep turns one night into a session and its phases.
func mapSleep(s Sleep, userID string) (*model.MapResult, error) {
	if s.ID == "" {
		return nil, fmt.Errorf("sandbox: sleep: missing id")
	}
	start, err := parse(s.Start)
	if err != nil {
		return nil, fmt.Errorf("sandbox: sleep: %w", err)
	}
	end, err := parse(s.End)
	if err != nil || !end.After(start) {
		return nil, fmt.Errorf("sandbox: sleep: bad end")
	}
	sess := model.SleepSession{
		UserID:            userID,
		Provider:          Slug,
		ExternalID:        s.ID,
		BedtimeStart:      start,
		BedtimeEnd:        end,
		TotalSleepSeconds: s.TotalSleepS,
		DeepSleepSeconds:  s.DeepS,
		REMSleepSeconds:   s.REMS,
		LightSleepSeconds: s.LightS,
		AwakeSeconds:      s.AwakeS,
		SleepScore:        s.Score,
		HRVAvgMs:          s.HRVms,
		RestingHR:         s.RestingHR,
		RespiratoryRate:   s.RespiratoryRate,
		Metadata:          metadata(s.Persona, s.Timezone),
		Replaceable:       true,
	}
	sess.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"external_id":   s.ID,
		"bedtime_start": start.Format(time.RFC3339),
	})
	res := &model.MapResult{SleepSessions: []model.SleepSession{sess}}
	for _, st := range s.Stages {
		ps, err1 := parse(st.Start)
		pe, err2 := parse(st.End)
		if err1 != nil || err2 != nil || !pe.After(ps) {
			continue
		}
		res.SleepPhases = append(res.SleepPhases, model.SleepPhase{
			UserID: userID, Phase: st.Stage, StartDate: ps, EndDate: pe,
			DurationS: int(pe.Sub(ps).Seconds()), ParentContentHash: sess.ContentHash,
		})
	}
	return res, nil
}

// mapWorkout turns one workout into a Workout.
func mapWorkout(w Workout, userID string) (*model.MapResult, error) {
	if w.ID == "" {
		return nil, fmt.Errorf("sandbox: workout: missing id")
	}
	start, err := parse(w.Start)
	if err != nil {
		return nil, fmt.Errorf("sandbox: workout: %w", err)
	}
	end, err := parse(w.End)
	if err != nil || end.Before(start) {
		return nil, fmt.Errorf("sandbox: workout: bad end")
	}
	activity := w.Sport
	if activity == "" {
		activity = "other"
	}
	out := model.Workout{
		UserID:           userID,
		Provider:         Slug,
		SourceSlug:       Slug,
		ExternalID:       w.ID,
		ActivityType:     activity,
		StartDate:        start,
		EndDate:          end,
		DurationS:        w.DurationS,
		ActiveEnergyKcal: w.EnergyKcal,
		DistanceM:        w.DistanceM,
		AvgHeartRate:     w.AvgHR,
		MaxHeartRate:     w.MaxHR,
		Metadata:         metadata(w.Persona, w.Timezone),
		Replaceable:      true,
	}
	out.Metadata["activity_type"] = activity
	out.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":     userID,
		"provider":    Slug,
		"start_date":  start.Format(time.RFC3339),
		"external_id": w.ID,
	})
	return &model.MapResult{Workouts: []model.Workout{out}}, nil
}
