// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGetter implements ObjectGetter for testing.
type fakeGetter struct {
	data    map[string][]byte // key -> stored bytes
	failKey string            // if Get is called with this key, return error
}

func (f *fakeGetter) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == f.failKey {
		return nil, fmt.Errorf("get %s: %w", key, rawstore.ErrNotFound)
	}
	data, ok := f.data[key]
	if !ok {
		return nil, fmt.Errorf("get %s: %w", key, rawstore.ErrNotFound)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// gzipLines compresses multiple lines of NDJSON into gzip bytes.
func gzipLines(lines []string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, line := range lines {
		mustGzip(gz.Write([]byte(line + "\n")))
	}
	mustGzip(0, gz.Close())
	return buf.Bytes()
}

// gzipBytes compresses raw bytes into gzip.
func gzipBytes(data []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	mustGzip(gz.Write(data))
	mustGzip(0, gz.Close())
	return buf.Bytes()
}

// mustGzip panics on a gzip write error. The helpers write to a bytes.Buffer,
// so an error means a broken fixture, not a test outcome.
func mustGzip(_ int, err error) {
	if err != nil {
		panic(err)
	}
}

// --- Tests ---

func TestReaderValidFile(t *testing.T) {
	lines := []string{
		`{"type":"HKQuantityTypeIdentifierHeartRate","value":72,"unit":"count/min","startDate":"2025-01-15T10:00:00Z"}`,
		`{"type":"HKQuantityTypeIdentifierStepCount","value":1500,"unit":"count","startDate":"2025-01-15T10:05:00Z"}`,
		`{"type":"HKQuantityTypeIdentifierBodyMass","value":75.5,"unit":"kg","startDate":"2025-01-15T06:00:00Z"}`,
	}
	data := gzipLines(lines)

	fake := &fakeGetter{
		data: map[string][]byte{"test/sample.ndjson.gz": data},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/sample.ndjson.gz")

	require.NoError(t, err)
	assert.Len(t, result.Records, 3)
	assert.Empty(t, result.ParseErrors)
	assert.Equal(t, int64(len(data)), result.FileSize)

	// Verify record content
	assert.Equal(t, "HKQuantityTypeIdentifierHeartRate", result.Records[0]["type"])
	assert.Equal(t, float64(72), result.Records[0]["value"])
}

func TestReaderMixedTypesWithMalformedLine(t *testing.T) {
	lines := []string{
		`{"type":"heart_rate","value":72}`,
		`{"type":"step_count","value":1500}`,
		`{"type":"body_mass","value":75.5}`,
		`{invalid`,
		``,
		`{"type":"spo2","value":98}`,
	}
	data := gzipLines(lines)

	fake := &fakeGetter{
		data: map[string][]byte{"test/mixed.ndjson.gz": data},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/mixed.ndjson.gz")

	require.NoError(t, err)
	// 4 valid records (blank line is skipped, not counted)
	assert.Len(t, result.Records, 4)
	// 1 parse error (malformed JSON)
	assert.Len(t, result.ParseErrors, 1)
	assert.Equal(t, 4, result.ParseErrors[0].LineNum) // line 4 is malformed
	assert.Contains(t, result.ParseErrors[0].RawLine, "{invalid")
	assert.NotNil(t, result.ParseErrors[0].Err)
}

func TestReaderEmptyFile(t *testing.T) {
	data := gzipBytes([]byte{})

	fake := &fakeGetter{
		data: map[string][]byte{"test/empty.ndjson.gz": data},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/empty.ndjson.gz")

	require.NoError(t, err)
	assert.Len(t, result.Records, 0)
	assert.Empty(t, result.ParseErrors)
}

func TestReaderNonExistentKey(t *testing.T) {
	fake := &fakeGetter{
		data:    map[string][]byte{},
		failKey: "nonexistent/key.ndjson.gz",
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "nonexistent/key.ndjson.gz")

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, rawstore.ErrNotFound)
}

func TestReaderLargeLines(t *testing.T) {
	// Create a line that's about 5MB to verify scanner buffer handles large ECG lines
	bigValue := strings.Repeat("x", 5*1024*1024)
	lines := []string{
		fmt.Sprintf(`{"type":"ecg","data":"%s"}`, bigValue),
	}
	data := gzipLines(lines)

	fake := &fakeGetter{
		data: map[string][]byte{"test/large.ndjson.gz": data},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/large.ndjson.gz")

	require.NoError(t, err)
	assert.Len(t, result.Records, 1)
	assert.Empty(t, result.ParseErrors)
}

func TestReaderParseErrorContainsLineInfo(t *testing.T) {
	lines := []string{
		`{"valid":true}`,
		`not json at all`,
		`{"also_valid":true}`,
	}
	data := gzipLines(lines)

	fake := &fakeGetter{
		data: map[string][]byte{"test/errors.ndjson.gz": data},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/errors.ndjson.gz")

	require.NoError(t, err)
	assert.Len(t, result.Records, 2)
	require.Len(t, result.ParseErrors, 1)
	assert.Equal(t, 2, result.ParseErrors[0].LineNum)
	assert.Equal(t, "not json at all", result.ParseErrors[0].RawLine)
	assert.Error(t, result.ParseErrors[0].Err)
}

func TestReaderBlankLinesSkipped(t *testing.T) {
	lines := []string{
		`{"a":1}`,
		``,
		``,
		`{"b":2}`,
		``,
	}
	data := gzipLines(lines)

	fake := &fakeGetter{
		data: map[string][]byte{"test/blanks.ndjson.gz": data},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/blanks.ndjson.gz")

	require.NoError(t, err)
	assert.Len(t, result.Records, 2)
	assert.Empty(t, result.ParseErrors)
}

// The raw store reports no content length, so FileSize is the count of stored
// (compressed) bytes the reader consumed.
func TestReaderFileSizeCountsStoredBytes(t *testing.T) {
	lines := []string{`{"x":1}`}
	data := gzipLines(lines)

	fake := &fakeGetter{
		data: map[string][]byte{"test/nosize.ndjson.gz": data},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/nosize.ndjson.gz")

	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), result.FileSize)
	assert.Len(t, result.Records, 1)
}

// --- Fix 2: scanner error preserves parsed head ---

func TestReaderTruncatedGzipPreservesHead(t *testing.T) {
	// A valid gzip stream truncated mid-way: the first records decode, then the
	// reader hits an unexpected EOF. Fix 2 keeps the parsed head and flags
	// ScanTruncated instead of discarding the whole object.
	lines := []string{
		`{"a":1}`,
		`{"b":2}`,
		`{"c":3}`,
	}
	full := gzipLines(lines)
	// Drop the gzip trailer/tail so decompression aborts after some records.
	truncated := full[:len(full)-5]

	fake := &fakeGetter{
		data: map[string][]byte{"test/trunc.ndjson.gz": truncated},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/trunc.ndjson.gz")

	require.NoError(t, err, "truncation must NOT be a hard error (Fix 2)")
	require.NotNil(t, result)
	assert.True(t, result.ScanTruncated, "ScanTruncated must be set on a truncated stream")
	assert.GreaterOrEqual(t, len(result.Records), 1, "parsed head must be preserved")
	require.NotEmpty(t, result.ParseErrors, "a synthetic ParseError marks the truncation")
	assert.Contains(t, result.ParseErrors[len(result.ParseErrors)-1].Err.Error(), "scan truncated")
}

func TestReaderOversizedLinePreservesOthers(t *testing.T) {
	// With a small custom line cap, an over-cap line aborts the scan. Fix 2 keeps
	// the records read before it and marks ScanTruncated. The over-cap line is the
	// last one here, so the two good records survive.
	bigValue := strings.Repeat("x", 2*1024*1024) // 2 MB line
	lines := []string{
		`{"a":1}`,
		`{"b":2}`,
		fmt.Sprintf(`{"big":"%s"}`, bigValue),
	}
	data := gzipLines(lines)

	fake := &fakeGetter{
		data: map[string][]byte{"test/oversized.ndjson.gz": data},
	}

	// 1 MB cap: the 2 MB line is over-cap.
	reader := NewReaderWithMaxLine(fake, 1*1024*1024)
	result, err := reader.ReadNDJSON(context.Background(), "test/oversized.ndjson.gz")

	require.NoError(t, err, "an over-cap line must NOT dead-letter the object (Fix 2)")
	assert.True(t, result.ScanTruncated)
	assert.Len(t, result.Records, 2, "the two in-cap records must be preserved")
}

func TestReaderLargeLineUnderRaisedCap(t *testing.T) {
	// Fix 5: a 12 MB ECG line that the old 10 MB cap would have aborted now reads
	// fine under the 32 MB default.
	bigValue := strings.Repeat("y", 12*1024*1024)
	lines := []string{fmt.Sprintf(`{"type":"ecg","data":"%s"}`, bigValue)}
	data := gzipLines(lines)

	fake := &fakeGetter{
		data: map[string][]byte{"test/ecg12mb.ndjson.gz": data},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/ecg12mb.ndjson.gz")

	require.NoError(t, err)
	assert.False(t, result.ScanTruncated)
	assert.Len(t, result.Records, 1, "12 MB line fits under the 32 MB default cap")
	assert.Empty(t, result.ParseErrors)
}

// --- Fix 4: gzip magic-byte detection + plain-NDJSON fallback ---

func TestReaderPlainNDJSONFallback(t *testing.T) {
	// A non-gzip (plain NDJSON) body must be read as-is, not dead-lettered at
	// gzip.NewReader.
	plain := []byte("{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n")

	fake := &fakeGetter{
		data: map[string][]byte{"test/plain.ndjson.gz": plain},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/plain.ndjson.gz")

	require.NoError(t, err, "plain NDJSON must fall back to a plain read (Fix 4)")
	assert.Len(t, result.Records, 3)
	assert.Empty(t, result.ParseErrors)
	assert.False(t, result.ScanTruncated)
}

func TestReaderGzipStillWorksWithMagicDetection(t *testing.T) {
	// Sanity: a normal gzip body still decompresses after the magic-byte peek.
	lines := []string{`{"x":1}`, `{"y":2}`}
	data := gzipLines(lines)
	fake := &fakeGetter{
		data: map[string][]byte{"test/ok.ndjson.gz": data},
	}
	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/ok.ndjson.gz")
	require.NoError(t, err)
	assert.Len(t, result.Records, 2)
}

func TestReaderFiveValidOneMalformed(t *testing.T) {
	// Explicit test: 5 valid + 1 malformed = 5 records, 1 error
	lines := []string{
		`{"a":1}`,
		`{"b":2}`,
		`{"c":3}`,
		`{"d":4}`,
		`{"e":5}`,
		`{broken`,
	}
	data := gzipLines(lines)

	fake := &fakeGetter{
		data: map[string][]byte{"test/5plus1.ndjson.gz": data},
	}

	reader := NewReader(fake)
	result, err := reader.ReadNDJSON(context.Background(), "test/5plus1.ndjson.gz")

	require.NoError(t, err)
	assert.Len(t, result.Records, 5, "should have 5 parsed records")
	assert.Len(t, result.ParseErrors, 1, "should have 1 parse error")
	assert.Equal(t, 6, result.ParseErrors[0].LineNum)
}

// sealedObject is a fresh key set and the sealed copy of body under it.
func sealedObject(t *testing.T, body []byte) (*vault.UserKeys, []byte) {
	t.Helper()
	k, err := vault.GenerateUserKeys(2)
	require.NoError(t, err)
	pub, err := k.Public()
	require.NoError(t, err)
	var b bytes.Buffer
	require.NoError(t, vault.SealObject(&b, pub, k.Version, vault.RawDigest(pub, body), bytes.NewReader(body)))
	return k, b.Bytes()
}

func keysOf(k *vault.UserKeys) func(uint32) (*vault.UserKeys, error) {
	return func(v uint32) (*vault.UserKeys, error) {
		if v != k.Version {
			return nil, fmt.Errorf("%w: no version %d", vault.ErrOpen, v)
		}
		return k, nil
	}
}

func TestReaderOpensSealedObject(t *testing.T) {
	body := gzipLines([]string{`{"a":1}`, `not json`, `{"b":2}`})
	k, sealed := sealedObject(t, body)
	g := &fakeGetter{data: map[string][]byte{"plain": body, "sealed": sealed}}

	want, err := NewReader(g).ReadNDJSON(context.Background(), "plain")
	require.NoError(t, err)
	r := NewReader(g).WithKeys(keysOf(k))
	got, err := r.ReadNDJSON(context.Background(), "sealed")
	require.NoError(t, err)
	assert.Equal(t, want.Records, got.Records)
	assert.Equal(t, want.ParseErrors, got.ParseErrors)
	assert.False(t, got.ScanTruncated)
	assert.EqualValues(t, len(sealed), got.FileSize, "FileSize counts the stored, sealed bytes")

	// The same reader still reads a legacy object.
	legacy, err := r.ReadNDJSON(context.Background(), "plain")
	require.NoError(t, err)
	assert.Equal(t, want.Records, legacy.Records)
}

func TestReaderSealedObjectNeedsKeys(t *testing.T) {
	body := gzipLines([]string{`{"a":1}`})
	_, sealed := sealedObject(t, body)
	other, _ := sealedObject(t, body)
	g := &fakeGetter{data: map[string][]byte{"sealed": sealed}}

	_, err := NewReader(g).ReadNDJSON(context.Background(), "sealed")
	require.Error(t, err, "no keys: nothing is read, not an empty object")

	_, err = NewReader(g).WithKeys(keysOf(other)).ReadNDJSON(context.Background(), "sealed")
	require.ErrorIs(t, err, vault.ErrOpen, "another user's keys")
}

// A chunk that does not open ends the read like a corrupt gzip tail: a
// stream truncation, nothing of the bad chunk returned.
func TestReaderSealedBadChunkIsStreamTruncation(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	lines := make([]string, 0, 3000)
	for i := range 3000 {
		pad := make([]byte, 64)
		for j := range pad {
			pad[j] = byte(rng.Uint32())
		}
		lines = append(lines, fmt.Sprintf(`{"i":%d,"pad":"%x"}`, i, pad))
	}
	k, sealed := sealedObject(t, gzipLines(lines))
	require.Greater(t, len(sealed), 2*64<<10, "more than one chunk")
	sealed[len(sealed)-20] ^= 1
	g := &fakeGetter{data: map[string][]byte{"sealed": sealed}}

	rr, err := NewReader(g).WithKeys(keysOf(k)).ReadNDJSON(context.Background(), "sealed")
	require.NoError(t, err)
	assert.True(t, rr.ScanTruncated)
	assert.Equal(t, TruncationStream, rr.Truncation)
	assert.Less(t, rr.RecordCount, len(lines))
}

// A sealed object whose body opens but does not match its header's digest
// fails the read: never records, never a partial.
func TestReaderSealedWrongDigestFails(t *testing.T) {
	body := gzipLines([]string{`{"a":1}`, `{"b":2}`})
	k, err := vault.GenerateUserKeys(2)
	require.NoError(t, err)
	pub, err := k.Public()
	require.NoError(t, err)
	var b bytes.Buffer
	require.NoError(t, vault.SealObject(&b, pub, k.Version, vault.RawDigest(pub, []byte("other")), bytes.NewReader(body)))
	g := &fakeGetter{data: map[string][]byte{"sealed": b.Bytes()}}

	rr, err := NewReader(g).WithKeys(keysOf(k)).ReadNDJSON(context.Background(), "sealed")
	require.ErrorIs(t, err, vault.ErrOpen)
	assert.Contains(t, err.Error(), "digest")
	assert.Nil(t, rr)
}
