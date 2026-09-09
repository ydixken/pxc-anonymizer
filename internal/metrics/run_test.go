package metrics

import (
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil/promlint"
	dto "github.com/prometheus/client_model/go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

const (
	testRunName      = "example-run"
	testRunCondition = "Complete"
	testRunPhase     = "phase"
	testRunStep      = "step"
	testRunStatus    = "status"
	testRunType      = "type"
)

func newMetricRun(t *testing.T) (*api.AnonymizationRun, RunObservation) {
	t.Helper()
	started, completed := metav1.NewTime(time.Unix(100, 0)), metav1.NewTime(time.Unix(300, 0))
	run := &api.AnonymizationRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testRunName},
		Status: api.AnonymizationRunStatus{
			Phase: api.RunPhaseCompleted, StartedAt: &started, CompletedAt: &completed,
			Anonymize: &api.AnonymizeStatus{
				Attempts: 2, Progress: &api.AnonymizeProgress{TablesDone: 3, TablesTotal: 4, RowsDone: 9},
			},
			Conditions: []metav1.Condition{{Type: testRunCondition, Status: metav1.ConditionTrue, LastTransitionTime: completed}},
		},
	}
	present, rows := true, int64(12)
	observation := RunObservation{
		TempClusterPresent: &present, RowsTotal: &rows,
		StepDurations: map[RunStep]time.Duration{
			RunStepRestore: 20 * time.Second, RunStepAnonymize: 30 * time.Second,
			RunStepBackup: 40 * time.Second, RunStepPublish: time.Second, RunStepCleanup: 2 * time.Second,
		},
	}
	ForgetRun(run.Namespace, run.Name)
	t.Cleanup(func() { ForgetRun(run.Namespace, run.Name) })
	return run, observation
}

func TestRunMetricsCatalogueAndPromlint(t *testing.T) {
	run, observation := newMetricRun(t)
	RecordRun(run, observation)
	families, err := controllermetrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"tables_done": 3, "tables_expected": 4, "rows_done": 9, "rows_expected": 12,
		"start_time_seconds": 100, "completion_time_seconds": 300, "attempts": 2, "temp_cluster_present": 1,
	}
	observed := make([]*dto.MetricFamily, 0, 11)
	for _, family := range families {
		name, isRun := strings.CutPrefix(family.GetName(), runMetricPrefix)
		if !isRun {
			continue
		}
		observed = append(observed, family)
		if family.GetType() != dto.MetricType_GAUGE {
			t.Fatalf("%s is not a gauge", family.GetName())
		}
		if value, known := want[name]; known {
			if len(family.Metric) != 1 {
				t.Fatalf("%s has %d samples, want one", name, len(family.Metric))
			}
			assertRunSample(t, family.Metric[0], run, value, nil)
			delete(want, name)
			continue
		}
		switch name {
		case testRunPhase:
			if len(family.Metric) != 1 {
				t.Fatal("phase must have exactly one active label")
			}
			assertRunSample(t, family.Metric[0], run, 1, map[string]string{testRunPhase: string(api.RunPhaseCompleted)})
		case "step_duration_seconds":
			if len(family.Metric) != 5 {
				t.Fatalf("step samples = %d, want all five", len(family.Metric))
			}
			remaining := maps.Clone(observation.StepDurations)
			for _, sample := range family.Metric {
				step := RunStep(pointerMetricLabels(sample)[testRunStep])
				duration, known := remaining[step]
				if !known {
					t.Fatalf("unexpected or repeated step %q", step)
				}
				assertRunSample(t, sample, run, duration.Seconds(), map[string]string{testRunStep: string(step)})
				delete(remaining, step)
			}
		case "condition_transition_timestamp_seconds":
			if len(family.Metric) != 1 {
				t.Fatal("known condition must create exactly one sample")
			}
			assertRunSample(t, family.Metric[0], run, 300, map[string]string{testRunType: testRunCondition, testRunStatus: "True"})
		default:
			t.Fatalf("unexpected Run family %s", family.GetName())
		}
	}
	if len(observed) != 11 || len(want) != 0 {
		t.Fatalf("observed %d families; required values missing: %v", len(observed), want)
	}
	problems, err := promlint.NewWithMetricFamilies(observed).Lint()
	if err != nil || len(problems) != 0 {
		t.Fatalf("promlint: problems=%v error=%v", problems, err)
	}
}

