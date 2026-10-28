// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// ObjectGetter is the part of rawstore.Store the reader needs.
type ObjectGetter interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// ParseError records a single line that failed JSON parsing.
type ParseError struct {
	LineNum int
	RawLine string
	Err     error
}

// TruncationKind says WHY a read stopped before the end of the object. The two
// cases need opposite handling, which is why they are told apart here and not at
// the call site: a stream that died mid-flight usually reads fine on the next
// attempt, a ceiling this reader enforces never will.
type TruncationKind string

const (
	// TruncationNone: the object was read to its end.
	TruncationNone TruncationKind = ""
	// TruncationStream: the body ended early (connection reset, partial
	// multipart upload, corrupt gzip tail). The stored object is usually
	// intact, so the caller must RETRY the job instead of registering a partial:
	// the unread tail exists nowhere else.
	TruncationStream TruncationKind = "stream"
	// TruncationLimit: a ceiling this reader enforces was reached (over-cap
	// line, decompressed-byte cap, record cap). Deterministic: re-reading the
	// same object hits the same ceiling, so the caller keeps the parsed head as
	// a partial instead of retrying forever.
	TruncationLimit TruncationKind = "limit"
)

// ReadResult holds the parsed output of an NDJSON.gz file.
type ReadResult struct {
	// Records holds every parsed record. StreamNDJSON leaves it nil: it hands
	// each record to the callback and releases it, which is the whole point of
	// the streaming path. Use RecordCount for the count either way.
	Records     []map[string]any
	ParseErrors []ParseError
	// FileSize is the number of stored (compressed) bytes read.
	FileSize int64
	// RecordCount is how many records parsed successfully. It equals
	// len(Records) on the buffered path and is the ONLY count on the streaming
	// path, where no record is retained.
	RecordCount int
	// ParseErrorsDropped counts malformed lines beyond the retention caps. They
	// are counted but not retained, so one pathological object cannot turn every
	// bad line into a live ParseError plus a live raw payload.
	ParseErrorsDropped int
	// ScanTruncated is set when the scan aborted before the end of the object.
	// The records parsed BEFORE the abort are still returned and a synthetic
	// ParseError marks the truncation point. Read Truncation to know whether the
	// abort is worth retrying.
	ScanTruncated bool
	// Truncation classifies the abort. See TruncationKind.
	Truncation TruncationKind
}

// DefaultMaxLineBytes is the default scanner line cap, large enough that a
// single big ECG voltage line does not abort the whole object. An over-cap line
// is a tracked ParseError, not a whole-object abort. Kept below the 64 MiB
// upload cap so it never silently truncates a legitimately sized part.
const DefaultMaxLineBytes = 32 * 1024 * 1024 // 32 MB

// DefaultMaxDecompressedBytes caps how far one object may expand on the way out
// of gzip. Without it a 64 MiB upload of a highly compressible stream (the
// upload handler deliberately does not decompress, so nothing upstream looks)
// expands past 60 GB and OOMs the process. The ohs binary runs every role in
// one process, so that kill is a full outage, and the poisoned object stays in
// the write-once raw store so a replay would hit it again after every restart.
// 512 MB is roughly 16x the largest real Apple Health part measured, and
// hitting it is reported as a deterministic truncation rather than a silent EOF.
const DefaultMaxDecompressedBytes int64 = 512 * 1024 * 1024 // 512 MB

// DefaultMaxRecords caps how many records one object may yield. The byte cap is
// the primary guard; this one bounds the work (and, on the buffered path, the
// live maps) for a stream that stays under it with millions of tiny records.
const DefaultMaxRecords = 2_000_000

// Retention caps for malformed lines. The count is always exact
// (ParseErrorsDropped carries the overflow), but a file that is malformed all
// the way down must not turn into millions of live ParseError structs each
// pinning its own raw line.
const (
	maxRetainedParseErrors     = 10_000
	maxRetainedParseErrorBytes = 8 * 1024 * 1024 // 8 MB of raw lines
)

// errDecompressedCap marks a read stopped by DefaultMaxDecompressedBytes.
var errDecompressedCap = errors.New("decompressed size cap reached")

