// SPDX-License-Identifier: AGPL-3.0-only

// Package export reads the copy of Mi Fitness data that Xiaomi delivers from
// its account privacy center, and turns it into the NDJSON parts of
// POST /v1/ingest/xiaomi. `ohs import mi-fitness` drives it.
//
// The copy is a set of CSV files named <date>_<account>_MiFitness_<table>.csv.
// Readings live in hlth_center_fitness_data (one row per reading: data
// source, key, time and a JSON value) and workouts in
// hlth_center_sport_record. hlth_center_data_source names the device behind
// each data source. The other tables (profile, goals, feedback, competitions)
// are not read. The account id column is dropped from every line.
package export

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/xiaomi"
)

// Table file suffixes in a Mi Fitness copy.
const (
	fileFitness    = "hlth_center_fitness_data.csv"
	fileSport      = "hlth_center_sport_record.csv"
	fileDataSource = "hlth_center_data_source.csv"
)

// formatVersion names the conversion rules. Bump it when a change alters the
// lines produced, so a re-run after an upgrade uses a fresh batch.
const formatVersion = "2"

// DefaultMaxPartBytes caps the uncompressed NDJSON of one part.
const DefaultMaxPartBytes = 8 << 20

// maxLineBytes bounds one CSV line.
const maxLineBytes = 16 << 20

// ErrNoTables reports a path that holds no table this importer reads.
var ErrNoTables = errors.New("no Mi Fitness table found: expected files ending in " +
	fileFitness + " or " + fileSport)

// Export is an opened Mi Fitness copy: a folder or a zip of it.
type Export struct {
	files map[string]source // by table suffix
	zip   *zip.ReadCloser
}

type source struct {
	name string
	size int64
	open func() (io.ReadCloser, error)
}

// Open opens a folder or a .zip holding the CSV files.
func Open(path string) (*Export, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	e := &Export{files: map[string]source{}}
	if st.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, de := range entries {
			if de.IsDir() {
				continue
			}
			info, err := de.Info()
			if err != nil {
				return nil, err
			}
			p := filepath.Join(path, de.Name())
			e.add(de.Name(), info.Size(), func() (io.ReadCloser, error) { return os.Open(p) })
		}
	} else {
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, fmt.Errorf("%s is neither a folder nor a readable zip (unzip a password protected copy first and pass the folder): %w", filepath.Base(path), err)
		}
		e.zip = zr
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			f := f
			e.add(f.Name, int64(f.UncompressedSize64), f.Open)
		}
	}
	if e.files[fileFitness].open == nil && e.files[fileSport].open == nil {
		_ = e.Close()
		return nil, ErrNoTables
	}
	return e, nil
}

func (e *Export) add(name string, size int64, open func() (io.ReadCloser, error)) {
	base := name
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	for _, suffix := range []string{fileFitness, fileSport, fileDataSource} {
		if strings.HasSuffix(base, suffix) {
			// Two copies in one folder: keep the latest by name (names
			// start with the export date).
			if cur, ok := e.files[suffix]; !ok || base > cur.name {
				e.files[suffix] = source{name: base, size: size, open: open}
			}
		}
	}
}

// Close releases the zip, if any.
func (e *Export) Close() error {
	if e.zip != nil {
		return e.zip.Close()
	}
	return nil
}

// Options tune a conversion. Every field takes part in BatchID.
type Options struct {
	// Since keeps only rows on or after this UTC day (YYYY-MM-DD).
	Since string
	// MaxPartBytes caps the uncompressed NDJSON of one part.
	MaxPartBytes int
}

// Validate checks Since.
func (o Options) Validate() error {
	if o.Since != "" {
		if _, err := time.Parse(time.DateOnly, o.Since); err != nil {
			return fmt.Errorf("since %q is not a date as YYYY-MM-DD", o.Since)
		}
	}
	return nil
}

