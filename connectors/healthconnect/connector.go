// SPDX-License-Identifier: AGPL-3.0-only

// Package healthconnect is the Health Connect device connector. An Android
// app (the Kotlin SDK) reads Android Health Connect on the phone and uploads
// gzip NDJSON parts to POST /v1/ingest/health-connect, one Health Connect
// record per line. The parts are stored verbatim; the ETL later hands each
// line to Map.
//
// Health Connect is an on-device store with no server API, so this is the
// only way its data reaches a server. It is also how glucose from apps that
// write to Health Connect (CGM apps such as FreeStyle Libre, whose maker
// offers no public API) arrives. The line format is documented in
// docs/connectors/health-connect.md.
package healthconnect

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
const Slug = "health_connect"

// MaxLineBytes is the longest line an upload may hold. It matches the ETL
// reader's line cap, so a part accepted here is never one the ETL cannot read.
const MaxLineBytes = 32 * 1024 * 1024

func init() {
	connector.Register(New())
}

// Connector implements connector.DeviceConnector for Health Connect.
type Connector struct{}

var _ connector.DeviceConnector = (*Connector)(nil)

// New returns a ready Health Connect connector.
func New() *Connector { return &Connector{} }

// Slug returns "health_connect".
func (c *Connector) Slug() string { return Slug }

// Enabled is always true: a device connector needs no operator keys.
func (c *Connector) Enabled(connector.Config) bool { return true }

// Map turns one uploaded line into canonical records. A line of a record type
// this connector does not store gives an empty result and no error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	res, err := mapRecord(raw.Fields, raw.UserID)
	if err != nil || res == nil {
		return model.MapResult{}, err
	}
	return *res, nil
}

// ErrInvalidLine reports an uploaded line that is not a JSON object with a
// recordType.
var ErrInvalidLine = errors.New("health_connect: invalid upload line")

// ErrNoLines reports an upload with no line at all.
var ErrNoLines = errors.New("health_connect: upload holds no line")

// ParseUpload validates a decompressed upload body: every non-blank line must
// be a JSON object no longer than MaxLineBytes with a string recordType, and
// there must be at least one. The upload handler stores the original bytes
// verbatim, so emit is not used and may be nil. A read error from body is
// returned wrapped, so the caller can tell a broken stream from a bad line.
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
			return fmt.Errorf("health_connect: read upload: %w", err)
		}
		if len(line) > 0 {
			num++
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				var head struct {
					RecordType *string `json:"recordType"`
				}
				if trimmed[0] != '{' || json.Unmarshal(trimmed, &head) != nil {
					return fmt.Errorf("%w: line %d is not a JSON object", ErrInvalidLine, num)
				}
				if head.RecordType == nil || *head.RecordType == "" {
					return fmt.Errorf("%w: line %d has no recordType", ErrInvalidLine, num)
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
