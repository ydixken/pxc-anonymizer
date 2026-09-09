package metrics

import (
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

const (
	testNamespace      = "metrics-test"
	testPointerName    = "example-pointer"
	testConditionReady = "Ready"
	testResultSuccess  = "success"
	testResultError    = "error"
	testNamespaceLabel = "namespace"
	testNameLabel      = "name"
	testStateLabel     = "state"
)

func newMetricPointer(t *testing.T) *api.BackupPointer {
	t.Helper()
	bp := &api.BackupPointer{ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testPointerName}}
	ForgetBackupPointer(bp.Namespace, bp.Name)
	t.Cleanup(func() { ForgetBackupPointer(bp.Namespace, bp.Name) })
	return bp
}

func TestBackupPointerUnknownTimestampsStayAbsent(t *testing.T) {
	bp := newMetricPointer(t)
	for _, current := range []*api.PublishedBackup{nil, {}, {PublishedAt: &metav1.Time{}, CompletedAt: &metav1.Time{}}} {
		bp.Status.Current = current
		RecordBackupPointer(bp)
		for _, metric := range pointerGauges {
			if count := len(collectPointerMetrics(t, metric)); count != 0 {
				t.Fatalf("unobserved status created %d metric series", count)
			}
		}
	}
	if count := len(collectPointerMetrics(t, pointerPublishAttempts)); count != 0 {
		t.Fatalf("recording status created %d upload series", count)
	}
}

func TestBackupPointerTimestampsRetainSourceAge(t *testing.T) {
	bp := newMetricPointer(t)
	completed := metav1.NewTime(time.Unix(100, 0))
	published := metav1.NewTime(time.Unix(200, 0))
	bp.Status.Current = &api.PublishedBackup{CompletedAt: &completed, PublishedAt: &published}
	RecordBackupPointer(bp)
	if got := pointerMetricValue(t, pointerSourceBackupTime.WithLabelValues(bp.Namespace, bp.Name)); got != 100 {
		t.Fatalf("source timestamp = %v, want 100", got)
	}

	published = metav1.NewTime(time.Unix(300, 0))
	RecordBackupPointer(bp)
	if got := pointerMetricValue(t, pointerPublishedTime.WithLabelValues(bp.Namespace, bp.Name)); got != 300 {
		t.Fatalf("publication timestamp = %v, want 300", got)
	}
	if got := pointerMetricValue(t, pointerSourceBackupTime.WithLabelValues(bp.Namespace, bp.Name)); got != 100 {
		t.Fatalf("republishing changed source timestamp to %v", got)
	}

	bp.Status.Current = &api.PublishedBackup{CompletedAt: &metav1.Time{}}
	RecordBackupPointer(bp)
	if len(collectPointerMetrics(t, pointerPublishedTime)) != 0 || len(collectPointerMetrics(t, pointerSourceBackupTime)) != 0 {
		t.Fatal("unknown replacement timestamps retained stale series")
	}
}

func TestBackupPointerCandidatesRequireObservation(t *testing.T) {
	bp := newMetricPointer(t)
	bp.Status.Backups = api.BackupCounts{Starting: 1, Running: 2, Failed: 3, Succeeded: 4}
	RecordBackupPointer(bp)
	if len(collectPointerMetrics(t, pointerCandidates)) != 0 {
		t.Fatal("generic status recording manufactured candidate observations")
	}
	RecordBackupPointerCandidates(bp)
	want := map[string]float64{"Starting": 1, "Running": 2, "Failed": 3, "Succeeded": 4}
	for _, sample := range collectPointerMetrics(t, pointerCandidates) {
		labels := pointerMetricLabels(sample)
		state := labels[testStateLabel]
		value, exists := want[state]
		if !exists || sample.Gauge == nil || sample.GetGauge().GetValue() != value ||
			!maps.Equal(labels, map[string]string{testNamespaceLabel: bp.Namespace, testNameLabel: bp.Name, testStateLabel: state}) {
			t.Fatalf("unexpected candidate sample: %v", sample)
		}
		delete(want, state)
	}
	if len(want) != 0 {
		t.Fatalf("candidate states missing from observations: %v", want)
	}
	bp.Status.Backups = api.BackupCounts{}
	RecordBackupPointerCandidates(bp)
	zeros := collectPointerMetrics(t, pointerCandidates)
	if len(zeros) != 4 {
		t.Fatal("an observed empty listing must retain all four zero counts")
	}
	want = map[string]float64{"Starting": 0, "Running": 0, "Failed": 0, "Succeeded": 0}
	for _, sample := range zeros {
		state := pointerMetricLabels(sample)[testStateLabel]
		if _, exists := want[state]; !exists || sample.Gauge == nil || sample.GetGauge().GetValue() != 0 {
			t.Fatalf("unexpected empty-listing sample: %v", sample)
		}
		delete(want, state)
	}
}

