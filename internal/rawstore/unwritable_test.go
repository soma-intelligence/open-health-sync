// SPDX-License-Identifier: AGPL-3.0-only

package rawstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestUnwritable(t *testing.T) {
	for _, err := range []error{syscall.ENOSPC, syscall.EDQUOT, syscall.EROFS, syscall.EACCES,
		fmt.Errorf("put raw object: %w", &os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC})} {
		if !Unwritable(err) {
			t.Errorf("Unwritable(%v) = false", err)
		}
	}
	for _, err := range []error{nil, ErrExists, ErrNotFound, errors.New("boom"), syscall.ECONNREFUSED} {
		if Unwritable(err) {
			t.Errorf("Unwritable(%v) = true", err)
		}
	}
}

// A file store whose directory turned read-only refuses Put with an error
// Unwritable recognises.
func TestFilePutOnReadOnlyDirIsUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	dir := t.TempDir()
	s, err := Open("file://" + dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	err = s.Put(context.Background(), "raw/a/b/c/2026-10-01/x.ndjson.gz", strings.NewReader("x"))
	if err == nil || !Unwritable(err) {
		t.Fatalf("Put on a read-only root: %v, want an Unwritable error", err)
	}
}
