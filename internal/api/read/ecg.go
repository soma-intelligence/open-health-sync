// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// MaxECGSamples bounds the voltage samples served for one recording: an
// Apple Watch recording holds about 15360, a Withings one about 9000. A
// variable so tests can lower it.
var MaxECGSamples = 100000

// MaxECGPerPage bounds the ECG recordings of one page of the merged
// observation list, which loads each one's samples: a page that reaches it
// ends there and its cursor continues after it. A variable so tests can
// lower it.
var MaxECGPerPage = 100

// ECGRecording is one ECG recording as the recording device made it. The
// classification is the device's own, in its maker's vocabulary
// (ECGClassification); nothing here is computed by Open Health Sync.
type ECGRecording struct {
	ID        int64      `json:"id"`
	StartDate time.Time  `json:"start_date"`
	EndDate   *time.Time `json:"end_date"`
	// Classification is the device's label for the stored value, nil when
	// the value is not one of its maker's.
	Classification      *string `json:"classification"`
	ClassificationValue int     `json:"classification_value"`
	// SymptomsStatus says whether the person recorded symptoms with it.
	SymptomsStatus       *string         `json:"symptoms_status"`
	AverageHeartRate     *float64        `json:"average_heart_rate"`
	SamplingFrequencyHz  float64         `json:"sampling_frequency_hz"`
	NumberOfMeasurements int             `json:"number_of_measurements"`
	Metadata             json.RawMessage `json:"metadata"`
	Provenance
}

// ECGSamples are the voltage samples of one recording in offset order: the
// offset from the recording's start in seconds and the voltage in
// microvolts, both as stored.
type ECGSamples struct {
	Count      int       `json:"count"`
	Truncated  bool      `json:"truncated"`
	OffsetsS   []float64 `json:"offsets_s"`
	VoltagesUV []float64 `json:"voltages_uv"`
}

// ECGRecordingDetail is one recording with its samples.
type ECGRecordingDetail struct {
	ECGRecording
	Samples ECGSamples `json:"samples"`
}

// ECGSummary describes one recording's stored samples without them: their
// number, the first and last offset (seconds) and the lowest and highest
// voltage (microvolts). The bounds are nil when no sample is stored.
type ECGSummary struct {
	ECGRecording
	StoredSamples int      `json:"stored_samples"`
	FirstOffsetS  *float64 `json:"first_offset_s"`
	LastOffsetS   *float64 `json:"last_offset_s"`
	MinVoltageUV  *float64 `json:"min_voltage_uv"`
	MaxVoltageUV  *float64 `json:"max_voltage_uv"`
}

// ecgClassifications are the labels of the stored classification, per
// provider: HealthKit's HKElectrocardiogram.Classification raw values for
// Apple Health, and the recording's AFib result for Withings.
var ecgClassifications = map[string]map[int]string{
	"apple_health": {
		0:   "not_set",
		1:   "sinus_rhythm",
		2:   "atrial_fibrillation",
		3:   "inconclusive_low_heart_rate",
		4:   "inconclusive_high_heart_rate",
		5:   "inconclusive_poor_reading",
		6:   "inconclusive_other",
		100: "unrecognized",
	},
	"withings": {0: "negative", 1: "positive", 2: "inconclusive"},
}

// ECGClassification is the label of a stored classification value of
// provider, in the vocabulary of the device's maker; "" when the value is
// not one of its labels.
func ECGClassification(provider string, v int) string {
	return ecgClassifications[provider][v]
}

// ECGClassificationNotSet is the label of a recording the device did not
// classify.
const ECGClassificationNotSet = "not_set"

var ecgSymptoms = map[int]string{0: "not_set", 1: "none", 2: "present"}

// ECGSymptomsStatus is the label of a stored symptoms status: HealthKit's
// HKElectrocardiogram.SymptomsStatus (0 not set, 1 none, 2 present), ""
// for another value.
func ECGSymptomsStatus(v int) string { return ecgSymptoms[v] }

// ecgCols are the columns of an ECGRecording, in the order of its dest.
const ecgCols = `id, start_date, end_date, classification, symptoms_status, average_heart_rate,
	sampling_frequency, number_of_measurements, metadata, provider, source_slug, device_id, ingested_at`

// ecgRow is a scanned ECGRecording before its labels are set.
type ecgRow struct {
	rec      ECGRecording
	symptoms int
}

