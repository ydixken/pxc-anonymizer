// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	record "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/conditions"
	"github.com/ydixken/pxc-anonymizer/internal/metrics"
	"github.com/ydixken/pxc-anonymizer/internal/objectstore"
	"github.com/ydixken/pxc-anonymizer/internal/pointer"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
	"github.com/ydixken/pxc-anonymizer/internal/runner/report"
)

const (
	runFinalizer           = "pxc-anonymizer.io/temp-cluster-cleanup"
	runPollInterval        = 10 * time.Second
	runConditionSource     = "SourceResolved"
	runConditionPolicy     = "PolicyValid"
	runConditionCluster    = "TempClusterReady"
	runConditionRestored   = "Restored"
	runConditionCleaned    = "CleanedUp"
	runConditionComplete   = "Complete"
	runConditionFailed     = "Failed"
	runConditionRetained   = "Retained"
	runConditionAnonymized = "Anonymized"
	runConditionBackedUp   = "BackedUp"
	runConditionPublished  = "Published"
)

// AnonymizationRunReconciler keeps durable ownership and snapshots ahead of side effects.
type AnonymizationRunReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	APIReader    client.Reader
	Recorder     record.EventRecorder
	RunnerImage  string
	now          func() time.Time
	fetchPointer func(context.Context, string, api.PointerSource) (*pointer.Document, error)
	podLogs      func(context.Context, string, string) ([]byte, error)
	putPointer   func(context.Context, string, api.PointerTarget, *pointer.Document) (string, error)
}

// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=anonymizationruns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=anonymizationruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=anonymizationruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=anonymizationpolicies;backuppointers,verbs=get
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusters;perconaxtradbclusterrestores,verbs=get;list;watch;create;delete;patch
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusterbackups,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete

func (r *AnonymizationRunReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	result, err := r.reconcileRun(ctx, request)
	if apierrors.IsConflict(err) {
		//nolint:staticcheck // A conflict retries fresh state without changing the pipeline verdict.
		return ctrl.Result{Requeue: true}, nil
	}
	return result, err
}

