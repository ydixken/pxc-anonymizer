//nolint:goconst // Literal wire fields keep decoding tests independent of the view structs.
package pxc

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func backupFixture(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile("testdata/backup-succeeded.json")
	if err != nil {
		t.Fatal(err)
	}
	object := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, object); err != nil {
		t.Fatal(err)
	}
	return object
}

func TestDecodeBackup(t *testing.T) {
	view, err := DecodeBackup(backupFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if view.Name != "example-backup" || view.Namespace != "example" || view.Spec.PXCCluster != "example-source" ||
		view.Spec.StorageName != "daily" || view.Status.StorageName != "daily" ||
		view.Status.Destination != "s3://example-backups/daily/example-backup" || !view.Succeeded() {
		t.Fatalf("unexpected backup view: %+v", view)
	}
	if view.Status.CompletedAt == nil || !view.Status.CompletedAt.Time.Equal(time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("completion was not decoded as a timestamp: %v", view.Status.CompletedAt)
	}
	if view.Status.S3 == nil || view.Status.S3.Bucket != "example-backups/daily" ||
		view.Status.S3.EndpointURL != "https://backup-source.example.com" || view.Status.S3.Region != "example-region" {
		t.Fatalf("source S3 metadata was not retained: %+v", view.Status.S3)
	}
	encoded, err := json.Marshal(view.Status.S3)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("credentials")) {
		t.Fatal("S3 view retained credential metadata")
	}
}

func TestDecodeBackupRejectsMalformedObjects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  []string
		value any
	}{
		{name: "wrong kind", path: []string{"kind"}, value: "Secret"},
		{name: "wrong version", path: []string{"apiVersion"}, value: "example.com/v1"},
		{name: "invalid timestamp", path: []string{"status", "completed"}, value: "not-a-time"},
		{name: "invalid cluster type", path: []string{"spec", "pxcCluster"}, value: int64(3)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := backupFixture(t)
			if err := unstructured.SetNestedField(object.Object, tc.value, tc.path...); err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeBackup(object); err == nil {
				t.Fatal("malformed backup was accepted")
			}
		})
	}
	if _, err := DecodeBackup(nil); err == nil {
		t.Fatal("nil backup was accepted")
	}
}

func TestDecodeRestoreAndCluster(t *testing.T) {
	restore, err := DecodeRestore(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "pxc.percona.com/v1", "kind": "PerconaXtraDBClusterRestore",
		"spec": map[string]any{"pxcCluster": "example-target", "backupSource": map[string]any{
			"destination": "s3://example-backups/example-backup",
		}},
		"status": map[string]any{"state": "Failed", "comments": "example restore failure"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if restore.Status.Comments != "example restore failure" || !restore.IsTerminal() || restore.Succeeded() ||
		restore.Spec.BackupSource == nil || restore.Spec.BackupSource.Destination != "s3://example-backups/example-backup" {
		t.Fatalf("unexpected restore view: %+v", restore)
	}
	cluster, err := DecodeCluster(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "pxc.percona.com/v1", "kind": "PerconaXtraDBCluster",
		"spec":   map[string]any{"crVersion": "1.20.0", "pxc": map[string]any{"size": int64(3)}},
		"status": map[string]any{"state": "ready", "pxc": map[string]any{"size": int64(3), "ready": int64(3)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if cluster.Spec.CRVersion != "1.20.0" || !cluster.Ready() {
		t.Fatalf("unexpected cluster view: %+v", cluster)
	}
}
