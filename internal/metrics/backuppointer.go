package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

const (
	pointerMetricPrefix = "pxc_anonymizer_backuppointer_"
	namespaceLabel      = "namespace"
	nameLabel           = "name"
)

var pointerGauges []*prometheus.GaugeVec

var (
	pointerPublishedTime       = gauge("published_time_seconds", "Unix timestamp of the last pointer publication.")
	pointerSourceBackupTime    = gauge("source_backup_time_seconds", "Unix timestamp of the pointed-at backup completion.")
	pointerCandidates          = gauge("candidate_backups", "Observed candidate backups by state.", "state")
	pointerConditionTransition = gauge("condition_transition_timestamp_seconds",
		"Unix timestamp of the current condition status transition.", "type", "status")
	pointerPublishAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: pointerMetricPrefix + "publish_attempts_total",
		Help: "Pointer object upload attempts by result.",
	}, []string{namespaceLabel, nameLabel, "result"})
)

func init() {
	controllermetrics.Registry.MustRegister(pointerPublishAttempts)
}

// Enrolling each gauge here keeps deletion cleanup complete as the catalogue grows.
func gauge(name, help string, extraLabels ...string) *prometheus.GaugeVec {
	metric := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: pointerMetricPrefix + name,
		Help: help,
	}, append([]string{namespaceLabel, nameLabel}, extraLabels...))
	controllermetrics.Registry.MustRegister(metric)
	pointerGauges = append(pointerGauges, metric)
	return metric
}

// RecordBackupPointer must follow a successful or unchanged status write.
func RecordBackupPointer(bp *api.BackupPointer) {
	var publishedAt, completedAt *metav1.Time
	if bp.Status.Current != nil {
		publishedAt = bp.Status.Current.PublishedAt
		completedAt = bp.Status.Current.CompletedAt
	}
	recordPointerTimestamp(pointerPublishedTime, bp.Namespace, bp.Name, publishedAt)
	recordPointerTimestamp(pointerSourceBackupTime, bp.Namespace, bp.Name, completedAt)

	// A condition flip must not leave its previous status visible as a current series.
	pointerConditionTransition.DeletePartialMatch(prometheus.Labels{namespaceLabel: bp.Namespace, nameLabel: bp.Name})
	for _, condition := range bp.Status.Conditions {
		if !condition.LastTransitionTime.IsZero() {
			pointerConditionTransition.WithLabelValues(bp.Namespace, bp.Name, condition.Type, string(condition.Status)).
				Set(float64(condition.LastTransitionTime.Unix()))
		}
	}
}

func recordPointerTimestamp(metric *prometheus.GaugeVec, namespace, name string, timestamp *metav1.Time) {
	if timestamp == nil || timestamp.IsZero() {
		metric.DeleteLabelValues(namespace, name)
		return
	}
	metric.WithLabelValues(namespace, name).Set(float64(timestamp.Unix()))
}

// RecordBackupPointerCandidates requires a successful listing, so zero means observed empty.
func RecordBackupPointerCandidates(bp *api.BackupPointer) {
	counts := bp.Status.Backups
	for state, count := range map[string]int32{
		"Starting":  counts.Starting,
		"Running":   counts.Running,
		"Failed":    counts.Failed,
		"Succeeded": counts.Succeeded,
	} {
		pointerCandidates.WithLabelValues(bp.Namespace, bp.Name, state).Set(float64(count))
	}
}

// RecordBackupPointerPublish counts only completed PutJSON calls, including failures.
func RecordBackupPointerPublish(namespace, name string, success bool) {
	result := "error"
	if success {
		result = "success"
	}
	pointerPublishAttempts.WithLabelValues(namespace, name, result).Inc()
}

// ForgetBackupPointer also drops counters so a recreated resource starts a new lifetime.
func ForgetBackupPointer(namespace, name string) {
	labels := prometheus.Labels{namespaceLabel: namespace, nameLabel: name}
	for _, metric := range pointerGauges {
		metric.DeletePartialMatch(labels)
	}
	pointerPublishAttempts.DeletePartialMatch(labels)
}
