// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/pointer"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
	"github.com/ydixken/pxc-anonymizer/internal/runner/report"
)

const (
	runTestConcurrentOutput = "concurrent-output"
	runTestRecentFailed     = "recent-failed"
	runTestUnknownOutput    = "unknown-output"
)

func runErrorJob(t *testing.T, s *runTestState) *batchv1.Job {
	t.Helper()
	s.toRestoring(t)
	restore := runTestObject(pxc.RestoreGVK.Kind, s.run.Status.RestoreName)
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(restore), restore); err != nil {
		t.Fatal(err)
	}
	restore.Object[runTestStatusField] = map[string]any{runTestStateField: string(pxc.RestoreSucceeded)}
	if err := s.reconciler.Status().Update(t.Context(), restore); err != nil {
		t.Fatal(err)
	}
	s.step(t)
	s.step(t)
	s.step(t)
	job := &batchv1.Job{}
	if err := s.reconciler.Get(t.Context(), client.ObjectKey{Namespace: s.run.Namespace, Name: s.run.Status.Anonymize.JobName}, job); err != nil {
		t.Fatal(err)
	}
	return job
}

func runErrorPod(t *testing.T, s *runTestState, job *batchv1.Job, result report.Report) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-pod", Namespace: job.Namespace,
		Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &yes}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: runContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: int32(result.ExitCode()), Message: string(data)}}}}}}
	if err = s.reconciler.Create(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
}

func TestRunReportsFailClosedAndRetryOnlyExplicitTransient(t *testing.T) {
	for _, class := range []report.Class{report.Policy, report.Schema, report.Permission, report.Unique, report.Transient, runTestMissingReport} {
		t.Run(string(class), func(t *testing.T) {
			s := newRunTest(t, func(run *api.AnonymizationRun, _ []client.Object) { run.Spec.BackoffLimit = 1 })
			job := runErrorJob(t, s)
			if class == runTestMissingReport {
				job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
				if err := s.reconciler.Status().Update(t.Context(), job); err != nil {
					t.Fatal(err)
				}
			} else {
				runErrorPod(t, s, job, report.Report{Result: report.ResultError, Class: class, PolicyHash: s.run.Status.PolicyHash, Message: "synthetic-private-message"})
			}
			s.step(t)
			if class != report.Transient {
				if s.run.Status.Phase != api.RunPhaseFailed || s.run.Status.Anonymize.Attempts != 1 {
					t.Fatalf("unsafe retry: %#v", s.run.Status)
				}
				encoded, err := json.Marshal(s.run.Status)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "synthetic-private-message") {
					t.Fatal("termination body leaked into status")
				}
				return
			}
			if s.run.Status.Anonymize.CompletedAt == nil || s.run.Status.Anonymize.LastResult.Class != string(report.Transient) {
				t.Fatal("retry decision was not durable")
			}
			if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(job), job); err != nil {
				t.Fatal("old Job deleted before durable decision", err)
			}
			s.step(t)
			s.step(t)
			s.step(t)
			s.step(t)
			if s.run.Status.Anonymize.Attempts != 2 || s.run.Status.Anonymize.JobName == job.Name {
				t.Fatalf("retry identity not advanced: %#v", s.run.Status.Anonymize)
			}
			if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(job), job); !apierrors.IsNotFound(err) {
				t.Fatal("old Job was recreated", err)
			}
			second := &batchv1.Job{}
			if err := s.reconciler.Get(t.Context(), client.ObjectKey{Namespace: s.run.Namespace, Name: s.run.Status.Anonymize.JobName}, second); err != nil {
				t.Fatal(err)
			}
			runErrorPod(t, s, second, report.Report{Result: report.ResultError, Class: report.Transient, PolicyHash: s.run.Status.PolicyHash})
			s.step(t)
			if reason := meta.FindStatusCondition(s.run.Status.Conditions, runConditionFailed); reason == nil || reason.Reason != api.ReasonRetryBudgetExhausted {
				t.Fatalf("retry budget ignored: %#v", reason)
			}
		})
	}
}

func runErrorSucceededOutput(t *testing.T, s *runTestState) {
	t.Helper()
	job := runErrorJob(t, s)
	runErrorPod(t, s, job, report.Report{Result: report.ResultOK, PolicyHash: s.run.Status.PolicyHash, TablesDone: 1, TablesTotal: 1, RowsDone: 3, StepsDone: 2})
	s.step(t)
	s.step(t)
	s.step(t)
	backup := runTestObject(pxc.BackupGVK.Kind, s.run.Status.Output.BackupName)
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(backup), backup); err != nil {
		t.Fatal(err)
	}
	if len(backup.GetOwnerReferences()) != 0 {
		t.Fatal("output backup is Run-owned")
	}
	backup.Object[runTestStatusField] = map[string]any{runTestStateField: string(pxc.BackupSucceeded), "destination": "s3://anonymized-backups/product-full", "completed": s.clock.Format(time.RFC3339),
		"s3": map[string]any{"bucket": "anonymized-backups", "endpointUrl": runTestEndpoint, "region": "auto"}}
	if err := s.reconciler.Status().Update(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	s.step(t)
	if s.run.Status.Phase != api.RunPhasePublishing {
		t.Fatalf("phase=%s", s.run.Status.Phase)
	}
}

