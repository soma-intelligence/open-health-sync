// SPDX-License-Identifier: AGPL-3.0-only

package logbridge

import (
	"bytes"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func TestNewWritesOneLeveledLinePerCall(t *testing.T) {
	var buf bytes.Buffer
	l := New(zerolog.New(&buf), zerolog.WarnLevel, "http_server_error")
	l.Printf("http: TLS handshake error from %s", "10.0.0.1:1234")
	assert.JSONEq(t, `{"level":"warn","detail":"http: TLS handshake error from 10.0.0.1:1234","message":"http_server_error"}`, buf.String())
}
