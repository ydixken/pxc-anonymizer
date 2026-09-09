// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package objectstore_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/objectstore"
	"github.com/ydixken/pxc-anonymizer/internal/pointer"
)

func TestMinIOPointerRoundTrip(t *testing.T) {
	endpoint := os.Getenv("PXC_ANONYMIZER_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("PXC_ANONYMIZER_TEST_S3_ENDPOINT is unset")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store, cleanupClient, bucket := minioTestClients(t, ctx, endpoint)
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("generate unique object key")
	}
	key := "service-test/objectstore/" + hex.EncodeToString(random[:]) + ".json"
	// Register before PUT because a failed response can still leave an uploaded object.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := cleanupClient.RemoveObject(cleanupCtx, bucket, key, minio.RemoveObjectOptions{}); err != nil {
			t.Error("remove exact service-test object failed")
			return
		}
		metadata, err := store.Head(cleanupCtx, key)
		if err != nil || metadata.Found {
			t.Error("confirm service-test object removal failed")
			return
		}
		t.Log("exact service-test object removed; HEAD reports Found=false")
	})

	publishedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	want := pointer.Document{
		Name: "service-backup", Destination: "s3://example-backups/daily/service-backup",
		SchemaVersion: pointer.SchemaVersion, PublishedAt: &publishedAt,
		PublishedBy: &pointer.Publisher{Kind: "BackupPointer", Namespace: "example", Name: "latest", UID: "service-test"},
		Backup:      &pointer.Backup{StorageName: "daily", CompletedAt: &publishedAt, State: "Succeeded"},
	}
	etag, err := store.PutJSON(ctx, key, want)
	if err != nil {
		t.Fatal("PUT against configured MinIO failed")
	}
	if etag == "" {
		t.Fatal("PUT returned an empty ETag")
	}
	metadata, err := store.Head(ctx, key)
	if err != nil {
		t.Fatal("HEAD against configured MinIO failed")
	}
	if !metadata.Found || metadata.ETag != etag || metadata.ContentType != "application/json" || metadata.CacheControl != "no-cache" || metadata.Size <= 0 {
		t.Fatal("stored object lacks the expected ETag, JSON content type, cache control or size")
	}
	var got pointer.Document
	if err := store.GetJSON(ctx, key, &got); err != nil {
		t.Fatal("GET against configured MinIO failed")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("stored schema-v2 pointer did not preserve its identity and publication metadata")
	}
	t.Log("MinIO PUT/HEAD/GET preserved schema-v2 pointer; application/json, no-cache and ETag verified")
}

func minioTestClients(t *testing.T, ctx context.Context, endpoint string) (*objectstore.Client, *minio.Client, string) {
	t.Helper()
	bucket := minioTestEnv(t, "PXC_ANONYMIZER_TEST_S3_BUCKET")
	accessKey := minioTestEnv(t, "PXC_ANONYMIZER_TEST_S3_ACCESS_KEY_ID")
	secretKey := minioTestEnv(t, "PXC_ANONYMIZER_TEST_S3_SECRET_ACCESS_KEY")
	region := os.Getenv("PXC_ANONYMIZER_TEST_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "service-storage", Namespace: "example"},
		Data:       map[string][]byte{"AWS_ACCESS_KEY_ID": []byte(accessKey), "AWS_SECRET_ACCESS_KEY": []byte(secretKey)},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(secret).Build()
	pathStyle := true
	store, err := objectstore.Resolve(ctx, reader, secret.Namespace, api.ObjectStorageSpec{
		Bucket: bucket, EndpointURL: endpoint, Region: region, PathStyle: &pathStyle,
		CredentialsSecretRef: &corev1.LocalObjectReference{Name: secret.Name},
	})
	if err != nil {
		t.Fatal("resolve configured MinIO client failed")
	}
	u, err := objectstore.ParseEndpoint(endpoint)
	if err != nil {
		t.Fatal("parse configured MinIO endpoint failed")
	}
	cleanupClient, err := minio.New(u.Host, &minio.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: u.Scheme == "https",
		Region: region, BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatal("configure MinIO cleanup client failed")
	}
	return store, cleanupClient, bucket
}

func minioTestEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required when the MinIO endpoint is configured", name)
	}
	return value
}
