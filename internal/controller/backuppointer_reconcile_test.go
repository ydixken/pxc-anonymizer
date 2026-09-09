// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/objectstore"
	"github.com/ydixken/pxc-anonymizer/internal/pointer"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
)

const (
	reconcileNamespace  = "pointer-tests"
	reconcileSource     = "selected-cluster"
	reconcileStorage    = "selected-storage"
	reconcileBackupName = "completed-backup"
	reconcileLabel      = "selected"
	reconcileLabelKey   = "include"
)

type reconcileStore struct {
	documents        []pointer.Document
	metadata         objectstore.Metadata
	putError         error
	headError        error
	puts             int
	heads            int
	operationContext context.Context
}

func (s *reconcileStore) PutJSON(ctx context.Context, _ string, value any) (string, error) {
	s.operationContext = ctx
	s.puts++
	if s.putError != nil {
		return "", s.putError
	}
	document, ok := value.(pointer.Document)
	if !ok {
		return "", fmt.Errorf("unexpected pointer value %T", value)
	}
	s.documents = append(s.documents, document)
	s.metadata = objectstore.Metadata{Found: true, ETag: fmt.Sprintf("etag-%d", s.puts)}
	return s.metadata.ETag, nil
}

func (s *reconcileStore) GetJSON(context.Context, string, any) error {
	return errors.New("unexpected pointer download")
}

func (s *reconcileStore) Head(ctx context.Context, _ string) (objectstore.Metadata, error) {
	s.operationContext = ctx
	s.heads++
	return s.metadata, s.headError
}

func reconcileTime() time.Time { return time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC) }

func reconcilePointer() *api.BackupPointer {
	return &api.BackupPointer{
		ObjectMeta: metav1.ObjectMeta{Name: "latest-pointer", Namespace: reconcileNamespace, Generation: 7, UID: "pointer-uid"},
		Spec: api.BackupPointerSpec{
			Source: api.BackupPointerSource{PXCCluster: reconcileSource, StorageNames: []string{reconcileStorage}},
			Target: api.PointerTarget{
				ObjectStorage: api.ObjectStorageSpec{Bucket: "pointer-output", EndpointURL: admissionEndpoint},
				Key:           admissionPointerKey, PublicURL: admissionPointerURL,
			},
			StaleAfter:     &metav1.Duration{Duration: 2 * time.Hour},
			VerifyInterval: &metav1.Duration{Duration: time.Hour},
		},
	}
}

func reconcileBackup(t *testing.T, name string, state pxc.BackupState, completed time.Time) *unstructured.Unstructured {
	t.Helper()
	view := pxc.BackupView{
		TypeMeta: metav1.TypeMeta{APIVersion: pxc.BackupGVK.GroupVersion().String(), Kind: pxc.BackupGVK.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: reconcileNamespace,
			CreationTimestamp: metav1.NewTime(completed.Add(-time.Minute)), Labels: map[string]string{reconcileLabelKey: reconcileLabel}},
		Spec: pxc.BackupSpec{PXCCluster: reconcileSource, StorageName: reconcileStorage},
		Status: pxc.BackupStatus{State: state, Destination: "s3://backup-input/" + name + "-full",
			CompletedAt: &metav1.Time{Time: completed},
			S3:          &pxc.BackupS3{Bucket: "backup-input", EndpointURL: "https://backup.example.com", Region: "source-region"}},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&view)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: object}
}

func reconcileClient(t *testing.T, bp *api.BackupPointer, objects ...client.Object) (*BackupPointerReconciler, *reconcileStore, *time.Time) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	scheme.AddKnownTypeWithName(pxc.BackupGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(pxc.BackupGVK.GroupVersion().WithKind(pxc.BackupGVK.Kind+"List"), &unstructured.UnstructuredList{})
	objects = append(objects, bp)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.BackupPointer{}).WithObjects(objects...).Build()
	now := reconcileTime()
	store := &reconcileStore{}
	r := &BackupPointerReconciler{Client: c, APIReader: c, Scheme: scheme, now: func() time.Time { return now },
		resolveStore: func(context.Context, client.Reader, string, api.ObjectStorageSpec) (pointer.Store, error) {
			return store, nil
		},
	}
	return r, store, &now
}

