// SPDX-License-Identifier: AGPL-3.0-only

package rawstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// s3Store works with AWS S3 and any S3-compatible server. Credentials come
// from the AWS default chain (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, ...).
// maxPart is the largest part a Put accepts (the upload limit).
const maxPart = 64 << 20

type s3Store struct {
	client *s3.Client
	bucket string
	// sse and sseKMSKey ask the server to encrypt every object written
	// (the sse and sse_kms_key_id URL parameters). Empty: no header is sent
	// and the bucket's own default applies.
	sse       types.ServerSideEncryption
	sseKMSKey string
}

// s3Encryption reads the server-side encryption asked for in the store URL:
// sse=AES256 (keys managed by S3) or sse=aws:kms, with sse_kms_key_id naming
// the KMS key (without it the account's default KMS key is used). No sse
// parameter sends no header, which every S3-compatible server accepts.
func s3Encryption(q url.Values) (types.ServerSideEncryption, string, error) {
	keyID := q.Get("sse_kms_key_id")
	switch v := q.Get("sse"); v {
	case "":
		if keyID != "" {
			return "", "", fmt.Errorf("rawstore: sse_kms_key_id needs sse=aws:kms")
		}
		return "", "", nil
	case string(types.ServerSideEncryptionAes256):
		if keyID != "" {
			return "", "", fmt.Errorf("rawstore: sse_kms_key_id needs sse=aws:kms, not sse=AES256")
		}
		return types.ServerSideEncryptionAes256, "", nil
	case string(types.ServerSideEncryptionAwsKms):
		return types.ServerSideEncryptionAwsKms, keyID, nil
	default:
		return "", "", fmt.Errorf("rawstore: bad sse %q (want AES256 or aws:kms)", v)
	}
}

func openS3(u *url.URL) (Store, error) {
	if u.Host == "" {
		return nil, fmt.Errorf("rawstore: s3 url needs a bucket (s3://bucket)")
	}
	q := u.Query()
	pathStyle := false
	if v := q.Get("path_style"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("rawstore: bad path_style %q", v)
		}
		pathStyle = b
	}
	sse, sseKMSKey, err := s3Encryption(q)
	if err != nil {
		return nil, err
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if r := q.Get("region"); r != "" {
		opts = append(opts, awsconfig.WithRegion(r))
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("rawstore: aws config: %w", err)
	}
	endpoint := q.Get("endpoint")
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = pathStyle
	})
	return &s3Store{client: client, bucket: u.Host, sse: sse, sseKMSKey: sseKMSKey}, nil
}

// Put reads the whole part into memory (parts are capped at 64 MiB upstream),
// which lets the SDK sign the payload on plain HTTP endpoints. The write is
// create-if-absent: If-None-Match: * makes the server refuse an existing key.
func (s *s3Store) Put(ctx context.Context, key string, r io.Reader) error {
	if err := checkKey(key); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(r, maxPart+1))
	if err != nil {
		return err
	}
	if len(data) > maxPart {
		return fmt.Errorf("rawstore: part exceeds %d bytes", maxPart)
	}
	in := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/gzip"),
		IfNoneMatch: aws.String("*"),
	}
	if s.sse != "" {
		in.ServerSideEncryption = s.sse
		if s.sseKMSKey != "" {
			in.SSEKMSKeyId = aws.String(s.sseKMSKey)
		}
	}
	_, err = s.client.PutObject(ctx, in)
	if err != nil {
		if isExistsErr(err) {
			return ErrExists
		}
		return err
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return out.Body, nil
}

func (s *s3Store) List(ctx context.Context, prefix string) ([]string, error) {
	if err := checkPrefix(prefix); err != nil {
		return nil, err
	}
	var keys []string
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			if o.Key != nil {
				keys = append(keys, *o.Key)
			}
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *s3Store) Walk(ctx context.Context, prefix string, fn func(keys []string) error) error {
	if err := checkPrefix(prefix); err != nil {
		return err
	}
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(WalkPageSize),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(page.Contents))
		for _, o := range page.Contents {
			if o.Key != nil {
				keys = append(keys, *o.Key)
			}
		}
		if len(keys) == 0 {
			continue
		}
		sort.Strings(keys)
		if err := fn(keys); err != nil {
			return err
		}
	}
	return nil
}

func (s *s3Store) DeletePrefix(ctx context.Context, prefix string) error {
	if err := checkDeletePrefix(prefix); err != nil {
		return err
	}
	keys, err := s.List(ctx, prefix)
	if err != nil {
		return err
	}
	const batch = 1000
	for len(keys) > 0 {
		n := min(batch, len(keys))
		ids := make([]types.ObjectIdentifier, n)
		for i, k := range keys[:n] {
			ids[i] = types.ObjectIdentifier{Key: aws.String(k)}
		}
		out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return err
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			return fmt.Errorf("rawstore: delete %s: %s", aws.ToString(e.Key), aws.ToString(e.Message))
		}
		keys = keys[n:]
	}
	return nil
}

// isExistsErr: the key is already there (412) or another put of it is in
// flight (409 ConditionalRequestConflict, what AWS answers to overlapping
// If-None-Match puts).
func isExistsErr(err error) bool {
	c := errCode(err)
	return c == "PreconditionFailed" || c == "ConditionalRequestConflict" ||
		isStatus(err, http.StatusPreconditionFailed) || isStatus(err, http.StatusConflict)
}

// isNoSuchKey matches only NoSuchKey; NoSuchBucket and other 404s stay errors.
func isNoSuchKey(err error) bool {
	var nsk *types.NoSuchKey
	return errors.As(err, &nsk) || errCode(err) == "NoSuchKey"
}

func errCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

func isStatus(err error, status int) bool {
	var re *smithyhttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == status
}
