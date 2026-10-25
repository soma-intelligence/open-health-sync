// SPDX-License-Identifier: AGPL-3.0-only

package quality

// Flag names something the score noticed about a figure or an estimate.
type Flag string

// Flags. The first group inflates sigma (stage 1); the rest explain.
const (
	FlagSuspectRange       Flag = "suspect_range"       // outside the metric's soft limits
	FlagRateJump           Flag = "rate_jump"           // changed faster than the body can
	FlagSpike              Flag = "spike"               // an isolated value far from its neighbours
	FlagRejected           Flag = "rejected"            // outside the hard limits, grade X
	FlagRelayCopy          Flag = "relay_copy"          // a copy of a figure read elsewhere
	FlagDefinitionMismatch Flag = "definition_mismatch" // compared with a figure of another definition
	FlagSingleSource       Flag = "single_source"       // no other independent source
	FlagNegativeVariance   Flag = "negative_variance"   // triple collocation gave a negative variance, clipped
	FlagUnknownMetric      Flag = "unknown_metric"      // the metric is not registered
)

// inflating says whether a flag inflates sigma.
func (f Flag) inflating() bool {
	switch f {
	case FlagSuspectRange, FlagRateJump, FlagSpike:
		return true
	}
	return false
}