func reconcileOnce(t *testing.T, r *BackupPointerReconciler) (*api.BackupPointer, ctrl.Result) {
	t.Helper()
	key := client.ObjectKey{Namespace: reconcileNamespace, Name: "latest-pointer"}
	result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatal(err)
	}
	bp := &api.BackupPointer{}
	if err := r.Get(t.Context(), key, bp); err != nil {
		t.Fatal(err)
	}
	return bp, result
}

func reconcileCondition(t *testing.T, bp *api.BackupPointer, kind string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	condition := meta.FindStatusCondition(bp.Status.Conditions, kind)
	if condition == nil || condition.Status != status || condition.Reason != reason || condition.ObservedGeneration != bp.Generation {
		t.Fatalf("condition %s = %#v, want status=%s reason=%s generation=%d", kind, condition, status, reason, bp.Generation)
	}
}

func TestBackupPointerPublishesSelectedBackup(t *testing.T) {
	bp := reconcilePointer()
	bp.Spec.Source.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{reconcileLabelKey: reconcileLabel}}
	selected := reconcileBackup(t, reconcileBackupName, pxc.BackupSucceeded, reconcileTime().Add(-10*time.Minute))
	objects := make([]client.Object, 0, 6)
	objects = append(objects, selected)
	for _, mismatch := range []string{"namespace-filter", "cluster-filter", "storage-filter", "label-filter"} {
		backup := reconcileBackup(t, "unrelated-"+mismatch, pxc.BackupSucceeded, reconcileTime().Add(-time.Minute))
		switch mismatch {
		case "namespace-filter":
			backup.SetNamespace("another-namespace")
		case "cluster-filter":
			if err := unstructured.SetNestedField(backup.Object, "another-cluster", "spec", "pxcCluster"); err != nil {
				t.Fatal(err)
			}
		case "storage-filter":
			if err := unstructured.SetNestedField(backup.Object, "another-storage", "spec", "storageName"); err != nil {
				t.Fatal(err)
			}
		case "label-filter":
			backup.SetLabels(map[string]string{reconcileLabelKey: "excluded"})
		}
		objects = append(objects, backup)
	}
	incomplete := reconcileBackup(t, "missing-completion", pxc.BackupSucceeded, reconcileTime())
	unstructured.RemoveNestedField(incomplete.Object, "status", "completed")
	objects = append(objects, incomplete)
	if err := unstructured.SetNestedField(selected.Object, "synthetic-private-reference", "status", "s3", "credentialsSecret"); err != nil {
		t.Fatal(err)
	}
	r, store, now := reconcileClient(t, bp, objects...)
	got, result := reconcileOnce(t, r)
	if len(store.documents) != 1 {
		t.Fatalf("published %d documents", len(store.documents))
	}
	document := store.documents[0]
	if document.Name != reconcileBackupName || document.SchemaVersion != 2 || document.PublishedAt == nil || !document.PublishedAt.Equal(*now) {
		t.Fatalf("unexpected published document: %#v", document)
	}
	if document.S3 == nil || document.S3.EndpointURL != "https://backup.example.com" || document.S3.Bucket != "backup-input" {
		t.Fatalf("source metadata replaced by target metadata: %#v", document.S3)
	}
	encoded, err := pointer.Encode(document)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-private-reference") || strings.Contains(string(encoded), "credentialsSecret") {
		t.Fatalf("pointer contains a credential reference: %s", encoded)
	}
	if got.Status.Current == nil || got.Status.Current.BackupName != reconcileBackupName || got.Status.Current.ETag != store.metadata.ETag || got.Status.Previous != nil {
		t.Fatalf("unexpected publication status: %#v", got.Status)
	}
	if got.Status.ObservedGeneration != bp.Generation || got.Status.Backups.Succeeded != 2 || result.RequeueAfter != time.Hour {
		t.Fatalf("generation/count/requeue mismatch: %#v, %v", got.Status, result)
	}
	reconcileCondition(t, got, conditionReady, metav1.ConditionTrue, api.ReasonPointerCurrent)
	reconcileCondition(t, got, conditionFresh, metav1.ConditionTrue, api.ReasonWithinStaleAfter)
	reconcileCondition(t, got, conditionPublished, metav1.ConditionTrue, api.ReasonUploaded)
}

