// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package pointer

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/objectstore"
)

const legacyPointer = `{"name":"backup-one","destination":"s3://test-bucket/backups/one"}`

const testNamespace = "test"

func TestLegacyDecodeAndV2RoundTrip(t *testing.T) {
	legacy, err := Decode([]byte(legacyPointer))
	if err != nil || legacy.SchemaVersion != 1 || legacy.PublishedAt != nil {
		t.Fatalf("legacy decode: %+v, %v", legacy, err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	document := Document{
		Name: "backup-one", Destination: "s3://test-bucket/backups/one", SchemaVersion: SchemaVersion,
		PublishedAt: &now, PublishedBy: &Publisher{Kind: "BackupPointer", Namespace: testNamespace, Name: "latest", UID: "test-uid"},
		SourceCluster: &SourceCluster{Name: "source", Namespace: testNamespace, CRVersion: "1.20.0"},
		Backup:        &Backup{StorageName: "daily", CompletedAt: &now, State: "Succeeded"},
		S3:            &S3{Bucket: "test-bucket", EndpointURL: "https://s3.example.com", Region: "auto"},
		Anonymized:    &Anonymized{Run: "run-one", Policy: "example", PolicyHash: "sha256:example"},
		PublicURL:     "https://public.example.com/pointers/latest",
	}
	encoded, err := Encode(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil || !reflect.DeepEqual(document, *decoded) {
		t.Fatalf("v2 round trip changed metadata: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 10 || string(fields["schemaVersion"]) != "2" {
		t.Fatalf("complete v2 field set missing: %d fields", len(fields))
	}
	encoded, err = Encode(*legacy)
	if err != nil || !strings.Contains(string(encoded), `"schemaVersion":2`) {
		t.Fatal("legacy document was not upgraded on publication")
	}
	withFutureField := strings.TrimSuffix(legacyPointer, "}") + `,"future":{"nested":true}}`
	if _, err := Decode([]byte(withFutureField)); err != nil {
		t.Fatalf("unknown field was rejected: %v", err)
	}
}

func TestRejectInvalidPointers(t *testing.T) {
	for _, data := range []string{
		`null`, `{}`, legacyPointer + `{}`, `{"name":"one","destination":"https://example.com/backup"}`,
		`{"name":"one","destination":"s3://test-bucket/"}`,
		`{"name":"one","destination":"s3://test-bucket/key","schemaVersion":0}`,
		`{"name":"one","destination":"s3://user:secret@test-bucket/key"}`,
		`{"name":"one","destination":"s3://test-bucket/key","publishedAt":"not-a-time"}`,
	} {
		if _, err := Decode([]byte(data)); err == nil {
			t.Fatal("invalid pointer accepted")
		}
	}
}

func TestHTTPFetchAndLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "sensitive-response-body")
		case "/slow":
			<-r.Context().Done()
		default:
			_, _ = io.WriteString(w, legacyPointer)
		}
	}))
	defer server.Close()
	fetcher := Fetcher{}
	source := api.PointerSource{HTTP: &api.HTTPPointerSource{URL: server.URL, AllowInsecure: true}}
	document, err := fetcher.Fetch(context.Background(), source)
	if err != nil || document.Name != "backup-one" || document.SchemaVersion != 1 {
		t.Fatalf("HTTP fetch: %v", err)
	}
	source.HTTP.AllowInsecure = false
	if _, err := fetcher.Fetch(context.Background(), source); err == nil {
		t.Fatal("HTTP accepted without allowInsecure")
	}
	source.HTTP.AllowInsecure = true
	source.HTTP.MaxBytes = int64(len(legacyPointer))
	if _, err := fetcher.Fetch(context.Background(), source); err != nil {
		t.Fatalf("exact byte limit rejected: %v", err)
	}
	source.HTTP.MaxBytes--
	if _, err := fetcher.Fetch(context.Background(), source); err == nil {
		t.Fatal("oversized pointer accepted")
	}
	source.HTTP.MaxBytes = 0
	source.HTTP.URL = server.URL + "/missing"
	if _, err := fetcher.Fetch(context.Background(), source); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("missing HTTP pointer lost category: %v", err)
	}
	source.HTTP.URL = server.URL + "/forbidden"
	if _, err := fetcher.Fetch(context.Background(), source); err == nil || errors.Is(err, objectstore.ErrNotFound) || strings.Contains(err.Error(), "sensitive-response-body") {
		t.Fatalf("HTTP error classification or response disclosure: %v", err)
	}
	source.HTTP.URL = server.URL + "/slow"
	source.HTTP.Timeout = &metav1.Duration{Duration: 20 * time.Millisecond}
	if _, err := fetcher.Fetch(context.Background(), source); err == nil {
		t.Fatal("HTTP timeout was ignored")
	}
	if _, err := fetcher.Fetch(context.Background(), api.PointerSource{}); err == nil {
		t.Fatal("missing pointer source accepted")
	}
	if _, err := fetcher.Fetch(context.Background(), api.PointerSource{HTTP: source.HTTP, S3: &api.S3PointerSource{}}); err == nil {
		t.Fatal("two pointer sources accepted")
	}
}

func TestHTTPSCustomCAAndRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://example.com/pointer", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, legacyPointer)
	}))
	defer server.Close()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: testNamespace},
		Data:       map[string][]byte{"bundle": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})},
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	fetcher := Fetcher{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build(), Namespace: testNamespace}
	source := api.PointerSource{HTTP: &api.HTTPPointerSource{URL: server.URL}}
	if _, err := fetcher.Fetch(context.Background(), source); err == nil {
		t.Fatal("untrusted server accepted without a CA")
	}
	source.HTTP.CASecretRef = &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "ca"}, Key: "bundle"}
	if _, err := fetcher.Fetch(context.Background(), source); err != nil {
		t.Fatalf("custom CA failed: %v", err)
	}
	source.HTTP.URL += "/redirect"
	if _, err := fetcher.Fetch(context.Background(), source); err == nil {
		t.Fatal("HTTPS redirect downgraded to HTTP")
	}
}

func TestS3FetchValidatesPointer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		if r.URL.Path == "/test-bucket/invalid" {
			_, _ = io.WriteString(w, `{"name":"one","destination":"file:///invalid"}`)
			return
		}
		_, _ = io.WriteString(w, legacyPointer)
	}))
	defer server.Close()
	source := api.PointerSource{S3: &api.S3PointerSource{
		ObjectStorage: api.ObjectStorageSpec{Bucket: "test-bucket", EndpointURL: server.URL}, Key: "latest",
	}}
	document, err := (Fetcher{}).Fetch(context.Background(), source)
	if err != nil || document.SchemaVersion != 1 {
		t.Fatalf("S3 pointer fetch: %v", err)
	}
	source.S3.Key = "invalid"
	if _, err := (Fetcher{}).Fetch(context.Background(), source); err == nil {
		t.Fatal("invalid S3 pointer bypassed codec validation")
	}
}
