# Quality score: how accurate and how precise each figure is

Status: draft, 2026-10-24. Research behind it: the data quality research note kept with the audit
(literature, formulas, priors, datasets). This spec is the contract the code follows.

## Goal

For every stored figure (a heart rate sample, a night's resting heart rate, a sleep duration, a
day's steps, a glucose reading, a blood pressure, a weight, later a lab result or a genotype
call), Open Health Sync answers four questions:

1. How far is it likely to be from the true value, on average (trueness, bias)?
2. How much does it scatter around that (precision, random error)?
3. What interval holds the true value, with a stated, tested coverage?
4. How likely is it to be within a clinically meaningful tolerance (one number, 0 to 1)?

The score is descriptive data quality metadata. It never replaces the reported value and makes
no diagnosis. The raw value stays the value; a bias-corrected estimate is a separate field.

## Output contract

```
quality {
  q            P(|value - true| <= tolerance), 0..1
  grade        A (q >= 0.9), B (0.75..0.9), C (0.5..0.75), D (< 0.5), X (rejected)
  tolerance    the metric's tolerance, in the value's unit (or relative, see metrics.go)
  bias         estimated bias of this source in this context (value - true), unit of value
  bias_sd      uncertainty of that bias
  sigma        estimated random error (1 SD) of this source in this context
  estimate     value - bias
  interval_68  [lo, hi] for the true value, calibrated
  interval_95  [lo, hi] for the true value, calibrated
  context      rest | exercise | sleep | day | unknown
  components   { prior, agreement, ml, plausibility } (what each contributed)
  flags        [suspect_range, rate_jump, relay_copy, definition_mismatch, single_source, ...]
  evidence     { sources_independent, prior_citation, prior_tag (V|S|U), model_version,
                 calibration_version }
}
```

Score formula, with the predictive error distribution a Student t with location `bias`,
scale `s = sqrt(bias_sd^2 + sigma^2)` and `nu` degrees of freedom:

```
q = T_nu((tol - bias) / s) - T_nu((-tol - bias) / s)
```

Intervals are `estimate +/- gamma_g * t_nu(p) * s`, where `gamma_g` is the conformal scale factor
of the figure's calibration group `g` (metric x device family x context), learned so that the
backtested coverage equals the nominal one.

## Metrics and tolerances (v1)

Defaults, configurable per tenant later. Relative tolerances are written `x%`.

| Metric | Unit | Tolerance | Basis |
|---|---|---|---|
| heart_rate | bpm | 5 bpm, or 10% above 100 bpm | CTA-2065 MAPE 10% |
| resting_heart_rate | bpm | 3 bpm | literature, nightly RHR |
| hrv_rmssd, hrv_sdnn | ms | 10% (log scale) | literature |
| sleep duration (total sleep) | min | 30 min | PSG validation practice |
| sleep stage minutes | min | 20 min | PSG validation practice |
| step_count (daily) | count | 10% | literature |
| active_energy_burned (daily) | kcal | 10% | literature |
| oxygen_saturation | % | 2 points | ISO 80601-2-61 (ARMS 4%) |
| vo2_max | mL/kg/min | 3.5 (1 MET) | literature |
| blood_glucose | mg/dL | 15 below 100, 15% above | ISO 15197 |
| blood_pressure_systolic, diastolic | mmHg | 5 | ISO 81060-2 |
| body_mass | kg | 0.5 | practice |
| body_fat_percentage | % | 2 points | practice |

Blood, urine and genome tests are phase 3 (see below): their tolerances come from allowable
total error tables (CLIA, biological variation) and from truth-set concordance, not from this
table.

## Stages

**Stage 0. Independence.** Copies are one observation, not two. A sample relayed through Apple
Health or Health Connect from a vendor we also read directly (Garmin, Oura, WHOOP, Withings,
Polar, Fitbit) is grouped with the direct one by provenance (`source_slug`, bundle id) and, when
provenance is missing, by equal value and start within the fusion tolerance. Shared-algorithm
families (Apple Watch HR and Apple Watch HRV, Garmin HR and Garmin energy) are one error family
for the agreement estimators. Definitions are compared like for like (`hrv_method` SDNN vs RMSSD,
asleep vs in bed, active vs total energy).

**Stage 1. Plausibility.** The validator's hard limits reject (grade X). Soft limits, rate of
change jumps and isolated spikes inflate `sigma` by a flag multiplier (defaults x2, learned in
stage 4).

**Stage 2. Priors.** An embedded, versioned table (`internal/quality/priors.json`) of published
error figures per metric, device family and context: bias mean and SD, sigma, degrees of
freedom, the citation, and the evidence tag (V checked against the paper, S secondary, U not
rechecked). Fallback chain: device model, then family, then sensor type (wrist PPG, ring PPG,
chest ECG, cuff, scale, CGM), then a generic wide prior. With one source and nothing else, the
score comes from the prior alone.

**Stage 3. Agreement between sources, per user.** For each user and metric with two or more
independent sources, aligned on the metric's grain (5 minute buckets for readings, nights for
sleep and nightly figures, days for totals), over a sliding 60 day window:

- two sources: Bland-Altman on the paired differences (mean, SD), which bounds the sum of the
  two error variances;
- three or more: triple collocation (covariance form) gives each source's error variance
  without a reference;
- the per-user estimate is shrunk toward the prior, precision weighted, with the effective
  sample size corrected for autocorrelation (`n_eff = n (1 - r1) / (1 + r1)`):
  `log sigma_post = (w0 log sigma_prior + n_eff log sigma_data) / (w0 + n_eff)`,
  `w0` = 50 for V priors, 20 for S, 10 for U.

