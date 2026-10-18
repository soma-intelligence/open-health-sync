// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SecretSource says where a secret came from, for `ohs doctor`: "env" (the
// variable itself), "file" (read from the _FILE path) or "created" (the
// _FILE path did not exist, so a random value was written there). Empty when
// the secret is not set.
type SecretSource string

// The SecretSource values.
const (
	SourceEnv     SecretSource = "env"
	SourceFile    SecretSource = "file"
	SourceCreated SecretSource = "created"
)

// newKey returns 32 random bytes in base64, the OHS_ENCRYPTION_KEY format.
func newKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// newToken returns 32 random bytes in hex, like `openssl rand -hex 32`.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// secret resolves a secret given directly in name or in a file named by
// name+"_FILE". Setting both is an error. A missing file is created with a
// value from gen, mode 0600, for platforms that cannot generate a secret per
// install (app store templates). The file content is trimmed of surrounding
// whitespace. Errors never contain the secret.
func secret(get func(string, string) string, name string, gen func() (string, error)) (string, SecretSource, error) {
	value, path := get(name, ""), get(name+"_FILE", "")
	switch {
	case value != "" && path != "":
		return "", "", fmt.Errorf("set %s or %s_FILE, not both", name, name)
	case value != "":
		return value, SourceEnv, nil
	case path == "":
		return "", "", nil
	}
	v, err := readSecretFile(path)
	if err == nil {
		return v, SourceFile, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", "", fmt.Errorf("%s_FILE: %w", name, err)
	}
	v, err = gen()
	if err != nil {
		return "", "", fmt.Errorf("%s_FILE: generating a value: %w", name, err)
	}
	created, err := createSecretFile(path, v)
	if err != nil {
		return "", "", fmt.Errorf("%s_FILE: %w", name, err)
	}
	if !created {
		// Another process created it first: use its value.
		if v, err = readSecretFile(path); err != nil {
			return "", "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		return v, SourceFile, nil
	}
	return v, SourceCreated, nil
}

func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty: delete it to have a new value generated, or write the value into it", path)
	}
	return v, nil
}

// createSecretFile writes value to path without ever exposing a partly
// written file: it writes a temporary file next to it, then links it into
// place, which fails if path already exists. It reports false when another
// process created path first.
func createSecretFile(path, value string) (bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".ohs-secret-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	err = tmp.Chmod(0o600)
	if err == nil {
		_, err = tmp.WriteString(value + "\n")
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