// cappedReader stops a read at a fixed number of bytes and SAYS SO, unlike
// io.LimitReader whose silent io.EOF is indistinguishable from a clean end of
// file. That distinction is the whole point here: a silent EOF would register
// the truncated head as a complete object and lose the tail without a trace.
type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errDecompressedCap
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// RecordFunc receives one parsed record. recordIndex is the position among the
// records that parsed successfully (0-based), which is what the processor
// reports as the failed record's line number. The map is NOT retained by the
// reader after the call returns, so a callback that keeps it owns it.
// Returning an error aborts the read and is surfaced to the caller as-is.
type RecordFunc func(recordIndex int, record map[string]any) error

// Reader streams NDJSON (optionally gzipped, optionally sealed) objects from
// the raw store and parses them line by line.
type Reader struct {
	store                ObjectGetter
	maxLineBytes         int
	maxDecompressedBytes int64
	maxRecords           int
	// keys opens sealed objects (WithKeys). Nil reads legacy objects only.
	keys func(version uint32) (*vault.UserKeys, error)
}

// WithKeys returns a copy of r that opens sealed objects with keys, the key
// set of a version (vault.UserVault.Keys of the object's owner). Legacy gzip
// and plain objects are still read as before.
func (r *Reader) WithKeys(keys func(version uint32) (*vault.UserKeys, error)) *Reader {
	c := *r
	c.keys = keys
	return &c
}

// NewReader creates a Reader over store with the default caps.
func NewReader(store ObjectGetter) *Reader {
	return NewReaderWithMaxLine(store, DefaultMaxLineBytes)
}

// NewReaderWithMaxLine creates a Reader with an explicit max line size.
// A non-positive value falls back to DefaultMaxLineBytes.
func NewReaderWithMaxLine(store ObjectGetter, maxLineBytes int) *Reader {
	return NewReaderWithLimits(store, maxLineBytes, DefaultMaxDecompressedBytes, DefaultMaxRecords)
}

// NewReaderWithLimits creates a Reader with every cap set explicitly. A
// non-positive value falls back to that cap's default.
func NewReaderWithLimits(store ObjectGetter, maxLineBytes int, maxDecompressedBytes int64, maxRecords int) *Reader {
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultMaxLineBytes
	}
	if maxDecompressedBytes <= 0 {
		maxDecompressedBytes = DefaultMaxDecompressedBytes
	}
	if maxRecords <= 0 {
		maxRecords = DefaultMaxRecords
	}
	return &Reader{
		store:                store,
		maxLineBytes:         maxLineBytes,
		maxDecompressedBytes: maxDecompressedBytes,
		maxRecords:           maxRecords,
	}
}

