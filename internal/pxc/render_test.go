// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

//nolint:goconst // Synthetic fixtures specify wire values independently of production constants.
package pxc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

const renderClusterName = "anon-01234567"

func renderRun() *api.AnonymizationRun {
	pathStyle := true
	return &api.AnonymizationRun{
		ObjectMeta: metav1.ObjectMeta{Name: "example-run", Namespace: "renderer-tests", UID: "example-run-uid"},
		Spec: api.AnonymizationRunSpec{
			TempCluster: api.TempClusterSpec{
				CRVersion: "1.20.0", Image: "percona/percona-xtradb-cluster:8.4.8-8.1",
				BackupImage: "percona/percona-xtrabackup:8.4.0-5.1", Size: 1,
				Storage: api.TempClusterStorage{StorageClassName: "example-storage", Size: resource.MustParse("10Gi")},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
				},
				SystemUsersSecretRef: &corev1.LocalObjectReference{Name: "source-users"},
				Configuration:        "[mysqld]\nsql_mode=STRICT_TRANS_TABLES\n",
				Labels:               map[string]string{"purpose": "example"},
				Annotations:          map[string]string{"example.io/note": "renderer fixture"},
			},
			Output: api.OutputSpec{
				ObjectStorage: api.ObjectStorageSpec{
					Bucket: "example-output", Prefix: "runs/example", EndpointURL: "https://objects.example.com",
					Region: "us-east-1", PathStyle: &pathStyle,
					CredentialsSecretRef: &corev1.LocalObjectReference{Name: "output-credentials"},
					TLS:                  &api.ObjectStorageTLS{CASecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "object-ca"}, Key: "ca.crt"}},
				},
				ContainerOptions: &api.XtrabackupContainerOptions{Xbcloud: []string{"--parallel=4"}, Xtrabackup: []string{"--read-buffer-size=64M"}},
			},
			Timeouts: api.RunTimeouts{Backup: metav1.Duration{Duration: 2 * time.Hour}},
		},
	}
}

func checkRenderGolden(t *testing.T, name string, object *unstructured.Unstructured) {
	t.Helper()
	expectedJSON, err := os.ReadFile(filepath.Join("testdata", name+"-render.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	actualJSON, err := json.Marshal(object.Object)
	if err != nil {
		t.Fatal(err)
	}
	var expected, actual any
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actualJSON, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("%s renderer differs from its golden:\n%s", name, actualJSON)
	}
}

func TestRenderTempClusterGolden(t *testing.T) {
	run := renderRun()
	before := run.DeepCopy()
	cluster, err := RenderTempCluster(run, renderClusterName)
	if err != nil {
		t.Fatal(err)
	}
	checkRenderGolden(t, "cluster", cluster)
	if !reflect.DeepEqual(run, before) {
		t.Fatal("cluster rendering mutated the Run")
	}
}

func TestRenderRestoreGolden(t *testing.T) {
	owner := &api.Bootstrap{ObjectMeta: metav1.ObjectMeta{Name: "example-bootstrap", UID: "example-bootstrap-uid"}}
	verifyTLS := false
	restore, err := RenderRestore(RestoreRenderOptions{
		Namespace: "renderer-tests", Name: renderClusterName + "-restore", ClusterName: renderClusterName,
		Destination: "s3://example-source/daily/source-full",
		Credentials: api.RestoreS3Credentials{CredentialsSecret: "restore-credentials", EndpointURL: "https://source.example.com", Region: "us-east-1", VerifyTLS: &verifyTLS},
		Owner:       *metav1.NewControllerRef(owner, api.GroupVersion.WithKind("Bootstrap")),
	})
	if err != nil {
		t.Fatal(err)
	}
	checkRenderGolden(t, "restore", restore)
}

func TestRestoreNameBudgetsPerconaJobs(t *testing.T) {
	cluster := strings.Repeat("c", 22)
	for _, candidate := range []string{"short-restore", strings.Repeat("r", 28), strings.Repeat("r", 29), strings.Repeat("r", 253)} {
		name, err := RestoreName(candidate, cluster)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidate) <= 28 && name != candidate {
			t.Fatal("safe restore identity changed")
		}
		if len(candidate) > 28 && (name == candidate || len(name) != 28) {
			t.Fatal("long restore identity did not use its full safe budget")
		}
		for _, prefix := range []string{"restore-job-", "prepare-job-"} {
			job := prefix + name + "-" + cluster
			if problems := validation.IsValidLabelValue(job); len(problems) != 0 {
				t.Fatalf("invalid derived Job label: %v", problems)
			}
		}
		again, err := RestoreName(candidate, cluster)
		if err != nil || again != name {
			t.Fatal("restore identity is not deterministic")
		}
	}
	first, err := RestoreName(strings.Repeat("r", 100)+"a", cluster)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RestoreName(strings.Repeat("r", 100)+"b", cluster)
	if err != nil || first == second {
		t.Fatal("shortening discarded the full candidate identity")
	}
	for _, input := range [][2]string{{"restore", strings.Repeat("c", 23)}, {"restore", ""}, {"restore", "Invalid"}, {"", cluster}, {"invalid_name", cluster}} {
		if _, err := RestoreName(input[0], input[1]); err == nil {
			t.Fatal("invalid restore identity was accepted")
		}
	}
}

