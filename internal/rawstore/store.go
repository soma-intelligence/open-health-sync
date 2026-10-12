// SPDX-License-Identifier: AGPL-3.0-only

// Package rawstore keeps the raw provider payloads. Raw files are written
// once and never rewritten, so Put refuses to overwrite an existing key.
//
// Two drivers exist: a filesystem driver ("file:///abs/path") and an S3
// driver ("s3://bucket?endpoint=...&region=...&path_style=true"). The S3 driver
// takes its credentials from the standard AWS default chain (AWS_ACCESS_KEY_ID
// and AWS_SECRET_ACCESS_KEY, shared config files, instance roles); nothing
// secret ever appears in the URL.
//
// The S3 server must honour conditional puts (If-None-Match: *), which is what
// makes Put create-if-absent. versitygw must not run with --nometa: it then
// ignores the header.
package rawstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

var (
	// ErrExists is returned by Put when the key is already stored.
	ErrExists = errors.New("rawstore: key exists")
	// ErrNotFound is returned by Get when the key is absent.
	ErrNotFound = errors.New("rawstore: key not found")
	// ErrInvalidKey is returned for keys containing "..", "." or empty segments, a leading or trailing "/", or temp-file names.
	ErrInvalidKey = errors.New("rawstore: invalid key")
)

// Store is a write-once object store addressed by slash separated keys.
type Store interface {
	// Put stores r under key. It fails with ErrExists if the key exists.
	Put(ctx context.Context, key string, r io.Reader) error
	// Get opens the object. It fails with ErrNotFound if the key is absent.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// List returns the keys starting with prefix, sorted.
	List(ctx context.Context, prefix string) ([]string, error)
	// Walk calls fn with the keys starting with prefix in pages of up to
	// WalkPageSize, each page sorted, so a caller never holds the whole store.
	// An error from fn stops the walk and is returned. Pages are not globally
	// ordered. Keys written during the walk may or may not be seen.
	Walk(ctx context.Context, prefix string, fn func(keys []string) error) error
	// DeletePrefix removes every key starting with prefix and nothing else.
	DeletePrefix(ctx context.Context, prefix string) error
}

// WalkPageSize is the most keys one Walk page holds.
const WalkPageSize = 1000

// Open builds a Store from a URL: "file:///abs/path" or
// "s3://bucket?endpoint=&region=&path_style=true".
func Open(rawURL string) (Store, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("rawstore: parse url: %w", err)
	}
	switch u.Scheme {
	case "file":
		if u.Host != "" || !strings.HasPrefix(u.Path, "/") {
			return nil, fmt.Errorf("rawstore: file url needs an absolute path (file:///abs/path)")
		}
		return openFS(u.Path)
	case "s3":
		return openS3(u)
	default:
		return nil, fmt.Errorf("rawstore: unsupported scheme %q", u.Scheme)
	}
}

// pinger is implemented by drivers that have a cheaper or stricter readiness
// check than listing.
type pinger interface {
	Ping(ctx context.Context) error
}

// pingPrefix names no object: listing it proves the store answers.
const pingPrefix = "raw/readyz-probe/"

// Ping reports whether the store is usable: the filesystem driver checks its
// root is still a directory, any other driver lists an empty prefix.
func Ping(ctx context.Context, s Store) error {
	if p, ok := s.(pinger); ok {
		return p.Ping(ctx)
	}
	_, err := s.List(ctx, pingPrefix)
	return err
}

// writeProbePrefix is outside raw/, so the file scanner and retention never
// see a probe, even one a crash left behind.
const writeProbePrefix = "probe/.ohs-write-check/"

// CheckWritable writes a small object and deletes it, proving the store
// accepts writes: a read-only mount or a directory owned by another user
// lists fine but fails every upload.
func CheckWritable(ctx context.Context, s Store) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	dir := writeProbePrefix + hex.EncodeToString(b[:]) + "/"
	if err := s.Put(ctx, dir+"probe", strings.NewReader("ok")); err != nil {
		return fmt.Errorf("not writable: %w", err)
	}
	if err := s.DeletePrefix(ctx, dir); err != nil {
		return fmt.Errorf("cannot delete the write probe %s: %w", dir, err)
	}
	return nil
}
