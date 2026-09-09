// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

// Package objectstore resolves namespace-local credentials for JSON objects in S3.
package objectstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

// ErrNotFound distinguishes a missing object from authentication or transport errors.
var ErrNotFound = errors.New("object not found")

var (
	ErrCredentialsInvalid  = errors.New("object storage credentials are invalid")
	ErrEndpointUnreachable = errors.New("object storage endpoint is unavailable")
)

// MaxJSONBytes bounds pointer downloads before decoding untrusted JSON.
const MaxJSONBytes int64 = 65536

type Metadata struct {
	ETag         string
	Found        bool
	ContentType  string
	CacheControl string
	Size         int64
}

type Client struct {
	s3                *minio.Client
	bucket            string
	prefix            string
	endpointURL       string
	region            string
	publicEndpointURL string
}

// Resolve uses the supplied uncached reader; Secret.Data is already decoded by Kubernetes.
func Resolve(ctx context.Context, reader client.Reader, namespace string, spec api.ObjectStorageSpec) (*Client, error) {
	if spec.Bucket == "" {
		return nil, errors.New("bucket is required")
	}
	if strings.HasPrefix(spec.Prefix, "/") {
		return nil, errors.New("prefix must not start with /")
	}
	keys := api.ObjectStorageSecretKeys{
		AccessKeyID: "AWS_ACCESS_KEY_ID", SecretAccessKey: "AWS_SECRET_ACCESS_KEY",
		EndpointURL: "S3_ENDPOINT_URL", PublicEndpointURL: "S3_PUBLIC_ENDPOINT_URL",
	}
	if spec.Keys != nil {
		if spec.Keys.AccessKeyID != "" {
			keys.AccessKeyID = spec.Keys.AccessKeyID
		}
		if spec.Keys.SecretAccessKey != "" {
			keys.SecretAccessKey = spec.Keys.SecretAccessKey
		}
		if spec.Keys.EndpointURL != "" {
			keys.EndpointURL = spec.Keys.EndpointURL
		}
		if spec.Keys.PublicEndpointURL != "" {
			keys.PublicEndpointURL = spec.Keys.PublicEndpointURL
		}
	}
	endpoint := spec.EndpointURL
	var accessKey, secretKey, publicEndpoint string
	if spec.CredentialsSecretRef != nil {
		if reader == nil || namespace == "" || spec.CredentialsSecretRef.Name == "" {
			return nil, errors.New("credentials require a reader, namespace and Secret name")
		}
		var secret corev1.Secret
		if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: spec.CredentialsSecretRef.Name}, &secret); err != nil {
			return nil, fmt.Errorf("read credentials Secret: %w", err)
		}
		accessKey = string(secret.Data[keys.AccessKeyID])
		secretKey = string(secret.Data[keys.SecretAccessKey])
		if accessKey == "" || secretKey == "" {
			return nil, fmt.Errorf("%w: Secret must contain nonempty access and secret keys", ErrCredentialsInvalid)
		}
		if endpoint == "" {
			endpoint = string(secret.Data[keys.EndpointURL])
		}
		publicEndpoint = string(secret.Data[keys.PublicEndpointURL])
	}
	u, err := ParseEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEndpointUnreachable, err)
	}
	if publicEndpoint != "" {
		if _, err := ParseURL(publicEndpoint, true); err != nil {
			return nil, errors.New("invalid public endpoint URL")
		}
	}
	region := spec.Region
	if region == "" {
		region = "auto"
	}
	lookup := minio.BucketLookupAuto
	if spec.PathStyle != nil {
		if *spec.PathStyle {
			lookup = minio.BucketLookupPath
		} else {
			lookup = minio.BucketLookupDNS
		}
	}
	var ca *corev1.SecretKeySelector
	var insecure bool
	if spec.TLS != nil {
		ca = spec.TLS.CASecretRef
		insecure = spec.TLS.InsecureSkipVerify
	}
	transport, err := Transport(ctx, reader, namespace, ca, insecure)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEndpointUnreachable, err)
	}
	s3, err := minio.New(u.Host, &minio.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: u.Scheme == "https",
		Region: region, BucketLookup: lookup, Transport: transport,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: create client: %w", ErrEndpointUnreachable, err)
	}
	return &Client{s3: s3, bucket: spec.Bucket, prefix: spec.Prefix, endpointURL: u.String(), region: region, publicEndpointURL: publicEndpoint}, nil
}

