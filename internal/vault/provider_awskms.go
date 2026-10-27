// SPDX-License-Identifier: AGPL-3.0-only

//go:build awskms

package vault

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

func init() { cloudOpeners["awskms"] = openAWSKMS }

// awsKMSProvider wraps with a symmetric AWS KMS key. The aad goes in as the
// encryption context, so a wrapped value only opens for the aad it was
// wrapped with. The version is the KeyId KMS answers, the ARN of the key
// that did the work (an alias resolves to it).
type awsKMSProvider struct {
	client *kms.Client
	keyARN string
}

func openAWSKMS(ctx context.Context, c config.Vault) (KeyProvider, error) {
	a, err := parseKMSARN(c.AWSKeyARN)
	if err != nil {
		return nil, err
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(a.Region))
	if err != nil {
		return nil, fmt.Errorf("vault: awskms configuration: %s", awsText(err.Error()))
	}
	return newAWSKMS(kms.NewFromConfig(cfg), c.AWSKeyARN), nil
}

func newAWSKMS(client *kms.Client, keyARN string) *awsKMSProvider {
	return &awsKMSProvider{client: client, keyARN: keyARN}
}

// parseKMSARN accepts a key or alias ARN of the kms service.
func parseKMSARN(s string) (arn.ARN, error) {
	a, err := arn.Parse(s)
	if err != nil || a.Service != "kms" || a.Region == "" ||
		!(strings.HasPrefix(a.Resource, "key/") || strings.HasPrefix(a.Resource, "alias/")) {
		return arn.ARN{}, errors.New("vault: OHS_VAULT_AWS_KEY_ARN must be a KMS key or alias ARN, arn:aws:kms:{region}:{account}:key/{id}")
	}
	return a, nil
}

func (p *awsKMSProvider) Name() string { return "awskms" }

func awsText(s string) string {
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "")
	}
	return s
}

// awsError classifies a failed call. Throttling, internal errors, 5xx and
// transport errors wrap ErrProviderUnavailable; any other refusal does not.
// The text keeps the error code and message KMS gives, which describe the
// failure and do not echo the request.
func awsError(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "ThrottlingException", "RequestLimitExceeded", "KMSInternalException",
			"DependencyTimeoutException", "ServiceUnavailableException", "InternalFailure":
			return fmt.Errorf("%w: awskms %s answered %s", ErrProviderUnavailable, what, api.ErrorCode())
		}
	}
	var re *smithyhttp.ResponseError
	if errors.As(err, &re) {
		sc := re.HTTPStatusCode()
		// Status 0: the transport failed before any answer.
		if sc == 0 || sc >= 500 || sc == http.StatusTooManyRequests || sc == http.StatusRequestTimeout {
			return fmt.Errorf("%w: awskms %s answered %d", ErrProviderUnavailable, what, sc)
		}
	}
	if api != nil {
		return fmt.Errorf("vault: awskms refused %s (%s): %s", what, api.ErrorCode(), awsText(api.ErrorMessage()))
	}
	if re != nil {
		return fmt.Errorf("vault: awskms refused %s (%d)", what, re.HTTPStatusCode())
	}
	// No answer at all: the network, or credentials that could not be had.
	return fmt.Errorf("%w: awskms %s: %s", ErrProviderUnavailable, what, awsText(err.Error()))
}

var errAWSEmptyAAD = errors.New("vault: awskms needs a non-empty aad (the encryption context)")

func awsContext(aad []byte) map[string]string {
	return map[string]string{"ohs": base64.StdEncoding.EncodeToString(aad)}
}

func (p *awsKMSProvider) Wrap(ctx context.Context, plain, aad []byte) ([]byte, string, error) {
	if len(aad) == 0 {
		return nil, "", errAWSEmptyAAD
	}
	if len(plain) == 0 {
		return nil, "", errors.New("vault: awskms cannot wrap an empty key")
	}
	out, err := p.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             &p.keyARN,
		Plaintext:         plain,
		EncryptionContext: awsContext(aad),
	})
	if err != nil {
		return nil, "", awsError(ctx, "encrypt", err)
	}
	if len(out.CiphertextBlob) == 0 || out.KeyId == nil || *out.KeyId == "" {
		return nil, "", errors.New("vault: awskms answered an empty encryption")
	}
	if _, err := parseKMSARN(*out.KeyId); err != nil {
		return nil, "", errors.New("vault: awskms answered an unexpected key id")
	}
	return out.CiphertextBlob, *out.KeyId, nil
}

func (p *awsKMSProvider) Unwrap(ctx context.Context, wrapped, aad []byte, version string) ([]byte, error) {
	if len(aad) == 0 {
		return nil, errAWSEmptyAAD
	}
	if _, err := parseKMSARN(version); err != nil {
		return nil, errors.New("vault: awskms needs the key ARN the value was wrapped with")
	}
	if len(wrapped) == 0 {
		return nil, errors.New("vault: awskms cannot unwrap an empty value")
	}
	out, err := p.client.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    wrapped,
		KeyId:             &version,
		EncryptionContext: awsContext(aad),
	})
	if err != nil {
		return nil, awsError(ctx, "decrypt", err)
	}
	if len(out.Plaintext) == 0 {
		return nil, errors.New("vault: awskms answered a missing or empty plaintext")
	}
	if out.KeyId == nil || *out.KeyId != version {
		clear(out.Plaintext)
		return nil, errors.New("vault: awskms opened the value with a different key than its version")
	}
	return out.Plaintext, nil
}
