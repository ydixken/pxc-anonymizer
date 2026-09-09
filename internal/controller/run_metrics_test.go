// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/metrics"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
)

const runMetricsPrefix = "pxc_anonymizer_run_"
const runMetricsClusterName = "metrics-temp"
const runMetricsClusterUID = "metrics-cluster-uid"
const runMetricsPresence = "temp_cluster_present"
const runMetricsPhase = "phase"
const runMetricsDeleting = "deleting"
const runMetricsAbsent = "absent"
const runMetricsNoIdentity = "no-identity"
const runMetricsNoReader = "no-reader"
const runMetricsGetError = "get-error"
const runMetricsFinalizerRemoved = "finalizer-removed"
const runMetricsForeign = "foreign"

func TestRunMetricsRecordOnlyPersistedStatus(t *testing.T) {
	s := newRunTest(t, nil)
	t.Cleanup(func() { metrics.ForgetRun(s.run.Namespace, s.run.Name) })
	s.step(t)
	assertRunMetric(t, s.run, runMetricsPhase, 1, runMetricsPhase, string(api.RunPhasePending))
	s.step(t)
	s.failSnapshotStatus = true
	if _, err := s.reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s.run)}); err == nil {
		t.Fatal("expected status write failure")
	}
	assertRunMetric(t, s.run, runMetricsPhase, 1, runMetricsPhase, string(api.RunPhasePending))
	if value, ok := runMetricValue(t, s.run, runMetricsPhase, runMetricsPhase, string(api.RunPhaseProvisioning)); ok && value != 0 {
		t.Fatal("failed status patch published the prospective phase")
	}
	s.step(t)
	assertRunMetric(t, s.run, runMetricsPhase, 1, runMetricsPhase, string(api.RunPhaseProvisioning))
	assertRunMetric(t, s.run, runMetricsPresence, 0)
	s.step(t)
	assertRunMetric(t, s.run, runMetricsPresence, 1)

	metrics.ForgetRun(s.run.Namespace, s.run.Name)
	if _, err := s.reconciler.patchRun(t.Context(), s.run, s.run.DeepCopy(), 0); err != nil {
		t.Fatal(err)
	}
	assertRunMetric(t, s.run, runMetricsPhase, 1, runMetricsPhase, string(api.RunPhaseProvisioning))
	base := s.run.DeepCopy()
	s.run.Status.Anonymize = &api.AnonymizeStatus{Attempts: 1, Progress: &api.AnonymizeProgress{}}
	if _, err := s.reconciler.patchRun(t.Context(), s.run, base, 0); err != nil {
		t.Fatal(err)
	}
	assertRunMetric(t, s.run, "tables_expected", 0)
	assertRunMetric(t, s.run, "rows_done", 0)
	if _, ok := runMetricValue(t, s.run, "rows_expected"); ok {
		t.Fatal("unreported expected rows became a zero sample")
	}
}

func TestRunMetricsPresenceRequiresOwnedObservation(t *testing.T) {
	for _, scenario := range []string{"owned", runMetricsDeleting, runMetricsAbsent, "read-error", runMetricsForeign, "replaced", "unrecorded", runMetricsNoIdentity, runMetricsNoReader} {
		t.Run(scenario, func(t *testing.T) {
			s := newRunTest(t, nil)
			s.run.Name = "metrics-" + scenario
			s.run.Status.TempCluster = &api.TempClusterStatus{Name: runMetricsClusterName, UID: runMetricsClusterUID}
			t.Cleanup(func() { metrics.ForgetRun(s.run.Namespace, s.run.Name) })
			cluster := runTestObject(pxc.ClusterGVK.Kind, runMetricsClusterName)
			cluster.SetUID(runMetricsClusterUID)
			cluster.SetOwnerReferences([]metav1.OwnerReference{runOwner(s.run)})
			switch scenario {
			case runMetricsDeleting:
				stamp := metav1.NewTime(s.clock)
				cluster.SetDeletionTimestamp(&stamp)
				cluster.SetFinalizers([]string{"example.com/hold"})
			case runMetricsForeign:
				owner := runOwner(s.run)
				owner.UID = "other-run"
				cluster.SetOwnerReferences([]metav1.OwnerReference{owner})
			case "replaced":
				cluster.SetUID("replacement-cluster")
			case "unrecorded":
				s.run.Status.TempCluster.UID = ""
			case runMetricsNoIdentity:
				s.run.Status.TempCluster = nil
			}
			objects := []client.Object{cluster}
			if scenario == runMetricsAbsent {
				objects = nil
			}
			reads := 0
			s.reconciler.APIReader = fake.NewClientBuilder().WithScheme(s.reconciler.Scheme).WithObjects(objects...).
				WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
					reads++
					if key.Namespace != s.run.Namespace || key.Name != runMetricsClusterName || object.GetObjectKind().GroupVersionKind() != pxc.ClusterGVK {
						t.Fatal("metrics read beyond the exact named PXC cluster")
					}
					if scenario == "read-error" {
						return errors.New("synthetic PXC read failure")
					}
					return c.Get(ctx, key, object, options...)
				}}).Build()
			if scenario == runMetricsNoReader {
				s.reconciler.APIReader = nil
			}
			present := true
			metrics.RecordRun(s.run, metrics.RunObservation{TempClusterPresent: &present})
			s.reconciler.recordRunMetrics(t.Context(), s.run)
			value, known := runMetricValue(t, s.run, runMetricsPresence)
			switch scenario {
			case "owned", runMetricsDeleting:
				if !known || value != 1 {
					t.Fatal("matching owned cluster was not present")
				}
			case runMetricsAbsent:
				if !known || value != 0 {
					t.Fatal("proven absence was not zero")
				}
			default:
				if known {
					t.Fatal("unknown presence retained or invented a sample")
				}
			}
			expectedReads := 1
			if scenario == runMetricsNoIdentity || scenario == runMetricsNoReader {
				expectedReads = 0
			}
			if reads != expectedReads {
				t.Fatalf("named reads=%d, want %d", reads, expectedReads)
			}
		})
	}
}

