// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package objectstore

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

const (
	testBucket     = "test-bucket"
	testBackupName = "backup-one"
)

func secretReader(t *testing.T, objects ...client.Object) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func TestParseEndpoint(t *testing.T) {
	for _, raw := range []string{
		"https://host.example.com", "https://test.example.com", "https://pointer.example.com",
		"https://s3.example.com", "http://localhost:9000", "https://[::1]:9443",
	} {
		t.Run(raw, func(t *testing.T) {
			u, err := ParseEndpoint(raw)
			if err != nil || u.String() != raw {
				t.Fatalf("endpoint changed or failed: %v", err)
			}
		})
	}
	for _, raw := range []string{
		"", "s3.example.com", "https:///missing-host", "https://s3.example.com/",
		"https://s3.example.com/bucket", "https://s3.example.com/%2f", "ftp://s3.example.com",
		"https://user:password@s3.example.com", "https://s3.example.com?credential=hidden",
		"https://s3.example.com?", "https://s3.example.com#fragment", "https://s3.example.com:invalid",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseEndpoint(raw); err == nil {
				t.Fatal("invalid endpoint accepted")
			}
		})
	}
}

func TestResolveCredentials(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "storage", Namespace: "test"},
		Data: map[string][]byte{
			"id": []byte("YWNjZXNz"), "secret": []byte("c2VjcmV0"),
			"endpoint": []byte("https://s3.example.com"), "public": []byte("https://public.example.com/pointers"),
		},
	}
	spec := api.ObjectStorageSpec{
		Bucket: testBucket, CredentialsSecretRef: &corev1.LocalObjectReference{Name: "storage"},
		Keys: &api.ObjectStorageSecretKeys{AccessKeyID: "id", SecretAccessKey: "secret", EndpointURL: "endpoint", PublicEndpointURL: "public"},
	}
	store, err := Resolve(context.Background(), secretReader(t, secret), "test", spec)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := store.s3.GetCreds()
	if err != nil || creds.AccessKeyID != string(secret.Data["id"]) || creds.SecretAccessKey != string(secret.Data["secret"]) {
		t.Fatal("Secret.Data was not used verbatim")
	}
	if store.EndpointURL() != "https://s3.example.com" || store.Region() != "auto" || store.PublicEndpointURL() != "https://public.example.com/pointers" {
		t.Fatal("resolved storage metadata differs")
	}
	spec.EndpointURL = "http://override.example.com:9000"
	spec.Region = "test-region"
	store, err = Resolve(context.Background(), secretReader(t, secret), "test", spec)
	if err != nil || store.EndpointURL() != spec.EndpointURL || store.Region() != spec.Region {
		t.Fatalf("explicit endpoint/region did not override Secret/default: %v", err)
	}
	if _, err := Resolve(context.Background(), secretReader(t, secret), "other", spec); !apierrors.IsNotFound(err) {
		t.Fatalf("cross-namespace Secret unexpectedly resolved: %v", err)
	}
	delete(secret.Data, "secret")
	if _, err := Resolve(context.Background(), secretReader(t, secret), "test", spec); !errors.Is(err, ErrCredentialsInvalid) {
		t.Fatalf("missing key did not classify as invalid credentials: %v", err)
	}
	spec.CredentialsSecretRef = nil
	spec.EndpointURL = "https://s3.example.com/path"
	if _, err := Resolve(context.Background(), nil, "test", spec); !errors.Is(err, ErrEndpointUnreachable) {
		t.Fatalf("invalid endpoint lost its category: %v", err)
	}
}

func TestTransportCertificateVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: "test"},
		Data:       map[string][]byte{"bundle": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})},
	}
	selector := &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "ca"}, Key: "bundle"}
	for _, test := range []struct {
		name     string
		ca       *corev1.SecretKeySelector
		insecure bool
		wantOK   bool
	}{
		{name: "verified by default"},
		{name: "custom CA", ca: selector, wantOK: true},
		{name: "explicit bypass", insecure: true, wantOK: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport, err := Transport(context.Background(), secretReader(t, secret), "test", test.ca, test.insecure)
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			response, err := (&http.Client{Transport: transport, Timeout: time.Second}).Get(server.URL)
			if response != nil {
				_ = response.Body.Close()
			}
			if (err == nil) != test.wantOK {
				t.Fatalf("certificate verification outcome: %v", err)
			}
		})
	}
	if _, err := Transport(context.Background(), secretReader(t, secret), "other", selector, false); !apierrors.IsNotFound(err) {
		t.Fatalf("CA read escaped its namespace: %v", err)
	}
	secret.Data["bundle"] = []byte("not a certificate")
	if _, err := Transport(context.Background(), secretReader(t, secret), "test", selector, false); err == nil {
		t.Fatal("invalid custom CA accepted")
	}
}

type testPointer struct {
	Name string `json:"name"`
}

func (p testPointer) BackupName() string { return p.Name }