func (r *AnonymizationRunReconciler) reconcileRun(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	run := &api.AnonymizationRun{}
	if err := r.Get(ctx, request.NamespacedName, run); err != nil {
		if apierrors.IsNotFound(err) {
			metrics.ForgetRun(request.Namespace, request.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(run, runFinalizer) {
		metrics.ForgetRun(run.Namespace, run.Name)
		return ctrl.Result{}, nil
	}
	r.recordRunMetrics(ctx, run)
	if !run.DeletionTimestamp.IsZero() {
		result, err := r.cleanupRun(ctx, run, true)
		if err == nil && !controllerutil.ContainsFinalizer(run, runFinalizer) {
			metrics.ForgetRun(run.Namespace, run.Name)
		}
		return result, err
	}
	if run.Status.Phase == "" {
		base := run.DeepCopy()
		run.Status.Phase = api.RunPhasePending
		started := metav1.NewTime(r.runTime())
		run.Status.StartedAt = &started
		return r.patchRun(ctx, run, base, time.Nanosecond)
	}
	if !controllerutil.ContainsFinalizer(run, runFinalizer) {
		base := run.DeepCopy()
		controllerutil.AddFinalizer(run, runFinalizer)
		err := r.Patch(ctx, run, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		return ctrl.Result{RequeueAfter: time.Nanosecond}, err
	}
	if meta.IsStatusConditionTrue(run.Status.Conditions, runConditionFailed) || run.Status.Phase == api.RunPhaseCompleted {
		if snapshot, err := r.loadRunSnapshot(ctx, run); err == nil {
			run.Spec = snapshot.Spec
		}
		if meta.IsStatusConditionTrue(run.Status.Conditions, runConditionCleaned) ||
			meta.IsStatusConditionTrue(run.Status.Conditions, runConditionRetained) {
			return ctrl.Result{}, nil
		}
		return r.cleanupRun(ctx, run, false)
	}
	if run.Status.Phase == api.RunPhasePending || run.Status.Phase == api.RunPhaseResolvingSource {
		return r.resolveRun(ctx, run)
	}
	snapshot, err := r.loadRunSnapshot(ctx, run)
	if err != nil {
		return r.failRun(ctx, run, runConditionPolicy, api.ReasonPolicyInvalid, "the immutable Run snapshot is unavailable")
	}
	// Mutable API fields cannot alter an already snapshotted pipeline.
	run.Spec = snapshot.Spec
	run.Labels = maps.Clone(run.Labels)
	if run.Labels == nil {
		run.Labels = map[string]string{}
	}
	run.Labels[pxc.LabelOutputGroup] = snapshot.OutputGroup
	switch run.Status.Phase {
	case api.RunPhaseProvisioning:
		return r.provisionRun(ctx, run, snapshot)
	case api.RunPhaseRestoring:
		return r.restoreRun(ctx, run)
	case api.RunPhaseAnonymizing:
		return r.anonymizeRun(ctx, run, snapshot)
	case api.RunPhaseBackingUp:
		return r.backupRun(ctx, run)
	case api.RunPhasePublishing:
		return r.publishRun(ctx, run)
	case api.RunPhasePruning:
		return r.pruneRun(ctx, run)
	case api.RunPhaseCleaningUp:
		return r.finishRun(ctx, run)
	default:
		return ctrl.Result{RequeueAfter: runPollInterval}, nil
	}
}

func (r *AnonymizationRunReconciler) resolveRun(ctx context.Context, run *api.AnonymizationRun) (ctrl.Result, error) {
	if r.runExpired(run.Status.StartedAt, run.Spec.Timeouts.ClusterReady.Duration, 30*time.Minute) {
		return r.failRun(ctx, run, runConditionSource, api.ReasonTimeout, "source and snapshot resolution timed out")
	}
	snapshot, err := r.prepareRunSnapshot(ctx, run)
	if err != nil {
		if invalid, ok := errors.AsType[*runValidationError](err); ok {
			return r.failRun(ctx, run, invalid.condition, invalid.reason, invalid.message)
		}
		return ctrl.Result{}, err
	}
	base := run.DeepCopy()
	run.Status.Source = &snapshot.Source
	run.Status.PolicyHash = snapshot.PolicyHash
	now := metav1.NewTime(r.runTime())
	run.Status.TempCluster = &api.TempClusterStatus{
		Name: runTempName(run), SecretName: runTempName(run) + "-secrets", CreatedAt: &now,
	}
	run.Status.Phase = api.RunPhaseProvisioning
	r.setRunCondition(run, runConditionSource, metav1.ConditionTrue, snapshot.SourceReason, "the restore source is snapshotted")
	r.setRunCondition(run, runConditionPolicy, metav1.ConditionTrue, api.ReasonSnapshotted, "policy and referenced payloads are immutable")
	r.setRunCondition(run, runConditionCluster, metav1.ConditionFalse, api.ReasonProvisioning, "the temporary cluster is provisioning")
	return r.patchRun(ctx, run, base, time.Nanosecond)
}

func (r *AnonymizationRunReconciler) provisionRun(
	ctx context.Context, run *api.AnonymizationRun, snapshot *runSnapshot,
) (ctrl.Result, error) {
	if run.Status.TempCluster == nil || run.Status.TempCluster.Name == "" {
		return r.failRun(ctx, run, runConditionCluster, api.ReasonClusterError, "temporary cluster identity is missing")
	}
	if r.runExpired(run.Status.TempCluster.CreatedAt, run.Spec.Timeouts.ClusterReady.Duration, 30*time.Minute) {
		return r.failRun(ctx, run, runConditionCluster, api.ReasonTimeout, "temporary cluster readiness timed out")
	}
	secret, err := pxc.RenderSystemUsersSecret(run, run.Status.TempCluster.Name, snapshot.SystemUsers)
	if err != nil {
		return r.failRun(ctx, run, runConditionCluster, api.ReasonClusterError, "source system-user credentials are invalid")
	}
	if err := r.createRunChild(ctx, run, secret); err != nil {
		return ctrl.Result{}, err
	}
	cluster, err := pxc.RenderTempCluster(run, run.Status.TempCluster.Name)
	if err != nil {
		return r.failRun(ctx, run, runConditionCluster, api.ReasonClusterError, "temporary cluster configuration is invalid")
	}
	if err := r.createRunChild(ctx, run, cluster); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(cluster), cluster); err != nil {
		return ctrl.Result{}, err
	}
	if err := runOwns(run, cluster); err != nil {
		return ctrl.Result{}, err
	}
	if cluster.GetUID() == "" {
		return ctrl.Result{}, errors.New("temporary cluster must have a persisted UID")
	}
	if run.Status.TempCluster.UID == "" {
		base := run.DeepCopy()
		run.Status.TempCluster.UID = cluster.GetUID()
		return r.patchRun(ctx, run, base, time.Nanosecond)
	}
	if run.Status.TempCluster.UID != cluster.GetUID() {
		return r.failRun(ctx, run, runConditionCluster, api.ReasonClusterError, "temporary cluster identity changed")
	}
	view, err := pxc.DecodeCluster(cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if view.Status.State == pxc.ClusterError {
		return r.failRun(ctx, run, runConditionCluster, api.ReasonClusterError, "the temporary cluster reports an error")
	}
	if !view.Ready() {
		return ctrl.Result{RequeueAfter: r.runDelay(run.Status.TempCluster.CreatedAt, run.Spec.Timeouts.ClusterReady.Duration, 30*time.Minute)}, nil
	}
	base := run.DeepCopy()
	now := metav1.NewTime(r.runTime())
	run.Status.TempCluster.ReadyAt = &now
	run.Status.Phase = api.RunPhaseRestoring
	r.setRunCondition(run, runConditionCluster, metav1.ConditionTrue, api.ReasonReady, "the temporary cluster is ready")
	r.setRunCondition(run, runConditionRestored, metav1.ConditionFalse, api.ReasonRestoring, "the source backup is restoring")
	return r.patchRun(ctx, run, base, time.Nanosecond)
}

func (r *AnonymizationRunReconciler) restoreRun(ctx context.Context, run *api.AnonymizationRun) (ctrl.Result, error) {
	if run.Status.Source == nil || run.Status.TempCluster == nil {
		return r.failRun(ctx, run, runConditionRestored, api.ReasonRestoreFailed, "durable restore inputs are missing")
	}
	condition := meta.FindStatusCondition(run.Status.Conditions, runConditionRestored)
	var started *metav1.Time
	if condition != nil {
		started = &condition.LastTransitionTime
	}
	if r.runExpired(started, run.Spec.Timeouts.Restore.Duration, 3*time.Hour) {
		return r.failRun(ctx, run, runConditionRestored, api.ReasonTimeout, "restore timed out; cleanup will remove the temporary cluster")
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(pxc.ClusterGVK)
	clusterKey := client.ObjectKey{Namespace: run.Namespace, Name: run.Status.TempCluster.Name}
	if err := r.Get(ctx, clusterKey, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return r.failRun(ctx, run, runConditionCluster, api.ReasonClusterError, "temporary cluster disappeared")
		}
		return ctrl.Result{}, err
	}
	if err := runOwns(run, cluster); err != nil {
		return ctrl.Result{}, err
	}
	if cluster.GetUID() != run.Status.TempCluster.UID || !cluster.GetDeletionTimestamp().IsZero() {
		return r.failRun(ctx, run, runConditionCluster, api.ReasonClusterError, "temporary cluster identity changed or is deleting")
	}
	name := run.Status.RestoreName
	if name == "" {
		var err error
		name, err = pxc.RestoreName(run.Status.TempCluster.Name+"-restore", run.Status.TempCluster.Name)
		if err != nil {
			return r.failRun(ctx, run, runConditionRestored, api.ReasonRestoreFailed, "restore name cannot fit the Percona Job labels")
		}
	}
	restore := &unstructured.Unstructured{}
	restore.SetGroupVersionKind(pxc.RestoreGVK)
	key := client.ObjectKey{Namespace: run.Namespace, Name: name}
	err := r.APIReader.Get(ctx, key, restore)
	if apierrors.IsNotFound(err) {
		if run.Status.RestoreName != "" {
			return r.failRun(ctx, run, runConditionRestored, api.ReasonRestoreVanished, "the recorded restore disappeared before success")
		}
		restore, err = pxc.RenderRestore(pxc.RestoreRenderOptions{
			Namespace: run.Namespace, Name: name, ClusterName: run.Status.TempCluster.Name,
			Destination: run.Status.Source.Destination, Credentials: run.Spec.Source.Restore,
			Owner: runOwner(run),
		})
		if err != nil {
			return r.failRun(ctx, run, runConditionRestored, api.ReasonRestoreFailed, "inline restore configuration is invalid")
		}
		base := run.DeepCopy()
		run.Status.RestoreName = name
		if _, err = r.patchRun(ctx, run, base, time.Nanosecond); err != nil {
			return ctrl.Result{}, err
		}
		if err = r.createRunChild(ctx, run, restore); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: runPollInterval}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := runOwns(run, restore); err != nil {
		return ctrl.Result{}, err
	}
	if run.Status.RestoreName == "" {
		base := run.DeepCopy()
		run.Status.RestoreName = name
		return r.patchRun(ctx, run, base, time.Nanosecond)
	}
	view, err := pxc.DecodeRestore(restore)
	if err != nil {
		return ctrl.Result{}, err
	}
	if view.Status.State == pxc.RestoreFailed {
		return r.failRun(ctx, run, runConditionRestored, api.ReasonRestoreFailed, "the PXC restore failed; inspect its status for details")
	}
	if !view.Succeeded() {
		return ctrl.Result{RequeueAfter: r.runDelay(started, run.Spec.Timeouts.Restore.Duration, 3*time.Hour)}, nil
	}
	base := run.DeepCopy()
	r.setRunCondition(run, runConditionRestored, metav1.ConditionTrue, api.ReasonSucceeded, "the PXC restore finished successfully")
	run.Status.Phase = api.RunPhaseAnonymizing
	r.setRunCondition(run, runConditionAnonymized, metav1.ConditionFalse, api.ReasonRunning, "the anonymization runner is starting")
	return r.patchRun(ctx, run, base, time.Nanosecond)
}

func (r *AnonymizationRunReconciler) anonymizeRun(ctx context.Context, run *api.AnonymizationRun, snapshot *runSnapshot) (ctrl.Result, error) {
	if run.Status.Anonymize == nil || run.Status.Anonymize.JobName == "" {
		base := run.DeepCopy()
		attempt := int32(1)
		if run.Status.Anonymize != nil {
			attempt = run.Status.Anonymize.Attempts + 1
		}
		now := metav1.NewTime(r.runTime())
		run.Status.Anonymize = &api.AnonymizeStatus{Attempts: attempt, JobName: runChildName(run, fmt.Sprintf("anonymize-%d", attempt)), StartedAt: &now}
		if _, err := r.patchRun(ctx, run, base, time.Nanosecond); err != nil {
			return ctrl.Result{}, err
		}
		job, err := r.renderRunJob(run, snapshot)
		if err != nil {
			return r.failRun(ctx, run, runConditionAnonymized, api.ReasonPolicyError, "runner image or configuration is invalid")
		}
		if err = r.createRunChild(ctx, run, job); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: runPollInterval}, nil
	}
	if run.Status.Anonymize.CompletedAt != nil && run.Status.Anonymize.LastResult != nil && run.Status.Anonymize.LastResult.Class == string(report.Transient) {
		return r.retryRunAttempt(ctx, run, run.DeepCopy(), run.Status.Anonymize.LastResult.Message)
	}
	if r.runExpired(run.Status.Anonymize.StartedAt, run.Spec.Timeouts.Anonymize.Duration, 6*time.Hour) {
		return r.failRun(ctx, run, runConditionAnonymized, api.ReasonTimeout, "the anonymization attempt timed out")
	}
	job, err := r.renderRunJob(run, snapshot)
	if err != nil {
		return r.failRun(ctx, run, runConditionAnonymized, api.ReasonPolicyError, "runner image or configuration is invalid")
	}
	if err = r.APIReader.Get(ctx, client.ObjectKeyFromObject(job), job); err != nil {
		if apierrors.IsNotFound(err) {
			return r.failRun(ctx, run, runConditionAnonymized, api.ReasonRunnerFailed, "the recorded runner Job vanished; replay is unsafe")
		}
		return ctrl.Result{}, err
	}
	if err = runOwns(run, job); err != nil {
		return ctrl.Result{}, err
	}
	pods := &corev1.PodList{}
	if err = r.List(ctx, pods, client.InNamespace(run.Namespace), client.MatchingLabels{"batch.kubernetes.io/job-name": job.Name}); err != nil {
		return ctrl.Result{}, err
	}
	base := run.DeepCopy()
	var termination *corev1.ContainerStateTerminated
	for i := range pods.Items {
		pod := &pods.Items[i]
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.UID != job.UID || owner.Kind != "Job" || owner.Name != job.Name {
			continue
		}
		r.sampleRunProgress(ctx, run, pod)
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == runContainerName && status.State.Terminated != nil {
				if termination != nil {
					return r.failRun(ctx, run, runConditionAnonymized, api.ReasonRunnerFailed, "multiple runner terminations are ambiguous")
				}
				termination = status.State.Terminated
			}
		}
	}
	if termination != nil {
		return r.finishRunAttempt(ctx, run, base, termination)
	}
	for _, condition := range job.Status.Conditions {
		if condition.Status == corev1.ConditionTrue && (condition.Type == batchv1.JobFailed || condition.Type == batchv1.JobComplete) {
			return r.failRun(ctx, run, runConditionAnonymized, api.ReasonRunnerFailed, "the Job ended without a valid runner report; replay is unsafe")
		}
	}
	return r.patchRun(ctx, run, base, r.runDelay(run.Status.Anonymize.StartedAt, run.Spec.Timeouts.Anonymize.Duration, 6*time.Hour))
}

func (r *AnonymizationRunReconciler) finishRunAttempt(ctx context.Context, run, base *api.AnonymizationRun, termination *corev1.ContainerStateTerminated) (ctrl.Result, error) {
	result, valid := runReport(termination, run.Status.PolicyHash)
	if !valid {
		return r.failRun(ctx, run, runConditionAnonymized, api.ReasonRunnerFailed, "runner termination JSON or exit code is invalid; replay is unsafe")
	}
	now := metav1.NewTime(r.runTime())
	run.Status.Anonymize.CompletedAt = &now
	// Arbitrary termination text is never copied into public status or Events.
	run.Status.Anonymize.LastResult = &api.AnonymizeResult{Class: string(result.Class), Message: "runner reported " + result.Result}
	run.Status.Anonymize.Progress = &api.AnonymizeProgress{TablesTotal: int32(result.TablesTotal), TablesDone: int32(result.TablesDone), RowsDone: result.RowsDone, StepsDone: int32(result.StepsDone)}
	if result.Result == report.ResultError {
		if result.Class == report.Transient {
			return r.retryRunAttempt(ctx, run, base, "the runner reported a transient failure")
		}
		r.markRunFailed(run, runConditionAnonymized, runReportReason(result.Class), "the runner reported an absorbing "+string(result.Class)+" failure")
		return r.patchRun(ctx, run, base, time.Nanosecond)
	}
	reason := api.ReasonSucceeded
	run.Status.Phase = api.RunPhaseBackingUp
	r.setRunCondition(run, runConditionBackedUp, metav1.ConditionFalse, api.ReasonRunning, "the anonymized backup is starting")
	if run.Spec.Runner.DryRun {
		reason = api.ReasonDryRun
		run.Status.Phase = api.RunPhaseCleaningUp
		r.setRunCondition(run, runConditionPublished, metav1.ConditionTrue, api.ReasonSkipped, "dry-run skips backup and publication")
	}
	r.setRunCondition(run, runConditionAnonymized, metav1.ConditionTrue, reason, "the runner completed successfully")
	return r.patchRun(ctx, run, base, time.Nanosecond)
}

func (r *AnonymizationRunReconciler) retryRunAttempt(ctx context.Context, run, base *api.AnonymizationRun, message string) (ctrl.Result, error) {
	if run.Status.Anonymize.Attempts > run.Spec.BackoffLimit {
		r.markRunFailed(run, runConditionAnonymized, api.ReasonRetryBudgetExhausted, message)
		return r.patchRun(ctx, run, base, time.Nanosecond)
	}
	if base.Status.Anonymize.CompletedAt == nil || base.Status.Anonymize.LastResult == nil || base.Status.Anonymize.LastResult.Class != string(report.Transient) {
		now := metav1.NewTime(r.runTime())
		run.Status.Anonymize.CompletedAt = &now
		run.Status.Anonymize.LastResult = &api.AnonymizeResult{Class: string(report.Transient), Message: message}
		return r.patchRun(ctx, run, base, time.Nanosecond)
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: run.Status.Anonymize.JobName, Namespace: run.Namespace}}
	gone, err := r.deleteRunOwned(ctx, run, job)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !gone {
		return ctrl.Result{RequeueAfter: runPollInterval}, nil
	}
	run.Status.Anonymize.JobName = ""
	run.Status.Anonymize.LastResult = &api.AnonymizeResult{Class: string(report.Transient), Message: message}
	return r.patchRun(ctx, run, base, time.Nanosecond)
}

func (r *AnonymizationRunReconciler) sampleRunProgress(ctx context.Context, run *api.AnonymizationRun, pod *corev1.Pod) {
	if r.podLogs == nil {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := r.podLogs(opCtx, pod.Namespace, pod.Name)
	if err != nil || len(data) > 128*1024 {
		return
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	previous := run.Status.Anonymize.Progress
	var outcomes []string
	for _, line := range lines {
		var entry struct {
			Msg, DB, Table, Step, Name         string
			TablesDone, TablesTotal, StepsDone int32
			RowsDone                           int64
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry.Msg == "progress" && entry.TablesDone >= 0 && entry.TablesTotal >= entry.TablesDone && entry.RowsDone >= 0 && entry.StepsDone >= 0 {
			run.Status.Anonymize.Progress = &api.AnonymizeProgress{TablesDone: entry.TablesDone, TablesTotal: entry.TablesTotal, RowsDone: entry.RowsDone,
				StepsDone: entry.StepsDone, CurrentTable: entry.DB + "." + entry.Table}
		}
		if (entry.Msg == "ok" || entry.Msg == "error") && (entry.Step == "pre" || entry.Step == "post") && len(outcomes) < 8 {
			outcomes = append(outcomes, "runner "+entry.Step+" step reported "+entry.Msg)
		}
	}
	current := run.Status.Anonymize.Progress
	if r.Recorder != nil {
		for _, outcome := range outcomes {
			r.Recorder.Eventf(run, nil, corev1.EventTypeNormal, "StepOutcome", "Anonymize", "%s", outcome)
		}
	}
	if current == nil || (previous != nil && current.StepsDone <= previous.StepsDone && current.RowsDone <= previous.RowsDone) || r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(run, nil, corev1.EventTypeNormal, "Progress", "Anonymize", "tables=%d/%d rows=%d steps=%d", current.TablesDone, current.TablesTotal, current.RowsDone, current.StepsDone)
}

func (r *AnonymizationRunReconciler) backupRun(ctx context.Context, run *api.AnonymizationRun) (ctrl.Result, error) {
	if run.Status.TempCluster == nil {
		return r.failRun(ctx, run, runConditionBackedUp, api.ReasonBackupFailed, "temporary cluster identity is missing")
	}
	if run.Status.Output == nil {
		prefix := run.Spec.Output.BackupNamePrefix
		if prefix == "" {
			prefix = run.Status.TempCluster.Name + "-anonymized"
		}
		base := run.DeepCopy()
		run.Status.Output = &api.PublishedBackup{BackupName: prefix + "-" + r.runTime().Format("200601021504"), StorageName: runOutputStorage}
		return r.patchRun(ctx, run, base, time.Nanosecond)
	}
	condition := meta.FindStatusCondition(run.Status.Conditions, runConditionBackedUp)
	if condition == nil || r.runExpired(&condition.LastTransitionTime, run.Spec.Timeouts.Backup.Duration, 3*time.Hour) {
		return r.failRun(ctx, run, runConditionBackedUp, api.ReasonTimeout, "the output backup timed out")
	}
	backup, err := pxc.RenderOutputBackup(run, run.Status.TempCluster.Name, run.Status.Output.BackupName, runOutputGroup(run))
	if err != nil {
		return r.failRun(ctx, run, runConditionBackedUp, api.ReasonBackupFailed, "output backup configuration is invalid")
	}
	backup.SetAnnotations(map[string]string{"pxc-anonymizer.io/run-uid": string(run.UID)})
	err = r.Get(ctx, client.ObjectKeyFromObject(backup), backup)
	if apierrors.IsNotFound(err) {
		if err = r.Create(ctx, backup); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: runPollInterval}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if err = runOutputIdentity(run, backup); err != nil {
		return ctrl.Result{}, err
	}
	view, err := pxc.DecodeBackup(backup)
	if err != nil {
		return ctrl.Result{}, err
	}
	if view.Status.State == pxc.BackupFailed {
		reason, message := api.ReasonBackupFailed, "the output backup failed"
		if strings.Contains(strings.ToLower(view.Status.Error), "deadline") {
			reason, message = api.ReasonBackupDeadlineExceeded, "the output backup deadline was exceeded; increase timeouts.backup"
		}
		return r.failRun(ctx, run, runConditionBackedUp, reason, message)
	}
	if !view.Succeeded() || view.Status.CompletedAt == nil || view.Status.CompletedAt.IsZero() {
		return ctrl.Result{RequeueAfter: runPollInterval}, nil
	}
	if err = (&pointer.Document{Name: view.Name, Destination: view.Status.Destination, SchemaVersion: pointer.SchemaVersion}).Validate(); err != nil {
		return r.failRun(ctx, run, runConditionBackedUp, api.ReasonBackupFailed, "successful backup lacks a valid destination")
	}
	base := run.DeepCopy()
	run.Status.Output.Destination = view.Status.Destination
	run.Status.Output.CompletedAt = view.Status.CompletedAt.DeepCopy()
	run.Status.Phase = api.RunPhasePublishing
	r.setRunCondition(run, runConditionBackedUp, metav1.ConditionTrue, api.ReasonSucceeded, "the output backup succeeded")
	r.setRunCondition(run, runConditionPublished, metav1.ConditionFalse, api.ReasonUploadFailed, "the output pointer is pending publication")
	return r.patchRun(ctx, run, base, time.Nanosecond)
}

func runOutputIdentity(run *api.AnonymizationRun, backup *unstructured.Unstructured) error {
	if len(backup.GetOwnerReferences()) != 0 || backup.GetAnnotations()["pxc-anonymizer.io/run-uid"] != string(run.UID) ||
		backup.GetLabels()[pxc.LabelRun] != pxc.LabelValue(run.Name) || backup.GetLabels()[pxc.LabelOutputGroup] != runOutputGroup(run) ||
		backup.GetLabels()[pxc.LabelOutput] != runOutputStorage || !backup.GetDeletionTimestamp().IsZero() {
		return errors.New("output backup identity is foreign or deleting")
	}
	return nil
}

func (r *AnonymizationRunReconciler) publishRun(ctx context.Context, run *api.AnonymizationRun) (ctrl.Result, error) {
	if run.Status.Output == nil || run.Status.Output.Destination == "" {
		return r.failRun(ctx, run, runConditionPublished, api.ReasonUploadFailed, "the successful output backup is missing")
	}
	base := run.DeepCopy()
	if run.Spec.Output.Pointer == nil {
		r.setRunCondition(run, runConditionPublished, metav1.ConditionTrue, api.ReasonSkipped, "pointer publication is disabled")
		run.Status.Phase = api.RunPhasePruning
		return r.patchRun(ctx, run, base, time.Nanosecond)
	}
	condition := meta.FindStatusCondition(run.Status.Conditions, runConditionPublished)
	if condition == nil || r.runExpired(&condition.LastTransitionTime, run.Spec.Timeouts.Publish.Duration, 10*time.Minute) {
		return r.failRun(ctx, run, runConditionPublished, api.ReasonUploadFailed, "output pointer publication timed out")
	}
	backup := &unstructured.Unstructured{}
	backup.SetGroupVersionKind(pxc.BackupGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Status.Output.BackupName}, backup); err != nil {
		return ctrl.Result{}, err
	}
	if err := runOutputIdentity(run, backup); err != nil {
		return ctrl.Result{}, err
	}
	view, err := pxc.DecodeBackup(backup)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !view.Succeeded() || view.Status.Destination != run.Status.Output.Destination {
		return r.failRun(ctx, run, runConditionPublished, api.ReasonUploadFailed, "the output backup no longer matches the recorded success")
	}
	now := r.runTime()
	document := &pointer.Document{Name: view.Name, Destination: view.Status.Destination, SchemaVersion: pointer.SchemaVersion, PublishedAt: &now,
		PublishedBy:   &pointer.Publisher{Kind: "AnonymizationRun", Namespace: run.Namespace, Name: run.Name, UID: string(run.UID)},
		SourceCluster: &pointer.SourceCluster{Name: run.Status.TempCluster.Name, Namespace: run.Namespace, CRVersion: run.Spec.TempCluster.CRVersion},
		Backup:        &pointer.Backup{StorageName: run.Status.Output.StorageName, State: string(pxc.BackupSucceeded)},
		Anonymized:    &pointer.Anonymized{Run: run.Name, Policy: run.Spec.PolicyRef.Name, PolicyHash: run.Status.PolicyHash}, PublicURL: run.Spec.Output.Pointer.PublicURL}
	if view.Status.CompletedAt != nil {
		completed := view.Status.CompletedAt.Time
		document.Backup.CompletedAt = &completed
	}
	if view.Status.S3 != nil {
		document.S3 = &pointer.S3{Bucket: view.Status.S3.Bucket, EndpointURL: view.Status.S3.EndpointURL, Region: view.Status.S3.Region}
	}
	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var etag string
	if r.putPointer != nil {
		etag, err = r.putPointer(opCtx, run.Namespace, *run.Spec.Output.Pointer, document)
	} else {
		var store *objectstore.Client
		store, err = objectstore.Resolve(opCtx, r.APIReader, run.Namespace, run.Spec.Output.Pointer.ObjectStorage)
		if err == nil {
			etag, err = store.PutJSON(opCtx, run.Spec.Output.Pointer.Key, document)
		}
	}
	if err != nil {
		r.setRunCondition(run, runConditionPublished, metav1.ConditionFalse, api.ReasonUploadFailed, "output pointer upload failed; the previous pointer is preserved")
		return r.patchRun(ctx, run, base, runPollInterval)
	}
	published := metav1.NewTime(now)
	run.Status.Output.PublishedAt = &published
	run.Status.Output.ETag = etag
	run.Status.Output.SchemaVersion = int32(pointer.SchemaVersion)
	run.Status.Phase = api.RunPhasePruning
	r.setRunCondition(run, runConditionPublished, metav1.ConditionTrue, api.ReasonUploaded, "the output pointer was uploaded")
	return r.patchRun(ctx, run, base, time.Nanosecond)
}

func (r *AnonymizationRunReconciler) pruneRun(ctx context.Context, run *api.AnonymizationRun) (ctrl.Result, error) {
	if run.Status.Output == nil || !meta.IsStatusConditionTrue(run.Status.Conditions, runConditionPublished) {
		return ctrl.Result{}, errors.New("publication must precede retention")
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(pxc.BackupGVK.GroupVersion().WithKind(pxc.BackupGVK.Kind + "List"))
	if err := r.List(ctx, list, client.InNamespace(run.Namespace), client.MatchingLabels{pxc.LabelOutputGroup: runOutputGroup(run), pxc.LabelOutput: runOutputStorage}); err != nil {
		return ctrl.Result{}, err
	}
	owners := &api.AnonymizationRunList{}
	if err := r.APIReader.List(ctx, owners, client.InNamespace(run.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	settled := map[string]bool{}
	for _, owner := range owners.Items {
		settled[string(owner.UID)] = scheduleRunSettled(&owner)
	}
	views := make([]pxc.BackupView, 0, len(list.Items))
	objects := map[string]*unstructured.Unstructured{}
	for i := range list.Items {
		object := &list.Items[i]
		view, err := pxc.DecodeBackup(object)
		if err != nil {
			return ctrl.Result{}, err
		}
		if len(object.GetOwnerReferences()) != 0 {
			continue
		}
		views = append(views, view)
		objects[view.Name] = object
	}
	slices.SortFunc(views, func(a, b pxc.BackupView) int {
		if a.Status.CompletedAt != nil && b.Status.CompletedAt != nil {
			if n := b.Status.CompletedAt.Compare(a.Status.CompletedAt.Time); n != 0 {
				return n
			}
		}
		if n := b.CreationTimestamp.Compare(a.CreationTimestamp.Time); n != 0 {
			return n
		}
		return strings.Compare(a.Name, b.Name)
	})
	keep := run.Spec.Output.Retention.KeepSucceeded
	if keep <= 0 {
		keep = 2
	}
	failedAfter := run.Spec.Output.Retention.DeleteFailedAfter.Duration
	if failedAfter <= 0 {
		failedAfter = 24 * time.Hour
	}
	base := run.DeepCopy()
	var succeeded int32
	for _, view := range views {
		remove := false
		if view.Succeeded() {
			succeeded++
			remove = succeeded > keep
		}
		if view.Status.State == pxc.BackupFailed && !view.CreationTimestamp.IsZero() {
			remove = !r.runTime().Before(view.CreationTimestamp.Add(failedAfter))
		}
		if !remove || view.Name == run.Status.Output.BackupName || !settled[objects[view.Name].GetAnnotations()["pxc-anonymizer.io/run-uid"]] {
			continue
		}
		if err := r.deleteRunObject(ctx, objects[view.Name]); err != nil {
			return ctrl.Result{}, err
		}
		if !slices.Contains(run.Status.PrunedBackups, view.Name) {
			run.Status.PrunedBackups = append(run.Status.PrunedBackups, view.Name)
		}
	}
	run.Status.Phase = api.RunPhaseCleaningUp
	return r.patchRun(ctx, run, base, time.Nanosecond)
}

func (r *AnonymizationRunReconciler) finishRun(ctx context.Context, run *api.AnonymizationRun) (ctrl.Result, error) {
	if hold := run.Spec.Cleanup.HoldTempClusterFor.Duration; hold > 0 {
		condition := meta.FindStatusCondition(run.Status.Conditions, runConditionPublished)
		if condition != nil && r.runTime().Before(condition.LastTransitionTime.Add(hold)) {
			return ctrl.Result{RequeueAfter: condition.LastTransitionTime.Add(hold).Sub(r.runTime())}, nil
		}
	}
	return r.cleanupRun(ctx, run, false)
}

func (r *AnonymizationRunReconciler) failRun(ctx context.Context, run *api.AnonymizationRun, kind, reason, message string) (ctrl.Result, error) {
	base := run.DeepCopy()
	r.markRunFailed(run, kind, reason, message)
	return r.patchRun(ctx, run, base, time.Nanosecond)
}

func (r *AnonymizationRunReconciler) markRunFailed(run *api.AnonymizationRun, kind, reason, message string) {
	run.Status.Phase = api.RunPhaseFailed
	now := metav1.NewTime(r.runTime())
	if run.Status.CompletedAt == nil {
		run.Status.CompletedAt = &now
	}
	r.setRunCondition(run, kind, metav1.ConditionFalse, reason, message)
	r.setRunCondition(run, runConditionFailed, metav1.ConditionTrue, reason, message)
	r.setRunCondition(run, runConditionComplete, metav1.ConditionFalse, reason, "the Run failed and cleanup must finish")
}

func (r *AnonymizationRunReconciler) runTime() time.Time {
	if r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

func (r *AnonymizationRunReconciler) setRunCondition(run *api.AnonymizationRun, kind string, status metav1.ConditionStatus, reason, message string) {
	conditions.Set(&run.Status.Conditions, run.Generation, r.runTime(), metav1.Condition{
		Type: kind, Status: status, Reason: reason, Message: message,
	})
}

func (r *AnonymizationRunReconciler) patchRun(ctx context.Context, run, base *api.AnonymizationRun, delay time.Duration) (ctrl.Result, error) {
	run.Status.ObservedGeneration = run.Generation
	if apiequality.Semantic.DeepEqual(run.Status, base.Status) {
		r.recordRunMetrics(ctx, run)
		return ctrl.Result{RequeueAfter: delay}, nil
	}
	err := r.Status().Patch(ctx, run, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if err == nil {
		r.recordRunMetrics(ctx, run)
	}
	return ctrl.Result{RequeueAfter: delay}, err
}

func (r *AnonymizationRunReconciler) recordRunMetrics(ctx context.Context, run *api.AnonymizationRun) {
	observation := metrics.RunObservation{StepDurations: runMetricDurations(run)}
	if temp := run.Status.TempCluster; temp != nil && temp.Name != "" && r.APIReader != nil {
		cluster := &unstructured.Unstructured{}
		cluster.SetGroupVersionKind(pxc.ClusterGVK)
		err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: temp.Name}, cluster)
		if apierrors.IsNotFound(err) {
			present := false
			observation.TempClusterPresent = &present
		} else if err == nil && temp.UID != "" && cluster.GetUID() == temp.UID && runOwns(run, cluster) == nil {
			present := true
			observation.TempClusterPresent = &present
		}
	}
	metrics.RecordRun(run, observation)
}

func runMetricDurations(run *api.AnonymizationRun) map[metrics.RunStep]time.Duration {
	durations := make(map[metrics.RunStep]time.Duration, 4)
	add := func(step metrics.RunStep, start, end *metav1.Time) {
		if start != nil && end != nil && !start.IsZero() && !end.IsZero() && !end.Before(start) {
			durations[step] = end.Sub(start.Time)
		}
	}
	if temp := run.Status.TempCluster; temp != nil {
		if restored := meta.FindStatusCondition(run.Status.Conditions, runConditionRestored); restored != nil && restored.Status == metav1.ConditionTrue {
			add(metrics.RunStepRestore, temp.ReadyAt, &restored.LastTransitionTime)
		}
	}
	if attempt := run.Status.Anonymize; attempt != nil {
		add(metrics.RunStepAnonymize, attempt.StartedAt, attempt.CompletedAt)
		if output := run.Status.Output; output != nil {
			add(metrics.RunStepBackup, attempt.CompletedAt, output.CompletedAt)
		}
	}
	if output := run.Status.Output; output != nil {
		if backedUp := meta.FindStatusCondition(run.Status.Conditions, runConditionBackedUp); backedUp != nil && backedUp.Status == metav1.ConditionTrue {
			add(metrics.RunStepPublish, &backedUp.LastTransitionTime, output.PublishedAt)
		}
	}
	return durations
}

func (r *AnonymizationRunReconciler) runExpired(start *metav1.Time, timeout, fallback time.Duration) bool {
	if timeout <= 0 {
		timeout = fallback
	}
	return start == nil || !r.runTime().Before(start.Add(timeout))
}

func (r *AnonymizationRunReconciler) runDelay(start *metav1.Time, timeout, fallback time.Duration) time.Duration {
	if timeout <= 0 {
		timeout = fallback
	}
	if start == nil {
		return time.Nanosecond
	}
	remaining := start.Add(timeout).Sub(r.runTime())
	if remaining <= 0 {
		return time.Nanosecond
	}
	return min(runPollInterval, remaining)
}

func (r *AnonymizationRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorder("anonymizationrun")
	}
	if r.podLogs == nil {
		kube, err := kubernetes.NewForConfig(mgr.GetConfig())
		if err != nil {
			return err
		}
		r.podLogs = func(ctx context.Context, namespace, name string) ([]byte, error) {
			lines, limit := int64(200), int64(128*1024)
			stream, err := kube.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{Container: runContainerName, TailLines: &lines, LimitBytes: &limit}).Stream(ctx)
			if err != nil {
				return nil, err
			}
			defer func() { _ = stream.Close() }()
			return io.ReadAll(io.LimitReader(stream, limit))
		}
	}
	cluster, restore, backup := &unstructured.Unstructured{}, &unstructured.Unstructured{}, &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(pxc.ClusterGVK)
	restore.SetGroupVersionKind(pxc.RestoreGVK)
	backup.SetGroupVersionKind(pxc.BackupGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.AnonymizationRun{}, builder.WithPredicates(runChangePredicate())).
		Owns(&batchv1.Job{}).Owns(&corev1.ConfigMap{}).Owns(cluster).Owns(restore).
		Watches(backup, handler.EnqueueRequestsFromMapFunc(r.runsForOutputBackup)).
		Named("anonymizationrun").Complete(r)
}

func runChangePredicate() predicate.Predicate {
	return predicate.Or(predicate.GenerationChangedPredicate{}, predicate.Funcs{UpdateFunc: func(update event.UpdateEvent) bool {
		return update.ObjectOld != nil && update.ObjectNew != nil && !update.ObjectOld.GetDeletionTimestamp().Equal(update.ObjectNew.GetDeletionTimestamp())
	}})
}
