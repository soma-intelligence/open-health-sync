// SPDX-License-Identifier: AGPL-3.0-only

// Package labs is the lab result connector. An app or a tenant server sends
// lab results to POST /v1/users/{id}/labs, as FHIR R4 (a Bundle, a
// DiagnosticReport or an Observation of category laboratory) or in the
// simple JSON form; the handler checks the body (internal/labs) and stores
// it as one NDJSON line like any device upload. The ETL then hands the line
// to Map, which writes one observation per result (canonical code
// lab:loinc:<code> or lab:local:<code>).
package labs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	report "github.com/soma-intelligence/open-health-sync/internal/labs"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Slug is the provider name of this connector, and the source slug of every
// record it maps.
const Slug = report.Slug

func init() {
	connector.Register(New())
}

// Connector implements connector.DeviceConnector for lab results.
type Connector struct{}

var _ connector.DeviceConnector = (*Connector)(nil)

// New returns a ready lab result connector.
func New() *Connector { return &Connector{} }

// Slug returns "labs".
func (c *Connector) Slug() string { return Slug }

// Enabled is always true: the connector needs no operator keys.
func (c *Connector) Enabled(connector.Config) bool { return true }

// ErrInvalidLine reports an uploaded line that is not a valid lab upload.
var ErrInvalidLine = errors.New("labs: invalid upload line")

// ErrNoLines reports an upload with no line at all.
var ErrNoLines = errors.New("labs: upload holds no line")

// ParseUpload validates a decompressed part: every non-blank line must be a
// lab upload that Decode accepts, and there must be at least one. The upload
// handler stores the original bytes verbatim, so emit is not used.
func (c *Connector) ParseUpload(_ connector.UploadMeta, body io.Reader, _ connector.RawSink) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), report.MaxBody+1)
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
			return fmt.Errorf("%w: line %d is longer than %d bytes", ErrInvalidLine, num+1, report.MaxBody)
		}
		return fmt.Errorf("labs: read upload: %w", err)
	}
	if lines == 0 {
		return ErrNoLines
	}
	return nil
}

// Map turns one stored line into one observation per result. The ETL hands
// the line as decoded JSON; it is checked again, since a raw file may be
// re-processed long after it was stored.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	line, err := json.Marshal(raw.Fields)
	if err != nil {
		return model.MapResult{}, fmt.Errorf("labs: map: %w", err)
	}
	reports, err := report.Decode(line)
	if err != nil {
		return model.MapResult{}, fmt.Errorf("%w: %s", ErrInvalidLine, err.Error())
	}
	return model.MapResult{Observations: report.Observations(raw.UserID, reports)}, nil
}

// DataTypes implements connector.DataTypes: every result is an observation.
func (c *Connector) DataTypes() []string { return []string{connector.KindObservation} }