func TestJSONHeadersAndMetadata(t *testing.T) {
	requests := make(chan http.Header, 1)
	const body = `{"name":"backup-one"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test-bucket/prefix/pointer.json" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Header().Set("ETag", `"version-one"`)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		switch r.Method {
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			requests <- r.Header.Clone()
		case http.MethodGet:
			_, _ = io.WriteString(w, body)
		case http.MethodHead:
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		default:
			t.Errorf("unexpected request method %q", r.Method)
		}
	}))
	defer server.Close()
	pathStyle := true
	store, err := Resolve(context.Background(), nil, "test", api.ObjectStorageSpec{
		Bucket: testBucket, Prefix: "prefix/", EndpointURL: server.URL, PathStyle: &pathStyle,
	})
	if err != nil {
		t.Fatal(err)
	}
	etag, err := store.PutJSON(context.Background(), "pointer.json", testPointer{Name: testBackupName})
	if err != nil || etag != "version-one" {
		t.Fatalf("PUT did not return the server ETag: %v", err)
	}
	headers := <-requests
	if headers.Get("Content-Type") != "application/json" || headers.Get("Cache-Control") != "no-cache" || headers.Get("X-Amz-Meta-Pxc-Anonymizer-Backup") != testBackupName {
		t.Fatal("publication headers are missing")
	}
	var decoded testPointer
	if err := store.GetJSON(context.Background(), "pointer.json", &decoded); err != nil || decoded.Name != testBackupName {
		t.Fatalf("GET did not decode the pointer: %v", err)
	}
	metadata, err := store.Head(context.Background(), "pointer.json")
	if err != nil || !metadata.Found || metadata.ETag != etag || metadata.ContentType != "application/json" || metadata.CacheControl != "no-cache" || metadata.Size != int64(len(body)) {
		t.Fatalf("HEAD metadata mismatch: %+v, %v", metadata, err)
	}
}

func TestMissingAndFailedRequests(t *testing.T) {
	for _, test := range []struct {
		name string
		code string
		want error
	}{
		{name: "missing", code: "NoSuchKey", want: ErrNotFound},
		{name: "forbidden", code: "AccessDenied", want: ErrCredentialsInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status := http.StatusNotFound
				if test.code == "AccessDenied" {
					status = http.StatusForbidden
				}
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(status)
				if r.Method != http.MethodHead {
					_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>request failed</Message></Error>", test.code)
				}
			}))
			defer server.Close()
			store, err := Resolve(context.Background(), nil, "test", api.ObjectStorageSpec{Bucket: testBucket, EndpointURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := store.GetJSON(context.Background(), "pointer.json", &value); !errors.Is(err, test.want) {
				t.Fatalf("GET error category: %v", err)
			}
			metadata, err := store.Head(context.Background(), "pointer.json")
			if test.want == ErrNotFound {
				if err != nil || metadata.Found {
					t.Fatalf("missing HEAD did not return Found=false: %v", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("HEAD authentication failure was treated as missing: %v", err)
			}
		})
	}
	if err := operationError("put", minio.ErrorResponse{Code: "InternalError"}); errors.Is(err, ErrNotFound) || errors.Is(err, ErrCredentialsInvalid) || errors.Is(err, ErrEndpointUnreachable) {
		t.Fatal("upload failure incorrectly classified")
	}
}

func TestJSONReadLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `"`+strings.Repeat("x", int(MaxJSONBytes))+`"`)
	}))
	defer server.Close()
	store, err := Resolve(context.Background(), nil, "test", api.ObjectStorageSpec{Bucket: testBucket, EndpointURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := store.GetJSON(context.Background(), "pointer.json", &value); err == nil {
		t.Fatal("oversized JSON accepted")
	}
	if _, err := store.PutJSON(context.Background(), "pointer.json", strings.Repeat("x", int(MaxJSONBytes))); err == nil {
		t.Fatal("oversized JSON uploaded")
	}
}

func TestBucketLookupRequests(t *testing.T) {
	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Host + r.URL.Path
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	base := http.DefaultTransport
	// Redirect only this test's synthetic DNS endpoints to its local HTTP fixture.
	http.DefaultTransport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	defer func() { http.DefaultTransport = base }()
	pathStyle, dnsStyle := true, false
	for _, test := range []struct {
		name     string
		endpoint string
		style    *bool
		want     string
	}{
		{name: "automatic R2", endpoint: "http://account.r2.cloudflarestorage.com", want: "account.r2.cloudflarestorage.com/test-bucket/latest"},
		{name: "explicit path", endpoint: "http://store.example.com", style: &pathStyle, want: "store.example.com/test-bucket/latest"},
		{name: "explicit DNS", endpoint: "http://store.example.com", style: &dnsStyle, want: "test-bucket.store.example.com/latest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := Resolve(context.Background(), nil, "test", api.ObjectStorageSpec{Bucket: testBucket, EndpointURL: test.endpoint, PathStyle: test.style})
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := store.GetJSON(context.Background(), "latest", &value); err != nil {
				t.Fatal(err)
			}
			if got := <-requests; got != test.want {
				t.Fatalf("addressing style: got %q, want %q", got, test.want)
			}
		})
	}
}

func TestEndpointFailureCategory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	store, err := Resolve(context.Background(), nil, "test", api.ObjectStorageSpec{Bucket: testBucket, EndpointURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := store.Head(ctx, "latest"); !errors.Is(err, ErrEndpointUnreachable) {
		t.Fatalf("connection failure lost endpoint category: %v", err)
	}
}
