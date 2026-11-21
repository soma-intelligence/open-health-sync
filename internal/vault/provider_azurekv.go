// SPDX-License-Identifier: AGPL-3.0-only

//go:build azurekv

package vault

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

func init() { cloudOpeners["azurekv"] = openAzureKV }

// azureAADSize is the length of the SHA-256 prefix that stands in for the aad:
// Key Vault wrap does not take one, so the wrapped plaintext is
// SHA-256(aad) || key and unwrap checks the prefix.
const azureAADSize = sha256.Size

// azureVersionRE is what a key version looks like (32 hex digits in practice).
var azureVersionRE = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

// azureKVProvider wraps with an Azure Key Vault or Managed HSM key: RSA-OAEP-256
// on an RSA key, A256KW on an oct-HSM key (Managed HSM).
type azureKVProvider struct {
	client  *azkeys.Client
	name    string
	version string // pinned by the configured key id, usually empty (latest)
	alg     azkeys.EncryptionAlgorithm
}

// parseAzureKeyID splits https://{vault}/keys/{name}[/{version}].
func parseAzureKeyID(id string) (vaultURL, name, version string, err error) {
	u, perr := url.Parse(id)
	if perr != nil || u.Scheme != "https" || u.Host == "" {
		return "", "", "", errors.New("vault: OHS_VAULT_AZURE_KEY_ID must be an https key URI, https://{vault}.vault.azure.net/keys/{name}")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "keys" || parts[1] == "" {
		return "", "", "", errors.New("vault: OHS_VAULT_AZURE_KEY_ID must look like https://{vault}.vault.azure.net/keys/{name}")
	}
	if len(parts) == 3 {
		version = parts[2]
		if !azureVersionRE.MatchString(version) {
			return "", "", "", errors.New("vault: OHS_VAULT_AZURE_KEY_ID has an unreadable key version")
		}
	}
	return u.Scheme + "://" + u.Host, parts[1], version, nil
}

func openAzureKV(ctx context.Context, c config.Vault) (KeyProvider, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("vault: azurekv credential: %s", azureText(err))
	}
	p, err := newAzureKV(c.AzureKeyID, cred, nil)
	if err != nil {
		return nil, err
	}
	if err := p.checkKey(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func newAzureKV(keyID string, cred azcore.TokenCredential, opts *azkeys.ClientOptions) (*azureKVProvider, error) {
	vaultURL, name, version, err := parseAzureKeyID(keyID)
	if err != nil {
		return nil, err
	}
	client, err := azkeys.NewClient(vaultURL, cred, opts)
	if err != nil {
		return nil, fmt.Errorf("vault: azurekv client: %s", azureText(err))
	}
	return &azureKVProvider{client: client, name: name, version: version}, nil
}

func (p *azureKVProvider) Name() string { return "azurekv" }

// checkKey reads the key and picks the wrap algorithm from its type, so a key
// that cannot wrap is refused at start and not at the first user.
func (p *azureKVProvider) checkKey(ctx context.Context) error {
	resp, err := p.client.GetKey(ctx, p.name, p.version, nil)
	if err != nil {
		return azureError(ctx, "reading the key", err)
	}
	if resp.Key == nil || resp.Key.Kty == nil {
		return errors.New("vault: azurekv answered a key with no type")
	}
	switch *resp.Key.Kty {
	case azkeys.KeyTypeRSA, azkeys.KeyTypeRSAHSM:
		p.alg = azkeys.EncryptionAlgorithmRSAOAEP256
	case azkeys.KeyTypeOctHSM:
		p.alg = azkeys.EncryptionAlgorithmA256KW
	default:
		return fmt.Errorf("vault: azurekv key type %q cannot wrap: use an RSA key, or an oct-HSM key on Managed HSM", string(*resp.Key.Kty))
	}
	return nil
}

// azureText renders an error for a message: bounded, and never the request.
func azureText(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = strings.ToValidUTF8(s[:300], "")
	}
	return s
}

// azureError classifies a failed call. Throttling, 5xx and transport errors
// wrap ErrProviderUnavailable; a refusal (403, 404, 400) and a credential
// that cannot be had do not. The error text keeps the status and the Key
// Vault error code, not the body, which could echo the request.
func azureError(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var re *azcore.ResponseError
	if errors.As(err, &re) {
		code := re.ErrorCode
		if code == "" {
			code = "no error code"
		}
		switch {
		case re.StatusCode >= 500, re.StatusCode == http.StatusTooManyRequests,
			re.StatusCode == http.StatusRequestTimeout:
			return fmt.Errorf("%w: azurekv %s answered %d", ErrProviderUnavailable, what, re.StatusCode)
		}
		return fmt.Errorf("vault: azurekv refused %s (%d, %s)", what, re.StatusCode, code)
	}
	// azidentity marks the failures a retry cannot cure.
	var nr interface{ NonRetriable() }
	if errors.As(err, &nr) {
		return fmt.Errorf("vault: azurekv %s: %s", what, azureText(err))
	}
	return fmt.Errorf("%w: azurekv %s: %s", ErrProviderUnavailable, what, azureText(err))
}

var errAzureEmptyAAD = errors.New("vault: azurekv needs a non-empty aad")

func (p *azureKVProvider) Wrap(ctx context.Context, plain, aad []byte) ([]byte, string, error) {
	if len(aad) == 0 {
		return nil, "", errAzureEmptyAAD
	}
	if len(plain) == 0 {
		return nil, "", errors.New("vault: azurekv cannot wrap an empty key")
	}
	if p.alg == "" {
		return nil, "", errors.New("vault: azurekv key was not checked")
	}
	sum := sha256.Sum256(aad)
	value := append(sum[:azureAADSize:azureAADSize], plain...)
	defer clear(value)
	if p.alg == azkeys.EncryptionAlgorithmA256KW && len(value)%8 != 0 {
		return nil, "", errors.New("vault: azurekv AES key wrap needs a key whose size is a multiple of 8 bytes")
	}
	alg := p.alg
	resp, err := p.client.WrapKey(ctx, p.name, p.version, azkeys.KeyOperationParameters{
		Algorithm: &alg,
		Value:     value,
	}, nil)
	if err != nil {
		return nil, "", azureError(ctx, "wrap", err)
	}
	if len(resp.Result) == 0 || resp.KID == nil {
		return nil, "", errors.New("vault: azurekv answered an empty wrap")
	}
	version := resp.KID.Version()
	if resp.KID.Name() != p.name || !azureVersionRE.MatchString(version) {
		return nil, "", errors.New("vault: azurekv answered a wrap from an unexpected key")
	}
	return resp.Result, version, nil
}

func (p *azureKVProvider) Unwrap(ctx context.Context, wrapped, aad []byte, version string) ([]byte, error) {
	if len(aad) == 0 {
		return nil, errAzureEmptyAAD
	}
	if !azureVersionRE.MatchString(version) {
		return nil, errors.New("vault: azurekv needs the key version the value was wrapped with")
	}
	if len(wrapped) == 0 {
		return nil, errors.New("vault: azurekv cannot unwrap an empty value")
	}
	if p.alg == "" {
		return nil, errors.New("vault: azurekv key was not checked")
	}
	alg := p.alg
	resp, err := p.client.UnwrapKey(ctx, p.name, version, azkeys.KeyOperationParameters{
		Algorithm: &alg,
		Value:     wrapped,
	}, nil)
	if err != nil {
		return nil, azureError(ctx, "unwrap", err)
	}
	out := resp.Result
	defer clear(out)
	if len(out) <= azureAADSize {
		return nil, errors.New("vault: azurekv answered a missing or short plaintext")
	}
	sum := sha256.Sum256(aad)
	if subtle.ConstantTimeCompare(out[:azureAADSize], sum[:]) != 1 {
		return nil, errors.New("vault: azurekv wrapped key does not belong to this aad")
	}
	return append([]byte(nil), out[azureAADSize:]...), nil
}