// BatchID derives the X-OHS-Batch of an import from the names and sizes of
// the tables read and the options, so the same copy imported again gets the
// same batch and the same parts: the server answers them as duplicates.
func (e *Export) BatchID(opts Options) string {
	if opts.MaxPartBytes <= 0 {
		opts.MaxPartBytes = DefaultMaxPartBytes
	}
	h := sha256.New()
	fmt.Fprintf(h, "v%s\x00%s\x00%d", formatVersion, opts.Since, opts.MaxPartBytes)
	for _, k := range []string{fileFitness, fileSport, fileDataSource} {
		if s, ok := e.files[k]; ok {
			fmt.Fprintf(h, "\x00%s\x00%d", s.name, s.size)
		}
	}
	return "mif-" + hex.EncodeToString(h.Sum(nil))[:16]
}

// Part is one upload: lines of one table.
type Part struct {
	Number int    // 0, 1, 2... in the order parts are produced
	Type   string // X-OHS-Type: the table
	Day    string // X-OHS-Day: UTC day of the part's first line
	Lines  int
	NDJSON []byte // uncompressed
}

// Stats counts what a conversion read and produced. It holds counts and key
// names only, never a health value.
type Stats struct {
	Rows        int            // data rows read
	Lines       int            // lines written
	Parts       int            // parts produced
	BeforeSince int            // rows dropped by Since
	Keys        map[string]int // lines by table/key
	Invalid     map[string]int // rows not converted, by reason
}

// Line is one uploaded line. Its field names are the upload contract.
type Line struct {
	Table      string          `json:"table"`
	SID        string          `json:"sid,omitempty"`
	Key        string          `json:"key"`
	Category   string          `json:"category,omitempty"`
	Time       int64           `json:"time"`
	UpdateTime int64           `json:"updateTime,omitempty"`
	Device     *Device         `json:"device,omitempty"`
	Value      json.RawMessage `json:"value"`
}

// Device is the data source a reading came from.
type Device struct {
	Model string `json:"model,omitempty"`
	Name  string `json:"name,omitempty"`
}

// Convert reads the tables and calls emit with each part, in order. It stops
// at the first error emit returns.
func (e *Export) Convert(opts Options, emit func(Part) error) (Stats, error) {
	if opts.MaxPartBytes <= 0 {
		opts.MaxPartBytes = DefaultMaxPartBytes
	}
	st := Stats{Keys: map[string]int{}, Invalid: map[string]int{}}
	var since int64
	if opts.Since != "" {
		d, err := time.Parse(time.DateOnly, opts.Since)
		if err != nil {
			return st, err
		}
		since = d.Unix()
	}
	devices, err := e.devices()
	if err != nil {
		return st, err
	}
	for _, t := range []struct{ suffix, table string }{
		{fileFitness, xiaomi.TableFitnessData},
		{fileSport, xiaomi.TableSportRecord},
	} {
		src, ok := e.files[t.suffix]
		if !ok {
			continue
		}
		var (
			buf   bytes.Buffer
			lines int
			day   string
		)
		flush := func() error {
			if lines == 0 {
				return nil
			}
			p := Part{Number: st.Parts, Type: t.table, Day: day, Lines: lines, NDJSON: bytes.Clone(buf.Bytes())}
			st.Parts++
			buf.Reset()
			lines = 0
			return emit(p)
		}
		err := readTable(src, func(row map[string]string) error {
			st.Rows++
			l, reason := toLine(t.table, row)
			if reason != "" {
				st.Invalid[reason]++
				return nil
			}
			if l.Time < since {
				st.BeforeSince++
				return nil
			}
			if d, ok := devices[l.SID]; ok {
				l.Device = &d
			}
			b, err := json.Marshal(l)
			if err != nil {
				return err
			}
			if lines > 0 && buf.Len()+len(b)+1 > opts.MaxPartBytes {
				if err := flush(); err != nil {
					return err
				}
			}
			if lines == 0 {
				day = time.Unix(l.Time, 0).UTC().Format(time.DateOnly)
			}
			buf.Write(b)
			buf.WriteByte('\n')
			lines++
			st.Lines++
			st.Keys[t.table+"/"+l.Key]++
			return nil
		})
		if err != nil {
			return st, fmt.Errorf("%s: %w", src.name, err)
		}
		if err := flush(); err != nil {
			return st, err
		}
	}
	return st, nil
}

