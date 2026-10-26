// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// requestReadTimeout bounds reading one request, its body included, on the
// API and metrics servers. Without it a client that sends its headers, then
// one byte of body every few seconds, held its connection and a goroutine
// for as long as it liked, on any route and without a credential: Go reads
// the rest of an unread body before it sends the answer. The upload route
// extends it to its own 5 minutes and the notification route cuts it to 30
// s. Once a body is read in full the server clears the deadline, so a long
// answer (an export) is not cut.
var requestReadTimeout = 2 * time.Minute

// newHTTPServer is the API's and the metrics' server: header and request
// read timeouts, an idle timeout, and errors in the process log.
func newHTTPServer(addr string, h http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       requestReadTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

// capListener accepts at most cap connections at once. A connection over
// the cap is closed as soon as it is accepted: thousands of idle or slow
// connections cost about 35 KB of memory each, and enough of them would
// push the process past its memory limit. Closing (rather than leaving them
// in the kernel's backlog) lets the client retry while the others keep
// being served.
type capListener struct {
	net.Listener
	slots   chan struct{}
	refused atomic.Int64
	// lastLog is the unix time of the last refusal logged, so a flood logs
	// once a minute.
	lastLog atomic.Int64
}

// limitConnections wraps ln; a cap below 1 returns ln as is.
func limitConnections(ln net.Listener, max int) net.Listener {
	if max < 1 {
		return ln
	}
	return &capListener{Listener: ln, slots: make(chan struct{}, max)}
}

func (l *capListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &capConn{Conn: c, l: l}, nil
		default:
		}
		_ = c.Close()
		n := l.refused.Add(1)
		now := time.Now().Unix()
		if last := l.lastLog.Load(); now-last >= 60 && l.lastLog.CompareAndSwap(last, now) {
			slog.Warn("api: connection refused, OHS_MAX_CONNECTIONS reached",
				"max_connections", cap(l.slots), "refused_total", n)
		}
	}
}

// capConn gives its slot back when it is closed, once.
type capConn struct {
	net.Conn
	l    *capListener
	once sync.Once
}

func (c *capConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { <-c.l.slots })
	return err
}