func TestRunPublishesBeforeScopedRetention(t *testing.T) {
	s := newRunTest(t, func(run *api.AnonymizationRun, _ []client.Object) {
		run.Spec.Output.Pointer = &api.PointerTarget{ObjectStorage: run.Spec.Output.ObjectStorage, Key: "latest.json"}
		run.Spec.Output.Retention.KeepSucceeded = 1
	})
	runErrorSucceededOutput(t, s)
	prior := &api.AnonymizationRun{ObjectMeta: metav1.ObjectMeta{Name: "prior-run", Namespace: s.run.Namespace, UID: types.UID("prior-uid")},
		Status: api.AnonymizationRunStatus{Phase: api.RunPhaseCompleted, Conditions: []metav1.Condition{{Type: runConditionCleaned, Status: metav1.ConditionTrue}}}}
	if err := s.reconciler.Create(t.Context(), prior); err != nil {
		t.Fatal(err)
	}
	active := prior.DeepCopy()
	active.Name = "concurrent-run"
	active.UID = "concurrent-uid"
	active.ResourceVersion = ""
	active.Status.Phase = api.RunPhasePublishing
	if err := s.reconciler.Create(t.Context(), active); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old-output", runTestForeignGroup, runTestRecentFailed, runTestConcurrentOutput, runTestUnknownOutput} {
		backup := runTestObject(pxc.BackupGVK.Kind, name)
		backup.SetUID(types.UID(name))
		backup.SetCreationTimestamp(metav1.NewTime(s.clock.Add(-48 * time.Hour)))
		backup.SetLabels(map[string]string{pxc.LabelOutputGroup: runOutputGroup(s.run), pxc.LabelOutput: runOutputStorage})
		backup.SetAnnotations(map[string]string{"pxc-anonymizer.io/run-uid": string(prior.UID)})
		if name == runTestConcurrentOutput {
			backup.SetAnnotations(map[string]string{"pxc-anonymizer.io/run-uid": string(active.UID)})
		}
		if name == runTestUnknownOutput {
			backup.SetAnnotations(nil)
		}
		if name == runTestForeignGroup {
			backup.SetLabels(map[string]string{pxc.LabelOutputGroup: "another", pxc.LabelOutput: runOutputStorage})
		}
		backup.Object["spec"] = map[string]any{"pxcCluster": "past", "storageName": runOutputStorage}
		backup.Object[runTestStatusField] = map[string]any{runTestStateField: string(pxc.BackupSucceeded), "completed": s.clock.Add(-24 * time.Hour).Format(time.RFC3339)}
		if name == runTestRecentFailed {
			backup.SetCreationTimestamp(metav1.NewTime(s.clock))
			backup.Object[runTestStatusField] = map[string]any{runTestStateField: string(pxc.BackupFailed)}
		}
		if err := s.reconciler.Create(t.Context(), backup); err != nil {
			t.Fatal(err)
		}
	}
	uploads := 0
	s.reconciler.putPointer = func(ctx context.Context, _ string, _ api.PointerTarget, document *pointer.Document) (string, error) {
		uploads++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("publication context is unbounded")
		}
		if document.SchemaVersion != 2 || document.Anonymized == nil || document.Anonymized.PolicyHash != s.run.Status.PolicyHash || document.S3 == nil {
			t.Fatalf("invalid pointer: %#v", document)
		}
		if uploads == 1 {
			return "", errors.New("synthetic upload failure")
		}
		return "safe-etag", nil
	}
	s.step(t)
	if s.run.Status.Phase != api.RunPhasePublishing {
		t.Fatal("failed publication advanced to pruning")
	}
	old := runTestObject(pxc.BackupGVK.Kind, "old-output")
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(old), old); err != nil {
		t.Fatal("backup pruned before publication", err)
	}
	s.step(t)
	if s.run.Status.Output.PublishedAt == nil || s.run.Status.Output.ETag != "safe-etag" {
		t.Fatal("publication status not durable")
	}
	s.step(t)
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(old), old); !apierrors.IsNotFound(err) {
		t.Fatal("old product not pruned", err)
	}
	for _, name := range []string{runTestForeignGroup, runTestRecentFailed, runTestConcurrentOutput, runTestUnknownOutput, s.run.Status.Output.BackupName} {
		object := runTestObject(pxc.BackupGVK.Kind, name)
		if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatal("retention deleted protected product", name, err)
		}
	}
}

