// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

// hashiCorpTimeout bounds every call to Vault.
const hashiCorpTimeout = 10 * time.Second

// hashiCorpProvider wraps with a HashiCorp Vault transit key. The key must be
// derived: the aad goes in as the transit context, so a wrapped value only
// opens for the aad it was wrapped with.
type hashiCorpProvider struct {
	base      string // {addr}/v1/{mount}
	key       string
	tokenFile string
	envToken  func() string
	client    *http.Client
}

func newHashiCorp(c config.Vault, envToken func() string) (*hashiCorpProvider, error) {
	addr := strings.TrimRight(c.HashiCorpAddr, "/")
	u, err := url.Parse(addr)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("vault: OHS_VAULT_HASHICORP_ADDR must be an absolute http or https URL")
	}
	if c.HashiCorpKey == "" {
		return nil, errors.New("vault: OHS_VAULT_HASHICORP_KEY is required")
	}
	mount := strings.Trim(c.HashiCorpMount, "/")
	if mount == "" {
		mount = "transit"
	}
	return &hashiCorpProvider{
		base:      addr + "/v1/" + escapePath(mount),
		key:       url.PathEscape(c.HashiCorpKey),
		tokenFile: c.HashiCorpTokenFile,
		envToken:  envToken,
		client:    &http.Client{Timeout: hashiCorpTimeout},
	}, nil
}

// escapePath escapes each segment of a mount path, which may hold slashes.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func openHashiCorp(ctx context.Context, c config.Vault) (KeyProvider, error) {
	p, err := newHashiCorp(c, func() string { return os.Getenv("VAULT_TOKEN") })
	if err != nil {
		return nil, err
	}
	if err := p.checkDerived(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *hashiCorpProvider) Name() string { return "hashicorp" }

// token reads the token at each call, so a token file Vault Agent renews is
// picked up. The file wins over VAULT_TOKEN when set.
func (p *hashiCorpProvider) token() (string, error) {
	if p.tokenFile != "" {
		b, err := os.ReadFile(p.tokenFile)
		if err != nil {
			return "", fmt.Errorf("vault: reading OHS_VAULT_HASHICORP_TOKEN_FILE: %w", err)
		}
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
		return "", errors.New("vault: OHS_VAULT_HASHICORP_TOKEN_FILE is empty")
	}
	if t := strings.TrimSpace(p.envToken()); t != "" {
		return t, nil
	}
	return "", errors.New("vault: no HashiCorp token: set VAULT_TOKEN or OHS_VAULT_HASHICORP_TOKEN_FILE")
}

// do sends one request and decodes the data member of the answer. Transport
// errors and 5xx answers wrap ErrProviderUnavailable. Errors never carry the
// token, the request or the plaintext.
func (p *hashiCorpProvider) do(ctx context.Context, method, path string, body any) (map[string]any, error) {
	tok, err := p.token()
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The url.Error text holds the address, not the token.
		return nil, fmt.Errorf("%w: hashicorp: %w", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: hashicorp: reading the answer: %w", ErrProviderUnavailable, err)
	}
	var out struct {
		Data   map[string]any `json:"data"`
		Errors []string       `json:"errors"`
	}
	_ = json.Unmarshal(raw, &out)
	switch {
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: hashicorp answered %d", ErrProviderUnavailable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("vault: hashicorp refused %s (%d)%s", path, resp.StatusCode, vaultErrors(out.Errors))
	}
	return out.Data, nil
}

// vaultErrors renders the messages Vault puts in a refusal. They describe the
// failure ("permission denied", "message authentication failed"); they do not
// echo the request.
func vaultErrors(msgs []string) string {
	if len(msgs) == 0 {
		return ""
	}
	s := strings.Join(msgs, "; ")
	if len(s) > 200 {
		s = s[:200]
	}
	return ": " + s
}

// checkDerived refuses a transit key that is not derived: without a context
// the aad would not bind anything.
func (p *hashiCorpProvider) checkDerived(ctx context.Context) error {
	data, err := p.do(ctx, http.MethodGet, "/keys/"+p.key, nil)
	if err != nil {
		return err
	}
	if d, _ := data["derived"].(bool); !d {
		return errors.New("vault: the HashiCorp transit key is not derived: create it with derived=true (the aad is its context)")
	}
	return nil
}

func (p *hashiCorpProvider) Wrap(ctx context.Context, plain, aad []byte) ([]byte, string, error) {
	data, err := p.do(ctx, http.MethodPost, "/encrypt/"+p.key, map[string]string{
		"plaintext": base64.StdEncoding.EncodeToString(plain),
		"context":   base64.StdEncoding.EncodeToString(aad),
	})
	if err != nil {
		return nil, "", err
	}
	ct, _ := data["ciphertext"].(string)
	// vault:v1:<base64>: the version is the part before the payload.
	parts := strings.SplitN(ct, ":", 3)
	if len(parts) != 3 || parts[0] != "vault" || !strings.HasPrefix(parts[1], "v") {
		return nil, "", errors.New("vault: hashicorp answered an unexpected ciphertext")
	}
	return []byte(ct), parts[0] + ":" + parts[1], nil
}

func (p *hashiCorpProvider) Unwrap(ctx context.Context, wrapped, aad []byte, version string) ([]byte, error) {
	if version != "" && !strings.HasPrefix(string(wrapped), version+":") {
		return nil, fmt.Errorf("vault: wrapped key is not of version %q", version)
	}
	data, err := p.do(ctx, http.MethodPost, "/decrypt/"+p.key, map[string]string{
		"ciphertext": string(wrapped),
		"context":    base64.StdEncoding.EncodeToString(aad),
	})
	if err != nil {
		return nil, err
	}
	s, _ := data["plaintext"].(string)
	plain, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("vault: hashicorp answered an unreadable plaintext")
	}
	return plain, nil
}