func (e *ecgRow) dest() []any {
	r := &e.rec
	return []any{&r.ID, &r.StartDate, &r.EndDate, &r.ClassificationValue, &e.symptoms, &r.AverageHeartRate,
		&r.SamplingFrequencyHz, &r.NumberOfMeasurements, &r.Metadata, &r.Provider, &r.Source, &r.Device, &r.IngestedAt}
}

func (e *ecgRow) finish() ECGRecording {
	r := e.rec
	utc(&r.StartDate)
	utc(r.EndDate)
	utc(&r.IngestedAt)
	if c := ECGClassification(r.Provider, r.ClassificationValue); c != "" {
		r.Classification = &c
	}
	if s := ECGSymptomsStatus(e.symptoms); s != "" {
		r.SymptomsStatus = &s
	}
	return r
}

// ECGRecordings lists the user's ECG recordings by start date, metadata
// only. q.Type and q.View are ignored: recordings have no type and no
// merged view (each one is listed).
func ECGRecordings(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[ECGRecording], error) {
	q.Type, q.View = "", ""
	n, err := q.normalize(false)
	if err != nil {
		return Page[ECGRecording]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[ECGRecording]{}, err
	}
	l := list{table: "ohs_data.ecg_recordings", timeCol: "start_date", cols: ecgSel.List("")}
	keys := keysFor(ctx, tenantID, userID)
	return run(ctx, db, tenantID, userID, l, n, func(r pgx.Rows) (ECGRecording, time.Time, int64, error) {
		var e ecgRow
		err := ecgSel.Scan(ctx, keys, r, e.dest())
		rec := e.finish()
		return rec, rec.StartDate, rec.ID, err
	})
}

// ecgRecording reads one recording of the user, ErrNotFound when the user
// or the recording is not the tenant's.
func ecgRecording(ctx context.Context, db Querier, tenantID, userID uuid.UUID, id int64) (ECGRecording, error) {
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return ECGRecording{}, err
	}
	var e ecgRow
	err := ecgSel.Scan(ctx, keysFor(ctx, tenantID, userID), db.QueryRow(ctx, `SELECT `+ecgSel.List("")+` FROM ohs_data.ecg_recordings
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3 LIMIT 1`, tenantID, userID, id), e.dest())
	if errors.Is(err, pgx.ErrNoRows) {
		return ECGRecording{}, ErrNotFound
	}
	if err != nil {
		return ECGRecording{}, fmt.Errorf("read: ecg recording: %w", err)
	}
	return e.finish(), nil
}

// ECGRecordingByID reads one recording of the user with its samples (at
// most MaxECGSamples).
func ECGRecordingByID(ctx context.Context, db Querier, tenantID, userID uuid.UUID, id int64) (ECGRecordingDetail, error) {
	rec, err := ecgRecording(ctx, db, tenantID, userID, id)
	if err != nil {
		return ECGRecordingDetail{}, err
	}
	samples, err := ecgSamples(ctx, db, tenantID, userID, []ECGRecording{rec})
	if err != nil {
		return ECGRecordingDetail{}, err
	}
	return ECGRecordingDetail{ECGRecording: rec, Samples: samples[rec.ID]}, nil
}

// ECGRecordingSummary reads one recording of the user and describes its
// stored samples without returning them.
func ECGRecordingSummary(ctx context.Context, db Querier, tenantID, userID uuid.UUID, id int64) (ECGSummary, error) {
	rec, err := ecgRecording(ctx, db, tenantID, userID, id)
	if err != nil {
		return ECGSummary{}, err
	}
	from, to := ecgSpan([]ECGRecording{rec})
	s := ECGSummary{ECGRecording: rec}
	// The offsets and voltages are sealed: the bounds are taken over the
	// opened samples, a NULL left out as min and max leave it out.
	rows, err := db.Query(ctx, `SELECT `+ecgSampleSel.List("")+` FROM ohs_data.ecg_samples
		WHERE tenant_id = $1 AND user_id = $2 AND ecg_id = $3 AND ts >= $4 AND ts < $5`,
		tenantID, userID, id, from, to)
	if err != nil {
		return ECGSummary{}, fmt.Errorf("read: ecg samples: %w", err)
	}
	defer rows.Close()
	keys := keysFor(ctx, tenantID, userID)
	widen := func(lo, hi **float64, v *float64) {
		if v == nil {
			return
		}
		if *lo == nil || *v < **lo {
			x := *v
			*lo = &x
		}
		if *hi == nil || *v > **hi {
			x := *v
			*hi = &x
		}
	}
	for rows.Next() {
		var off, uv *float64
		if err := ecgSampleSel.Scan(ctx, keys, rows, []any{&off, &uv}); err != nil {
			return ECGSummary{}, fmt.Errorf("read: ecg samples: scan: %w", err)
		}
		s.StoredSamples++
		widen(&s.FirstOffsetS, &s.LastOffsetS, off)
		widen(&s.MinVoltageUV, &s.MaxVoltageUV, uv)
	}
	if err := rows.Err(); err != nil {
		return ECGSummary{}, fmt.Errorf("read: ecg samples: %w", err)
	}
	return s, nil
}