func TestRunMetricsForgetAfterDeletion(t *testing.T) {
	for _, scenario := range []string{"not-found", "finalizer-gone", runMetricsGetError, runMetricsFinalizerRemoved} {
		t.Run(scenario, func(t *testing.T) {
			s := newRunTest(t, func(run *api.AnonymizationRun, _ []client.Object) {
				if scenario == "finalizer-gone" || scenario == runMetricsFinalizerRemoved {
					stamp := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
					run.DeletionTimestamp = &stamp
					run.Finalizers = []string{"example.com/other-finalizer"}
					if scenario == runMetricsFinalizerRemoved {
						run.Finalizers = append(run.Finalizers, runFinalizer)
					}
				}
			})
			t.Cleanup(func() { metrics.ForgetRun(s.run.Namespace, s.run.Name) })
			if scenario == "not-found" {
				if err := s.reconciler.Delete(t.Context(), s.run); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == runMetricsGetError {
				s.reconciler.Client = fake.NewClientBuilder().WithScheme(s.reconciler.Scheme).WithObjects(s.run).WithInterceptorFuncs(interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						return errors.New("synthetic Run read failure")
					},
				}).Build()
			}
			run := s.run.DeepCopy()
			stamp := metav1.NewTime(s.clock)
			run.Status.Phase, run.Status.StartedAt, run.Status.CompletedAt = api.RunPhaseCompleted, &stamp, &stamp
			run.Status.Conditions = []metav1.Condition{{Type: runConditionComplete, Status: metav1.ConditionTrue, LastTransitionTime: stamp}}
			run.Status.Anonymize = &api.AnonymizeStatus{Attempts: 1, Progress: &api.AnonymizeProgress{TablesDone: 1, TablesTotal: 1, RowsDone: 5}}
			if scenario == runMetricsFinalizerRemoved {
				run.Status.ObservedGeneration = run.Generation
				run.Status.Conditions = append(run.Status.Conditions, metav1.Condition{Type: runConditionCleaned, Status: metav1.ConditionTrue,
					ObservedGeneration: run.Generation, LastTransitionTime: stamp, Reason: api.ReasonTempClusterDeleted,
					Message: "the temporary cluster and owned credentials are absent"})
				if err := s.reconciler.Status().Update(t.Context(), run); err != nil {
					t.Fatal(err)
				}
			}
			present, expectedRows := true, int64(5)
			metrics.RecordRun(run, metrics.RunObservation{TempClusterPresent: &present, RowsTotal: &expectedRows,
				StepDurations: map[metrics.RunStep]time.Duration{metrics.RunStepCleanup: time.Second}})
			if families := len(observedRunMetrics(t, run)); families != 11 {
				t.Fatalf("populated families=%d, want all 11 before deletion", families)
			}
			_, err := s.reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s.run)})
			if scenario == runMetricsGetError {
				if err == nil || len(observedRunMetrics(t, run)) != 11 {
					t.Fatal("failed Run read forgot existing metrics")
				}
			} else if err != nil || len(observedRunMetrics(t, run)) != 0 {
				t.Fatalf("deleted Run retained metrics: %v", err)
			}
			if scenario == runMetricsFinalizerRemoved {
				current := &api.AnonymizationRun{}
				if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(run), current); err != nil {
					t.Fatal("the other finalizer did not retain the Run", err)
				}
				if controllerutil.ContainsFinalizer(current, runFinalizer) || !controllerutil.ContainsFinalizer(current, "example.com/other-finalizer") {
					t.Fatalf("unexpected remaining finalizers: %v", current.Finalizers)
				}
			}
		})
	}
}

