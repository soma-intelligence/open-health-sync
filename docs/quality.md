# Data quality score

Every figure a wearable reports is a little off. A wrist heart rate is a couple of beats off at
rest and often ten or more while running; a ring's night of sleep is usually 15 to 40 minutes
longer than a sleep lab would score it. The quality score says, for one figure, how far off it is
likely to be and how sure that is. It is descriptive data quality metadata: it never replaces the
reported value and makes no diagnosis.

Phase 1 is the scoring library (`internal/quality`) and its backtest (`internal/quality/bench`).
Nothing in the API or the database uses it yet; that is phase 2 of
[the spec](specs/quality-score.md), which is the contract this page explains.

## What a score says

For a figure, the score gives:

| Field | In plain words |
|---|---|
| `q` | the probability that the figure is within the metric's tolerance of the truth, 0 to 1 |
| `grade` | A when `q` is at least 0.9, B from 0.75, C from 0.5, D below, X when the value cannot be real |
| `tolerance` | what "close enough" means for the metric (5 bpm for a heart rate, 30 min for a night's sleep) |
| `bias` | how far this source tends to read high (positive) or low in this context |
| `bias_sd`, `sigma` | how unsure the bias is, and how much single readings scatter around it |
| `estimate` | the value with the bias taken out, a separate field: the reported value stays the value |
| `interval_68`, `interval_95` | where the true value is, with that tested coverage |
| `context` | rest, exercise, sleep, day or unknown, inferred from heart rate, steps, motion and sleep windows |
| `components` | what the prior, the agreement between sources, the residual model and the plausibility checks did |
| `flags` | what was noticed: `suspect_range`, `rate_jump`, `low_signal`, `relay_copy`, `single_source`... |
| `evidence` | the prior's citation and evidence tag, the model and calibration versions, the calibration group |

For HRV (`hrv_rmssd`, `hrv_sdnn`) errors are relative, so `tolerance`, `bias`, `bias_sd` and
`sigma` are in natural log units (0.1 is about 10%); the value, the estimate and the intervals
stay in milliseconds.

The tolerances are the spec's table: heart rate 5 bpm (10% above 100 bpm), nightly resting heart
rate 3 bpm, HRV 10%, total sleep 30 min, stage minutes 20 min, daily steps and active energy 10%,
SpO2 2 points, VO2 max 3.5 mL/kg/min, glucose 15 mg/dL below 100 and 15% above, blood pressure
5 mmHg, weight 0.5 kg, body fat 2 points (`internal/quality/metrics.go`).

## How it is worked out

The error of a figure, value minus truth, is modelled as a bias plus a Student t: heavier tailed
than a normal, because motion artifacts and cadence lock make large misses more common than a bell
curve says. The stages:

1. **Copies are one figure** (`independence.go`). A Garmin heart rate relayed into Apple Health is
   the Garmin's reading again, with the same error. Copies are grouped by provenance (same
   producer, same original id), and without provenance by an equal value at the same start, each
   copy joined to the nearest such reading only. Metrics computed by one algorithm (Apple Watch
   heart rate and HRV, Garmin heart rate and energy) count as one error family. SDNN and RMSSD,
   asleep and in bed, are never compared with each other.
2. **Plausibility.** A value outside the metric's hard limits is grade X. Outside the soft limits,
   or with a jump, a spike or a device's own weak signal flag, sigma is doubled per kind of flag.
3. **Priors** (`priors.json`). Published error figures per metric, device and context, each with
   its citation and an evidence tag: V checked against the paper, S seen in secondary sources, U a
   default not rechecked. A device nobody validated falls back to its family, then to its kind of
   sensor (wrist PPG, ring, chest strap, cuff, scale, CGM), then to a wide generic prior. With one
   source and nothing else, the score is the prior's.
4. **Agreement between a user's sources** (`agreement.go`). Two independent sources bound the sum
   of their error variances (Bland-Altman on paired differences); three give each one's variance
   with no reference at all (triple collocation, covariance form, a negative variance clipped and
   flagged). The per-user figure is shrunk toward the prior in log space, the prior counting as
   50, 20 or 10 observations for a V, S or U figure and the data as its effective sample size
   after autocorrelation, `n (1 - r1) / (1 + r1)`. Bias is only identified against an anchor
   (a chest strap, a cuff); otherwise it stays the prior's.