func TestRunMetricsUnknownAndObservedZero(t *testing.T) {
	run, observation := newMetricRun(t)
	RecordRun(run, observation)
	run.Status = api.AnonymizationRunStatus{StartedAt: &metav1.Time{}, CompletedAt: &metav1.Time{}}
	RecordRun(run, RunObservation{})
	for _, metric := range runGauges {
		if len(collectPointerMetrics(t, metric)) != 0 {
			t.Fatal("unknown observation retained a previous series")
		}
	}
	zero, absent := int64(0), false
	run.Status.Anonymize = &api.AnonymizeStatus{Progress: &api.AnonymizeProgress{}}
	RecordRun(run, RunObservation{TempClusterPresent: &absent, RowsTotal: &zero})
	for _, metric := range []*prometheus.GaugeVec{
		runTablesDone, runTablesExpected, runRowsDone, runRowsExpected, runAttempts, runTempClusterPresent,
	} {
		samples := collectPointerMetrics(t, metric)
		if len(samples) != 1 {
			t.Fatal("observed zero did not produce exactly one sample")
		}
		assertRunSample(t, samples[0], run, 0, nil)
	}
	RecordRun(run, RunObservation{})
	if len(collectPointerMetrics(t, runRowsExpected)) != 0 || len(collectPointerMetrics(t, runTempClusterPresent)) != 0 {
		t.Fatal("unknown row expectation or cluster presence retained a zero")
	}
}

func TestRunMetricsLabelReplacement(t *testing.T) {
	run, observation := newMetricRun(t)
	RecordRun(run, observation)
	run.Status.Phase = api.RunPhaseFailed
	run.Status.Conditions = []metav1.Condition{
		{Type: testRunCondition, Status: metav1.ConditionFalse, LastTransitionTime: metav1.NewTime(time.Unix(400, 0))},
		{Type: testConditionReady, Status: metav1.ConditionUnknown},
	}
	observation.StepDurations = map[RunStep]time.Duration{RunStepPublish: 0, RunStepRestore: -time.Second, "unknown": time.Second}
	RecordRun(run, observation)
	checks := []struct {
		metric *prometheus.GaugeVec
		value  float64
		labels map[string]string
	}{
		{runPhase, 1, map[string]string{testRunPhase: string(api.RunPhaseFailed)}},
		{runConditionTransition, 400, map[string]string{testRunType: testRunCondition, testRunStatus: "False"}},
		{runStepDuration, 0, map[string]string{testRunStep: string(RunStepPublish)}},
	}
	for _, check := range checks {
		samples := collectPointerMetrics(t, check.metric)
		if len(samples) != 1 {
			t.Fatalf("replacement left %d samples, want one", len(samples))
		}
		assertRunSample(t, samples[0], run, check.value, check.labels)
	}
}

func TestRunMetricsForgetAllFamiliesAndIsolation(t *testing.T) {
	run, observation := newMetricRun(t)
	otherNamespace, otherName := run.DeepCopy(), run.DeepCopy()
	otherNamespace.Namespace = "other-run-namespace"
	otherName.Name = "other-run"
	for _, item := range []*api.AnonymizationRun{run, otherNamespace, otherName} {
		t.Cleanup(func() { ForgetRun(item.Namespace, item.Name) })
		RecordRun(item, observation)
	}
	for _, metric := range runGauges {
		if len(collectPointerMetrics(t, metric)) < 3 {
			t.Fatal("Forget precondition did not positively populate every family")
		}
	}
	ForgetRun(run.Namespace, run.Name)
	ForgetRun(run.Namespace, run.Name)
	for _, metric := range runGauges {
		samples := collectPointerMetrics(t, metric)
		perRun := 1
		if metric == runStepDuration {
			perRun = 5
		}
		if len(samples) != 2*perRun {
			t.Fatalf("Forget left %d samples, want %d", len(samples), 2*perRun)
		}
		for _, sample := range samples {
			labels := pointerMetricLabels(sample)
			if labels[namespaceLabel] == run.Namespace && labels[nameLabel] == run.Name {
				t.Fatal("Forget retained the removed Run")
			}
		}
	}
	ForgetRun(otherNamespace.Namespace, otherNamespace.Name)
	ForgetRun(otherName.Namespace, otherName.Name)
	for _, metric := range runGauges {
		if len(collectPointerMetrics(t, metric)) != 0 {
			t.Fatal("Forget retained series after every Run disappeared")
		}
	}
}

func assertRunSample(
	t *testing.T, sample *dto.Metric, run *api.AnonymizationRun, value float64, extra map[string]string,
) {
	t.Helper()
	labels := map[string]string{namespaceLabel: run.Namespace, nameLabel: run.Name}
	maps.Copy(labels, extra)
	if sample.Gauge == nil || sample.GetGauge().GetValue() != value || !maps.Equal(pointerMetricLabels(sample), labels) {
		t.Fatalf("unexpected Run metric sample: %v, want value %v and labels %v", sample, value, labels)
	}
}
