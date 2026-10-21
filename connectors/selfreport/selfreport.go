// SPDX-License-Identifier: AGPL-3.0-only

// Package selfreport is the self report connector. An app or a tenant server
// sends a person's own ratings to POST /v1/users/{id}/self-reports, which
// checks them (internal/selfreport) and stores them as one NDJSON part like
// any device upload; the ETL then hands each line to Map, which writes one
// quantity sample of type reported_<kind> per report.
package selfreport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	report "github.com/soma-intelligence/open-health-sync/internal/selfreport"
)

// Slug is the provider name of this connector, and the source slug of every
// record it maps.
const Slug = report.Slug

func init() {
	connector.Register(New())
}

// Connector implements connector.DeviceConnector for self reports.
type Connector struct{}

var _ connector.DeviceConnector = (*Connector)(nil)

// New returns a ready self report connector.
func New() *Connector { return &Connector{} }

// Slug returns "self_report".
func (c *Connector) Slug() string { return Slug }

// Enabled is always true: the connector needs no operator keys.
func (c *Connector) Enabled(connector.Config) bool { return true }

// ErrInvalidLine reports an uploaded line that is not a valid report.
var ErrInvalidLine = errors.New("self_report: invalid upload line")

// ErrNoLines reports an upload with no line at all.
var ErrNoLines = errors.New("self_report: upload holds no line")

// ParseUpload validates a decompressed part: every non-blank line must be a
// report that passes Check, and there must be at least one. The upload
// handler stores the original bytes verbatim, so emit is not used.
func (c *Connector) ParseUpload(_ connector.UploadMeta, body io.Reader, _ connector.RawSink) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 1024), report.MaxLineBytes+1)
	lines, num := 0, 0
	for sc.Scan() {
		num++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if _, err := report.Decode(line); err != nil {
			return fmt.Errorf("%w: line %d %s", ErrInvalidLine, num, err.Error())
		}
		lines++
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return fmt.Errorf("%w: line %d is longer than %d bytes", ErrInvalidLine, num+1, report.MaxLineBytes)
		}
		return fmt.Errorf("self_report: read upload: %w", err)
	}
	if lines == 0 {
		return ErrNoLines
	}
	return nil
}

// Map turns one stored line into one quantity sample. The ETL hands the line
// as decoded JSON; it is checked again, since a raw file may be re-processed
// long after it was stored.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	line, err := json.Marshal(raw.Fields)
	if err != nil {
		return model.MapResult{}, fmt.Errorf("self_report: map: %w", err)
	}
	r, err := report.Decode(line)
	if err != nil {
		return model.MapResult{}, fmt.Errorf("%w: %s", ErrInvalidLine, err.Error())
	}
	return model.MapResult{QuantitySamples: []model.QuantitySample{report.Sample(raw.UserID, r)}}, nil
}

// DataTypes implements connector.DataTypes: Map builds its quantity types
// from the report kind, so they are listed here rather than read from the
// source.
func (c *Connector) DataTypes() []string {
	out := []string{connector.KindSample}
	for _, s := range model.SelfReportScales {
		out = append(out, s.Type)
	}
	sort.Strings(out[1:])
	return out
}
