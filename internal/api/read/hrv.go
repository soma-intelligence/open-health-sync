// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"math"
	"time"
)

// Bounds of the HRV derivation. An interval outside [MinRRMs, MaxRRMs] (200
// to 30 bpm) is not a plausible heartbeat; one that differs from the
// interval before it by more than MaxRRChange of that interval is taken for
// an ectopic beat or a missed detection (the usual 20 % rule). Neither is used.
const (
	MinRRMs     = 300.0
	MaxRRMs     = 2000.0
	MaxRRChange = 0.20
	// MinHRVPairs is the number of accepted successive pairs below which
	// RMSSD, SDNN and pNN50 are left null: a few beats give a number, not
	// a measure.
	MinHRVPairs = 10
	// adjacencyMs is how far a beat's time may be from the previous beat's
	// time plus its own interval and still count as the next beat. Beat
	// times are rounded to the nanosecond and intervals to the microsecond,
	// so they agree far closer than this; a gap in the series (a beat
	// HealthKit marks precededByGap, which stores no interval) leaves
	// at least one whole beat between them.
	adjacencyMs = 5.0
)

// Beat is one stored RR interval, stamped at the beat that ends it.
type Beat struct {
	At   time.Time
	RRMs float64
}

// HRVStats are the time domain measures of one series of beats.
type HRVStats struct {
	// Intervals counts the stored intervals; Accepted those used.
	Intervals int `json:"intervals"`
	Accepted  int `json:"accepted_intervals"`
	// Pairs counts the successive accepted intervals RMSSD and pNN50 use.
	Pairs    int      `json:"successive_pairs"`
	MeanRRMs *float64 `json:"mean_rr_ms"`
	MeanHR   *float64 `json:"mean_heart_rate_bpm"`
	SDNNMs   *float64 `json:"sdnn_ms"`
	RMSSDMs  *float64 `json:"rmssd_ms"`
	PNN50    *float64 `json:"pnn50_percent"`
}

// SummarizeBeats derives the HRV of one series. beats must be in time order.
//
// An interval is accepted when it is in [MinRRMs, MaxRRMs] and, when the
// beat before it is the directly preceding beat, differs from that beat's
// interval by at most MaxRRChange. A successive pair is two accepted
// intervals of directly consecutive beats: a pair is never formed across a
// gap or a rejected interval. Mean RR, mean heart rate and SDNN use the
// accepted intervals; RMSSD and pNN50 the pairs. SDNN, RMSSD and pNN50 are
// nil below MinHRVPairs pairs, the means when nothing is accepted.
func SummarizeBeats(beats []Beat) HRVStats {
	var s HRVStats
	var sum, sumSq, diffSq float64
	var nn50 int
	var prev *Beat
	prevAccepted := false
	for i := range beats {
		b := beats[i]
		if prev != nil && !b.At.After(prev.At) {
			// A repeated beat time: the same beat stored twice.
			continue
		}
		s.Intervals++
		adjacent := prev != nil &&
			math.Abs(float64(b.At.Sub(prev.At))/float64(time.Millisecond)-b.RRMs) <= adjacencyMs
		ok := b.RRMs >= MinRRMs && b.RRMs <= MaxRRMs
		if ok && adjacent && prev.RRMs >= MinRRMs && prev.RRMs <= MaxRRMs &&
			math.Abs(b.RRMs-prev.RRMs) > MaxRRChange*prev.RRMs {
			ok = false
		}
		if ok {
			s.Accepted++
			sum += b.RRMs
			sumSq += b.RRMs * b.RRMs
			if adjacent && prevAccepted {
				d := b.RRMs - prev.RRMs
				s.Pairs++
				diffSq += d * d
				if math.Abs(d) > 50 {
					nn50++
				}
			}
		}
		prev, prevAccepted = &beats[i], ok
	}
	if s.Accepted > 0 {
		mean := sum / float64(s.Accepted)
		hr := 60000 / mean
		s.MeanRRMs, s.MeanHR = round3(mean), round3(hr)
	}
	if s.Pairs >= MinHRVPairs {
		n := float64(s.Accepted)
		// Sample standard deviation; the pairs bound makes n at least 11.
		v := (sumSq - sum*sum/n) / (n - 1)
		s.SDNNMs = round3(math.Sqrt(math.Max(v, 0)))
		s.RMSSDMs = round3(math.Sqrt(diffSq / float64(s.Pairs)))
		s.PNN50 = round3(100 * float64(nn50) / float64(s.Pairs))
	}
	return s
}

// round3 rounds to three decimals, so a re-read gives the same digits.
func round3(v float64) *float64 {
	r := math.Round(v*1000) / 1000
	return &r
}
