// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	record "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/pointer"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
	runnerpolicy "github.com/ydixken/pxc-anonymizer/internal/runner/policy"
)

const runTestStorageSecret = "storage"
const runTestSourceUsers = "source-users"
const runTestStatusField = "status"
const runTestStateField = "state"
const runTestPXCField = "pxc"
const runTestBackupField = "backup"
const runTestConstantValue = "synthetic-constant"
const runTestMissingReport = "missing"
const runTestForeignGroup = "foreign-group"

const runTestReady = "ready"
const runTestBackupPointer = "backup-pointer"

const runTestNamespace = "run-test"
const runTestDestination = "s3://source-backups/example-full"
const runTestEndpoint = "http://storage.example"

type runTestState struct {
	reconciler         *AnonymizationRunReconciler
	run                *api.AnonymizationRun
	clock              time.Time
	creates            int
	failSnapshotStatus bool
	failRestoreIntent  bool
}

func newRunTest(t *testing.T, mutate func(*api.AnonymizationRun, []client.Object)) *runTestState {
	t.Helper()
	state := &runTestState{clock: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	state.run = &api.AnonymizationRun{ObjectMeta: metav1.ObjectMeta{Name: "example-run", Namespace: runTestNamespace,
		UID: types.UID("11111111-2222-3333-4444-555555555555"), Generation: 1, CreationTimestamp: metav1.NewTime(state.clock)},
		Spec: api.AnonymizationRunSpec{
			PolicyRef: corev1.LocalObjectReference{Name: "example-policy"},
			Source:    api.RunSource{Destination: runTestDestination, Restore: api.RestoreS3Credentials{CredentialsSecret: runTestStorageSecret}},
			TempCluster: api.TempClusterSpec{NamePrefix: "anonymization-example", CRVersion: "1.20.0",
				Image: "percona/percona-xtradb-cluster:8.4.8-8.1", BackupImage: "percona/percona-xtrabackup:8.4.0-5.1", Size: 1,
				SystemUsersSecretRef: &corev1.LocalObjectReference{Name: runTestSourceUsers},
				Storage:              api.TempClusterStorage{StorageClassName: "test-storage", Size: resource.MustParse("5Gi")}},
			Output: api.OutputSpec{ObjectStorage: api.ObjectStorageSpec{Bucket: "anonymized-backups", CredentialsSecretRef: &corev1.LocalObjectReference{Name: runTestStorageSecret}}},
			Runner: api.RunnerSpec{Workers: 4},
		}}
	policy := policyRuntimeFixture(state.run.Spec.PolicyRef.Name)
	policy.Namespace, policy.Generation = runTestNamespace, 1
	hash, err := runnerpolicy.Hash(policy.Spec)
	if err != nil {
		t.Fatal(err)
	}
	policy.Status = api.AnonymizationPolicyStatus{Hash: hash, Conditions: []metav1.Condition{{Type: conditionPolicyValid,
		Status: metav1.ConditionTrue, Reason: api.ReasonSpecValid, ObservedGeneration: 1, LastTransitionTime: metav1.NewTime(state.clock)}}}
	users := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: runTestSourceUsers, Namespace: runTestNamespace}, Data: map[string][]byte{}}
	for _, key := range runSystemKeys {
		users.Data[key] = []byte("synthetic-" + key)
	}
	storage := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: runTestStorageSecret, Namespace: runTestNamespace}, Data: map[string][]byte{
		runAWSAccessKey: []byte("synthetic-access"), runAWSSecretKey: []byte("synthetic-secret"), "S3_ENDPOINT_URL": []byte(runTestEndpoint)}}
	objects := append([]client.Object{state.run, policy, users, storage}, policyRuntimeReferences(runTestNamespace)...)
	for _, object := range objects {
		if secret, ok := object.(*corev1.Secret); ok && secret.Name == policy.Spec.Determinism.SeedSecretRef.Name {
			secret.Data[policy.Spec.Determinism.SeedSecretRef.Key] = []byte("synthetic-fixed-seed-with-32-bytes-minimum")
		}
	}
	if mutate != nil {
		mutate(state.run, objects)
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, batchv1.AddToScheme, api.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	cluster, restore, backup := runTestObject(pxc.ClusterGVK.Kind, "unused"), runTestObject(pxc.RestoreGVK.Kind, "unused"), runTestObject(pxc.BackupGVK.Kind, "unused")
	for _, object := range []*unstructured.Unstructured{cluster, restore, backup} {
		scheme.AddKnownTypeWithName(object.GroupVersionKind(), &unstructured.Unstructured{})
		listGVK := object.GroupVersionKind()
		listGVK.Kind += "List"
		scheme.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	}
	var uidCounter int
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
		WithStatusSubresource(&api.AnonymizationRun{}, &api.AnonymizationPolicy{}, cluster, restore, backup).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.CreateOption) error {
				latest := &api.AnonymizationRun{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(state.run), latest); err != nil {
					return err
				}
				if !controllerutil.ContainsFinalizer(latest, runFinalizer) {
					t.Fatal("child created before persisted finalizer")
				}
				if _, ok := object.(*batchv1.Job); ok && (latest.Status.Anonymize == nil || latest.Status.Anonymize.JobName != object.GetName()) {
					t.Fatal("Job created before durable attempt intent")
				}
				if object.GetObjectKind().GroupVersionKind() == pxc.RestoreGVK && latest.Status.RestoreName != object.GetName() {
					t.Fatal("restore created before durable intent")
				}
				state.creates++
				uidCounter++
				if object.GetUID() == "" {
					object.SetUID(types.UID(fmt.Sprintf("child-%d", uidCounter)))
				}
				return c.Create(ctx, object, options...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
				if run, ok := object.(*api.AnonymizationRun); ok && state.failRestoreIntent && run.Status.RestoreName != "" {
					state.failRestoreIntent = false
					return errors.New("synthetic interrupted restore intent")
				}
				if run, ok := object.(*api.AnonymizationRun); ok && state.failSnapshotStatus && run.Status.Phase == api.RunPhaseProvisioning {
					state.failSnapshotStatus = false
					return errors.New("synthetic interrupted status write")
				}
				return c.SubResource(subresource).Patch(ctx, object, patch, options...)
			},
		}).Build()
	state.reconciler = &AnonymizationRunReconciler{Client: c, APIReader: c, Scheme: scheme, Recorder: record.NewFakeRecorder(100), RunnerImage: "registry.example/runner:test", now: func() time.Time { return state.clock }}
	return state
}

