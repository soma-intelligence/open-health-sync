package logging

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sharedFiles is the list of files copied between repos and hashed, in hash
// order. The sync script of the canonical repo keeps the same list.
var sharedFiles = []string{
	"VERSION",
	"logger.go",
	"context.go",
	"redact.go",
	"logger_test.go",
	"context_test.go",
	"redact_test.go",
	"copy_test.go",
	"httplog/middleware.go",
	"httplog/access.go",
	"httplog/holder.go",
	"httplog/requestid.go",
	"httplog/recover.go",
	"httplog/middleware_test.go",
	"httplog/holder_test.go",
	"httplog/requestid_test.go",
}

// checksum hashes the shared files of dir. For each file, in order, it feeds
// "<relative path>\n" then the content with every occurrence of importPath
// replaced by "@LOGGING@", then "\n".
func checksum(dir, importPath string) (string, error) {
	h := sha256.New()
	for _, rel := range sharedFiles {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		data = bytes.ReplaceAll(data, []byte(importPath), []byte("@LOGGING@"))
		h.Write([]byte(rel + "\n"))
		h.Write(data)
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readChecksumFile(t *testing.T) (importPath, sum string) {
	t.Helper()
	raw, err := os.ReadFile("CHECKSUM")
	if err != nil {
		t.Fatalf("read CHECKSUM: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("CHECKSUM wants 2 lines, got %d", len(lines))
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
}

// TestCopyChecksum fails when a shared file differs from the canonical copy.
// Change the canonical package, then run
// scripts/sync-logging.sh again; never edit a copy by hand.
func TestCopyChecksum(t *testing.T) {
	importPath, want := readChecksumFile(t)
	got, err := checksum(".", importPath)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if got != want {
		t.Fatalf("shared files differ from CHECKSUM (hand edit?): got %s want %s", got, want)
	}
}

func TestCopyChecksumDetectsEdit(t *testing.T) {
	importPath, _ := readChecksumFile(t)
	before, err := checksum(".", importPath)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	tmp := t.TempDir()
	for _, rel := range sharedFiles {
		data, err := os.ReadFile(filepath.FromSlash(rel))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(filepath.Join(tmp, "logger.go"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	after, err := checksum(tmp, importPath)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if after == before {
		t.Fatal("one-byte edit did not change the checksum")
	}
}