5. **Residual model** (`gbrt.go`, `model.go`). Gradient boosted trees, written in Go, predict the
   log absolute error of a figure from its context: metric, device family, context, value, distance
   to the user's baseline, rate of change, hour, motion, steps, flags, number of independent
   sources. The prediction scales sigma, within a factor of four either way. It applies only to the
   metrics it was trained on with at least 3000 figures; today that is heart rate. The shipped model
   is three such ensembles, each fitted on two of three folds of the subjects, averaged in log scale.
6. **Calibration** (`conformal.go`). Split conformal, Mondrian per group (metric, device family,
   context), fitted on people the model never saw. Each group learns three things from the
   normalized residuals: a shift (the median, the part of the bias the prior missed against the
   references), a factor gamma that makes the 90% interval cover 90%, and the degrees of freedom
   that make the 50 to 95% intervals follow. Figures of one person are autocorrelated, so sizes
   count people too: a group needs 50 figures and 8 subjects for its own shift and gamma (200 and
   10 for its degrees of freedom), and otherwise falls back to its parent (metric and family, then
   metric), never to another metric. The calibration is fitted on each fold's residuals under the
   ensemble that did not see that fold, which is exactly what the backtest validates.
7. **The score.** `q` is `T((tol - bias) / s) - T((-tol - bias) / s)` with the calibrated bias and
   scale, then passed through the group's q map: a monotone (isotonic) curve fitted on the same
   held-out figures, because a t with the right interval coverage can still put its central mass,
   which `q` reads, in the wrong place (a q map needs 300 figures and 10 subjects). Intervals are
   `estimate +/- gamma * t(p) * s`. The spec writes these formulas out.

## The backtest

The bench hides the reference, scores every figure, and compares. The split is leave one subject
out: for each person, the model and calibration are built by the shipped procedure on everyone
else. It reports, per dataset, metric, device and context: coverage of the 50, 80, 90 and 95%
intervals (PICP), mean width of the 90% interval, interval score, CRPS, the reliability of `q` (ECE
over 20 equal-mass bins, and over 10 equal-width bins beside it) and its Brier score, against the
priors alone and against the priors with conformal factors (the calibrated baseline). It also
publishes coverage per subject. The latest numbers are in [quality/report.md](quality/report.md).

Figures of one person are autocorrelated (a missed reading sits next to other misses), so every
headline number and gate carries a 95% subject-cluster bootstrap interval: people are resampled,
all their figures with them, and comparisons between variants are paired on the same resamples.
ECE over 20 bins is biased upward in small groups, so each group also shows the ECE a perfectly
calibrated `q` would have on the same figures.

Device timestamps are used as written (lag 0), as production sees them. The E4 stamps its heart
rate 30 to 40 s after the beats it describes; a variant that shifts each device by the lag that
best matches the reference is reported beside the headline, for comparison only. Production
applies no lag.

The gates for a model version are PICP90 in [0.88, 0.92] overall and in [0.85, 0.95] for every
group of at least 50 figures and 5 subjects, ECE of `q` below 0.03 overall and per such group, and
CRPS no worse than the priors alone and than the calibrated baseline. They are checked on the
reference datasets. For model `2026-10-25.1` at lag 0, all pass except ECE per group: 7 of the 9
heart rate groups sit between 0.038 and 0.072, above both the gate and their noise floor (0.019 to
0.038), because people and protocols differ more than 15 to 24 subjects per device can calibrate.
The synthetic source is reported beside them as a check of the machinery, not as a gate: its errors
are drawn from the priors, so the priors are right there by construction.

A model version is frozen once written. Walch et al. 2019 is held out of training and calibration
entirely and scores the frozen version (`TestWalchEval`, [quality/walch-eval.md](quality/walch-eval.md)).
On its 31 nights the sleep duration intervals cover every night (PICP90 1.00) but are about 200
minutes wide for an error of 28 minutes on average (SD 17): the sleep priors are far too cautious,
and the synthetic-only calibration does not narrow them. Sleep scores are honest but not sharp until
a sleep dataset can calibrate them.

### Datasets

