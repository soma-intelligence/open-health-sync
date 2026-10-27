// SPDX-License-Identifier: AGPL-3.0-only

// Package applehealth is the Apple Health device connector. An iOS app reads
// HealthKit and uploads gzip NDJSON parts, one data type per part, to
// POST /v1/ingest/apple-health. The parts are stored verbatim; the ETL later
// hands each line to Map.
package applehealth

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Slug is the provider name of this connector.
const Slug = "apple_health"

// MaxLineBytes is the longest line an upload may hold. It matches the ETL
// reader's line cap, so a part accepted here is never one the ETL cannot read.
const MaxLineBytes = 32 * 1024 * 1024

func init() {
	connector.Register(New())
}

// Connector implements connector.DeviceConnector for Apple Health.
type Connector struct {
	*mapper
}

var _ connector.DeviceConnector = (*Connector)(nil)

// New returns a ready Apple Health connector.
func New() *Connector { return &Connector{mapper: newMapper()} }

// Slug returns "apple_health".
func (c *Connector) Slug() string { return Slug }

// Enabled is always true: a device connector needs no operator keys.
func (c *Connector) Enabled(connector.Config) bool { return true }

// Map turns one uploaded line into canonical records. A line of a type this
// connector does not store gives an empty result and no error. Records keep
// their HealthKit UUID as an alias (model.MapResult.AliasExternalIDs): the
// content hash folds two samples of one reading into one row, and the row
// stays until every one of them is deleted.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	res, err := c.mapRecord(raw.Fields, raw.UserID)
	if err != nil {
		return model.MapResult{}, err
	}
	if res == nil {
		return model.MapResult{AliasExternalIDs: true}, nil
	}
	res.AliasExternalIDs = true
	return *res, nil
}

// ErrInvalidLine reports an uploaded line that is not a JSON object.
var ErrInvalidLine = errors.New("apple_health: invalid upload line")

// ErrNoLines reports an upload with no line at all.
var ErrNoLines = errors.New("apple_health: upload holds no line")

// ParseUpload validates a decompressed upload body: every non-blank line must
// be a JSON object no longer than MaxLineBytes, and there must be at least one.
// The upload handler stores the original bytes verbatim (the raw format is the
// SDK's line format, unchanged), so nothing is re-encoded and emit is not used:
// it may be nil. A read error from body is returned as is, wrapped, so the
// caller can tell a broken stream (for example a truncated gzip) from a bad line.
func (c *Connector) ParseUpload(_ connector.UploadMeta, body io.Reader, _ connector.RawSink) error {
	r := bufio.NewReaderSize(body, 64*1024)
	var (
		line  []byte
		lines int
		num   int
	)
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > MaxLineBytes+1 { // +1: the newline itself
			return fmt.Errorf("%w: line %d is longer than %d bytes", ErrInvalidLine, num+1, MaxLineBytes)
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("apple_health: read upload: %w", err)
		}
		if len(line) > 0 {
			num++
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				if trimmed[0] != '{' || !json.Valid(trimmed) {
					return fmt.Errorf("%w: line %d is not a JSON object", ErrInvalidLine, num)
				}
				lines++
			}
		}
		line = line[:0]
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if lines == 0 {
		return ErrNoLines
	}
	return nil
}