func TestBackupPointerCredentialConditions(t *testing.T) {
	for _, test := range []struct {
		name, reason string
		secret       *corev1.Secret
	}{
		{name: "missing", reason: api.ReasonCredentialsUnavailable},
		{name: "invalid", reason: api.ReasonCredentialsInvalid, secret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "storage-credentials", Namespace: reconcileNamespace},
			Data:       map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("synthetic-access")},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bp := reconcilePointer()
			bp.Spec.Target.ObjectStorage.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "storage-credentials"}
			objects := []client.Object{reconcileBackup(t, reconcileBackupName, pxc.BackupSucceeded, reconcileTime().Add(-time.Minute))}
			if test.secret != nil {
				objects = append(objects, test.secret)
			}
			r, _, _ := reconcileClient(t, bp, objects...)
			r.resolveStore = nil
			got, result := reconcileOnce(t, r)
			reconcileCondition(t, got, conditionPublished, metav1.ConditionFalse, test.reason)
			reconcileCondition(t, got, conditionReady, metav1.ConditionFalse, api.ReasonPointerCurrent)
			if got.Status.Current != nil || result.RequeueAfter != pointerRetryInterval {
				t.Fatalf("credential failure published or failed to retry: %#v, %v", got.Status, result)
			}
		})
	}
}

func TestBackupPointerNewerFailurePreservesPublication(t *testing.T) {
	bp := reconcilePointer()
	selected := reconcileBackup(t, reconcileBackupName, pxc.BackupSucceeded, reconcileTime().Add(-30*time.Minute))
	r, store, _ := reconcileClient(t, bp, selected)
	first, _ := reconcileOnce(t, r)
	for _, state := range []pxc.BackupState{pxc.BackupFailed, pxc.BackupRunning} {
		backup := reconcileBackup(t, "newer-"+strings.ToLower(string(state)), state, reconcileTime())
		if err := r.Create(t.Context(), backup); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := reconcileOnce(t, r)
	reconcileCondition(t, got, conditionBackupSelected, metav1.ConditionTrue, api.ReasonNewerBackupFailed)
	selectedCondition := meta.FindStatusCondition(got.Status.Conditions, conditionBackupSelected)
	if store.puts != 1 || got.Status.Current.ETag != first.Status.Current.ETag || got.Status.Previous != nil ||
		got.Status.Backups.Failed != 1 || got.Status.Backups.Running != 1 || !strings.Contains(selectedCondition.Message, "1 newer backups failed") {
		t.Fatalf("newer failure changed publication or lost counts: %#v, puts=%d", got.Status, store.puts)
	}
	reconcileCondition(t, got, conditionReady, metav1.ConditionTrue, api.ReasonPointerCurrent)
}

func TestBackupPointerRecoversMissingOrChangedObject(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			bp := reconcilePointer()
			backup := reconcileBackup(t, reconcileBackupName, pxc.BackupSucceeded, reconcileTime().Add(-time.Minute))
			r, store, now := reconcileClient(t, bp, backup)
			first, _ := reconcileOnce(t, r)
			*now = now.Add(time.Hour)
			store.metadata = objectstore.Metadata{Found: !missing, ETag: "changed-etag"}
			store.putError = errors.New("synthetic upload failure")
			failed, _ := reconcileOnce(t, r)
			reconcileCondition(t, failed, conditionPublished, metav1.ConditionFalse, api.ReasonUploadFailed)
			reconcileCondition(t, failed, conditionReady, metav1.ConditionFalse, api.ReasonPointerCurrent)
			if failed.Status.Current.ETag != first.Status.Current.ETag || len(store.documents) != 1 || store.heads != 1 {
				t.Fatalf("failed recovery changed durable publication: %#v", failed.Status)
			}
			store.putError = nil
			recovered, _ := reconcileOnce(t, r)
			reconcileCondition(t, recovered, conditionPublished, metav1.ConditionTrue, api.ReasonUploaded)
			reconcileCondition(t, recovered, conditionReady, metav1.ConditionTrue, api.ReasonPointerCurrent)
			if len(store.documents) != 2 || recovered.Status.Current.ETag == first.Status.Current.ETag ||
				recovered.Status.Previous == nil || recovered.Status.Previous.ETag != first.Status.Current.ETag {
				t.Fatalf("missing recovery publication/history: %#v", recovered.Status)
			}
		})
	}
}