// ReadNDJSON reads an object and retains every record. Prefer StreamNDJSON on
// the ETL path: this shape holds the whole file as map[string]any at once.
func (r *Reader) ReadNDJSON(ctx context.Context, key string) (*ReadResult, error) {
	var records []map[string]any
	result, err := r.StreamNDJSON(ctx, key, func(_ int, record map[string]any) error {
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.Records = records
	return result, nil
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// StreamNDJSON reads an object, opens it when it is sealed, decompresses it
// when it is gzip, and hands each parsed record to fn one at a time so the raw
// map can be released immediately. A sealed object without keys (WithKeys),
// or one that does not open, is an error: nothing of it is read.
// Valid records are counted, malformed lines are recorded as parse errors (up to
// the retention caps), empty lines are skipped silently.
func (r *Reader) StreamNDJSON(ctx context.Context, key string, fn RecordFunc) (*ReadResult, error) {
	body, err := r.store.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("raw store get %s: %w", key, err)
	}
	defer body.Close()
	counted := &countingReader{r: body}
	result := &ReadResult{}
	defer func() { result.FileSize = counted.n }()

	// A sealed object starts with its header; the default bufio buffer holds
	// all of it, so ReadObjectHeader only peeks. Its body (the gzip bytes)
	// then goes through the same detection as a legacy object. A chunk that
	// fails to open mid-stream ends the scan like a corrupt gzip tail. A
	// read error here is left to the scan, which meets it again and reports
	// a stream truncation, as for a legacy object.
	br := bufio.NewReader(counted)
	_, sealed, err := vault.ReadObjectHeader(br)
	if errors.Is(err, vault.ErrOpen) {
		return nil, fmt.Errorf("raw object %s: %w", key, err)
	}
	if sealed {
		if r.keys == nil {
			return nil, fmt.Errorf("raw object %s is sealed and this reader has no keys", key)
		}
		_, plain, err := vault.OpenObject(br, r.keys)
		if err != nil {
			return nil, fmt.Errorf("open raw object %s: %w", key, err)
		}
		br = bufio.NewReader(plain)
		// Under the wrong keys the header still opens (the KEM rejects
		// implicitly) and the first chunk does not: an error, not an
		// object that seems to end at once.
		if _, err := br.Peek(2); errors.Is(err, vault.ErrOpen) {
			return nil, fmt.Errorf("open raw object %s: %w", key, err)
		}
	}

	// Detect gzip by its 1F 8B magic bytes instead of assuming every body is
	// gzip. A plain NDJSON body (a non-compressed upload) is read as-is.
	magic, _ := br.Peek(2)
	var src io.Reader = br
	if len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, gzErr := gzip.NewReader(br)
		if gzErr != nil {
			return nil, fmt.Errorf("gzip reader: %w", gzErr)
		}
		defer gz.Close()
		src = gz
	}
	// The cap applies to both shapes. A plain body is already bounded by the
	// object size, but wrapping it too keeps one ceiling for one read.
	src = &cappedReader{r: src, left: r.maxDecompressedBytes}

	maxLine := r.maxLineBytes
	if maxLine <= 0 {
		maxLine = DefaultMaxLineBytes
	}
	maxRecords := r.maxRecords
	if maxRecords <= 0 {
		maxRecords = DefaultMaxRecords
	}

	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 1024*1024), maxLine)

	rawLineBudget := 0
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		// Bytes(), not Text(): Text() copies the line into a string and
		// json.Unmarshal([]byte(line)) copies it a second time. Unmarshal copies
		// out whatever it keeps, so the scanner's buffer can be reused.
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			result.addParseError(&rawLineBudget, lineNum, line, err)
			continue
		}
		if err := fn(result.RecordCount, record); err != nil {
			return nil, err
		}
		result.RecordCount++

		if result.RecordCount >= maxRecords {
			result.markTruncated(TruncationLimit, lineNum+1,
				fmt.Errorf("record cap reached after line %d (%d records)", lineNum, maxRecords))
			return result, nil
		}
	}

	// On a scanner error (truncated gzip, an over-cap line, a corrupt tail)
	// keep the records parsed so far instead of discarding the whole object. Append
	// a synthetic ParseError marking the truncation point and flag ScanTruncated.
	// Truncation says which recovery the caller owes: a stream abort is a retry,
	// a cap is a partial.
	if scanErr := scanner.Err(); scanErr != nil {
		kind := TruncationStream
		if errors.Is(scanErr, bufio.ErrTooLong) || errors.Is(scanErr, errDecompressedCap) {
			kind = TruncationLimit
		}
		result.markTruncated(kind, lineNum+1,
			fmt.Errorf("scan truncated after line %d: %w", lineNum, scanErr))
		return result, nil
	}

	return result, nil
}

// markTruncated flags the abort and records where it happened. It bypasses the
// parse-error retention caps on purpose: the truncation marker is the one entry
// that must always survive.
func (rr *ReadResult) markTruncated(kind TruncationKind, lineNum int, err error) {
	rr.ScanTruncated = true
	rr.Truncation = kind
	rr.ParseErrors = append(rr.ParseErrors, ParseError{
		LineNum: lineNum,
		RawLine: "",
		Err:     err,
	})
}

// addParseError retains a malformed line within the caps. Past the entry cap the
// line is only counted; past the byte budget the entry is kept without its raw
// text, so the count and the line number survive while the memory does not.
func (rr *ReadResult) addParseError(rawLineBudget *int, lineNum int, line []byte, err error) {
	if len(rr.ParseErrors) >= maxRetainedParseErrors {
		rr.ParseErrorsDropped++
		return
	}
	raw := ""
	if *rawLineBudget+len(line) <= maxRetainedParseErrorBytes {
		raw = string(line)
		*rawLineBudget += len(line)
	}
	rr.ParseErrors = append(rr.ParseErrors, ParseError{
		LineNum: lineNum,
		RawLine: raw,
		Err:     err,
	})
}
