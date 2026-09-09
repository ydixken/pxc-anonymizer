// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

//nolint:goconst // Synthetic wire fixtures stay independent of controller constants.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/crossplane"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
)

const bootstrapTestNamespace = "bootstrap-tests"

var bootstrapUsers = schema.GroupVersionResource{Group: "mysql.sql.crossplane.io", Version: "v1alpha1", Resource: "users"}

type bootstrapFixture struct {
	r        *BootstrapReconciler
	key      client.ObjectKey
	clock    *time.Time
	dynamic  *dynamicfake.FakeDynamicClient
	recorder *events.FakeRecorder
}

func bootstrapTestObject() *api.Bootstrap {
	return &api.Bootstrap{ObjectMeta: metav1.ObjectMeta{Name: "example-bootstrap", Namespace: bootstrapTestNamespace, UID: "bootstrap-uid", Generation: 1}, Spec: api.BootstrapSpec{
		Pointer: api.BootstrapSource{Destination: "s3://example-backups/daily/source-full"},
		Restore: api.RestoreS3Credentials{CredentialsSecret: "restore-credentials", EndpointURL: "https://objects.example.com"},
		Targets: []api.BootstrapTarget{{PXCCluster: "z-target"}, {PXCCluster: "a-target"}},
	}}
}

func bootstrapTestCluster(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": pxc.ClusterGVK.GroupVersion().String(), "kind": pxc.ClusterGVK.Kind,
		"metadata": map[string]any{"name": name, "namespace": bootstrapTestNamespace, "uid": name + "-uid"},
		"spec":     map[string]any{"pause": false, "pxc": map[string]any{"size": int64(1)}},
		"status":   map[string]any{"state": "ready", "pxc": map[string]any{"ready": int64(1)}},
	}}
}

func bootstrapTestManaged(name string, human bool) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "mysql.sql.crossplane.io/v1alpha1", "kind": "User", "metadata": map[string]any{"name": name, "uid": name + "-uid"}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}, map[string]any{"type": "Synced", "status": "True"}}}}}
	if human {
		object.SetAnnotations(map[string]string{crossplane.PausedAnnotation: "true"})
	}
	return object
}

func newBootstrapFixture(t *testing.T, bs *api.Bootstrap, managed ...runtime.Object) *bootstrapFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, corev1.AddToScheme, batchv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	objects := make([]client.Object, 0, 2+len(bs.Spec.Targets))
	objects = append(objects, bs, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "restore-credentials", Namespace: bs.Namespace}, Data: map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("synthetic-access"), "AWS_SECRET_ACCESS_KEY": []byte("synthetic-secret")}})
	for _, target := range bs.Spec.Targets {
		objects = append(objects, bootstrapTestCluster(target.PXCCluster))
	}
	hooks := interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.CreateOption) error {
		if object.GetObjectKind().GroupVersionKind() == pxc.RestoreGVK {
			persisted := &api.Bootstrap{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(bs), persisted); err != nil {
				return err
			}
			if !slices.Contains(persisted.Finalizers, bootstrapFinalizer) || persisted.Status.Source == nil || persisted.Status.Execution == nil {
				return fmt.Errorf("restore attempted before durable finalizer and source checkpoint")
			}
			object.SetUID(types.UID(object.GetName() + "-uid"))
		}
		return c.Create(ctx, object, options...)
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Bootstrap{}).WithObjects(objects...).WithInterceptorFuncs(hooks).Build()
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{bootstrapUsers: "UserList"}, managed...)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	recorder := events.NewFakeRecorder(20)
	return &bootstrapFixture{r: &BootstrapReconciler{Client: c, APIReader: c, Scheme: scheme, DynamicClient: dynamic, Recorder: recorder, now: func() time.Time { return now }}, key: client.ObjectKeyFromObject(bs), clock: &now, dynamic: dynamic, recorder: recorder}
}

