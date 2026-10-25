// SPDX-License-Identifier: AGPL-3.0-only

// Package quality says how accurate and how precise each stored figure is.
//
// For a figure (a heart rate sample, a night's resting heart rate, a day's
// steps) it estimates the bias of its source in its context, the random error
// around that bias, calibrated intervals for the true value, and one number q,
// the probability that the figure is within the metric's clinical tolerance
// of the truth. The contract is docs/specs/quality-score.md; docs/quality.md
// explains it in plain words.
//
// The score is descriptive data quality metadata. It never replaces the
// reported value and makes no diagnosis.
//
// The package is pure Go with no database: metric tolerances (metrics.go),
// published error figures (priors.json), the Student t (dist.go), agreement
// between sources (agreement.go), the context of a figure (context.go), copy
// detection (independence.go), the residual tree model (gbrt.go), conformal
// calibration (conformal.go), the scoring rules the backtest reports
// (eval.go) and the score itself (score.go). The backtest on public reference
// datasets lives in the bench subpackage, behind the bench build tag.
package quality