func TestBackupPointerSuspendDoesNotResolveOrPublish(t *testing.T) {
	bp := reconcilePointer()
	bp.Spec.Suspend = true
	r, store, _ := reconcileClient(t, bp, reconcileBackup(t, reconcileBackupName, pxc.BackupSucceeded, reconcileTime()))
	r.resolveStore = func(context.Context, client.Reader, string, api.ObjectStorageSpec) (pointer.Store, error) {
		t.Fatal("suspended pointer resolved object storage")
		return nil, errors.New("unreachable")
	}
	got, result := reconcileOnce(t, r)
	reconcileCondition(t, got, conditionReady, metav1.ConditionFalse, api.ReasonSuspended)
	if got.Status.Phase != api.BackupPointerPhaseSuspended || store.puts != 0 || store.heads != 0 || result.RequeueAfter != 0 {
		t.Fatalf("suspended pointer performed work: %#v, %v", got.Status, result)
	}
}

func TestBackupPointerDanglingFreshnessExpires(t *testing.T) {
	bp := reconcilePointer()
	bp.Spec.StaleAfter.Duration = 30 * time.Minute
	backup := reconcileBackup(t, reconcileBackupName, pxc.BackupSucceeded, reconcileTime().Add(-10*time.Minute))
	r, store, now := reconcileClient(t, bp, backup)
	reconcileOnce(t, r)
	if err := r.Delete(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	dangling, result := reconcileOnce(t, r)
	reconcileCondition(t, dangling, conditionReady, metav1.ConditionFalse, api.ReasonDangling)
	reconcileCondition(t, dangling, conditionFresh, metav1.ConditionTrue, api.ReasonWithinStaleAfter)
	if result.RequeueAfter != 20*time.Minute {
		t.Fatalf("freshness deadline lost: %v", result)
	}
	*now = now.Add(result.RequeueAfter)
	stale, result := reconcileOnce(t, r)
	reconcileCondition(t, stale, conditionFresh, metav1.ConditionFalse, api.ReasonStaleBackup)
	if result.RequeueAfter != 0 || store.puts != 1 || stale.Status.Current == nil {
		t.Fatalf("dangling pointer republished, lost history, or kept polling: %#v, %v", stale.Status, result)
	}
}

func TestBackupPointerStorageTimeoutPreservesFailureStatus(t *testing.T) {
	bp := reconcilePointer()
	r, store, _ := reconcileClient(t, bp,
		reconcileBackup(t, reconcileBackupName, pxc.BackupSucceeded, reconcileTime().Add(-time.Minute)))
	store.putError = context.DeadlineExceeded
	var operationContext context.Context
	r.resolveStore = func(ctx context.Context, _ client.Reader, _ string, _ api.ObjectStorageSpec) (pointer.Store, error) {
		operationContext = ctx
		deadline, exists := ctx.Deadline()
		if !exists || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
			t.Fatalf("storage context lacks its bounded deadline: %v, %t", deadline, exists)
		}
		return store, nil
	}
	patched := false
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object,
			patch client.Patch, options ...client.SubResourcePatchOption) error {
			if ctx != t.Context() || ctx == operationContext || ctx.Err() != nil {
				t.Fatal("failure status did not use the original live reconcile context")
			}
			patched = true
			return c.SubResource(subresource).Patch(ctx, obj, patch, options...)
		},
	})
	got, result := reconcileOnce(t, r)
	reconcileCondition(t, got, conditionPublished, metav1.ConditionFalse, api.ReasonEndpointUnreachable)
	if !patched || store.operationContext != operationContext || got.Status.Current != nil || result.RequeueAfter != pointerRetryInterval {
		t.Fatalf("timed-out publication lost its durable failure: patched=%t status=%#v result=%v", patched, got.Status, result)
	}
}