| Dataset | Licence | What it can tell | What it cannot |
|---|---|---|---|
| Walch et al. 2019, PhysioNet sleep-accel 1.0.0 | ODC-By 1.0 | total sleep from Apple Watch motion against polysomnography, 31 nights | heart rate error (the release has no ECG), Apple's own sleep figures |
| GalaxyPPG, Zenodo 14635823 (Sci Data 12:892, 2025) | CC BY 4.0 | Galaxy Watch 5 and Empatica E4 heart rate as each device computed it, against a Polar H10, 24 people at rest, under stress, typing, walking, jogging, running; triple collocation without a reference | sleep, nightly figures, free living |
| PPG-DaLiA, UCI 495 (Reiss et al., Sensors 19:3079, 2019) | CC BY 4.0 | Empatica E4 heart rate against chest ECG, 15 people through daily life activities | consumer watch or ring outputs, sleep |
| Synthetic, `internal/synth` | in the repository | whether the machinery recovers what it should when the priors are right: coverage, shrinkage, triple collocation, copy detection | whether the priors are right |

No public dataset pairs the exported figures of Oura, WHOOP or Garmin with a reference. Their
scores rest on the published figures in `priors.json` (Dial et al. 2025 for nightly resting heart
rate and HRV, Schyvens et al. 2025 for sleep) and on the generic fallbacks. Only derived numbers
(tree splits, conformal factors, summary error figures) enter the repository, never a dataset file.

### Running it

The data live outside the repository, in `OHS_QUALITY_DATA` (by default an `ohs-quality-data`
directory next to the checkout):

```
$OHS_QUALITY_DATA/
  walch/walch.zip           -> walch/extracted/<release>/{heart_rate,labels,motion,steps}/
  galaxyppg/GalaxyPPG.zip   -> galaxyppg/extracted/Dataset/   (Meta.csv, Event.csv, the
                               GalaxyWatch, E4 and PolarH10 HR.csv and ACC.csv files)
  ppg-dalia/ppg_dalia.zip   -> ppg-dalia/csv/                 (written by the converter below)
```

PPG-DaLiA ships its references inside Python pickles, which Go cannot read. A one-off offline
converter reads them straight from the zip with a restricted unpickler that resolves only numpy
array names, each to a local stub, so nothing from the archive is executed or extracted. Run it
with the system Python in isolated mode, from outside the data directory:

```sh
python3 -I scripts/quality/dalia_to_csv.py "$OHS_QUALITY_DATA/ppg-dalia/ppg_dalia.zip" "$OHS_QUALITY_DATA/ppg-dalia/csv"
```

Then:

```sh
go test -tags bench ./internal/quality/bench -run 'TestBench$' -v                       # prints the report
OHS_QUALITY_WRITE=1 go test -tags bench ./internal/quality/bench -run 'TestBench$' -v   # writes it
OHS_QUALITY_WRITE=1 go test -tags bench ./internal/quality/bench -run TestWalchEval -v  # held out evaluation
```

The second form writes `docs/quality/report.md`, `internal/quality/model.json` and
`internal/quality/calibration.json` (the last two are embedded in the binary); bump `ModelVersion`
in the bench before refitting, since a written version is frozen. The third scores Walch with the
embedded model and refuses to run against another version. A dataset that is not on disk is skipped
and the report says so. The run is deterministic: the same data give the same files. It takes about
two minutes, most of it the bootstrap.

## Limits

- The reference datasets are small (15 and 24 people, a couple of hours each, plus 31 held out
  nights), young and in a lab or semi-natural protocol. A calibration group's factors carry that
  sampling error: `q` is calibrated overall but not within each device and context (ECE 0.04 to
  0.07 per group), and 9 of 39 people have PICP90 outside [0.85, 0.95].
- Calibration groups for devices seen only in the synthetic source (Apple Watch, Garmin, Oura heart
  rate) reproduce the priors; they are not evidence about those devices.
- Triple collocation assumes independent errors. On GalaxyPPG the two wrist devices err together
  during movement, and it underestimates their error by about a third: it is a lower bound there.
- Shared context, not just shared algorithms, correlates errors: two wrist devices on one moving
  arm are less independent than the count of sources says.
- The context is inferred from the device's own heart rate and motion, so a reading pushed high by
  noise can be called exercise; the per group shift absorbs the average effect, not each case.
