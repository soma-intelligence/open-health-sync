// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func serveTest(t *testing.T, h http.Handler, max int) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := newHTTPServer("", h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { _ = srv.Serve(limitConnections(ln, max)) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// A client that sends its headers and then dribbles the body used to keep
// its connection and a goroutine forever, on any route, without a key: the
// server reads an unread body before it answers. The request read timeout
// now closes it.
func TestSlowBodyIsCut(t *testing.T) {
	old := requestReadTimeout
	requestReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { requestReadTimeout = old })
	addr := serveTest(t, http.NotFoundHandler(), 0)

	c, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer c.Close()
	_, err = io.WriteString(c, "POST /inbound/x HTTP/1.1\r\nHost: x\r\nContent-Length: 100000\r\n\r\n{")
	require.NoError(t, err)
	start := time.Now()
	closed := false
	for time.Since(start) < 5*time.Second {
		_, _ = c.Write([]byte(" "))
		_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		buf := make([]byte, 512)
		if _, err := c.Read(buf); err != nil && !isTimeout(err) {
			closed = true
			break
		}
	}
	require.True(t, closed, "the slow body was still being read after 5 s")
	require.Less(t, time.Since(start), 3*time.Second)
}

// An answer that takes longer than the read timeout, once the request is
// read, is not cut: the server clears the read deadline at the end of the
// body (an export streams for minutes).
func TestLongAnswerIsNotCut(t *testing.T) {
	old := requestReadTimeout
	requestReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { requestReadTimeout = old })
	addr := serveTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		for i := 0; i < 6; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			_, _ = io.WriteString(w, "x")
			w.(http.Flusher).Flush()
		}
	}), 0)
	for _, body := range []string{"", `{"a":1}`} {
		resp, err := http.Post("http://"+addr+"/export", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		b, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, "xxxxxx", string(b), "body %q", body)
	}
}

// Connections over the cap are closed at once; a slot comes back when a
// connection closes.
func TestConnectionCap(t *testing.T) {
	addr := serveTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }), 2)
	held := make([]net.Conn, 0, 2)
	for range 2 {
		c, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		held = append(held, c)
		// One request on it, so it is accepted and counted.
		_, err = io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	over, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	_ = over.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = io.WriteString(over, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	if err == nil {
		_, err = over.Read(make([]byte, 64))
	}
	require.Error(t, err, "a third connection must be closed")
	require.False(t, isTimeout(err), "the third connection was left open: %v", err)
	_ = over.Close()

	_ = held[0].Close()
	require.Eventually(t, func() bool {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return false
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(time.Second))
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			return false
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == 200
	}, 3*time.Second, 50*time.Millisecond)
	_ = held[1].Close()
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}