func TestRunMetricsRemainDuringFinalizerCleanup(t *testing.T) {
	s := newRunTest(t, nil)
	t.Cleanup(func() { metrics.ForgetRun(s.run.Namespace, s.run.Name) })
	s.toProvisioning(t)
	s.step(t)
	if err := s.reconciler.Delete(t.Context(), s.run); err != nil {
		t.Fatal(err)
	}
	s.step(t)
	assertRunMetric(t, s.run, runMetricsPresence, 1)
	cluster := runTestObject(pxc.ClusterGVK.Kind, s.run.Status.TempCluster.Name)
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.GetDeletionTimestamp().IsZero() {
		t.Fatal("test did not reach pending PXC finalization")
	}
	cluster.SetFinalizers(nil)
	if err := s.reconciler.Update(t.Context(), cluster); err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s.run)}
	for range 8 {
		if _, err := s.reconciler.Reconcile(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		current := &api.AnonymizationRun{}
		if err := s.reconciler.Get(t.Context(), request.NamespacedName, current); apierrors.IsNotFound(err) {
			if _, err := s.reconciler.Reconcile(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if len(observedRunMetrics(t, s.run)) != 0 {
				t.Fatal("final Run deletion did not forget metrics")
			}
			return
		} else if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("finalizer cleanup did not finish")
}

func TestRunMetricDurationsRequireDurableEndpoints(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	at := func(seconds int) *metav1.Time {
		value := metav1.NewTime(start.Add(time.Duration(seconds) * time.Second))
		return &value
	}
	run := &api.AnonymizationRun{Status: api.AnonymizationRunStatus{
		TempCluster: &api.TempClusterStatus{ReadyAt: at(0), DeletedAt: at(60)},
		Anonymize:   &api.AnonymizeStatus{StartedAt: at(12), CompletedAt: at(20)},
		Output:      &api.PublishedBackup{CompletedAt: at(30), PublishedAt: at(45)},
		Conditions: []metav1.Condition{
			{Type: runConditionRestored, Status: metav1.ConditionTrue, LastTransitionTime: *at(10)},
			{Type: runConditionBackedUp, Status: metav1.ConditionTrue, LastTransitionTime: *at(32)},
			{Type: runConditionCleaned, Status: metav1.ConditionTrue, LastTransitionTime: *at(60)},
		},
	}}
	expected := map[metrics.RunStep]time.Duration{metrics.RunStepRestore: 10 * time.Second, metrics.RunStepAnonymize: 8 * time.Second,
		metrics.RunStepBackup: 10 * time.Second, metrics.RunStepPublish: 13 * time.Second}
	if actual := runMetricDurations(run); !maps.Equal(actual, expected) {
		t.Fatalf("durations=%v, want %v without invented cleanup duration", actual, expected)
	}
	run.Status.TempCluster.ReadyAt = at(11)
	run.Status.Anonymize.StartedAt = &metav1.Time{}
	run.Status.Anonymize.CompletedAt = nil
	run.Status.Output.PublishedAt = nil
	if actual := runMetricDurations(run); len(actual) != 0 {
		t.Fatalf("invalid, zero or missing endpoints produced durations: %v", actual)
	}
	if actual := runMetricDurations(&api.AnonymizationRun{}); len(actual) != 0 {
		t.Fatal("empty status produced durations")
	}
}

func observedRunMetrics(t *testing.T, run *api.AnonymizationRun) map[string][]*dto.Metric {
	t.Helper()
	families, err := controllermetrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	observed := map[string][]*dto.Metric{}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["namespace"] == run.Namespace && labels["name"] == run.Name {
				observed[family.GetName()] = append(observed[family.GetName()], metric)
			}
		}
	}
	return observed
}

func runMetricValue(t *testing.T, run *api.AnonymizationRun, suffix string, extra ...string) (float64, bool) {
	t.Helper()
	for _, metric := range observedRunMetrics(t, run)[runMetricsPrefix+suffix] {
		if len(extra) == 0 {
			return metric.GetGauge().GetValue(), true
		}
		for _, label := range metric.GetLabel() {
			if label.GetName() == extra[0] && label.GetValue() == extra[1] {
				return metric.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func assertRunMetric(t *testing.T, run *api.AnonymizationRun, suffix string, expected float64, extra ...string) {
	t.Helper()
	if actual, ok := runMetricValue(t, run, suffix, extra...); !ok || actual != expected {
		t.Fatalf("metric %s%v=%v present=%t, want %v", suffix, extra, actual, ok, expected)
	}
}
