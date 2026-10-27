// SPDX-License-Identifier: AGPL-3.0-only

//go:build awskms

package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/stretchr/testify/require"
)

const (
	awsTestKey   = "arn:aws:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	awsTestAlias = "arn:aws:kms:us-east-1:111122223333:alias/ohs-root"
)

// fakeKMS speaks the KMS JSON 1.1 shapes of Encrypt and Decrypt for one key.
// "Encrypting" is AES-GCM with the encryption context as additional data,
// which is what KMS does with it.
type fakeKMS struct {
	t        *testing.T
	mu       sync.Mutex
	aead     cipher.AEAD
	keyARN   string // the key an alias resolves to
	fail     int    // status to answer with
	failType string
	empty    bool
	calls    int
	lastCtx  map[string]string
	srv      *httptest.Server
}

func newFakeKMS(t *testing.T) *fakeKMS {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	b, _ := aes.NewCipher(k)
	g, _ := cipher.NewGCM(b)
	f := &fakeKMS{t: t, aead: g, keyARN: awsTestKey}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKMS) provider(keyARN string) *awsKMSProvider {
	c := kms.New(kms.Options{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(f.srv.URL),
		Credentials:      credentials.NewStaticCredentialsProvider("AKIATEST", "secret", ""),
		RetryMaxAttempts: 1,
		HTTPClient:       f.srv.Client(),
	})
	return newAWSKMS(c, keyARN)
}

func (f *fakeKMS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	require.True(f.t, strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256"), "requests are signed")
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	if f.fail != 0 {
		w.Header().Set("X-Amzn-ErrorType", f.failType)
		w.WriteHeader(f.fail)
		_ = json.NewEncoder(w).Encode(map[string]string{"__type": f.failType, "message": "detail from kms"})
		return
	}
	var in struct {
		KeyId             string
		Plaintext         []byte
		CiphertextBlob    []byte
		EncryptionContext map[string]string
	}
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
	f.lastCtx = in.EncryptionContext
	ctxBytes, _ := json.Marshal(in.EncryptionContext) // map keys sort: stable
	switch r.Header.Get("X-Amz-Target") {
	case "TrentService.Encrypt":
		nonce := make([]byte, f.aead.NonceSize())
		_, _ = rand.Read(nonce)
		ct := f.aead.Seal(nonce, nonce, in.Plaintext, ctxBytes)
		out := map[string]any{"CiphertextBlob": ct, "KeyId": f.keyARN, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"}
		if f.empty {
			out["CiphertextBlob"] = []byte{}
		}
		_ = json.NewEncoder(w).Encode(out)
	case "TrentService.Decrypt":
		n := f.aead.NonceSize()
		if in.KeyId != f.keyARN || len(in.CiphertextBlob) < n {
			f.refuse(w, "NotFoundException")
			return
		}
		pt, err := f.aead.Open(nil, in.CiphertextBlob[:n], in.CiphertextBlob[n:], ctxBytes)
		if err != nil {
			f.refuse(w, "InvalidCiphertextException")
			return
		}
		out := map[string]any{"Plaintext": pt, "KeyId": f.keyARN, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"}
		if f.empty {
			out["Plaintext"] = []byte{}
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		f.refuse(w, "UnknownOperationException")
	}
}

func (f *fakeKMS) refuse(w http.ResponseWriter, typ string) {
	w.Header().Set("X-Amzn-ErrorType", typ)
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": typ, "message": "detail from kms"})
}

func TestAWSKMSWrapUnwrap(t *testing.T) {
	ctx := context.Background()
	f := newFakeKMS(t)
	p := f.provider(awsTestKey)
	require.Equal(t, "awskms", p.Name())

	key := rootKey(7)
	wrapped, version, err := p.Wrap(ctx, key, []byte("tenant-1"))
	require.NoError(t, err)
	require.Equal(t, awsTestKey, version)
	require.Equal(t, map[string]string{"ohs": base64.StdEncoding.EncodeToString([]byte("tenant-1"))}, f.lastCtx)
	require.NotContains(t, string(wrapped), string(key))

	got, err := p.Unwrap(ctx, wrapped, []byte("tenant-1"), version)
	require.NoError(t, err)
	require.Equal(t, key, got)

	// Another aad is another encryption context: KMS refuses.
	_, err = p.Unwrap(ctx, wrapped, []byte("tenant-2"), version)
	require.ErrorContains(t, err, "InvalidCiphertextException")
	require.False(t, errors.Is(err, ErrProviderUnavailable))

	// A version that names another key is refused.
	_, err = p.Unwrap(ctx, wrapped, []byte("tenant-1"), "arn:aws:kms:us-east-1:111122223333:key/other")
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrProviderUnavailable))
	_, err = p.Unwrap(ctx, wrapped, []byte("tenant-1"), "not an arn")
	require.Error(t, err)
	_, err = p.Unwrap(ctx, wrapped, []byte("tenant-1"), "")
	require.Error(t, err)

	bad := append([]byte(nil), wrapped...)
	bad[len(bad)-1] ^= 1
	_, err = p.Unwrap(ctx, bad, []byte("tenant-1"), version)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrProviderUnavailable))
}