func runTestObject(kind, name string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetAPIVersion("pxc.percona.com/v1")
	object.SetKind(kind)
	object.SetNamespace(runTestNamespace)
	object.SetName(name)
	return object
}

func (s *runTestState) step(t *testing.T) ctrl.Result {
	t.Helper()
	result, err := s.reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s.run)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(s.run), s.run); err != nil {
		t.Fatal(err)
	}
	return result
}

func (s *runTestState) toProvisioning(t *testing.T) {
	t.Helper()
	for range 3 {
		s.step(t)
	}
	if s.run.Status.Phase != api.RunPhaseProvisioning {
		t.Fatalf("phase=%s", s.run.Status.Phase)
	}
}

func (s *runTestState) toRestoring(t *testing.T) {
	t.Helper()
	s.toProvisioning(t)
	s.step(t)
	cluster := runTestObject(pxc.ClusterGVK.Kind, s.run.Status.TempCluster.Name)
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Object[runTestStatusField] = map[string]any{runTestStateField: runTestReady, runTestPXCField: map[string]any{runTestReady: int64(1)}}
	if err := s.reconciler.Status().Update(t.Context(), cluster); err != nil {
		t.Fatal(err)
	}
	s.step(t)
	if s.run.Status.Phase != api.RunPhaseRestoring {
		t.Fatalf("phase=%s", s.run.Status.Phase)
	}
	s.step(t)
	if s.run.Status.RestoreName == "" {
		t.Fatal("restore identity was not recorded")
	}
}