func (f *bootstrapFixture) current(t *testing.T) *api.Bootstrap {
	t.Helper()
	bs := &api.Bootstrap{}
	if err := f.r.Get(t.Context(), f.key, bs); err != nil {
		t.Fatal(err)
	}
	return bs
}
func (f *bootstrapFixture) step(t *testing.T) ctrl.Result {
	t.Helper()
	result, err := f.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: f.key})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func (f *bootstrapFixture) until(t *testing.T, condition func(*api.Bootstrap) bool) *api.Bootstrap {
	t.Helper()
	for range 35 {
		current := f.current(t)
		if condition(current) {
			return current
		}
		f.step(t)
	}
	t.Fatalf("bounded reconciliation did not reach expected result: %#v", f.current(t).Status)
	return nil
}
func (f *bootstrapFixture) firstRestore(t *testing.T) *api.Bootstrap {
	t.Helper()
	return f.until(t, func(bs *api.Bootstrap) bool {
		return bs.Status.Execution != nil && len(bs.Status.Execution.Targets) > 0 && bs.Status.Execution.Targets[0].RestoreUID != ""
	})
}
func (f *bootstrapFixture) restore(t *testing.T, name string, state pxc.RestoreState) *unstructured.Unstructured {
	t.Helper()
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(pxc.RestoreGVK)
	if err := f.r.Get(t.Context(), client.ObjectKey{Namespace: f.key.Namespace, Name: name}, object); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(object.Object, string(state), "status", "state"); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Update(t.Context(), object); err != nil {
		t.Fatal(err)
	}
	return object
}
func (f *bootstrapFixture) firstCluster(t *testing.T, paused, ready bool) {
	t.Helper()
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(pxc.ClusterGVK)
	if err := f.r.Get(t.Context(), client.ObjectKey{Namespace: f.key.Namespace, Name: "z-target"}, object); err != nil {
		t.Fatal(err)
	}
	state := "initializing"
	if ready {
		state = "ready"
	}
	if err := unstructured.SetNestedField(object.Object, paused, "spec", "pause"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(object.Object, state, "status", "state"); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Update(t.Context(), object); err != nil {
		t.Fatal(err)
	}
}
func (f *bootstrapFixture) managed(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	object, err := f.dynamic.Resource(bootstrapUsers).Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func TestBootstrapOrderedRestoreReadinessAndRearm(t *testing.T) {
	f := newBootstrapFixture(t, bootstrapTestObject())
	first := f.firstRestore(t)
	if first.Status.Targets[0].PXCCluster != "z-target" || first.Status.Targets[1].RestoreName != "" {
		t.Fatal("target order changed")
	}
	name := first.Status.Targets[0].RestoreName
	restore := f.restore(t, name, pxc.RestoreStartCluster)
	if restore.GetAnnotations()["argocd.argoproj.io/compare-options"] != "IgnoreExtraneous" || restore.GetLabels()["app.kubernetes.io/instance"] != "" {
		t.Fatal("restore may be pruned by Argo")
	}
	f.firstCluster(t, true, false)
	for range 3 {
		f.step(t)
	}
	if f.current(t).Status.Targets[1].RestoreName != "" {
		t.Fatal("Starting Cluster was treated as terminal")
	}
	f.restore(t, name, pxc.RestoreSucceeded)
	f.firstCluster(t, false, false)
	for range 3 {
		f.step(t)
	}
	if f.current(t).Status.Targets[1].RestoreName != "" {
		t.Fatal("next target started before cluster readiness")
	}
	f.firstCluster(t, false, true)
	second := f.until(t, func(bs *api.Bootstrap) bool { return bs.Status.Execution.Targets[1].RestoreUID != "" })
	f.restore(t, second.Status.Targets[1].RestoreName, pxc.RestoreSucceeded)
	completed := f.until(t, func(bs *api.Bootstrap) bool { return bs.Status.Phase == api.BootstrapPhaseCompleted })
	if completed.Status.TargetsSummary != "2/2" || !meta.IsStatusConditionTrue(completed.Status.Conditions, "Complete") {
		t.Fatal("complete did not require both ready targets")
	}
	f.step(t)
	if f.current(t).Status.CompletedAt.String() != completed.Status.CompletedAt.String() {
		t.Fatal("completed Bootstrap was not absorbing")
	}
	for i := range 5 {
		completed.Status.History = append(completed.Status.History, api.BootstrapRunRecord{Trigger: fmt.Sprintf("old-%d", i), Outcome: api.BootstrapPhaseCompleted})
	}
	if err := f.r.Status().Update(t.Context(), completed); err != nil {
		t.Fatal(err)
	}
	completed.Spec.Trigger = "again"
	if err := f.r.Update(t.Context(), completed); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	rearmed := f.current(t)
	if rearmed.Status.Phase != api.BootstrapPhaseWaitingForClusters || rearmed.Status.Source != nil || rearmed.Status.ObservedTrigger != "again" || len(rearmed.Status.History) != 5 || rearmed.Status.History[0].Trigger != "old-1" {
		t.Fatal("trigger did not archive the last five outcomes and reset execution")
	}
}

func TestBootstrapPointerHTTPAgeAndLegacyEvent(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, payload        string
		wantError, wantEvent bool
	}{
		{"fresh", `{"name":"example","destination":"s3://example/backups/full","schemaVersion":2,"publishedAt":"2026-01-02T11:30:00Z"}`, false, false},
		{"stale", `{"name":"example","destination":"s3://example/backups/full","schemaVersion":2,"publishedAt":"2026-01-01T11:30:00Z"}`, true, false},
		{"v2 missing timestamp", `{"name":"example","destination":"s3://example/backups/full","schemaVersion":2}`, true, false},
		{"legacy", `{"name":"example","destination":"s3://example/backups/full"}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.payload)) }))
			defer server.Close()
			bs := bootstrapTestObject()
			bs.Spec.Pointer = api.BootstrapSource{HTTP: &api.HTTPPointerSource{URL: server.URL, AllowInsecure: true}, MaxAge: &metav1.Duration{Duration: time.Hour}}
			f := newBootstrapFixture(t, bs)
			*f.clock = now
			document, err := f.r.bootstrapFetch(t.Context(), bs)
			if (err != nil) != tc.wantError {
				t.Fatalf("fetch error=%v", err)
			}
			if !tc.wantError && (document == nil || document.Destination == "") {
				t.Fatal("missing pointer passed")
			}
			if tc.wantEvent {
				select {
				case event := <-f.recorder.Events:
					if !strings.Contains(event, "LegacyPointerAgeUnknown") {
						t.Fatal(event)
					}
				default:
					t.Fatal("legacy exception was not reported")
				}
			}
		})
	}
}

func TestBootstrapS3PointerAndFrozenSourceEndpoint(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if !strings.Contains(r.URL.Path, "/pointer-bucket/latest.json") || r.Header.Get("Authorization") == "" {
			t.Error("S3 pointer was not requested with scoped credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Last-Modified", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC).Format(http.TimeFormat))
		_, _ = w.Write([]byte(`{"name":"source","destination":"s3://source-bucket/full","schemaVersion":2,"s3":{"bucket":"source-bucket","endpointUrl":"https://source.example.com","region":"source-region"}}`))
	}))
	defer server.Close()
	bs := bootstrapTestObject()
	pathStyle := true
	bs.Spec.Restore.EndpointURL = ""
	bs.Spec.Restore.Region = ""
	bs.Spec.Pointer = api.BootstrapSource{S3: &api.S3PointerSource{Key: "latest.json", ObjectStorage: api.ObjectStorageSpec{Bucket: "pointer-bucket", EndpointURL: server.URL, PathStyle: &pathStyle, CredentialsSecretRef: &corev1.LocalObjectReference{Name: "restore-credentials"}}}}
	f := newBootstrapFixture(t, bs)
	current := f.firstRestore(t)
	if requests != 1 || current.Status.Execution.Targets[0].Restore.EndpointURL != "https://source.example.com" || current.Status.Execution.Targets[0].Restore.Region != "source-region" {
		t.Fatal("resolved source endpoint was not durably frozen")
	}
	f.step(t)
	if requests != 1 {
		t.Fatal("source was refetched after resolution")
	}
}

func TestBootstrapVanishedRestoreWaitsForJobThenCompensates(t *testing.T) {
	f := newBootstrapFixture(t, bootstrapTestObject())
	bs := f.firstRestore(t)
	restore := f.restore(t, bs.Status.Targets[0].RestoreName, pxc.RestoreRestore)
	f.firstCluster(t, true, false)
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "owned-restore-job", Namespace: bs.Namespace, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(restore, pxc.RestoreGVK)}}}
	if err := f.r.Create(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Delete(t.Context(), restore); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	failed := f.current(t)
	condition := meta.FindStatusCondition(failed.Status.Conditions, "Failed")
	if condition == nil || condition.Reason != "RestoreVanished" {
		t.Fatal("vanished restore was not a failure")
	}
	if result := f.step(t); result.RequeueAfter <= 0 {
		t.Fatal("compensation did not stay active while the job existed")
	}
	cluster, err := f.r.bootstrapReadCluster(t.Context(), bs.Namespace, "z-target")
	if err != nil || !cluster.Spec.Pause {
		t.Fatal("cluster was unpaused with an owned job present")
	}
	if err := f.r.Delete(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	cluster, err = f.r.bootstrapReadCluster(t.Context(), bs.Namespace, "z-target")
	if err != nil || cluster.Spec.Pause {
		t.Fatal("vanished restore compensation did not unpause the cluster")
	}
	if f.current(t).Status.Targets[1].RestoreName != "" {
		t.Fatal("failed bootstrap advanced to another target")
	}
}

func TestBootstrapDeletionWaitsAndResumesOnlyOwnedPauses(t *testing.T) {
	bs := bootstrapTestObject()
	bs.Spec.Crossplane = &api.CrossplaneSpec{Kinds: []string{"users"}}
	f := newBootstrapFixture(t, bs, bootstrapTestManaged("ours", false), bootstrapTestManaged("human", true))
	current := f.firstRestore(t)
	restore := f.restore(t, current.Status.Targets[0].RestoreName, pxc.RestoreStartCluster)
	if len(current.Status.Crossplane.Paused) != 1 || current.Status.Crossplane.Paused[0].Name != "ours" {
		t.Fatal("human pause was claimed")
	}
	if err := f.r.Delete(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	if result := f.step(t); result.RequeueAfter <= 0 {
		t.Fatal("deletion did not wait for nonterminal restore")
	}
	if !slices.Contains(f.current(t).Finalizers, bootstrapFinalizer) || f.managed(t, "ours").GetAnnotations()[crossplane.PausedAnnotation] != "true" {
		t.Fatal("active restore lost its cleanup guard")
	}
	f.restore(t, restore.GetName(), pxc.RestoreSucceeded)
	f.step(t)
	deleted := &api.Bootstrap{}
	if err := f.r.Get(t.Context(), f.key, deleted); !apierrors.IsNotFound(err) {
		t.Fatalf("deletion did not finish: %v", err)
	}
	if f.managed(t, "ours").GetAnnotations()[crossplane.PausedAnnotation] != "" || f.managed(t, "human").GetAnnotations()[crossplane.PausedAnnotation] != "true" {
		t.Fatal("cleanup did not preserve pause ownership")
	}
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(pxc.RestoreGVK)
	if err := f.r.Get(t.Context(), client.ObjectKeyFromObject(restore), object); err != nil {
		t.Fatal("controller deleted the restore")
	}
}

func TestBootstrapRecoversPauseAfterLostStatus(t *testing.T) {
	bs := bootstrapTestObject()
	bs.Spec.Crossplane = &api.CrossplaneSpec{Kinds: []string{"users"}}
	f := newBootstrapFixture(t, bs, bootstrapTestManaged("ours", false), bootstrapTestManaged("human", true))
	current := f.until(t, func(bs *api.Bootstrap) bool { return bs.Status.Phase == api.BootstrapPhaseRestoring })
	current.Status.Crossplane.Paused = nil
	current.Status.Phase = api.BootstrapPhaseFailed
	if err := f.r.Status().Update(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	if len(f.current(t).Status.Crossplane.Paused) != 1 || f.managed(t, "ours").GetAnnotations()[crossplane.PausedAnnotation] != "true" {
		t.Fatal("recovered pause was not persisted before resumption")
	}
	f.step(t)
	if f.managed(t, "ours").GetAnnotations()[crossplane.PausedAnnotation] != "" || f.managed(t, "human").GetAnnotations()[crossplane.PausedAnnotation] != "true" {
		t.Fatal("recovery resumed the wrong set")
	}
}

func TestBootstrapRecreationPersistsUIDBeforeDeleteAndWaitsForSynced(t *testing.T) {
	bs := bootstrapTestObject()
	bs.Spec.Targets = bs.Spec.Targets[:1]
	bs.Spec.Crossplane = &api.CrossplaneSpec{Kinds: []string{"users"}, Recreate: &api.CrossplaneRecreate{Kinds: []string{"users"}}}
	f := newBootstrapFixture(t, bs, bootstrapTestManaged("ours", false))
	current := f.firstRestore(t)
	f.restore(t, current.Status.Targets[0].RestoreName, pxc.RestoreSucceeded)
	f.until(t, func(bs *api.Bootstrap) bool { return bs.Status.Phase == api.BootstrapPhaseRecreatingCrossplane })
	f.step(t)
	checkpoint := f.current(t)
	if len(checkpoint.Status.Crossplane.Recreating) != 1 || checkpoint.Status.Crossplane.Recreating[0].UID != "ours-uid" {
		t.Fatal("old UID checkpoint is missing")
	}
	for _, action := range f.dynamic.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("resource deleted before checkpoint persisted")
		}
	}
	f.step(t)
	f.step(t)
	replacement := bootstrapTestManaged("ours", false)
	replacement.SetUID("replacement-uid")
	if err := unstructured.SetNestedSlice(replacement.Object, []any{map[string]any{"type": "Ready", "status": "True"}, map[string]any{"type": "Synced", "status": "False"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dynamic.Resource(bootstrapUsers).Create(t.Context(), replacement, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	if f.current(t).Status.Phase == api.BootstrapPhaseCompleted {
		t.Fatal("Ready without Synced was accepted")
	}
	if err := unstructured.SetNestedSlice(replacement.Object, []any{map[string]any{"type": "Ready", "status": "True"}, map[string]any{"type": "Synced", "status": "True"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dynamic.Resource(bootstrapUsers).Update(t.Context(), replacement, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	if f.current(t).Status.Phase != api.BootstrapPhaseCompleted {
		data, _ := json.Marshal(f.current(t).Status)
		t.Fatalf("recreation did not complete: %s", data)
	}
	if f.managed(t, "ours").GetUID() != "replacement-uid" {
		t.Fatal("replacement was deleted")
	}
}

func TestBootstrapTimeoutKeepsCompensationActive(t *testing.T) {
	f := newBootstrapFixture(t, bootstrapTestObject())
	current := f.firstRestore(t)
	f.restore(t, current.Status.Targets[0].RestoreName, pxc.RestoreStartCluster)
	*f.clock = f.clock.Add(4 * time.Hour)
	f.step(t)
	if f.current(t).Status.Phase != api.BootstrapPhaseFailed {
		t.Fatal("restore timeout was not enforced")
	}
	result := f.step(t)
	if result.RequeueAfter <= 0 || !slices.Contains(f.current(t).Finalizers, bootstrapFinalizer) {
		t.Fatal("failed phase abandoned an active restore")
	}
}

func TestBootstrapRepeatedTriggerUsesNewExecutionIdentity(t *testing.T) {
	bs := bootstrapTestObject()
	bs.Name = strings.Repeat("b", 64)
	bs.Spec.Targets = bs.Spec.Targets[:1]
	bs.Spec.Targets[0].PXCCluster = strings.Repeat("c", 22)
	bs.Spec.Trigger = "A"
	f := newBootstrapFixture(t, bs)
	names := map[string]bool{}
	identities := map[string]bool{}
	for i, trigger := range []string{"A", "B", "A"} {
		if i > 0 {
			current := f.current(t)
			current.Spec.Trigger = trigger
			if err := f.r.Update(t.Context(), current); err != nil {
				t.Fatal(err)
			}
			f.step(t)
		}
		current := f.firstRestore(t)
		name, id := current.Status.Targets[0].RestoreName, current.Status.Execution.ID
		if name == "" || id == "" || names[name] || identities[id] {
			t.Fatal("a repeated trigger reused an execution or restore identity")
		}
		for _, prefix := range []string{"restore-job-", "prepare-job-"} {
			job := prefix + name + "-" + current.Status.Targets[0].PXCCluster
			if len(job) != 63 || len(validation.IsValidLabelValue(job)) != 0 {
				t.Fatal("long Bootstrap identity exceeded the derived Percona Job label budget")
			}
		}
		names[name] = true
		identities[id] = true
		restore := f.restore(t, name, pxc.RestoreSucceeded)
		if restore.GetName() != name || metav1.GetControllerOf(restore).Name != bs.Name {
			t.Fatal("shortening changed durable restore identity or Bootstrap ownership")
		}
		if restore.GetAnnotations()[bootstrapExecutionAnnotation] != id {
			t.Fatal("restore lacks its execution identity")
		}
		f.until(t, func(bs *api.Bootstrap) bool { return bs.Status.Phase == api.BootstrapPhaseCompleted })
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(pxc.RestoreGVK.GroupVersion().WithKind(pxc.RestoreGVK.Kind + "List"))
	if err := f.r.List(t.Context(), list, client.InNamespace(bs.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("repeated trigger created %d restores, want three", len(list.Items))
	}
}

func TestBootstrapLiteralMaxAgeIsRejected(t *testing.T) {
	bs := bootstrapTestObject()
	bs.Spec.Trigger = "invalid-age"
	bs.Spec.Pointer.MaxAge = &metav1.Duration{Duration: time.Hour}
	f := newBootstrapFixture(t, bs)
	if _, err := f.r.bootstrapFetch(t.Context(), bs); err == nil || !strings.Contains(err.Error(), "literal destination") {
		t.Fatalf("literal age check was not rejected clearly: %v", err)
	}
	failed := f.until(t, func(bs *api.Bootstrap) bool { return bs.Status.Phase == api.BootstrapPhaseFailed })
	condition := meta.FindStatusCondition(failed.Status.Conditions, "Failed")
	if condition == nil || condition.Reason != "InvalidSpec" || !strings.Contains(condition.Message, "maxAge") {
		t.Fatal("invalid literal age was not reported before effects")
	}
	f.step(t)
	if len(f.current(t).Status.History) != 0 || f.current(t).Status.Execution != nil {
		t.Fatal("invalid literal age retried or created an execution")
	}
}

func TestBootstrapCreateStatusLossAdoptsOrFailsClosed(t *testing.T) {
	for _, vanish := range []bool{false, true} {
		t.Run(fmt.Sprintf("vanish=%v", vanish), func(t *testing.T) {
			bs := bootstrapTestObject()
			bs.Spec.Targets = bs.Spec.Targets[:1]
			f := newBootstrapFixture(t, bs)
			planned := f.until(t, func(bs *api.Bootstrap) bool {
				return len(bs.Status.Targets) > 0 && bs.Status.Targets[0].RestoreName != ""
			})
			failure := errors.New("injected post-create status loss")
			loseStatus := true
			creates := 0
			underlying := f.r.Client.(client.WithWatch)
			f.r.Client = interceptor.NewClient(underlying, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.CreateOption) error {
					if object.GetObjectKind().GroupVersionKind() == pxc.RestoreGVK {
						creates++
						persisted := &api.Bootstrap{}
						if err := c.Get(ctx, f.key, persisted); err != nil {
							return err
						}
						if persisted.Status.Execution.ID == "" || !persisted.Status.Execution.Targets[0].RestoreAttempted {
							return errors.New("create preceded its durable attempt checkpoint")
						}
					}
					return c.Create(ctx, object, options...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
					current, ok := object.(*api.Bootstrap)
					if ok && subresource == "status" && loseStatus && current.Status.Execution.Targets[0].RestoreUID != "" {
						loseStatus = false
						if vanish {
							child := &unstructured.Unstructured{}
							child.SetGroupVersionKind(pxc.RestoreGVK)
							if err := c.Get(ctx, client.ObjectKey{Namespace: bs.Namespace, Name: planned.Status.Targets[0].RestoreName}, child); err != nil {
								return err
							}
							if err := c.Delete(ctx, child); err != nil {
								return err
							}
						}
						return failure
					}
					return c.SubResource(subresource).Patch(ctx, object, patch, options...)
				},
			})
			if _, err := f.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: f.key}); !errors.Is(err, failure) {
				t.Fatalf("lost status write was not exercised: %v", err)
			}
			current := f.current(t)
			if !current.Status.Execution.Targets[0].RestoreAttempted || current.Status.Execution.Targets[0].RestoreUID != "" || creates != 1 {
				t.Fatal("durable creation intent or injected status loss was not preserved")
			}
			if vanish {
				f.firstCluster(t, true, false)
			}
			f.step(t)
			if vanish {
				condition := meta.FindStatusCondition(f.current(t).Status.Conditions, "Failed")
				if condition == nil || condition.Reason != "RestoreVanished" {
					t.Fatal("ambiguous missing restore was retried")
				}
				f.step(t)
				cluster, err := f.r.bootstrapReadCluster(t.Context(), bs.Namespace, "z-target")
				if err != nil || cluster.Spec.Pause {
					t.Fatal("empty RestoreUID prevented attempted-restore compensation")
				}
			} else if f.current(t).Status.Execution.Targets[0].RestoreUID == "" {
				t.Fatal("matching existing restore was not adopted")
			}
			if creates != 1 {
				t.Fatalf("restore Create was repeated %d times", creates)
			}
		})
	}
}

func TestBootstrapCreateAttemptCheckpointFailureAndCrash(t *testing.T) {
	for _, beforeCreate := range []bool{false, true} {
		t.Run(fmt.Sprintf("crashBeforeCreate=%v", beforeCreate), func(t *testing.T) {
			bs := bootstrapTestObject()
			bs.Spec.Targets = bs.Spec.Targets[:1]
			f := newBootstrapFixture(t, bs)
			f.until(t, func(bs *api.Bootstrap) bool {
				return len(bs.Status.Targets) > 0 && bs.Status.Targets[0].RestoreName != ""
			})
			failure := errors.New("injected creation checkpoint interruption")
			failOnce := true
			creates := 0
			f.r.Client = interceptor.NewClient(f.r.Client.(client.WithWatch), interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.CreateOption) error {
					if object.GetObjectKind().GroupVersionKind() == pxc.RestoreGVK {
						creates++
						if beforeCreate && failOnce {
							failOnce = false
							return failure
						}
					}
					return c.Create(ctx, object, options...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
					current, ok := object.(*api.Bootstrap)
					if !beforeCreate && failOnce && ok && subresource == "status" && current.Status.Execution.Targets[0].RestoreAttempted {
						failOnce = false
						return failure
					}
					return c.SubResource(subresource).Patch(ctx, object, patch, options...)
				},
			})
			if _, err := f.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: f.key}); !errors.Is(err, failure) {
				t.Fatalf("checkpoint interruption was not exercised: %v", err)
			}
			if beforeCreate {
				if !f.current(t).Status.Execution.Targets[0].RestoreAttempted || creates != 1 {
					t.Fatal("pre-create crash lost its durable intent")
				}
				f.step(t)
				condition := meta.FindStatusCondition(f.current(t).Status.Conditions, "Failed")
				if condition == nil || condition.Reason != "RestoreVanished" || creates != 1 {
					t.Fatal("ambiguous pre-create crash repeated the restore")
				}
			} else {
				if f.current(t).Status.Execution.Targets[0].RestoreAttempted || creates != 0 {
					t.Fatal("failed checkpoint permitted a Create")
				}
				f.step(t)
				if creates != 1 || f.current(t).Status.Execution.Targets[0].RestoreUID == "" {
					t.Fatal("a successfully persisted retry did not create its restore")
				}
			}
		})
	}
}

func TestBootstrapMissingExecutionIDKeepsCompensationReferences(t *testing.T) {
	bs := bootstrapTestObject()
	bs.Spec.Crossplane = &api.CrossplaneSpec{Kinds: []string{"users"}}
	f := newBootstrapFixture(t, bs, bootstrapTestManaged("ours", false))
	current := f.until(t, func(bs *api.Bootstrap) bool { return bs.Status.Phase == api.BootstrapPhaseRestoring })
	current.Status.Execution.ID = ""
	if err := f.r.Status().Update(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	failed := f.current(t)
	if failed.Status.Phase != api.BootstrapPhaseFailed || len(failed.Status.Execution.Selected) != 1 {
		t.Fatal("invalid checkpoint discarded compensation references")
	}
	f.step(t)
	if f.managed(t, "ours").GetAnnotations()[crossplane.PausedAnnotation] != "" {
		t.Fatal("missing execution ID abandoned pause compensation")
	}
}