func TestAWSKMSAliasWrapsUnderTheKeyARN(t *testing.T) {
	ctx := context.Background()
	f := newFakeKMS(t)
	p := f.provider(awsTestAlias)
	wrapped, version, err := p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.NoError(t, err)
	require.Equal(t, awsTestKey, version, "the version is the key the alias resolved to")
	_, err = p.Unwrap(ctx, wrapped, []byte("tenant-1"), version)
	require.NoError(t, err)
}

func TestAWSKMSUnwrapPinsAccountAndRegion(t *testing.T) {
	ctx := context.Background()
	f := newFakeKMS(t)
	p := f.provider(awsTestKey)
	wrapped, _, err := p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.NoError(t, err)
	f.mu.Lock()
	f.calls = 0
	f.mu.Unlock()
	for _, v := range []string{
		"arn:aws:kms:us-east-1:999999999999:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		"arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		"arn:aws-us-gov:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		awsTestAlias,
	} {
		_, err = p.Unwrap(ctx, wrapped, []byte("tenant-1"), v)
		require.Error(t, err, v)
	}
	require.Zero(t, f.calls, "refused before any KMS call")
}

func TestAWSKMSWrapPinsConfiguredKeyARN(t *testing.T) {
	f := newFakeKMS(t)
	f.keyARN = "arn:aws:kms:us-east-1:111122223333:key/another"
	_, _, err := f.provider(awsTestKey).Wrap(context.Background(), rootKey(1), []byte("tenant-1"))
	require.ErrorContains(t, err, "unexpected key")
	f.keyARN = "arn:aws:kms:us-east-1:999999999999:key/x"
	_, _, err = f.provider(awsTestAlias).Wrap(context.Background(), rootKey(1), []byte("tenant-1"))
	require.ErrorContains(t, err, "unexpected key id")
}

func TestAWSKMSCredentialErrorsArePermanent(t *testing.T) {
	f := newFakeKMS(t)
	c := kms.New(kms.Options{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(f.srv.URL),
		Credentials:      credentials.NewStaticCredentialsProvider("", "", ""),
		RetryMaxAttempts: 1,
		HTTPClient:       f.srv.Client(),
	})
	_, _, err := newAWSKMS(c, awsTestKey).Wrap(context.Background(), rootKey(1), []byte("tenant-1"))
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrProviderUnavailable), err.Error())
	require.NotContains(t, err.Error(), f.srv.URL)
}

func TestAWSKMSRefusesEmptyAAD(t *testing.T) {
	ctx := context.Background()
	p := newFakeKMS(t).provider(awsTestKey)
	_, _, err := p.Wrap(ctx, rootKey(1), nil)
	require.Error(t, err)
	_, err = p.Unwrap(ctx, []byte("x"), nil, awsTestKey)
	require.Error(t, err)
}

func TestAWSKMSErrorClasses(t *testing.T) {
	ctx := context.Background()
	f := newFakeKMS(t)
	p := f.provider(awsTestKey)
	for _, c := range []struct {
		status      int
		typ         string
		unavailable bool
	}{
		{400, "ThrottlingException", true},
		{500, "KMSInternalException", true},
		{503, "ServiceUnavailableException", true},
		{429, "SomethingElse", true},
		{502, "", true},
		{400, "DependencyTimeoutException", true},
		{400, "AccessDeniedException", false},
		{400, "NotFoundException", false},
		{400, "DisabledException", false},
		{400, "KMSInvalidStateException", false},
	} {
		f.mu.Lock()
		f.fail, f.failType = c.status, c.typ
		f.mu.Unlock()
		_, _, err := p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
		require.Error(t, err, c.typ)
		require.Equal(t, c.unavailable, errors.Is(err, ErrProviderUnavailable), "%d %s: %v", c.status, c.typ, err)
	}
	f.srv.Close()
	_, _, err := p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.ErrorIs(t, err, ErrProviderUnavailable)
}

func TestAWSKMSMalformedAnswers(t *testing.T) {
	ctx := context.Background()
	f := newFakeKMS(t)
	p := f.provider(awsTestKey)
	wrapped, version, err := p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.NoError(t, err)

	f.mu.Lock()
	f.empty = true
	f.mu.Unlock()
	_, _, err = p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.ErrorContains(t, err, "empty encryption")
	_, err = p.Unwrap(ctx, wrapped, []byte("tenant-1"), version)
	require.ErrorContains(t, err, "empty plaintext")

	f.mu.Lock()
	f.empty = false
	f.keyARN = "not an arn"
	f.mu.Unlock()
	_, _, err = p.Wrap(ctx, rootKey(1), []byte("tenant-1"))
	require.ErrorContains(t, err, "unexpected key")
}

func TestParseKMSARN(t *testing.T) {
	a, err := parseKMSARN(awsTestKey)
	require.NoError(t, err)
	require.Equal(t, "us-east-1", a.Region)
	_, err = parseKMSARN(awsTestAlias)
	require.NoError(t, err)
	for _, bad := range []string{"", "1234abcd", "arn:aws:s3:::bucket", "arn:aws:kms::111122223333:key/x", "arn:aws:kms:us-east-1:111122223333:grant/x"} {
		_, err = parseKMSARN(bad)
		require.Error(t, err, bad)
	}
}

func TestOpenProviderAWSKMSRegistered(t *testing.T) {
	_, ok := cloudOpeners["awskms"]
	require.True(t, ok)
}