Bias is only identifiable against an anchor (a chest strap, a cuff, a fingerstick, a lab) or a
prior. Without an anchor, bias stays the prior's.

**Stage 4. Residual model (machine learning).** A gradient boosted tree model, written in Go
with no new dependency, predicts the log of the absolute error from context features: metric,
device family, context (rest, exercise, sleep from heart rate and steps), value level, distance
to the user's baseline, rate of change, hour of day, flags, and the number of independent
sources. Trained offline on the reference datasets (below) and on synthetic data with known
error; its trees are exported as JSON and embedded. At run time it scales `sigma` per figure.

**Stage 5. Calibration.** Split conformal (Mondrian per calibration group) on held out subjects
gives `gamma_g`. Groups with fewer than 50 calibration points fall back to their parent group.

## Where it runs

- `internal/quality`: pure Go, no database. Metrics, tolerances, priors, the t distribution, the
  agreement estimators, the tree model, conformal calibration, the score. Unit tested with
  goldens.
- `internal/quality/bench`: loaders for the reference datasets and the backtest harness, behind
  the `bench` build tag, reading data from `OHS_QUALITY_DATA` (never committed).
- Storage (phase 2): `ohs_data.source_quality` holds the per user, metric, source, device family
  and context estimates (bias, bias_sd, sigma, n_eff, method, versions, computed_at), rebuilt
  nightly by the etl role and by `ohs quality rebuild`.
- Read side (phase 2): `include=quality` on samples, daily and sleep reads;
  `GET /v1/users/{id}/quality` (per source and metric summary); the MCP tool `data_quality`; a
  FHIR extension on Observation. A `fusion.Scorer` backed by `source_quality` ranks the most
  accurate source first in the merged view, stats and rollups.

## Reference data and backtest

| Dataset | Licence | What it gives |
|---|---|---|
| Walch et al. 2019 (PhysioNet sleep-accel) | ODC-By 1.0 | Apple Watch heart rate and motion, 31 nights with polysomnography: sleep and nightly HR |
| GalaxyPPG (Sci Data 2025, Zenodo 14635823) | CC BY 4.0 | Galaxy Watch 5 heart rate as computed by the watch, against a chest ECG, 24 people in daily activities and stress: wrist HR error by context |
| PPG-DaLiA (UCI 495) | CC BY 4.0 | Wrist PPG against chest ECG, 15 people, daily activities: HR error by activity, a PPG-derived HR as a second wrist source |
| Synthetic (`internal/synth`) with per device bias and noise profiles | in repo | known truth for every metric, every provider, relays and duplicates |

The bench hides the reference, runs stages 0 to 5, and reports per metric, device and context:
coverage of the 50, 80, 90 and 95% intervals (PICP), mean interval width, interval score, CRPS,
the reliability (expected calibration error) of `q`, and Brier score. Splits are leave one
subject out. Acceptance gates for a model version: PICP90 in [0.88, 0.92] overall and in
[0.85, 0.95] per group with at least 50 points, ECE of `q` below 0.03, CRPS no worse than the
prior only baseline. The bench writes a report the docs publish with each model version.

Only derived numbers (tree splits, conformal factors, summary error figures) enter the repo,
with attribution to each dataset as its licence asks.

## Phases

1. Core and bench: `internal/quality`, priors, agreement estimators, tree model, conformal,
   bench on the three datasets and synthetic data, published report. No API change.
2. Service: `source_quality` table and nightly job, read side, Scorer, MCP, FHIR extension, docs.
3. Blood, urine and genome tests: the same output contract, with `q` defined per result type,
   once those records can be ingested.
   - **Blood (continuous lab values).** Two scores per result. The analytic `q` uses the CLIA
     2024 acceptance limit of the analyte as tolerance (glucose 6 mg/dL or 8%, HbA1c 8%, LDL
     20%, TSH 20% or 0.2, ...) and the lab method's imprecision and bias, taken from EFLM
     biological variation targets when the lab gives none (imprecision 0.5 CVI, bias
     0.25 sqrt(CVI^2 + CVG^2)). The biologic `q` adds within person variation (CVI) and answers
     whether the value is the person's usual level. Between two results, the reference change
     value (log normal form) says whether a change is real. Capillary (finger prick, mailed)
     samples add a specimen error term; pre-analytical flags (not fasting, hemolysis, shipping
     delay, biotin, hard exercise in the previous 48 h from wearable data) inflate sigma. Values
     read from a PDF carry an extraction confidence term.
   - **Urine.** Quantitative results (albumin to creatinine ratio, creatinine) follow the blood
     rules with urine biological variation (ACR CVI about 30% in CKD; a change must pass about
     +141% / -58%) and hydration correction. Dipstick and smartphone read strips are ordinal:
     `q` is the posterior probability of the correct colour block (and of being within one
     block) from the strip's confusion matrix against lab results.
   - **Genome.** Per genotype call, `q` is the probability the call is correct, from call metrics
     (QUAL, GQ recalibrated against GIAB, depth, allele balance) and a prior on the variant's
     population frequency, so a rare variant from a SNP array (positive predictive value as low
     as 4 to 16%) is graded D with "needs clinical confirmation". Per sample: call rate, Ti/Tv,
     het/hom, contamination, and GIAB concordance (precision, recall, F1) for the sequencing
     method. Polygenic scores are shown as percentile intervals, never single numbers. Quality
     of the measurement stays separate from what a variant means clinically.
   - **Ingestion needed:** FHIR DiagnosticReport, Observation and Specimen (patient portals,
     Apple Health clinical records), lab PDFs with extraction confidence, urine strip results,
     VCF or gVCF, and 23andMe or AncestryDNA raw text converted to VCF.