func TestRunDryRunSkipsOutputAndHoldsBeforeCleanup(t *testing.T) {
	s := newRunTest(t, func(run *api.AnonymizationRun, _ []client.Object) {
		run.Spec.Runner.DryRun = true
		run.Spec.Cleanup.HoldTempClusterFor = metav1.Duration{Duration: time.Minute}
	})
	job := runErrorJob(t, s)
	runErrorPod(t, s, job, report.Report{Result: report.ResultOK, PolicyHash: s.run.Status.PolicyHash})
	s.step(t)
	if s.run.Status.Phase != api.RunPhaseCleaningUp || s.run.Status.Output != nil {
		t.Fatal("dry-run created output")
	}
	if result := s.step(t); result.RequeueAfter != time.Minute {
		t.Fatalf("hold=%v", result.RequeueAfter)
	}
	cluster := runTestObject(pxc.ClusterGVK.Kind, s.run.Status.TempCluster.Name)
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster); err != nil {
		t.Fatal(err)
	}
	if !cluster.GetDeletionTimestamp().IsZero() {
		t.Fatal("hold deleted cluster early")
	}
	s.clock = s.clock.Add(time.Minute)
	s.step(t)
	if meta.IsStatusConditionTrue(s.run.Status.Conditions, runConditionComplete) {
		t.Fatal("live cluster reported Complete")
	}
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster); err != nil {
		t.Fatal(err)
	}
	cluster.SetFinalizers(nil)
	if err := s.reconciler.Update(t.Context(), cluster); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if s.run.Status.Phase == api.RunPhaseCompleted {
			break
		}
		s.step(t)
	}
	if !meta.IsStatusConditionTrue(s.run.Status.Conditions, runConditionComplete) || !meta.IsStatusConditionTrue(s.run.Status.Conditions, runConditionCleaned) {
		t.Fatalf("cleanup did not complete: %#v", s.run.Status)
	}
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(job), job); err != nil {
		t.Fatal("successful Job bypassed TTL retention", err)
	}
}

func TestRunDeletionPredicateWakesAbsorbingRun(t *testing.T) {
	run := &api.AnonymizationRun{ObjectMeta: metav1.ObjectMeta{Generation: 1}, Status: api.AnonymizationRunStatus{Phase: api.RunPhaseCompleted}}
	deleted := run.DeepCopy()
	now := metav1.Now()
	deleted.DeletionTimestamp = &now
	if !runChangePredicate().Update(event.UpdateEvent{ObjectOld: run, ObjectNew: deleted}) {
		t.Fatal("deletion timestamp did not wake completed Run")
	}
	statusOnly := run.DeepCopy()
	statusOnly.Status.PolicyHash = "changed"
	if runChangePredicate().Update(event.UpdateEvent{ObjectOld: run, ObjectNew: statusOnly}) {
		t.Fatal("status-only update caused a reconcile loop")
	}
}

func TestRunRejectsShortSeedBeforeChildren(t *testing.T) {
	s := newRunTest(t, func(_ *api.AnonymizationRun, objects []client.Object) {
		for _, object := range objects {
			if secret, ok := object.(*corev1.Secret); ok && secret.Name == "policy-seed" {
				secret.Data["seed"] = []byte("short")
			}
		}
	})
	for range 3 {
		s.step(t)
	}
	if s.creates != 0 || s.run.Status.Phase != api.RunPhaseFailed {
		t.Fatal("short fixed seed reached child creation")
	}
}

func TestRunVanishedJobNeverReplays(t *testing.T) {
	s := newRunTest(t, nil)
	job := runErrorJob(t, s)
	if err := s.reconciler.Delete(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	created := s.creates
	s.step(t)
	if s.creates != created || s.run.Status.Phase != api.RunPhaseFailed {
		t.Fatal("vanished Job was recreated")
	}
}

func TestRunRestoreIntentFailureCreatesNothing(t *testing.T) {
	s := newRunTest(t, nil)
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
	s.failRestoreIntent = true
	created := s.creates
	if _, err := s.reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s.run)}); err == nil {
		t.Fatal("expected failed status patch")
	}
	if s.creates != created {
		t.Fatal("restore created despite lost intent patch")
	}
	s.step(t)
	if s.run.Status.RestoreName == "" || s.creates != created+1 {
		t.Fatal("safe retry did not persist intent before exactly one Create")
	}
}

func TestRunStepOnlyLogEmitsBoundedOutcome(t *testing.T) {
	s := newRunTest(t, nil)
	s.run.Status.Anonymize = &api.AnonymizeStatus{}
	s.reconciler.podLogs = func(context.Context, string, string) ([]byte, error) {
		return []byte(`{"msg":"ok","step":"pre","db":"example","name":"prepare"}`), nil
	}
	s.reconciler.sampleRunProgress(t.Context(), s.run, &corev1.Pod{})
	recorder, ok := s.reconciler.Recorder.(*events.FakeRecorder)
	if !ok {
		t.Fatal("expected native fake recorder")
	}
	select {
	case emitted := <-recorder.Events:
		if !strings.Contains(emitted, "StepOutcome") || strings.Contains(emitted, "prepare") {
			t.Fatal("step-only outcome missing or raw log fields leaked", emitted)
		}
	default:
		t.Fatal("step-only log produced no Event")
	}
}
