package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

const runMetricPrefix = "pxc_anonymizer_run_"

type RunStep string

const (
	RunStepRestore   RunStep = "restore"
	RunStepAnonymize RunStep = "anonymize"
	RunStepBackup    RunStep = "backup"
	RunStepPublish   RunStep = "publish"
	RunStepCleanup   RunStep = "cleanup"
)

// Optional observations distinguish measured zero from information absent in Run status.
type RunObservation struct {
	TempClusterPresent *bool
	StepDurations      map[RunStep]time.Duration
	RowsTotal          *int64
}

var runGauges []*prometheus.GaugeVec

var (
	runPhase               = runGauge("phase", "Current Run phase.", "phase")
	runTablesDone          = runGauge("tables_done", "Tables completed by the runner.")
	runTablesExpected      = runGauge("tables_expected", "Tables expected by the runner.")
	runRowsDone            = runGauge("rows_done", "Rows completed by the runner.")
	runRowsExpected        = runGauge("rows_expected", "Rows expected by the runner.")
	runStepDuration        = runGauge("step_duration_seconds", "Observed completed step duration in seconds.", "step")
	runStartTime           = runGauge("start_time_seconds", "Unix timestamp when the Run started.")
	runCompletionTime      = runGauge("completion_time_seconds", "Unix timestamp when the Run completed.")
	runAttempts            = runGauge("attempts", "Observed runner attempt count.")
	runConditionTransition = runGauge("condition_transition_timestamp_seconds",
		"Unix timestamp of the current Run condition status transition.", "type", "status")
	runTempClusterPresent = runGauge("temp_cluster_present", "Whether the owned temporary cluster was observed present.")
)

// Run-only enrollment keeps ForgetRun from touching another resource kind's series.
func runGauge(name, help string, extraLabels ...string) *prometheus.GaugeVec {
	metric := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: runMetricPrefix + name,
		Help: help,
	}, append([]string{namespaceLabel, nameLabel}, extraLabels...))
	controllermetrics.Registry.MustRegister(metric)
	runGauges = append(runGauges, metric)
	return metric
}

// RecordRun requires freshly read or successfully persisted status and observed child state.
func RecordRun(run *api.AnonymizationRun, observation RunObservation) {
	// Replacing observations removes old labels and values that are no longer known.
	ForgetRun(run.Namespace, run.Name)
	if run.Status.Phase != "" {
		runPhase.WithLabelValues(run.Namespace, run.Name, string(run.Status.Phase)).Set(1)
	}
	recordPointerTimestamp(runStartTime, run.Namespace, run.Name, run.Status.StartedAt)
	recordPointerTimestamp(runCompletionTime, run.Namespace, run.Name, run.Status.CompletedAt)
	for _, condition := range run.Status.Conditions {
		if !condition.LastTransitionTime.IsZero() {
			runConditionTransition.WithLabelValues(run.Namespace, run.Name, condition.Type, string(condition.Status)).
				Set(float64(condition.LastTransitionTime.Unix()))
		}
	}
	if anonymize := run.Status.Anonymize; anonymize != nil {
		runAttempts.WithLabelValues(run.Namespace, run.Name).Set(float64(anonymize.Attempts))
		if progress := anonymize.Progress; progress != nil {
			runTablesDone.WithLabelValues(run.Namespace, run.Name).Set(float64(progress.TablesDone))
			runTablesExpected.WithLabelValues(run.Namespace, run.Name).Set(float64(progress.TablesTotal))
			runRowsDone.WithLabelValues(run.Namespace, run.Name).Set(float64(progress.RowsDone))
		}
	}
	if observation.RowsTotal != nil {
		runRowsExpected.WithLabelValues(run.Namespace, run.Name).Set(float64(*observation.RowsTotal))
	}
	for _, step := range []RunStep{RunStepRestore, RunStepAnonymize, RunStepBackup, RunStepPublish, RunStepCleanup} {
		if duration, known := observation.StepDurations[step]; known && duration >= 0 {
			runStepDuration.WithLabelValues(run.Namespace, run.Name, string(step)).Set(duration.Seconds())
		}
	}
	if observation.TempClusterPresent != nil {
		value := float64(0)
		if *observation.TempClusterPresent {
			value = 1
		}
		runTempClusterPresent.WithLabelValues(run.Namespace, run.Name).Set(value)
	}
}

// ForgetRun removes every series so a recreated Run cannot inherit an old lifetime.
func ForgetRun(namespace, name string) {
	labels := prometheus.Labels{namespaceLabel: namespace, nameLabel: name}
	for _, metric := range runGauges {
		metric.DeletePartialMatch(labels)
	}
}
