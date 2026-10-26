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
//
// A few vendor services carry what the standard profiles do not (a Polar
// strap's PP intervals and raw ECG). Their values come on the same route,
// keyed by the characteristic's full UUID, with `ctx`: the bytes the client
// sent to start the stream, so each line still decodes on its own.
//
// A client that decodes a vendor protocol itself (a driver for a band whose
// protocol this server does not know) sends `sample` instead of `value`: a
// canonical type, value, unit and time, checked here and stored as sent.
package bluetooth

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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

// MaxCtxBytes is the longest context a line may carry.
const MaxCtxBytes = 64

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

// sampleLine is a value the client decoded: a canonical quantity type, or
// rrInterval.
type sampleLine struct {
	Type  string   `json:"type"`
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

// rrInterval is the sample type of an RR interval in ms, stored as a beat.
const rrInterval = "rr_interval"

// maxUnit bounds a sample's unit, in bytes.
const maxUnit = 32

// line is one uploaded line.
type line struct {
	At         string      `json:"at"`
	Char       string      `json:"char"`
	Value      string      `json:"value"`
	Ctx        string      `json:"ctx"`
	Sample     *sampleLine `json:"sample"`
	Session    string      `json:"session"`
	Device     *device     `json:"device"`
	DeviceUser *int        `json:"device_user"`
}

// decoded is a sample that passed check, in its storage unit.
type decoded struct {
	qt         string
	value      float64
	unit       string
	start, end time.Time
}

// parsed is a line that passed check.
type parsed struct {
	at     time.Time
	char   string
	value  []byte
	ctx    []byte
	sample *decoded
	line
}

// baseUUIDSuffix is the Bluetooth base UUID after its 16-bit part.
const baseUUIDSuffix = "-0000-1000-8000-00805f9b34fb"

// charKey returns the key of a characteristic UUID: four lowercase hex
// digits for a Bluetooth SIG number (written short, or in the full form of the
// Bluetooth base UUID), the full lowercase UUID for a vendor characteristic.
func charKey(s string) (string, bool) {
	s = strings.ToLower(s)
	if len(s) == 36 && strings.HasPrefix(s, "0000") && strings.HasSuffix(s, baseUUIDSuffix) {
		s = s[4:8]
	}
	switch len(s) {
	case 4:
		if _, err := hex.DecodeString(s); err != nil {
			return "", false
		}
		return s, true
	case 36:
		for i, c := range s {
			dash := i == 8 || i == 13 || i == 18 || i == 23
			hexDigit := c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
			if dash && c != '-' || !dash && !hexDigit {
				return "", false
			}
		}
		return s, true
	}
	return "", false
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
	if p.char, ok = charKey(l.Char); !ok {
		return p, fmt.Errorf("%w: char must be a 16-bit or a 128-bit UUID", ErrInvalidLine)
	}
	switch {
	case l.Sample != nil && l.Value != "":
		return p, fmt.Errorf("%w: a line carries value or sample, not both", ErrInvalidLine)
	case l.Sample != nil:
		if p.sample, err = l.Sample.check(p.at); err != nil {
			return p, err
		}
	default:
		if len(l.Value) == 0 || len(l.Value) > 2*MaxValueBytes {
			return p, fmt.Errorf("%w: value must hold 1 to %d bytes", ErrInvalidLine, MaxValueBytes)
		}
		if p.value, err = hex.DecodeString(l.Value); err != nil {
			return p, fmt.Errorf("%w: value must be hex", ErrInvalidLine)
		}
	}
	if len(l.Ctx) > 2*MaxCtxBytes {
		return p, fmt.Errorf("%w: ctx holds at most %d bytes", ErrInvalidLine, MaxCtxBytes)
	}
	if p.ctx, err = hex.DecodeString(l.Ctx); err != nil {
		return p, fmt.Errorf("%w: ctx must be hex", ErrInvalidLine)
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

// check validates a decoded sample of a line that arrived at `at`: a declared
// type in a unit that converts to its storage unit, a finite value, and a
// time that is not before 2000 nor more than deviceAhead after the arrival.
func (s sampleLine) check(at time.Time) (*decoded, error) {
	d := &decoded{qt: s.Type}
	if s.Value == nil || math.IsNaN(*s.Value) || math.IsInf(*s.Value, 0) {
		return nil, fmt.Errorf("%w: sample value must be a finite number", ErrInvalidLine)
	}
	if s.Unit == "" || len(s.Unit) > maxUnit {
		return nil, fmt.Errorf("%w: sample unit must hold 1 to %d bytes", ErrInvalidLine, maxUnit)
	}
	if s.Type == rrInterval {
		if !strings.EqualFold(s.Unit, "ms") || *s.Value <= 0 || *s.Value > 60000 {
			return nil, fmt.Errorf("%w: an rr_interval is 0 to 60000 ms", ErrInvalidLine)
		}
		d.value, d.unit = *s.Value, "ms"
	} else {
		if _, ok := model.StorageUnit(s.Type); !ok {
			return nil, fmt.Errorf("%w: sample type is not a declared quantity type", ErrInvalidLine)
		}
		v, u, err := model.ToStorageUnit(s.Type, *s.Value, s.Unit)
		if err != nil {
			return nil, fmt.Errorf("%w: sample unit does not convert to the type's unit", ErrInvalidLine)
		}
		d.value, d.unit = v, u
	}
	var err error
	if d.start, err = time.Parse(time.RFC3339Nano, s.Start); err != nil {
		return nil, fmt.Errorf("%w: sample start must be an RFC 3339 time with an offset", ErrInvalidLine)
	}
	if d.start.Before(earliestDeviceTime) || d.start.After(at.Add(deviceAhead)) {
		return nil, fmt.Errorf("%w: sample start must be after 2000 and not after its arrival", ErrInvalidLine)
	}
	d.end = d.start
	if s.End != "" {
		if d.end, err = time.Parse(time.RFC3339Nano, s.End); err != nil {
			return nil, fmt.Errorf("%w: sample end must be an RFC 3339 time with an offset", ErrInvalidLine)
		}
		if d.end.Before(d.start) || d.end.After(at.Add(deviceAhead)) {
			return nil, fmt.Errorf("%w: sample end must be after its start and not after its arrival", ErrInvalidLine)
		}
	}
	return d, nil
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
	if p.sample != nil {
		return mapSample(raw.UserID, p), nil
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