func (c *Client) EndpointURL() string       { return c.endpointURL }
func (c *Client) Region() string            { return c.region }
func (c *Client) PublicEndpointURL() string { return c.publicEndpointURL }

func (c *Client) objectKey(key string) (string, error) {
	if key == "" {
		return "", errors.New("object key is required")
	}
	if c.prefix == "" {
		return key, nil
	}
	// S3 keys are opaque: path.Join would silently change dot segments or repeated slashes.
	return strings.TrimSuffix(c.prefix, "/") + "/" + key, nil
}

func (c *Client) PutJSON(ctx context.Context, key string, value any) (string, error) {
	key, err := c.objectKey(key)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode object JSON: %w", err)
	}
	if int64(len(data)) > MaxJSONBytes {
		return "", errors.New("object JSON exceeds 65536 bytes")
	}
	options := minio.PutObjectOptions{
		ContentType: "application/json", CacheControl: "no-cache",
	}
	if pointer, ok := value.(interface{ BackupName() string }); ok {
		options.UserMetadata = map[string]string{"pxc-anonymizer-backup": pointer.BackupName()}
	}
	info, err := c.s3.PutObject(ctx, c.bucket, key, bytes.NewReader(data), int64(len(data)), options)
	if err != nil {
		return "", operationError("put", err)
	}
	return info.ETag, nil
}

func (c *Client) GetJSON(ctx context.Context, key string, destination any) error {
	key, err := c.objectKey(key)
	if err != nil {
		return err
	}
	object, err := c.s3.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return operationError("get", err)
	}
	defer func() { _ = object.Close() }()
	data, err := io.ReadAll(io.LimitReader(object, MaxJSONBytes+1))
	if err != nil {
		return operationError("get", err)
	}
	if int64(len(data)) > MaxJSONBytes {
		return errors.New("object JSON exceeds 65536 bytes")
	}
	if err := json.Unmarshal(data, destination); err != nil {
		return fmt.Errorf("decode object JSON: %w", err)
	}
	return nil
}

func (c *Client) Head(ctx context.Context, key string) (Metadata, error) {
	key, err := c.objectKey(key)
	if err != nil {
		return Metadata{}, err
	}
	info, err := c.s3.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		err = operationError("head", err)
		if errors.Is(err, ErrNotFound) {
			return Metadata{}, nil
		}
		return Metadata{}, err
	}
	return Metadata{ETag: info.ETag, Found: true, ContentType: info.ContentType, CacheControl: info.Metadata.Get("Cache-Control"), Size: info.Size}, nil
}

func operationError(operation string, err error) error {
	if response, ok := errors.AsType[minio.ErrorResponse](err); ok {
		switch response.Code {
		case "NoSuchKey", "NoSuchObject", "NotFound":
			return ErrNotFound
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "InvalidToken", "ExpiredToken":
			return fmt.Errorf("%w: %s object: %w", ErrCredentialsInvalid, operation, err)
		}
	}
	var networkError net.Error
	var requestError *url.Error
	if errors.As(err, &networkError) || errors.As(err, &requestError) {
		return fmt.Errorf("%w: %s object: %w", ErrEndpointUnreachable, operation, err)
	}
	return fmt.Errorf("%s object: %w", operation, err)
}

// ParseEndpoint keeps URL parsing separate from the SDK's host-only endpoint argument.
func ParseEndpoint(endpoint string) (*url.URL, error) {
	u, err := ParseURL(endpoint, true)
	if err != nil {
		return nil, errors.New("endpoint must be an absolute http or https URL without userinfo, query or fragment")
	}
	if u.Path != "" || u.RawPath != "" {
		return nil, errors.New("endpoint must not carry a path")
	}
	return u, nil
}

// ParseURL rejects credential-bearing and ambiguous URLs without echoing their contents.
func ParseURL(raw string, allowInsecure bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return nil, errors.New("invalid URL")
	}
	if u.Scheme != "https" && (!allowInsecure || u.Scheme != "http") {
		return nil, errors.New("URL must use https, or explicitly allow http")
	}
	return u, nil
}