func TestRunFinalizerAndAtomicImmutableSnapshot(t *testing.T) {
	s := newRunTest(t, nil)
	s.step(t)
	if s.creates != 0 {
		t.Fatal("Pending created a child")
	}
	s.step(t)
	if s.creates != 0 || !controllerutil.ContainsFinalizer(s.run, runFinalizer) {
		t.Fatal("finalizer was not an independent persisted step")
	}
	s.failSnapshotStatus = true
	if _, err := s.reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s.run)}); err == nil {
		t.Fatal("expected interrupted status write")
	}
	original, err := s.reconciler.loadRunSnapshot(t.Context(), s.run)
	if err != nil {
		t.Fatal(err)
	}
	seedBefore := bytes.Clone(original.Payload.Data[runSeedKey])
	policy := &api.AnonymizationPolicy{}
	if err := s.reconciler.Get(t.Context(), client.ObjectKey{Namespace: runTestNamespace, Name: s.run.Spec.PolicyRef.Name}, policy); err != nil {
		t.Fatal(err)
	}
	policy.Spec.Databases[0].Pre = []api.StepRef{{Name: "changed-invalid-step"}}
	if err := s.reconciler.Update(t.Context(), policy); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{}
	if err := s.reconciler.Get(t.Context(), client.ObjectKey{Namespace: runTestNamespace, Name: "policy-constant"}, secret); err != nil {
		t.Fatal(err)
	}
	secret.Data[policyRuntimeValueKey] = []byte("changed-secret")
	if err := s.reconciler.Update(t.Context(), secret); err != nil {
		t.Fatal(err)
	}
	s.step(t)
	snapshot, err := s.reconciler.loadRunSnapshot(t.Context(), s.run)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PolicyHash != original.PolicyHash || !bytes.Equal(seedBefore, snapshot.Payload.Data[runSeedKey]) ||
		string(snapshot.Payload.Data[runConstantKey(policy.Spec.Databases[0].Tables[0].Columns[0].Params.ValueFrom)]) != runTestConstantValue {
		t.Fatal("retry resnapshotted changed policy, seed or Secret payload")
	}
	configMap := &corev1.ConfigMap{}
	if err := s.reconciler.Get(t.Context(), client.ObjectKey{Namespace: runTestNamespace, Name: runChildName(s.run, runPolicyVolume)}, configMap); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(configMap.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"synthetic-root", "SELECT 2;", runTestConstantValue} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("sensitive snapshot payload entered ConfigMap")
		}
	}
	if configMap.Immutable == nil || !*configMap.Immutable || snapshot.Payload.Immutable == nil || !*snapshot.Payload.Immutable {
		t.Fatal("snapshot resources are mutable")
	}
	if len(s.run.Status.TempCluster.Name) > 22 {
		t.Fatal("temporary cluster name exceeds PXC limit")
	}
	s.run.Spec.Output.ObjectStorage.EndpointURL = "http://changed.example"
	if err := s.reconciler.Update(t.Context(), s.run); err != nil {
		t.Fatal(err)
	}
	s.step(t)
	cluster := runTestObject(pxc.ClusterGVK.Kind, s.run.Status.TempCluster.Name)
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster); err != nil {
		t.Fatal(err)
	}
	endpoint, _, err := unstructured.NestedString(cluster.Object, "spec", runTestBackupField, "storages", runOutputStorage, "s3", "endpointUrl")
	if err != nil || endpoint != runTestEndpoint {
		t.Fatalf("cluster did not use frozen endpoint: %q %v", endpoint, err)
	}
	if s.run.Spec.Output.ObjectStorage.EndpointURL != "http://changed.example" {
		t.Fatal("controller overwrote the persisted Run spec")
	}
}

func TestRunRejectsUnsafeInputsBeforeChildren(t *testing.T) {
	for _, scenario := range []string{"missing-ref", "missing-password", "remapped-credentials"} {
		t.Run(scenario, func(t *testing.T) {
			s := newRunTest(t, func(run *api.AnonymizationRun, objects []client.Object) {
				switch scenario {
				case "missing-ref":
					run.Spec.TempCluster.SystemUsersSecretRef = nil
				case "missing-password":
					delete(objects[2].(*corev1.Secret).Data, "root")
				case "remapped-credentials":
					run.Spec.Output.ObjectStorage.Keys = &api.ObjectStorageSecretKeys{AccessKeyID: "custom-access"}
				}
			})
			for range 3 {
				s.step(t)
			}
			condition := meta.FindStatusCondition(s.run.Status.Conditions, runConditionCluster)
			if s.creates != 0 || s.run.Status.Phase != api.RunPhaseFailed || condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != api.ReasonClusterError {
				t.Fatalf("unsafe preflight created children or lacked failure: creates=%d status=%#v", s.creates, s.run.Status)
			}
		})
	}
}

func TestRunRequiresActualRestoreSuccess(t *testing.T) {
	s := newRunTest(t, nil)
	s.toRestoring(t)
	restore := runTestObject(pxc.RestoreGVK.Kind, s.run.Status.RestoreName)
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(restore), restore); err != nil {
		t.Fatal(err)
	}
	destination, _, err := unstructured.NestedString(restore.Object, "spec", "backupSource", "destination")
	if err != nil || destination != runTestDestination {
		t.Fatal("restore is not inline from the frozen source")
	}
	for _, phase := range []pxc.RestoreState{pxc.RestoreStartCluster, pxc.RestoreSucceeded} {
		restore.Object[runTestStatusField] = map[string]any{runTestStateField: string(phase)}
		if err := s.reconciler.Status().Update(t.Context(), restore); err != nil {
			t.Fatal(err)
		}
		result := s.step(t)
		if phase == pxc.RestoreStartCluster {
			if s.run.Status.Phase != api.RunPhaseRestoring || result.RequeueAfter != runPollInterval {
				t.Fatal("Starting Cluster was treated as terminal")
			}
		} else if s.run.Status.Phase != api.RunPhaseAnonymizing || !meta.IsStatusConditionTrue(s.run.Status.Conditions, runConditionRestored) {
			t.Fatal("Succeeded did not advance the Run")
		}
	}
}