// toLine converts one row. A row that cannot be converted gives a reason.
func toLine(table string, row map[string]string) (Line, string) {
	l := Line{
		Table:    table,
		SID:      row["sid"],
		Key:      strings.TrimSpace(row["key"]),
		Category: strings.TrimSpace(firstOf(row, "category", "categroy")),
	}
	if l.Key == "" {
		return l, "no key"
	}
	t, err := strconv.ParseInt(strings.TrimSpace(row["time"]), 10, 64)
	if err != nil || t <= 0 {
		return l, "unreadable time"
	}
	l.Time = t
	if u, err := strconv.ParseInt(strings.TrimSpace(firstOf(row, "updatetime", "uptimedata")), 10, 64); err == nil && u > 0 {
		l.UpdateTime = u
	}
	v := bytes.TrimSpace([]byte(row["value"]))
	// Decoded the way the server reads a line, not only checked for syntax:
	// a number no float64 holds (1e700) is valid JSON the server refuses.
	var obj map[string]any
	if len(v) == 0 || v[0] != '{' || json.Unmarshal(v, &obj) != nil {
		return l, "unreadable value"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, v); err != nil {
		return l, "unreadable value"
	}
	l.Value = compact.Bytes()
	return l, ""
}

func firstOf(row map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := row[k]; ok {
			return v
		}
	}
	return ""
}

// devices reads the data source table: sid to device model and name.
func (e *Export) devices() (map[string]Device, error) {
	out := map[string]Device{}
	src, ok := e.files[fileDataSource]
	if !ok {
		return out, nil
	}
	err := readTable(src, func(row map[string]string) error {
		sid := row["sid"]
		// The alias is left out: it is the name the user gave the device.
		d := Device{Model: strings.TrimSpace(row["model"]), Name: strings.TrimSpace(row["name"])}
		if sid != "" && (d.Model != "" || d.Name != "") {
			out[sid] = d
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.name, err)
	}
	return out, nil
}

// readTable calls fn with each data row, keyed by the normalized header
// (lowercase, without underscores). Some copies wrap every line, header
// included, in one more pair of quotes; such lines are unwrapped first.
func readTable(src source, fn func(map[string]string) error) error {
	rc, err := src.open()
	if err != nil {
		return err
	}
	defer rc.Close()
	br := bufio.NewReaderSize(rc, 1<<20)
	var header []string
	for num := 1; ; num++ {
		raw, err := readLine(br)
		if errors.Is(err, io.EOF) && raw == "" {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if num == 1 {
			raw = strings.TrimPrefix(raw, "\ufeff")
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		fields, perr := parseLine(raw)
		if perr != nil {
			if header == nil {
				return fmt.Errorf("line %d: unreadable header", num)
			}
			continue
		}
		if header == nil {
			for _, f := range fields {
				header = append(header, strings.ReplaceAll(strings.ToLower(strings.TrimSpace(f)), "_", ""))
			}
			continue
		}
		row := make(map[string]string, len(header))
		for i, h := range header {
			if i < len(fields) {
				row[h] = fields[i]
			}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	if header == nil {
		return errors.New("empty table")
	}
	return nil
}

// readLine reads one physical line. A quoted field holding a newline is not
// expected: values are single-line JSON.
func readLine(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		chunk, err := br.ReadSlice('\n')
		if sb.Len()+len(chunk) > maxLineBytes {
			return "", fmt.Errorf("a line is longer than %d bytes", maxLineBytes)
		}
		sb.Write(chunk)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		s := strings.TrimRight(sb.String(), "\r\n")
		return s, err
	}
}

// parseLine splits one CSV line. Some copies wrap every line in one more
// pair of quotes. Wrapped properly, the line reads as one field holding the
// real line; wrapped naively (inner quotes left as they are), it does not
// parse, and dropping the outer quotes gives the real line.
func parseLine(s string) ([]string, error) {
	fields, err := splitCSV(s)
	if err == nil {
		if len(fields) == 1 && strings.Contains(fields[0], ",") {
			return splitCSV(fields[0])
		}
		return fields, nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return splitCSV(s[1 : len(s)-1])
	}
	return nil, err
}

func splitCSV(s string) ([]string, error) {
	r := csv.NewReader(strings.NewReader(s))
	r.FieldsPerRecord = -1
	return r.Read()
}