func TestBackupPointerConditionLabelsReplacePreviousStatus(t *testing.T) {
	bp := newMetricPointer(t)
	transition := metav1.NewTime(time.Unix(100, 0))
	bp.Status.Conditions = []metav1.Condition{
		{Type: testConditionReady, Status: metav1.ConditionTrue, LastTransitionTime: transition},
		{Type: "Published", Status: metav1.ConditionTrue, LastTransitionTime: transition},
		{Type: "Fresh", Status: metav1.ConditionUnknown},
	}
	RecordBackupPointer(bp)
	if count := len(collectPointerMetrics(t, pointerConditionTransition)); count != 2 {
		t.Fatalf("condition series = %d, want two known transition timestamps", count)
	}
	bp.Status.Conditions = []metav1.Condition{{
		Type: testConditionReady, Status: metav1.ConditionFalse, LastTransitionTime: metav1.NewTime(time.Unix(200, 0)),
		Reason: "Dangling", Message: "The selected backup disappeared.",
	}}
	RecordBackupPointer(bp)
	samples := collectPointerMetrics(t, pointerConditionTransition)
	if len(samples) != 1 {
		t.Fatalf("condition replacement left %d samples, want exactly one", len(samples))
	}
	wantLabels := map[string]string{testNamespaceLabel: bp.Namespace, testNameLabel: bp.Name, "type": testConditionReady, "status": "False"}
	if samples[0].Gauge == nil || samples[0].GetGauge().GetValue() != 200 || !maps.Equal(pointerMetricLabels(samples[0]), wantLabels) {
		t.Fatalf("unexpected replacement condition sample: %v", samples[0])
	}
	bp.Status.Conditions = nil
	RecordBackupPointer(bp)
	if len(collectPointerMetrics(t, pointerConditionTransition)) != 0 {
		t.Fatal("removed conditions retained series")
	}
}

func TestBackupPointerPublishCountsOutcomesOnly(t *testing.T) {
	bp := newMetricPointer(t)
	RecordBackupPointerPublish(bp.Namespace, bp.Name, true)
	RecordBackupPointerPublish(bp.Namespace, bp.Name, false)
	RecordBackupPointerPublish(bp.Namespace, bp.Name, true)
	RecordBackupPointer(bp)
	RecordBackupPointerCandidates(bp)
	if got := pointerMetricValue(t, pointerPublishAttempts.WithLabelValues(bp.Namespace, bp.Name, testResultSuccess)); got != 2 {
		t.Fatalf("successful uploads = %v, want 2", got)
	}
	if got := pointerMetricValue(t, pointerPublishAttempts.WithLabelValues(bp.Namespace, bp.Name, testResultError)); got != 1 {
		t.Fatalf("failed uploads = %v, want 1", got)
	}
	ForgetBackupPointer(bp.Namespace, bp.Name)
	if len(collectPointerMetrics(t, pointerPublishAttempts)) != 0 {
		t.Fatal("forget retained upload counters")
	}
	RecordBackupPointerPublish(bp.Namespace, bp.Name, true)
	if got := pointerMetricValue(t, pointerPublishAttempts.WithLabelValues(bp.Namespace, bp.Name, testResultSuccess)); got != 1 {
		t.Fatalf("recreated pointer inherited %v successful uploads", got)
	}
}

