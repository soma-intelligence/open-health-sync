// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Server-Sent Events framing (https://html.spec.whatwg.org/multipage/server-sent-events.html):
// fields are "name: value" lines, an event ends with a blank line, and a
// line starting with ":" is a comment the client ignores.

// sseField strips what would end a field early: a field value is one line.
func sseField(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// writeSSEEvent writes one event: its id (what the client sends back in
// Last-Event-ID), its name and its data, one data line per line of data.
// An empty id or name leaves the field out.
func writeSSEEvent(w io.Writer, id, name string, data []byte) error {
	var b bytes.Buffer
	if id != "" {
		b.WriteString("id: " + sseField(id) + "\n")
	}
	if name != "" {
		b.WriteString("event: " + sseField(name) + "\n")
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		b.WriteString("data: ")
		b.Write(bytes.TrimSuffix(line, []byte("\r")))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	_, err := w.Write(b.Bytes())
	return err
}

// writeSSERetry tells the client how long to wait before it reconnects.
func writeSSERetry(w io.Writer, d time.Duration) error {
	_, err := io.WriteString(w, "retry: "+strconv.FormatInt(d.Milliseconds(), 10)+"\n\n")
	return err
}

// writeSSEComment writes a comment: a heartbeat that keeps proxies and the
// client from timing the connection out, invisible to an EventSource.
func writeSSEComment(w io.Writer, text string) error {
	_, err := io.WriteString(w, ": "+sseField(text)+"\n\n")
	return err
}

// sseConn writes to a client and sends each write at once, under a write
// deadline: a client that stops reading is cut off after timeout instead of
// holding the handler. The deadline is set again before every write, so the
// stream itself lives as long as the client reads.
type sseConn struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

func newSSEConn(w http.ResponseWriter, timeout time.Duration) *sseConn {
	return &sseConn{w: w, rc: http.NewResponseController(w), timeout: timeout}
}

// send runs write against the response and flushes it.
func (c *sseConn) send(write func(io.Writer) error) error {
	if c.timeout > 0 {
		if err := c.rc.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
	}
	if err := write(c.w); err != nil {
		return err
	}
	return c.rc.Flush()
}
