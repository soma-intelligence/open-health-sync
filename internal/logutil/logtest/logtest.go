// SPDX-License-Identifier: AGPL-3.0-only

// Package logtest redirects the process logger in tests, so a test can assert on what the code
// under test logs. The process logger is the zerolog global that internal/logging.Init sets.
package logtest

import (
	"bytes"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Buffer is a mutex guarded buffer: a log line may be written from another goroutine.
type Buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

// String is everything logged so far, one JSON line per event.
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// Reset forgets what was logged.
func (b *Buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.b.Reset()
}

// Capture sends the process logger to a buffer, without a timestamp, for lines at level or
// above, and restores the logger and the global level when the test ends.
func Capture(t testing.TB, level zerolog.Level) *Buffer {
	t.Helper()
	buf := &Buffer{}
	prev, prevLevel := log.Logger, zerolog.GlobalLevel()
	log.Logger = zerolog.New(buf)
	zerolog.SetGlobalLevel(level)
	t.Cleanup(func() {
		log.Logger = prev
		zerolog.SetGlobalLevel(prevLevel)
	})
	return buf
}

// Discard silences the process logger for the rest of the process, for a TestMain of a package
// whose tests log a lot and assert nothing on it.
func Discard() { log.Logger = zerolog.Nop() }