func TestBackupPointerForgetIsolatesNamespacedNames(t *testing.T) {
	b := newMetricPointer(t)
	otherNamespace, otherName := b.DeepCopy(), b.DeepCopy()
	otherNamespace.Namespace = "other-namespace"
	otherName.Name = "other-pointer"
	for _, bp := range []*api.BackupPointer{b, otherNamespace, otherName} {
		t.Cleanup(func() { ForgetBackupPointer(bp.Namespace, bp.Name) })
		stamp := metav1.NewTime(time.Unix(123, 0))
		bp.Status.Current = &api.PublishedBackup{CompletedAt: &stamp, PublishedAt: &stamp}
		bp.Status.Conditions = []metav1.Condition{{Type: testConditionReady, Status: metav1.ConditionTrue, LastTransitionTime: stamp}}
		RecordBackupPointer(bp)
		RecordBackupPointerCandidates(bp)
		RecordBackupPointerPublish(bp.Namespace, bp.Name, true)
		RecordBackupPointerPublish(bp.Namespace, bp.Name, false)
	}
	assertPointerFamilyCounts(t, 3)
	ForgetBackupPointer(b.Namespace, b.Name)
	ForgetBackupPointer(b.Namespace, b.Name)
	assertPointerFamilyCounts(t, 2)
	for _, bp := range []*api.BackupPointer{otherNamespace, otherName} {
		if got := pointerMetricValue(t, pointerPublishedTime.WithLabelValues(bp.Namespace, bp.Name)); got != 123 {
			t.Fatalf("forget changed another pointer's timestamp to %v", got)
		}
		if got := pointerMetricValue(t, pointerPublishAttempts.WithLabelValues(bp.Namespace, bp.Name, testResultError)); got != 1 {
			t.Fatalf("forget changed another pointer's upload count to %v", got)
		}
	}
}

func assertPointerFamilyCounts(t *testing.T, pointers int) {
	t.Helper()
	for metric, perPointer := range map[prometheus.Collector]int{
		pointerPublishedTime: 1, pointerSourceBackupTime: 1, pointerConditionTransition: 1,
		pointerCandidates: 4, pointerPublishAttempts: 2,
	} {
		if got := len(collectPointerMetrics(t, metric)); got != pointers*perPointer {
			t.Fatalf("metric series = %d, want %d", got, pointers*perPointer)
		}
	}
}

func TestBackupPointerRegistryCatalogue(t *testing.T) {
	bp := newMetricPointer(t)
	stamp := metav1.NewTime(time.Unix(123, 0))
	bp.Status.Current = &api.PublishedBackup{CompletedAt: &stamp, PublishedAt: &stamp}
	bp.Status.Conditions = []metav1.Condition{{Type: testConditionReady, Status: metav1.ConditionTrue, LastTransitionTime: stamp}}
	RecordBackupPointer(bp)
	RecordBackupPointerCandidates(bp)
	RecordBackupPointerPublish(bp.Namespace, bp.Name, true)
	families, err := controllermetrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]dto.MetricType{
		"published_time_seconds": dto.MetricType_GAUGE, "source_backup_time_seconds": dto.MetricType_GAUGE,
		"candidate_backups": dto.MetricType_GAUGE, "condition_transition_timestamp_seconds": dto.MetricType_GAUGE,
		"publish_attempts_total": dto.MetricType_COUNTER,
	}
	for _, family := range families {
		name, found := strings.CutPrefix(family.GetName(), pointerMetricPrefix)
		if !found {
			continue
		}
		if kind, known := want[name]; !known || family.GetType() != kind {
			t.Fatalf("unexpected pointer family %s of type %s", family.GetName(), family.GetType())
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("catalogue families absent from manager registry: %v", want)
	}
}

func collectPointerMetrics(t *testing.T, collector prometheus.Collector) []*dto.Metric {
	t.Helper()
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) == 0 {
		return nil
	}
	if len(families) != 1 {
		t.Fatalf("collector returned %d families, want one", len(families))
	}
	return families[0].GetMetric()
}

func pointerMetricValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	value := &dto.Metric{}
	if err := metric.Write(value); err != nil {
		t.Fatal(err)
	}
	switch {
	case value.Gauge != nil:
		return value.Gauge.GetValue()
	case value.Counter != nil:
		return value.Counter.GetValue()
	default:
		t.Fatal("metric has neither a gauge nor a counter value")
		return 0
	}
}

func pointerMetricLabels(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.Label))
	for _, pair := range metric.Label {
		labels[pair.GetName()] = pair.GetValue()
	}
	return labels
}
