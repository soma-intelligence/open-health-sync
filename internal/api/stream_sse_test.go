// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bufio"
	"bytes"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteSSEEvent(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, writeSSEEvent(&b, "0199d6c4-8a5e-7d3c-9f1e-6b2a4c8d0e17", "data.written", []byte(`{"a":1}`)))
	assert.Equal(t, "id: 0199d6c4-8a5e-7d3c-9f1e-6b2a4c8d0e17\nevent: data.written\ndata: {\"a\":1}\n\n", b.String())

	// Data over several lines is one data field per line; CR LF and CR
	// endings never leave a bare CR that would end a field early.
	b.Reset()
	require.NoError(t, writeSSEEvent(&b, "", "", []byte("one\r\ntwo\nthree")))
	assert.Equal(t, "data: one\ndata: two\ndata: three\n\n", b.String())

	// A newline in the id or the name cannot inject a field.
	b.Reset()
	require.NoError(t, writeSSEEvent(&b, "1\ndata: evil", "x\r\nid: 2", []byte("{}")))
	assert.Equal(t, "id: 1data: evil\nevent: xid: 2\ndata: {}\n\n", b.String())

	// Empty data is still one data line, so the event is dispatched.
	b.Reset()
	require.NoError(t, writeSSEEvent(&b, "", "stream.reset", nil))
	assert.Equal(t, "event: stream.reset\ndata: \n\n", b.String())
}

func TestWriteSSERetryAndComment(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, writeSSERetry(&b, 3*time.Second))
	require.NoError(t, writeSSEComment(&b, "ping"))
	require.NoError(t, writeSSEComment(&b, "two\nlines"))
	assert.Equal(t, "retry: 3000\n\n: ping\n\n: twolines\n\n", b.String())
}

// sseFrame is one block of a stream as a client parses it.
type sseFrame struct {
	id, event, data, comment string
	retry                    time.Duration
}

// readSSEFrame reads the next block (up to a blank line).
func readSSEFrame(r *bufio.Reader) (sseFrame, error) {
	var f sseFrame
	var data []string
	seen := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && seen {
				return f, io.ErrUnexpectedEOF
			}
			return f, err
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			if !seen {
				continue
			}
			f.data = strings.Join(data, "\n")
			return f, nil
		}
		seen = true
		if c, ok := strings.CutPrefix(line, ":"); ok {
			f.comment = strings.TrimPrefix(c, " ")
			continue
		}
		name, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "id":
			f.id = value
		case "event":
			f.event = value
		case "data":
			data = append(data, value)
		case "retry":
			ms, _ := strconv.Atoi(value)
			f.retry = time.Duration(ms) * time.Millisecond
		}
	}
}

func TestReadSSEFrameRoundTrip(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, writeSSERetry(&b, 1500*time.Millisecond))
	require.NoError(t, writeSSEComment(&b, "connected"))
	require.NoError(t, writeSSEEvent(&b, "id-1", "tag.created", []byte("a\nb")))
	r := bufio.NewReader(&b)
	f, err := readSSEFrame(r)
	require.NoError(t, err)
	assert.Equal(t, 1500*time.Millisecond, f.retry)
	f, err = readSSEFrame(r)
	require.NoError(t, err)
	assert.Equal(t, "connected", f.comment)
	f, err = readSSEFrame(r)
	require.NoError(t, err)
	assert.Equal(t, sseFrame{id: "id-1", event: "tag.created", data: "a\nb"}, f)
	_, err = readSSEFrame(r)
	assert.ErrorIs(t, err, io.EOF)
}