func TestRunRestoreVanishedFailsOnNextPoll(t *testing.T) {
	s := newRunTest(t, nil)
	s.toRestoring(t)
	restore := runTestObject(pxc.RestoreGVK.Kind, s.run.Status.RestoreName)
	if err := s.reconciler.Delete(t.Context(), restore); err != nil {
		t.Fatal(err)
	}
	s.step(t)
	condition := meta.FindStatusCondition(s.run.Status.Conditions, runConditionRestored)
	if condition == nil || condition.Reason != api.ReasonRestoreVanished || s.run.Status.Phase != api.RunPhaseFailed {
		t.Fatalf("vanished restore was not terminal: %#v", s.run.Status)
	}
}

func TestRunStageDeadlinesUseInjectedClock(t *testing.T) {
	for _, phase := range []string{"cluster", "restore"} {
		t.Run(phase, func(t *testing.T) {
			s := newRunTest(t, nil)
			conditionType := runConditionCluster
			if phase == "cluster" {
				s.toProvisioning(t)
				s.clock = s.clock.Add(30 * time.Minute)
			} else {
				s.toRestoring(t)
				s.clock = s.clock.Add(3 * time.Hour)
				conditionType = runConditionRestored
			}
			s.step(t)
			condition := meta.FindStatusCondition(s.run.Status.Conditions, conditionType)
			if condition == nil || condition.Reason != api.ReasonTimeout || s.run.Status.Phase != api.RunPhaseFailed {
				t.Fatalf("deadline did not fail the stage: %#v", s.run.Status)
			}
			if phase == "restore" {
				restore := runTestObject(pxc.RestoreGVK.Kind, s.run.Status.RestoreName)
				if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(restore), restore); err != nil {
					t.Fatal("timeout deleted the nonterminal restore")
				}
			}
		})
	}
}

func TestRunSourceForms(t *testing.T) {
	for _, form := range []string{"literal", runTestBackupField, runTestBackupPointer, "pointer"} {
		t.Run(form, func(t *testing.T) {
			s := newRunTest(t, nil)
			var object client.Object
			reason := api.ReasonLiteral
			switch form {
			case runTestBackupField:
				s.run.Spec.Source.Destination = ""
				s.run.Spec.Source.BackupRef = &corev1.LocalObjectReference{Name: "successful-backup"}
				backup := runTestObject(pxc.BackupGVK.Kind, "successful-backup")
				backup.Object["spec"] = map[string]any{"pxcCluster": "source"}
				backup.Object[runTestStatusField] = map[string]any{runTestStateField: "Succeeded", "destination": runTestDestination}
				object = backup
				reason = api.ReasonFromBackup
			case runTestBackupPointer:
				s.run.Spec.Source.Destination = ""
				s.run.Spec.Source.BackupPointerRef = &corev1.LocalObjectReference{Name: "source-pointer"}
				object = &api.BackupPointer{ObjectMeta: metav1.ObjectMeta{Name: "source-pointer", Namespace: runTestNamespace, Generation: 1},
					Status: api.BackupPointerStatus{Current: &api.PublishedBackup{BackupName: "example-full", Destination: runTestDestination, SchemaVersion: 2},
						Conditions: []metav1.Condition{{Type: conditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 1}, {Type: conditionFresh, Status: metav1.ConditionFalse}}}}
				reason = api.ReasonFromBackupPointer
			case "pointer":
				s.run.Spec.Source.Destination = ""
				s.run.Spec.Source.Pointer = &api.PointerSource{HTTP: &api.HTTPPointerSource{URL: "https://source.example/pointer"}}
				s.reconciler.fetchPointer = func(ctx context.Context, _ string, _ api.PointerSource) (*pointer.Document, error) {
					return &pointer.Document{Name: "example-full", Destination: runTestDestination, SchemaVersion: 2}, ctx.Err()
				}
				reason = api.ReasonFromPointer
			}
			if object != nil {
				controllerutil.AddFinalizer(s.run, runFinalizer)
				if err := s.reconciler.Update(t.Context(), s.run); err != nil {
					t.Fatal(err)
				}
				if err := s.reconciler.Create(t.Context(), object); err != nil {
					t.Fatal(err)
				}
			}
			source, actual, _, _, err := s.reconciler.resolveRunSource(t.Context(), s.run)
			if err != nil || source.Destination != runTestDestination || actual != reason {
				t.Fatalf("resolution: %+v %s %v", source, actual, err)
			}
			if form == runTestBackupPointer {
				select {
				case event := <-s.reconciler.Recorder.(*record.FakeRecorder).Events:
					if !strings.Contains(event, api.ReasonStaleBackup) {
						t.Fatal(event)
					}
				default:
					t.Fatal("stale pointer warning missing")
				}
			}
		})
	}
}
