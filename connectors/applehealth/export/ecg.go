// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

// defaultECGRate is the Apple Watch sampling frequency, used when the CSV
// header gives none.
const defaultECGRate = 512.0

// ecgClassifications maps the classification an English export writes to
// HKElectrocardiogram.Classification raw values. Any other text, a
// translated one included, becomes 100 (unrecognized).
var ecgClassifications = map[string]int{
	"sinus rhythm":        1,
	"atrial fibrillation": 2,
	"heart rate under 50": 3,
	"low heart rate":      3,
	"heart rate over 120": 4,
	"heart rate over 150": 4,
	"high heart rate":     4,
	"poor recording":      5,
	"inconclusive":        6,
}

func (c *converter) readECGs() error {
	files, err := c.exp.ecgFiles()
	if err != nil {
		return err
	}
	for _, name := range files {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		c.stats.Records++
		if err := c.ecg(name); err != nil {
			return err
		}
	}
	return nil
}

// ecg converts one electrocardiograms/*.csv file: a header of "key,value"
// rows (the name and birth date among them, never read), then one voltage
// per row in microvolts. It becomes one part: the recording line, then its
// voltage lines, so the server attaches the voltages to it.
func (c *converter) ecg(name string) error {
	rc, err := c.exp.openFile(name)
	if err != nil {
		c.stats.Invalid[TypeECG]++
		return nil
	}
	defer rc.Close()
	r := csv.NewReader(rc)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	var (
		recorded       time.Time
		classification = 100
		symptoms       = 0
		rate           = defaultECGRate
		microvolts     []float64
	)
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			c.stats.Invalid[TypeECG]++
			return nil
		}
		if len(row) == 1 {
			if v, err := strconv.ParseFloat(strings.TrimSpace(row[0]), 64); err == nil {
				microvolts = append(microvolts, v)
			}
			continue
		}
		key, value := strings.TrimSpace(row[0]), strings.TrimSpace(row[1])
		switch {
		case key == "Recorded Date":
			recorded, _ = parseExportTime(value)
		case key == "Classification":
			if v, ok := ecgClassifications[strings.ToLower(value)]; ok {
				classification = v
			}
		case key == "Symptoms":
			switch {
			case value == "":
				symptoms = 0 // not set
			case strings.EqualFold(value, "none"):
				symptoms = 1
			default:
				symptoms = 2
			}
		case key == "Sample Rate":
			if v, err := strconv.ParseFloat(strings.Fields(value + " x")[0], 64); err == nil && v > 0 {
				rate = v
			}
		case recorded.IsZero():
			// A translated header: the recording date is still the one value
			// written as an export date.
			if t, err := parseExportTime(value); err == nil {
				recorded = t
			}
		}
	}
	if recorded.IsZero() || len(microvolts) == 0 {
		c.stats.Invalid[TypeECG]++
		return nil
	}
	end := recorded.Add(time.Duration(float64(len(microvolts)) / rate * float64(time.Second)))
	s, ok := c.newSample(TypeECG, commonAttrs{
		StartDate: recorded.Format(exportLayout),
		EndDate:   end.Format(exportLayout),
	}, nil)
	if !ok {
		return nil
	}
	s.fields["classification"] = classification
	s.fields["symptomsStatus"] = symptoms
	s.fields["samplingFrequency"] = rate
	s.fields["numberOfVoltageMeasurements"] = len(microvolts)

	p := c.newSolo(TypeECG, s.day)
	if err := p.add(s.fields); err != nil {
		return err
	}
	for i, uv := range microvolts {
		// The contract carries volts, as HealthKit gives them.
		if err := p.add(map[string]any{
			"timeSinceSampleStart": float64(i) / rate,
			"value":                uv / 1e6,
		}); err != nil {
			return err
		}
	}
	return p.flush()
}
