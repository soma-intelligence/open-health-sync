// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
)

// accessLine writes the one access line of a finished request. The level follows the status
// (2xx and 3xx debug, 4xx info, 5xx error), the route is the matched pattern and never the raw
// path, and no header, query string or body reaches the line.
func accessLine(r *http.Request, status int, bytes int64, took time.Duration) {
	lvl := zerolog.DebugLevel
	switch {
	case status >= 500:
		lvl = zerolog.ErrorLevel
	case status >= 400:
		lvl = zerolog.InfoLevel
	}
	route := r.Pattern
	if route == "" {
		route = "unmatched"
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	logging.Ctx(r.Context()).WithLevel(lvl).Str("method", r.Method).Str("route", route).Int("status", status).
		Int64("duration_ms", took.Milliseconds()).Int64("bytes", bytes).Str("remote_ip", ip).Msg("http_request")
}