func TestRenderRestoreEnforcesDerivedJobBoundary(t *testing.T) {
	owner := &api.Bootstrap{ObjectMeta: metav1.ObjectMeta{Name: "example-bootstrap", UID: "example-bootstrap-uid"}}
	options := RestoreRenderOptions{
		Namespace: "renderer-tests", ClusterName: strings.Repeat("c", 22),
		Destination: "s3://example-source/daily/source-full",
		Credentials: api.RestoreS3Credentials{CredentialsSecret: "restore-credentials"},
		Owner:       *metav1.NewControllerRef(owner, api.GroupVersion.WithKind("Bootstrap")),
	}
	for _, size := range []int{28, 29} {
		options.Name = strings.Repeat("r", size)
		restore, err := RenderRestore(options)
		if size == 28 {
			if err != nil || restore.GetName() != options.Name {
				t.Fatalf("63-byte derived Job label was rejected: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "Job label budget") {
			t.Fatalf("64-byte derived Job label was not rejected clearly: %v", err)
		}
	}
}

func TestRenderOutputBackupGolden(t *testing.T) {
	backup, err := RenderOutputBackup(renderRun(), renderClusterName, "example-output-20260102t030405z", "example-schedule")
	if err != nil {
		t.Fatal(err)
	}
	checkRenderGolden(t, "backup", backup)
	if len(backup.GetOwnerReferences()) != 0 {
		t.Fatal("output backup must survive Run deletion")
	}
}

func TestRenderTempClusterRejectsUnsafeOverrides(t *testing.T) {
	for _, patch := range []string{
		`null`, `[]`, `{`,
		`{"secretsName":"other-users"}`,
		`{"tls":{"enabled":true}}`,
		`{"unsafeFlags":{"backupIfUnhealthy":true}}`,
		`{"pxc":{"volumeSpec":{"persistentVolumeClaim":{"storageClassName":""}}}}`,
		`{"pxc":{"volumeSpec":{"persistentVolumeClaim":null}}}`,
		`{"pxc":{"volumeSpec":{"hostPath":{"path":"/data"}}}}`,
		`{"pxc":{"volumeSpec":{"persistentVolumeClaim":{"resources":{"requests":{"storage":"21Gi"}}}}}}`,
		`{"pxc":{"size":3}}`, `{"pxc":{"size":0}}`,
		`{"backup":{"pitr":{"enabled":true}}}`,
		`{"backup":{"storages":{"anonymized":null}}}`,
		`{"backup":{"runningDeadlineSeconds":0}}`,
		`{"haproxy":{"image":""}}`,
	} {
		t.Run(patch, func(t *testing.T) {
			run := renderRun()
			run.Spec.TempCluster.Overrides = &apiextensionsv1.JSON{Raw: []byte(patch)}
			if _, err := RenderTempCluster(run, renderClusterName); err == nil {
				t.Fatal("unsafe or invalid override was accepted")
			}
		})
	}
}

func TestRenderTempClusterPreservesSafeOverrides(t *testing.T) {
	run := renderRun()
	run.Spec.TempCluster.Overrides = &apiextensionsv1.JSON{Raw: []byte(`{"haproxy":{"image":"example.com/haproxy:tested"},"pxc":{"resources":{"requests":{"cpu":"750m"}}}}`)}
	before := run.DeepCopy()
	cluster, err := RenderTempCluster(run, renderClusterName)
	if err != nil {
		t.Fatal(err)
	}
	cpu, _, _ := unstructured.NestedString(cluster.Object, "spec", "pxc", "resources", "requests", "cpu")
	image, _, _ := unstructured.NestedString(cluster.Object, "spec", "haproxy", "image")
	if cpu != "750m" || image != "example.com/haproxy:tested" || !reflect.DeepEqual(run, before) {
		t.Fatal("safe merge patch was lost or mutated the input")
	}
}

func TestRenderTempClusterRejectsUnusableInputs(t *testing.T) {
	for name, change := range map[string]func(*api.AnonymizationRun){
		"missing class":       func(r *api.AnonymizationRun) { r.Spec.TempCluster.Storage.StorageClassName = "" },
		"missing users ref":   func(r *api.AnonymizationRun) { r.Spec.TempCluster.SystemUsersSecretRef = nil },
		"empty image":         func(r *api.AnonymizationRun) { r.Spec.TempCluster.Image = "" },
		"unsupported version": func(r *api.AnonymizationRun) { r.Spec.TempCluster.CRVersion = "1.19.0" },
		"zero storage":        func(r *api.AnonymizationRun) { r.Spec.TempCluster.Storage.Size = resource.Quantity{} },
		"total storage":       func(r *api.AnonymizationRun) { r.Spec.TempCluster.Size = 3 },
		"nonstandard keys": func(r *api.AnonymizationRun) {
			r.Spec.Output.ObjectStorage.Keys = &api.ObjectStorageSecretKeys{AccessKeyID: "custom-access"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			run := renderRun()
			change(run)
			if _, err := RenderTempCluster(run, renderClusterName); err == nil {
				t.Fatal("unusable temp cluster input was accepted")
			}
		})
	}
	if _, err := RenderTempCluster(renderRun(), strings.Repeat("a", 23)); err == nil {
		t.Fatal("PXC operator name limit was not enforced")
	}
}

func TestRenderSystemUsersSecretCopiesOnlyRequiredCredentials(t *testing.T) {
	run := renderRun()
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "source-users", Namespace: run.Namespace}, Data: map[string][]byte{}}
	for _, key := range []string{"root", "xtrabackup", "monitor", "proxyadmin", "operator", "replication", "xtrabackup-aes256-psk", "unrelated"} {
		source.Data[key] = []byte("synthetic-" + key)
	}
	before := source.DeepCopy()
	secret, err := RenderSystemUsersSecret(run, renderClusterName, source)
	if err != nil {
		t.Fatal(err)
	}
	if secret.Name != renderClusterName+"-secrets" || secret.Namespace != run.Namespace || len(secret.Data) != 7 || secret.Data["unrelated"] != nil {
		t.Fatal("copied Secret has unexpected identity or keys")
	}
	refs := secret.OwnerReferences
	if len(refs) != 1 || refs[0].UID != run.UID || refs[0].Controller == nil || !*refs[0].Controller {
		t.Fatal("copied Secret lacks Run ownership")
	}
	secret.Data["root"][0] = 'X'
	if !reflect.DeepEqual(source, before) {
		t.Fatal("copied credential data aliases the source Secret")
	}
	source.Namespace = "different-namespace"
	if _, err := RenderSystemUsersSecret(run, renderClusterName, source); err == nil {
		t.Fatal("cross-namespace system credentials were accepted")
	}
	source.Namespace = run.Namespace
	delete(source.Data, "root")
	if _, err := RenderSystemUsersSecret(run, renderClusterName, source); err == nil {
		t.Fatal("missing source system password was accepted")
	}
	if _, err := RenderSystemUsersSecret(run, renderClusterName, nil); err == nil {
		t.Fatal("missing resolved source Secret was accepted")
	}
}

func TestRenderTempClusterCompareOptions(t *testing.T) {
	for _, annotations := range []map[string]string{nil, {"argocd.argoproj.io/compare-options": "custom"}} {
		run := renderRun()
		run.Spec.TempCluster.Annotations = annotations
		before := run.DeepCopy()
		cluster, err := RenderTempCluster(run, renderClusterName)
		if err != nil {
			t.Fatal(err)
		}
		want := "IgnoreExtraneous"
		if annotations != nil {
			want = "custom"
		}
		if cluster.GetAnnotations()["argocd.argoproj.io/compare-options"] != want || !reflect.DeepEqual(run, before) {
			t.Fatal("compare options default or explicit override was lost, or input was mutated")
		}
	}
}