// ecgSampleSel reads the offset and the voltage of an ECG sample.
var ecgSampleSel = vault.Select("ecg_samples", `time_since_sample_start, voltage_uv`)

// ecgSpan bounds the time of the samples of recs: a sample is stored at its
// recording's start plus its offset, and the offsets run over the
// recording's duration, or over its count divided by its frequency (1 Hz
// when the frequency is unknown, as the Withings mapper assumes). An hour
// of margin covers rounding.
func ecgSpan(recs []ECGRecording) (time.Time, time.Time) {
	var from, to time.Time
	for _, r := range recs {
		span := time.Duration(0)
		if r.EndDate != nil && r.EndDate.After(r.StartDate) {
			span = r.EndDate.Sub(r.StartDate)
		}
		hz := r.SamplingFrequencyHz
		if hz <= 0 {
			hz = 1
		}
		if d := time.Duration(float64(r.NumberOfMeasurements) / hz * float64(time.Second)); d > span {
			span = d
		}
		end := r.StartDate.Add(span + time.Hour)
		if from.IsZero() || r.StartDate.Before(from) {
			from = r.StartDate
		}
		if end.After(to) {
			to = end
		}
	}
	return from, to
}

// ecgSamples reads the samples of recs, at most MaxECGSamples each, keyed
// by recording id. Every recording of recs has an entry.
func ecgSamples(ctx context.Context, db Querier, tenantID, userID uuid.UUID, recs []ECGRecording) (map[int64]ECGSamples, error) {
	out := make(map[int64]ECGSamples, len(recs))
	if len(recs) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.ID)
		out[r.ID] = ECGSamples{OffsetsS: []float64{}, VoltagesUV: []float64{}}
	}
	from, to := ecgSpan(recs)
	// One bounded read per recording: the lateral LIMIT stops at the cap
	// instead of reading a recording's every row. The offset is sealed, so
	// the samples are taken in the order of their time (a sample is stored at
	// its recording's start plus its offset, so the two orders agree) and
	// put in offset order once opened.
	rows, err := db.Query(ctx, `SELECT `+ecgSampleSel.List("s")+`, r.id, s.id
		FROM unnest($3::bigint[]) AS r(id)
		CROSS JOIN LATERAL (
			SELECT * FROM ohs_data.ecg_samples
			WHERE tenant_id = $1 AND user_id = $2 AND ecg_id = r.id AND ts >= $4 AND ts < $5
			ORDER BY ts, id LIMIT $6) s
		ORDER BY r.id, s.ts, s.id`, tenantID, userID, ids, from, to, MaxECGSamples+1)
	if err != nil {
		return nil, fmt.Errorf("read: ecg samples: %w", err)
	}
	defer rows.Close()
	type sample struct {
		off, v float64
		id     int64
	}
	read := make(map[int64][]sample, len(recs))
	keys := keysFor(ctx, tenantID, userID)
	for rows.Next() {
		var id int64
		var x sample
		if err := ecgSampleSel.Scan(ctx, keys, rows, []any{&x.off, &x.v}, &id, &x.id); err != nil {
			return nil, fmt.Errorf("read: ecg samples: scan: %w", err)
		}
		read[id] = append(read[id], x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: ecg samples: %w", err)
	}
	for id, samples := range read {
		slices.SortStableFunc(samples, func(a, b sample) int {
			return cmp.Or(cmp.Compare(a.off, b.off), cmp.Compare(a.id, b.id))
		})
		s := out[id]
		if len(samples) > MaxECGSamples {
			s.Truncated = true
			samples = samples[:MaxECGSamples]
		}
		for _, x := range samples {
			s.Count++
			s.OffsetsS = append(s.OffsetsS, x.off)
			s.VoltagesUV = append(s.VoltagesUV, x.v)
		}
		out[id] = s
	}
	return out, nil
}
