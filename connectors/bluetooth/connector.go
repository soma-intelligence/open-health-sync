// SPDX-License-Identifier: AGPL-3.0-only

// Package bluetooth is the device connector for standard Bluetooth health
// devices: heart rate straps, blood pressure cuffs, scales, glucose meters,
// thermometers and pulse oximeters that speak the Bluetooth SIG GATT
// profiles. A client (the ohs-bridge desktop bridge, the Swift or Kotlin SDK)
// forwards each characteristic value it receives, as hex, with the time it
// arrived, in gzip NDJSON parts to POST /v1/ingest/bluetooth. The parts are
// stored verbatim; the ETL hands each line to Map, which decodes the value
// by the GATT Specification Supplement and emits canonical samples.
//
// One decoder, here: clients never decode, so a fix re-maps stored data. The
// line format and the mapping are documented in docs/connectors/bluetooth.md.
package bluetooth

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Slug is the provider name of this connector. It is also the source slug of
// a device whose vendor is unknown.
const Slug = "bluetooth"

// DataType is the X-OHS-Type of every part.
const DataType = "gatt"

// MaxValueBytes is the longest characteristic value a line may carry, the
// ATT maximum attribute length.
const MaxValueBytes = 512

// maxLineBytes bounds one line: a full value in hex and the device strings.
const maxLineBytes = 16 * 1024

// maxText bounds each device string, in characters.
const maxText = 128

func init() {
	connector.Register(New())
}

// Connector implements connector.DeviceConnector for Bluetooth devices.
type Connector struct{}

var _ connector.DeviceConnector = (*Connector)(nil)

// New returns a ready Bluetooth connector.
func New() *Connector { return &Connector{} }

// Slug returns "bluetooth".
func (c *Connector) Slug() string { return Slug }

// Enabled is always true: a device connector needs no operator keys.
func (c *Connector) Enabled(connector.Config) bool { return true }

// Errors of ParseUpload and Map. Their messages name the problem, never a
// value.
var (
	// ErrInvalidLine reports a line that breaks the upload contract.
	ErrInvalidLine = errors.New("bluetooth: invalid upload line")
	// ErrNoLines reports a part with no line at all.
	ErrNoLines = errors.New("bluetooth: upload holds no line")
	// ErrInvalidType reports a part whose X-OHS-Type is not DataType.
	ErrInvalidType = fmt.Errorf("bluetooth: X-OHS-Type must be %q", DataType)
)

// device is the optional device object of a line.
type device struct {
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Firmware     string `json:"firmware"`
}

// line is one uploaded line.
type line struct {
	At         string  `json:"at"`
	Char       string  `json:"char"`
	Value      string  `json:"value"`
	Session    string  `json:"session"`
	Device     *device `json:"device"`
	DeviceUser *int    `json:"device_user"`
}

// parsed is a line that passed check.
type parsed struct {
	at    time.Time
	char  string
	value []byte
	line
}

// baseUUIDSuffix is the Bluetooth base UUID after its 16-bit part.
const baseUUIDSuffix = "-0000-1000-8000-00805f9b34fb"

// shortUUID returns the 16-bit form of a characteristic UUID: four hex
// digits, or the full form of the Bluetooth base UUID.
func shortUUID(s string) (string, bool) {
	s = strings.ToLower(s)
	if len(s) == 36 && strings.HasPrefix(s, "0000") && strings.HasSuffix(s, baseUUIDSuffix) {
		s = s[4:8]
	}
	if len(s) != 4 {
		return "", false
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", false
	}
	return s, true
}

func validSession(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// check validates a line against the upload contract.
func (l line) check() (parsed, error) {
	p := parsed{line: l}
	var err error
	if p.at, err = time.Parse(time.RFC3339Nano, l.At); err != nil {
		return p, fmt.Errorf("%w: at must be an RFC 3339 time with an offset", ErrInvalidLine)
	}
	var ok bool
	if p.char, ok = shortUUID(l.Char); !ok {
		return p, fmt.Errorf("%w: char must be a 16-bit or Bluetooth base UUID", ErrInvalidLine)
	}
	if len(l.Value) == 0 || len(l.Value) > 2*MaxValueBytes {
		return p, fmt.Errorf("%w: value must hold 1 to %d bytes", ErrInvalidLine, MaxValueBytes)
	}
	if p.value, err = hex.DecodeString(l.Value); err != nil {
		return p, fmt.Errorf("%w: value must be hex", ErrInvalidLine)
	}
	if !validSession(l.Session) {
		return p, fmt.Errorf("%w: session must be 8 to 64 letters, digits, '_' or '-'", ErrInvalidLine)
	}
	if d := l.Device; d != nil {
		for _, s := range []string{d.Name, d.Manufacturer, d.Model, d.Firmware} {
			if utf8.RuneCountInString(s) > maxText {
				return p, fmt.Errorf("%w: device strings hold at most %d characters", ErrInvalidLine, maxText)
			}
		}
	}
	if u := l.DeviceUser; u != nil && (*u < 0 || *u >= unknownUser) {
		return p, fmt.Errorf("%w: device_user must be 0 to 254", ErrInvalidLine)
	}
	return p, nil
}

// ParseUpload validates a decompressed part: every non-blank line must meet
// the upload contract, and there must be one. It does not decode values: a
// value that does not decode is a failed record in the ETL, and the other
// lines are kept. The upload handler stores the original bytes, so emit is
// not used and may be nil.
func (c *Connector) ParseUpload(meta connector.UploadMeta, body io.Reader, _ connector.RawSink) error {
	if meta.Type != DataType {
		return ErrInvalidType
	}
	r := bufio.NewReaderSize(body, 64*1024)
	var (
		buf   []byte
		lines int
		num   int
	)
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > maxLineBytes+1 { // +1: the newline itself
			return fmt.Errorf("%w: line %d is longer than %d bytes", ErrInvalidLine, num+1, maxLineBytes)
		}
		buf = append(buf, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("bluetooth: read upload: %w", err)
		}
		if len(buf) > 0 {
			num++
			if trimmed := bytes.TrimSpace(buf); len(trimmed) > 0 {
				var l line
				if trimmed[0] != '{' || json.Unmarshal(trimmed, &l) != nil {
					return fmt.Errorf("%w: line %d is not a JSON object of the line format", ErrInvalidLine, num)
				}
				if _, err := l.check(); err != nil {
					return fmt.Errorf("%w (line %d)", err, num)
				}
				lines++
			}
		}
		buf = buf[:0]
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if lines == 0 {
		return ErrNoLines
	}
	return nil
}

// Map turns one stored line into canonical records. A characteristic this
// connector does not decode (battery level, a vendor service) gives an empty
// result and no error; a value shorter than its flags say is an error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	b, err := json.Marshal(raw.Fields)
	if err != nil {
		return model.MapResult{}, fmt.Errorf("bluetooth: re-encode line: %w", err)
	}
	var l line
	if err := json.Unmarshal(b, &l); err != nil {
		return model.MapResult{}, fmt.Errorf("%w: %v", ErrInvalidLine, err)
	}
	p, err := l.check()
	if err != nil {
		return model.MapResult{}, err
	}
	decode, ok := decoders[p.char]
	if !ok {
		return model.MapResult{}, nil
	}
	m, err := decode(&reader{b: p.value})
	if err != nil {
		return model.MapResult{}, truncated(p.char, err)
	}
	return mapMeasurement(raw.UserID, p, m), nil
}
