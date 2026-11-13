// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"context"
	"errors"
	"fmt"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

// KeyProvider wraps the tenant keys under a root key it guards: the local
// file or environment key, or a key held by a KMS. Plain is a key, small; aad
// binds the wrapped value to its use (the tenant) so it cannot be moved.
// Implementations never log plain or the wrapped value.
type KeyProvider interface {
	Name() string
	// Wrap returns the wrapped form of plain and the version of the root key
	// that wrapped it, to store beside it.
	Wrap(ctx context.Context, plain, aad []byte) (wrapped []byte, version string, err error)
	// Unwrap opens a value Wrap returned, under the root key version that
	// wrapped it. A wrong aad, a tampered value or an unknown version fails.
	Unwrap(ctx context.Context, wrapped, aad []byte, version string) ([]byte, error)
}

// ErrProviderUnavailable is wrapped by a provider error that a retry may
// cure: the provider cannot be reached or answers with a server error. A
// refusal (wrong aad, unknown key, bad credentials) does not wrap it.
var ErrProviderUnavailable = errors.New("vault: key provider unavailable")

// cloudOpeners holds the providers that live behind build tags, registered
// by their files. A provider not registered is not built into this binary.
var cloudOpeners = map[string]func(context.Context, config.Vault) (KeyProvider, error){}

// OpenProvider builds the key provider c names. The HashiCorp provider
// checks its key is derived before it returns.
func OpenProvider(ctx context.Context, c config.Vault) (KeyProvider, error) {
	switch c.Provider {
	case "", "local":
		return newLocalProvider(c.RootKey, c.RootKeyPrevious)
	case "hashicorp":
		return openHashiCorp(ctx, c)
	}
	if open, ok := cloudOpeners[c.Provider]; ok {
		return open(ctx, c)
	}
	return nil, fmt.Errorf("vault: key provider %q is not built into this binary: the published image and release binaries "+
		"carry it, a build from source needs `go build -tags azurekv,awskms`", c.Provider)
}
