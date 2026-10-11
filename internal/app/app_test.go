// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
)

// An unwritable raw store stops startup with a message naming the variable,
// instead of failing every upload later.
func TestRunRefusesUnwritableRawStore(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	cfg := config.Config{
		DatabaseURL:   "postgres://ohs@127.0.0.1:1/ohs?connect_timeout=1",
		RawStore:      "file://" + dir,
		LogLevel:      "info",
		EncryptionKey: make([]byte, 32),
		Roles:         map[string]bool{"api": true},
	}
	err := run(context.Background(), cfg, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OHS_RAW_STORE")
	require.Contains(t, err.Error(), "not writable")
}

func TestListenHint(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	_, err = net.Listen("tcp", ln.Addr().String())
	require.Error(t, err)
	require.Contains(t, listenHint(err, ln.Addr().String()), "OHS_LISTEN")
	require.Empty(t, listenHint(errors.New("other"), ":8080"))
}

// Durations log as "24h0m0s", not as nanoseconds, and a request id on the
// context lands on the line.
func TestLoggerFormat(t *testing.T) {
	var buf strings.Builder
	log := newLogger(&buf, slog.LevelInfo)
	log.InfoContext(reqid.With(context.Background(), "rid"), "x", "interval", 24*time.Hour)
	require.Contains(t, buf.String(), `"interval":"24h0m0s"`)
	require.Contains(t, buf.String(), `"request_id":"rid"`)
}

// A root the process cannot create gets the same chown hint.
func TestRunRawStoreCannotCreateRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root creates anything")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	cfg := config.Config{
		DatabaseURL: "postgres://ohs@127.0.0.1:1/ohs?connect_timeout=1", RawStore: "file://" + dir + "/raw",
		LogLevel: "info", EncryptionKey: make([]byte, 32), Roles: map[string]bool{"api": true},
	}
	err := run(context.Background(), cfg, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "chown")
}
