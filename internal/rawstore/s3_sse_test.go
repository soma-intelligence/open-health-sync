// SPDX-License-Identifier: AGPL-3.0-only

package rawstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// putHeaders opens the S3 store with the given URL parameters against a
// server that accepts every write, puts one object and returns the headers
// of that PUT.
func putHeaders(t *testing.T, params string) http.Header {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	var mu sync.Mutex
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Method == http.MethodPut {
			mu.Lock()
			got = r.Header.Clone()
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	st, err := Open("s3://bucket?region=us-east-1&path_style=true&endpoint=" + srv.URL + params)
	require.NoError(t, err)
	require.NoError(t, st.Put(context.Background(),
		"raw/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/test/2026-01-01/a.json.gz",
		strings.NewReader("x")))
	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, got, "the server saw no PUT")
	return got
}

// Server-side encryption is asked for on every put when the URL says so, and
// never by default.
func TestS3ServerSideEncryption(t *testing.T) {
	const sse, kmsKey = "X-Amz-Server-Side-Encryption", "X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id"
	t.Run("off by default", func(t *testing.T) {
		h := putHeaders(t, "")
		assert.Empty(t, h.Values(sse))
		assert.Empty(t, h.Values(kmsKey))
		assert.Equal(t, "*", h.Get("If-None-Match"))
	})
	t.Run("AES256", func(t *testing.T) {
		h := putHeaders(t, "&sse=AES256")
		assert.Equal(t, "AES256", h.Get(sse))
		assert.Empty(t, h.Values(kmsKey))
	})
	t.Run("aws:kms with the default key", func(t *testing.T) {
		h := putHeaders(t, "&sse=aws:kms")
		assert.Equal(t, "aws:kms", h.Get(sse))
		assert.Empty(t, h.Values(kmsKey))
	})
	t.Run("aws:kms with a key", func(t *testing.T) {
		h := putHeaders(t, "&sse=aws:kms&sse_kms_key_id=arn:aws:kms:us-east-1:111122223333:key/invented")
		assert.Equal(t, "aws:kms", h.Get(sse))
		assert.Equal(t, "arn:aws:kms:us-east-1:111122223333:key/invented", h.Get(kmsKey))
	})
}

func TestS3ServerSideEncryptionBadSettings(t *testing.T) {
	for _, params := range []string{"sse=aes256", "sse=DES", "sse_kms_key_id=k", "sse=AES256&sse_kms_key_id=k"} {
		_, err := Open("s3://bucket?region=us-east-1&" + params)
		assert.Error(t, err, params)
	}
}
