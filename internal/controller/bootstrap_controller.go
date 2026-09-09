// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

//nolint:goconst // Literal upstream wire fields keep restore and compensation paths directly reviewable.
package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/conditions"
	"github.com/ydixken/pxc-anonymizer/internal/crossplane"
	"github.com/ydixken/pxc-anonymizer/internal/pointer"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
)

const (
	bootstrapFinalizer           = "pxc-anonymizer.io/bootstrap-cleanup"
	bootstrapExecutionAnnotation = "pxc-anonymizer.io/bootstrap-execution"
	bootstrapPoll                = 10 * time.Second
	bootstrapImmediate           = time.Nanosecond
	bootstrapClustersReady       = "ClustersReady"
	bootstrapPointerResolved     = "PointerResolved"
	bootstrapCrossplanePaused    = "CrossplanePaused"
	bootstrapRestored            = "Restored"
	bootstrapCrossplaneResumed   = "CrossplaneResumed"
	bootstrapCrossplaneRecreated = "CrossplaneRecreated"
	bootstrapComplete            = "Complete"
	bootstrapFailed              = "Failed"
)

// BootstrapReconciler restores targets in order and keeps compensation active after failure.
type BootstrapReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	APIReader     client.Reader
	DynamicClient dynamic.Interface
	Recorder      events.EventRecorder
	now           func() time.Time
	fetchPointer  func(context.Context, client.Reader, string, api.PointerSource) (*pointer.Document, error)
}

// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=bootstraps,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=bootstraps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=bootstraps/finalizers,verbs=update
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusterrestores,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusters,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=mysql.sql.crossplane.io,resources=databases;users;grants,verbs=get;list;patch;delete

func (r *BootstrapReconciler) bootstrapTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *BootstrapReconciler) bootstrapReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *BootstrapReconciler) bootstrapCondition(bs *api.Bootstrap, kind string, status metav1.ConditionStatus, reason, message string) {
	conditions.Set(&bs.Status.Conditions, bs.Generation, r.bootstrapTime(), metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message})
}

func (r *BootstrapReconciler) bootstrapStatus(ctx context.Context, bs, base *api.Bootstrap) error {
	bs.Status.ObservedGeneration = bs.Generation
	complete := 0
	for _, target := range bs.Status.Targets {
		if target.CompletedAt != nil {
			complete++
		}
	}
	bs.Status.TargetsSummary = fmt.Sprintf("%d/%d", complete, len(bs.Status.Targets))
	if apiequality.Semantic.DeepEqual(bs.Status, base.Status) {
		return nil
	}
	return r.Status().Patch(ctx, bs, client.MergeFrom(base))
}

func (r *BootstrapReconciler) bootstrapEvent(bs *api.Bootstrap, kind, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Eventf(bs, nil, kind, reason, reason, "%s", message)
	}
}

func bootstrapDuration(value *metav1.Duration, fallback time.Duration) time.Duration {
	if value == nil {
		return fallback
	}
	return value.Duration
}

func (r *BootstrapReconciler) bootstrapExpired(start *metav1.Time, timeout time.Duration) bool {
	return timeout > 0 && start != nil && !r.bootstrapTime().Before(start.Add(timeout))
}

func bootstrapConditionStart(bs *api.Bootstrap, name string) *metav1.Time {
	condition := meta.FindStatusCondition(bs.Status.Conditions, name)
	if condition == nil {
		return nil
	}
	return &condition.LastTransitionTime
}

func (r *BootstrapReconciler) bootstrapReadCluster(ctx context.Context, namespace, name string) (*pxc.ClusterView, error) {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(pxc.ClusterGVK)
	if err := r.bootstrapReader().Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, object); err != nil {
		return nil, err
	}
	view, err := pxc.DecodeCluster(object)
	if err != nil {
		return nil, err
	}
	if view.UID == "" || !view.DeletionTimestamp.IsZero() {
		return nil, errors.New("target cluster is deleting or has no UID")
	}
	return &view, nil
}

func (r *BootstrapReconciler) bootstrapFetch(ctx context.Context, bs *api.Bootstrap) (*pointer.Document, error) {
	source := bs.Spec.Pointer
	if source.Destination != "" {
		if source.MaxAge != nil {
			return nil, errors.New("maxAge requires a pointer publication timestamp and cannot be used with a literal destination")
		}
		if source.HTTP != nil || source.S3 != nil {
			return nil, errors.New("literal pointer cannot also use a transport")
		}
		document := &pointer.Document{Name: "literal", Destination: source.Destination, SchemaVersion: pointer.SchemaVersion}
		return document, document.Validate()
	}
	requestContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	fetch := r.fetchPointer
	if fetch == nil {
		fetch = func(ctx context.Context, reader client.Reader, namespace string, source api.PointerSource) (*pointer.Document, error) {
			return (pointer.Fetcher{Reader: reader, Namespace: namespace}).Fetch(ctx, source)
		}
	}
	document, err := fetch(requestContext, r.bootstrapReader(), bs.Namespace, api.PointerSource{HTTP: source.HTTP, S3: source.S3})
	if err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("pointer response was empty")
	}
	if err := document.Validate(); err != nil {
		return nil, err
	}
	if source.MaxAge != nil {
		if source.MaxAge.Duration <= 0 {
			return nil, errors.New("pointer maxAge must be positive")
		}
		if document.PublishedAt == nil || document.PublishedAt.IsZero() {
			if document.SchemaVersion != 1 {
				return nil, errors.New("pointer publishedAt is required for maxAge")
			}
			r.bootstrapEvent(bs, corev1.EventTypeWarning, "LegacyPointerAgeUnknown", "Legacy pointer has no publishedAt; maxAge cannot be evaluated")
		} else if r.bootstrapTime().Sub(*document.PublishedAt) > source.MaxAge.Duration || document.PublishedAt.After(r.bootstrapTime()) {
			return nil, errors.New("pointer publication timestamp is outside maxAge")
		}
	}
	return document, nil
}

func bootstrapResolvedSource(document *pointer.Document) *api.ResolvedSource {
	source := &api.ResolvedSource{BackupName: document.Name, Destination: document.Destination, PointerSchemaVersion: int32(document.SchemaVersion)}
	if document.PublishedAt != nil {
		value := metav1.NewTime(*document.PublishedAt)
		source.PointerPublishedAt = &value
	}
	if document.SourceCluster != nil {
		source.SourceCluster = document.SourceCluster.Name
	}
	return source
}

func bootstrapRestoreName(bs *api.Bootstrap, target string) string {
	sum := sha256.Sum256([]byte(string(bs.UID) + "/" + bs.Status.Execution.ID + "/" + bs.Status.ObservedTrigger + "/" + target))
	prefix := bs.Name + "-" + target
	if len(prefix) > 56 {
		prefix = strings.TrimRight(prefix[:56], "-.")
	}
	return prefix + "-" + hex.EncodeToString(sum[:3])
}

func (r *BootstrapReconciler) bootstrapCredentials(ctx context.Context, namespace string, credentials api.RestoreS3Credentials) error {
	secret := &corev1.Secret{}
	if err := r.bootstrapReader().Get(ctx, types.NamespacedName{Namespace: namespace, Name: credentials.CredentialsSecret}, secret); err != nil {
		return errors.New("restore credentials Secret is unavailable")
	}
	if len(secret.Data["AWS_ACCESS_KEY_ID"]) == 0 || len(secret.Data["AWS_SECRET_ACCESS_KEY"]) == 0 {
		return errors.New("restore credentials Secret requires standard AWS credential keys")
	}
	return nil
}

func (r *BootstrapReconciler) mapClusterToBootstraps(ctx context.Context, object client.Object) []reconcile.Request {
	list := &api.BootstrapList{}
	if err := r.List(ctx, list, client.InNamespace(object.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, bs := range list.Items {
		for _, target := range bs.Spec.Targets {
			if target.PXCCluster == object.GetName() {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&bs)})
				break
			}
		}
	}
	return requests
}

func (r *BootstrapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorder("bootstrap")
	}
	if r.DynamicClient == nil {
		var err error
		r.DynamicClient, err = dynamic.NewForConfig(mgr.GetConfig())
		if err != nil {
			return err
		}
	}
	restore := &unstructured.Unstructured{}
	restore.SetGroupVersionKind(pxc.RestoreGVK)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(pxc.ClusterGVK)
	primary := predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() || !e.ObjectOld.GetDeletionTimestamp().Equal(e.ObjectNew.GetDeletionTimestamp())
	}}
	return ctrl.NewControllerManagedBy(mgr).For(&api.Bootstrap{}, builder.WithPredicates(primary)).Owns(restore).Watches(cluster, handler.EnqueueRequestsFromMapFunc(r.mapClusterToBootstraps)).Named("bootstrap").Complete(r)
}

func (r *BootstrapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	result, err := r.reconcileBootstrap(ctx, req)
	if apierrors.IsConflict(err) {
		return ctrl.Result{RequeueAfter: bootstrapImmediate}, nil
	}
	return result, err
}

func (r *BootstrapReconciler) reconcileBootstrap(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	bs := &api.Bootstrap{}
	if err := r.Get(ctx, req.NamespacedName, bs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	base := bs.DeepCopy()
	if !bs.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(bs, bootstrapFinalizer) {
			return ctrl.Result{}, nil
		}
		done, err := r.bootstrapCompensate(ctx, bs)
		if statusErr := r.bootstrapStatus(ctx, bs, base); statusErr != nil {
			return ctrl.Result{}, errors.Join(err, statusErr)
		}
		if err != nil || !done {
			return ctrl.Result{RequeueAfter: bootstrapPoll}, err
		}
		before := bs.DeepCopy()
		controllerutil.RemoveFinalizer(bs, bootstrapFinalizer)
		return ctrl.Result{}, r.Patch(ctx, bs, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	}
	if !controllerutil.ContainsFinalizer(bs, bootstrapFinalizer) {
		controllerutil.AddFinalizer(bs, bootstrapFinalizer)
		return ctrl.Result{RequeueAfter: bootstrapImmediate}, r.Patch(ctx, bs, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	}
	if bs.Status.Phase == api.BootstrapPhaseCompleted || bs.Status.Phase == api.BootstrapPhaseFailed {
		if bs.Status.Phase == api.BootstrapPhaseFailed || bs.Spec.Trigger != bs.Status.ObservedTrigger {
			done, err := r.bootstrapCompensate(ctx, bs)
			if statusErr := r.bootstrapStatus(ctx, bs, base); statusErr != nil {
				return ctrl.Result{}, errors.Join(err, statusErr)
			}
			if err != nil || !done {
				return ctrl.Result{RequeueAfter: bootstrapPoll}, err
			}
			base = bs.DeepCopy()
		}
		if bs.Spec.Trigger == bs.Status.ObservedTrigger {
			return ctrl.Result{}, nil
		}
		r.bootstrapArchive(bs)
	}
	if bs.Status.Phase == "" || bs.Status.Phase == api.BootstrapPhasePending {
		if err := r.bootstrapInitialize(bs); err != nil {
			r.bootstrapFail(bs, "InvalidSpec", err.Error())
		}
		return ctrl.Result{RequeueAfter: bootstrapImmediate}, r.bootstrapStatus(ctx, bs, base)
	}
	if bs.Status.Execution == nil || bs.Status.Execution.ID == "" {
		r.bootstrapFail(bs, "CheckpointMissing", "Execution checkpoint identity is missing")
		return ctrl.Result{RequeueAfter: bootstrapImmediate}, r.bootstrapStatus(ctx, bs, base)
	}
	hash, err := bootstrapSpecHash(bs.Spec)
	if err != nil {
		return ctrl.Result{}, err
	}
	if hash != bs.Status.Execution.SpecHash {
		r.bootstrapFail(bs, "SpecChanged", "Active execution settings changed; compensation must finish before re-arming")
		return ctrl.Result{RequeueAfter: bootstrapImmediate}, r.bootstrapStatus(ctx, bs, base)
	}
	delay, err := r.bootstrapAdvance(ctx, bs)
	return ctrl.Result{RequeueAfter: delay}, errors.Join(err, r.bootstrapStatus(ctx, bs, base))
}

func bootstrapSpecHash(spec api.BootstrapSpec) (string, error) {
	spec.Trigger = ""
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (r *BootstrapReconciler) bootstrapInitialize(bs *api.Bootstrap) error {
	bs.Status.ObservedTrigger = bs.Spec.Trigger
	if bs.Spec.Pointer.Destination != "" && bs.Spec.Pointer.MaxAge != nil {
		return errors.New("maxAge requires a pointer publication timestamp and cannot be used with a literal destination")
	}
	if bs.UID == "" || len(bs.Spec.Targets) == 0 {
		return errors.New("a persisted Bootstrap with at least one target is required")
	}
	hash, err := bootstrapSpecHash(bs.Spec)
	if err != nil {
		return err
	}
	now := metav1.NewTime(r.bootstrapTime())
	bs.Status.Phase = api.BootstrapPhaseWaitingForClusters
	bs.Status.ObservedTrigger = bs.Spec.Trigger
	bs.Status.StartedAt = &now
	bs.Status.Execution = &api.BootstrapExecutionStatus{ID: rand.Text(), SpecHash: hash}
	seen := map[string]bool{}
	for _, target := range bs.Spec.Targets {
		if target.PXCCluster == "" || seen[target.PXCCluster] {
			return errors.New("target clusters must be nonempty and unique")
		}
		seen[target.PXCCluster] = true
		credentials := bs.Spec.Restore
		if target.Restore != nil {
			credentials = *target.Restore.DeepCopy()
		}
		bs.Status.Targets = append(bs.Status.Targets, api.BootstrapTargetStatus{PXCCluster: target.PXCCluster})
		bs.Status.Execution.Targets = append(bs.Status.Execution.Targets, api.BootstrapTargetExecution{PXCCluster: target.PXCCluster, Restore: credentials})
	}
	r.bootstrapCondition(bs, bootstrapClustersReady, metav1.ConditionFalse, "WaitingForClusters", "Waiting for all target clusters")
	return nil
}

func (r *BootstrapReconciler) bootstrapArchive(bs *api.Bootstrap) {
	history := append(slices.Clone(bs.Status.History), api.BootstrapRunRecord{Trigger: bs.Status.ObservedTrigger, StartedAt: bs.Status.StartedAt, CompletedAt: bs.Status.CompletedAt, Outcome: bs.Status.Phase})
	if bs.Status.Source != nil {
		history[len(history)-1].Destination = bs.Status.Source.Destination
	}
	if len(history) > 5 {
		history = history[len(history)-5:]
	}
	bs.Status = api.BootstrapStatus{History: history}
}

func (r *BootstrapReconciler) bootstrapFail(bs *api.Bootstrap, reason, message string) {
	bs.Status.Phase = api.BootstrapPhaseFailed
	if bs.Status.CompletedAt == nil {
		value := metav1.NewTime(r.bootstrapTime())
		bs.Status.CompletedAt = &value
	}
	r.bootstrapCondition(bs, bootstrapFailed, metav1.ConditionTrue, reason, message)
	r.bootstrapCondition(bs, bootstrapComplete, metav1.ConditionFalse, reason, "Bootstrap did not complete; compensation remains active")
}

func (r *BootstrapReconciler) bootstrapFinish(bs *api.Bootstrap) {
	bs.Status.Phase = api.BootstrapPhaseCompleted
	value := metav1.NewTime(r.bootstrapTime())
	bs.Status.CompletedAt = &value
	r.bootstrapCondition(bs, bootstrapComplete, metav1.ConditionTrue, "Completed", "All target restores and configured Crossplane operations completed")
	r.bootstrapCondition(bs, bootstrapFailed, metav1.ConditionFalse, "Completed", "Bootstrap completed")
}

func (r *BootstrapReconciler) bootstrapAdvance(ctx context.Context, bs *api.Bootstrap) (time.Duration, error) {
	switch bs.Status.Phase {
	case api.BootstrapPhaseWaitingForClusters:
		return r.bootstrapWaitClusters(ctx, bs)
	case api.BootstrapPhaseResolvingPointer:
		return r.bootstrapResolvePointer(ctx, bs)
	case api.BootstrapPhasePausingCrossplane:
		return r.bootstrapPause(ctx, bs)
	case api.BootstrapPhaseRestoring:
		return r.bootstrapRestoreTargets(ctx, bs)
	case api.BootstrapPhaseResumingCrossplane:
		return r.bootstrapResume(ctx, bs)
	case api.BootstrapPhaseRecreatingCrossplane:
		return r.bootstrapRecreate(ctx, bs)
	default:
		r.bootstrapFail(bs, "InvalidPhase", "Bootstrap phase is unsupported")
		return bootstrapImmediate, nil
	}
}

func (r *BootstrapReconciler) bootstrapWaitClusters(ctx context.Context, bs *api.Bootstrap) (time.Duration, error) {
	if r.bootstrapExpired(bs.Status.StartedAt, bootstrapDuration(bs.Spec.Timeouts.ClustersReady, 0)) {
		r.bootstrapFail(bs, "ClustersNotReady", "Target clusters did not become ready within the configured timeout")
		return bootstrapImmediate, nil
	}
	for i, target := range bs.Status.Execution.Targets {
		cluster, err := r.bootstrapReadCluster(ctx, bs.Namespace, target.PXCCluster)
		if err != nil || !cluster.Ready() {
			delay := 15 * time.Second
			if bs.Status.StartedAt != nil {
				delay += max(0, r.bootstrapTime().Sub(bs.Status.StartedAt.Time)) / 10
			}
			return min(delay, 5*time.Minute), nil
		}
		if target.UID != "" && target.UID != cluster.UID {
			r.bootstrapFail(bs, "TargetReplaced", "A target cluster UID changed")
			return bootstrapImmediate, nil
		}
		bs.Status.Execution.Targets[i].UID = cluster.UID
	}
	r.bootstrapCondition(bs, bootstrapClustersReady, metav1.ConditionTrue, "Ready", "All target clusters are ready")
	r.bootstrapCondition(bs, bootstrapPointerResolved, metav1.ConditionFalse, "ResolvingPointer", "Resolving the immutable backup source")
	bs.Status.Phase = api.BootstrapPhaseResolvingPointer
	return bootstrapImmediate, nil
}

func (r *BootstrapReconciler) bootstrapResolvePointer(ctx context.Context, bs *api.Bootstrap) (time.Duration, error) {
	document, err := r.bootstrapFetch(ctx, bs)
	if err != nil {
		if r.bootstrapExpired(bootstrapConditionStart(bs, bootstrapPointerResolved), bootstrapDuration(bs.Spec.Timeouts.Pointer, 15*time.Minute)) {
			r.bootstrapFail(bs, "PointerUnavailable", "A valid, sufficiently recent pointer was unavailable within the timeout")
			return bootstrapImmediate, nil
		}
		return bootstrapPoll, nil
	}
	for i := range bs.Status.Execution.Targets {
		target := &bs.Status.Execution.Targets[i]
		if document.S3 != nil {
			if target.Restore.EndpointURL == "" {
				target.Restore.EndpointURL = document.S3.EndpointURL
			}
			if target.Restore.Region == "" {
				target.Restore.Region = document.S3.Region
			}
		}
		if err := r.bootstrapCredentials(ctx, bs.Namespace, target.Restore); err != nil {
			r.bootstrapFail(bs, "CredentialsUnavailable", err.Error())
			return bootstrapImmediate, nil
		}
		if _, err := pxc.RenderRestore(pxc.RestoreRenderOptions{Namespace: bs.Namespace, Name: bootstrapRestoreName(bs, target.PXCCluster), ClusterName: target.PXCCluster, Destination: document.Destination, Credentials: target.Restore, Owner: *metav1.NewControllerRef(bs, api.GroupVersion.WithKind("Bootstrap"))}); err != nil {
			r.bootstrapFail(bs, "InvalidRestoreConfiguration", err.Error())
			return bootstrapImmediate, nil
		}
	}
	bs.Status.Source = bootstrapResolvedSource(document)
	r.bootstrapCondition(bs, bootstrapPointerResolved, metav1.ConditionTrue, "Resolved", "Backup source and restore credentials are resolved")
	if bs.Spec.Crossplane == nil {
		bs.Status.Phase = api.BootstrapPhaseRestoring
		return bootstrapImmediate, nil
	}
	bs.Status.Crossplane = &api.CrossplaneStatus{}
	bs.Status.Phase = api.BootstrapPhasePausingCrossplane
	r.bootstrapCondition(bs, bootstrapCrossplanePaused, metav1.ConditionFalse, "Pausing", "Pausing selected Crossplane resources")
	return bootstrapImmediate, nil
}

func (r *BootstrapReconciler) bootstrapCrossplaneClient(bs *api.Bootstrap) (*crossplane.Client, error) {
	return crossplane.New(r.DynamicClient, bs.Status.Execution.CrossplaneGroup, bs.Status.Execution.CrossplaneVersion)
}

func bootstrapRefs(refs []api.CrossplaneObjectReference) []crossplane.Reference {
	result := make([]crossplane.Reference, len(refs))
	for i, ref := range refs {
		result[i] = crossplane.Reference{Kind: ref.Kind, Name: ref.Name, UID: ref.UID}
	}
	return result
}

func bootstrapAPIRefs(refs []crossplane.Reference) []api.CrossplaneObjectReference {
	result := make([]api.CrossplaneObjectReference, len(refs))
	for i, ref := range refs {
		result[i] = api.CrossplaneObjectReference{Kind: ref.Kind, Name: ref.Name, UID: ref.UID}
	}
	return result
}

func bootstrapMergeRefs(old []api.CrossplaneObjectReference, refs []crossplane.Reference) []api.CrossplaneObjectReference {
	result := slices.Clone(old)
	for _, ref := range refs {
		converted := api.CrossplaneObjectReference{Kind: ref.Kind, Name: ref.Name, UID: ref.UID}
		if !slices.Contains(result, converted) {
			result = append(result, converted)
		}
	}
	return result
}

func (r *BootstrapReconciler) bootstrapPause(ctx context.Context, bs *api.Bootstrap) (time.Duration, error) {
	execution := bs.Status.Execution
	if r.bootstrapExpired(bootstrapConditionStart(bs, bootstrapCrossplanePaused), bootstrapDuration(bs.Spec.Timeouts.Crossplane, 15*time.Minute)) {
		r.bootstrapFail(bs, "CrossplanePauseTimeout", "Crossplane pause did not finish within the timeout")
		return bootstrapImmediate, nil
	}
	if len(execution.Selected) == 0 {
		group, version := bs.Spec.Crossplane.Group, bs.Spec.Crossplane.Version
		if group == "" {
			group = "mysql.sql.crossplane.io"
		}
		if version == "" {
			version = "v1alpha1"
		}
		cp, err := crossplane.New(r.DynamicClient, group, version)
		if err != nil {
			return bootstrapPoll, err
		}
		kinds := bs.Spec.Crossplane.Kinds
		if len(kinds) == 0 {
			kinds = []string{"databases", "users", "grants"}
		}
		selected, err := cp.Select(ctx, kinds, bs.Spec.Crossplane.Selector)
		if err != nil {
			return bootstrapPoll, nil
		}
		execution.CrossplaneGroup = group
		execution.CrossplaneVersion = version
		execution.Selected = bootstrapAPIRefs(selected)
		return bootstrapImmediate, nil
	}
	cp, err := r.bootstrapCrossplaneClient(bs)
	if err != nil {
		return bootstrapPoll, err
	}
	if bs.Spec.Crossplane.Pause == nil || *bs.Spec.Crossplane.Pause {
		paused, err := cp.Pause(ctx, string(bs.UID), bootstrapRefs(execution.Selected))
		bs.Status.Crossplane.Paused = bootstrapMergeRefs(bs.Status.Crossplane.Paused, paused)
		if err != nil {
			return bootstrapPoll, nil
		}
	}
	r.bootstrapCondition(bs, bootstrapCrossplanePaused, metav1.ConditionTrue, "Paused", "The selected pause operation is complete")
	bs.Status.Phase = api.BootstrapPhaseRestoring
	return bootstrapImmediate, nil
}

func (r *BootstrapReconciler) bootstrapReadRestore(ctx context.Context, bs *api.Bootstrap, target api.BootstrapTargetStatus, execution api.BootstrapTargetExecution) (*unstructured.Unstructured, *pxc.RestoreView, error) {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(pxc.RestoreGVK)
	if err := r.bootstrapReader().Get(ctx, types.NamespacedName{Namespace: bs.Namespace, Name: target.RestoreName}, object); err != nil {
		return nil, nil, err
	}
	if bs.Status.Execution.ID != "" && object.GetAnnotations()[bootstrapExecutionAnnotation] != bs.Status.Execution.ID {
		return nil, nil, errors.New("restore execution identity changed")
	}
	if !metav1.IsControlledBy(object, bs) || object.GetUID() == "" || (execution.RestoreUID != "" && execution.RestoreUID != object.GetUID()) || !object.GetDeletionTimestamp().IsZero() {
		return nil, nil, errors.New("restore identity or ownership changed")
	}
	view, err := pxc.DecodeRestore(object)
	if err != nil {
		return nil, nil, err
	}
	if view.Spec.PXCCluster != target.PXCCluster || view.Spec.BackupSource == nil || bs.Status.Source == nil || view.Spec.BackupSource.Destination != bs.Status.Source.Destination {
		return nil, nil, errors.New("restore source or target changed")
	}
	return object, &view, nil
}

func (r *BootstrapReconciler) bootstrapRestoreTargets(ctx context.Context, bs *api.Bootstrap) (time.Duration, error) {
	for i := range bs.Status.Targets {
		target := &bs.Status.Targets[i]
		if target.CompletedAt != nil {
			continue
		}
		if target.StartedAt == nil {
			now := metav1.NewTime(r.bootstrapTime())
			target.StartedAt = &now
			target.RestoreName = bootstrapRestoreName(bs, target.PXCCluster)
			return bootstrapImmediate, nil
		}
		return r.bootstrapRestoreTarget(ctx, bs, i)
	}
	r.bootstrapCondition(bs, bootstrapRestored, metav1.ConditionTrue, "Restored", "Every target restore succeeded and its cluster became ready")
	if bs.Spec.Crossplane == nil {
		r.bootstrapFinish(bs)
		return 0, nil
	}
	bs.Status.Phase = api.BootstrapPhaseResumingCrossplane
	r.bootstrapCondition(bs, bootstrapCrossplaneResumed, metav1.ConditionFalse, "Resuming", "Resuming our paused Crossplane resources and waiting for readiness")
	return bootstrapImmediate, nil
}

func (r *BootstrapReconciler) bootstrapRestoreTarget(ctx context.Context, bs *api.Bootstrap, index int) (time.Duration, error) {
	target := &bs.Status.Targets[index]
	execution := &bs.Status.Execution.Targets[index]
	if r.bootstrapExpired(target.StartedAt, bootstrapDuration(bs.Spec.Timeouts.Restore, 3*time.Hour)) {
		r.bootstrapFail(bs, "RestoreTimeout", "The target restore and readiness wait exceeded its timeout")
		return bootstrapImmediate, nil
	}
	cluster, err := r.bootstrapReadCluster(ctx, bs.Namespace, target.PXCCluster)
	if err != nil || cluster.UID != execution.UID {
		r.bootstrapFail(bs, "TargetUnavailable", "The original target cluster is unavailable")
		return bootstrapImmediate, nil
	}
	object, view, err := r.bootstrapReadRestore(ctx, bs, *target, *execution)
	if apierrors.IsNotFound(err) {
		if target.State == string(pxc.RestoreSucceeded) {
			if cluster.Ready() {
				now := metav1.NewTime(r.bootstrapTime())
				target.CompletedAt = &now
				return bootstrapImmediate, nil
			}
			return bootstrapPoll, nil
		}
		if execution.RestoreAttempted || execution.RestoreUID != "" {
			r.bootstrapFail(bs, "RestoreVanished", "The restore is absent after a durable creation attempt; refusing to repeat it")
			return bootstrapImmediate, nil
		}
		if !cluster.Ready() {
			return bootstrapPoll, nil
		}
		object, err = pxc.RenderRestore(pxc.RestoreRenderOptions{Namespace: bs.Namespace, Name: target.RestoreName, ClusterName: target.PXCCluster, Destination: bs.Status.Source.Destination, Credentials: execution.Restore, ContainerOptions: bs.Spec.Targets[index].ContainerOptions, Owner: *metav1.NewControllerRef(bs, api.GroupVersion.WithKind("Bootstrap"))})
		if err != nil {
			r.bootstrapFail(bs, "InvalidRestoreConfiguration", err.Error())
			return bootstrapImmediate, nil
		}
		object.SetAnnotations(map[string]string{"argocd.argoproj.io/compare-options": "IgnoreExtraneous", bootstrapExecutionAnnotation: bs.Status.Execution.ID})
		intentBase := bs.DeepCopy()
		execution.RestoreAttempted = true
		if err := r.bootstrapStatus(ctx, bs, intentBase); err != nil {
			bs.Status = intentBase.Status
			return bootstrapPoll, err
		}
		if err := r.Create(ctx, object); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return bootstrapImmediate, nil
			}
			return bootstrapPoll, err
		}
		bs.Status.Execution.Targets[index].RestoreUID = object.GetUID()
		return bootstrapImmediate, nil
	}
	if err != nil {
		r.bootstrapFail(bs, "RestoreIdentityChanged", "The recorded restore no longer matches this execution")
		return bootstrapImmediate, nil
	}
	execution.RestoreAttempted = true
	execution.RestoreUID = object.GetUID()
	target.State = string(view.Status.State)
	if view.Status.State == pxc.RestoreFailed {
		target.Message = "PXC restore failed"
		r.bootstrapFail(bs, "RestoreFailed", target.Message)
		return bootstrapImmediate, nil
	}
	if !view.Succeeded() {
		return bootstrapPoll, nil
	}
	if !cluster.Ready() {
		return bootstrapPoll, nil
	}
	now := metav1.NewTime(r.bootstrapTime())
	target.CompletedAt = &now
	return bootstrapImmediate, nil
}

func (r *BootstrapReconciler) bootstrapResume(ctx context.Context, bs *api.Bootstrap) (time.Duration, error) {
	cp, err := r.bootstrapCrossplaneClient(bs)
	if err != nil {
		return bootstrapPoll, err
	}
	if r.bootstrapExpired(bootstrapConditionStart(bs, bootstrapCrossplaneResumed), bootstrapDuration(bs.Spec.Crossplane.ReadinessTimeout, 15*time.Minute)) {
		r.bootstrapFail(bs, "CrossplaneReadinessTimeout", "Crossplane resources did not become Ready and Synced within the timeout")
		return bootstrapImmediate, nil
	}
	if bs.Spec.Crossplane.Resume == nil || *bs.Spec.Crossplane.Resume {
		pending := bootstrapUnresumed(bs.Status.Crossplane)
		if len(pending) > 0 {
			resumed, err := cp.Resume(ctx, string(bs.UID), bootstrapRefs(pending))
			bs.Status.Crossplane.Resumed = bootstrapMergeRefs(bs.Status.Crossplane.Resumed, resumed)
			if err != nil {
				return bootstrapPoll, nil
			}
			return bootstrapImmediate, nil
		}
	}
	pending, err := cp.Ready(ctx, bootstrapRefs(bs.Status.Execution.Selected))
	bs.Status.Crossplane.Pending = bootstrapAPIRefs(pending)
	if err != nil || len(pending) > 0 {
		return bootstrapPoll, nil
	}
	r.bootstrapCondition(bs, bootstrapCrossplaneResumed, metav1.ConditionTrue, "ReadyAndSynced", "Selected Crossplane resources are Ready and Synced")
	if bs.Spec.Crossplane.Recreate == nil {
		r.bootstrapFinish(bs)
		return 0, nil
	}
	bs.Status.Phase = api.BootstrapPhaseRecreatingCrossplane
	r.bootstrapCondition(bs, bootstrapCrossplaneRecreated, metav1.ConditionFalse, "Recreating", "Recreating the configured managed resources")
	return bootstrapImmediate, nil
}

func bootstrapUnresumed(status *api.CrossplaneStatus) []api.CrossplaneObjectReference {
	if status == nil {
		return nil
	}
	pending := make([]api.CrossplaneObjectReference, 0)
	for _, ref := range status.Paused {
		if !slices.Contains(status.Resumed, ref) {
			pending = append(pending, ref)
		}
	}
	return pending
}

func (r *BootstrapReconciler) bootstrapRecreate(ctx context.Context, bs *api.Bootstrap) (time.Duration, error) {
	cp, err := r.bootstrapCrossplaneClient(bs)
	if err != nil {
		return bootstrapPoll, err
	}
	if r.bootstrapExpired(bootstrapConditionStart(bs, bootstrapCrossplaneRecreated), bootstrapDuration(bs.Spec.Crossplane.ReadinessTimeout, 15*time.Minute)) {
		r.bootstrapFail(bs, "CrossplaneRecreationTimeout", "Managed resource recreation did not finish within the timeout")
		return bootstrapImmediate, nil
	}
	config := bs.Spec.Crossplane.Recreate
	kinds := config.Kinds
	if len(kinds) == 0 {
		kinds = []string{"users", "grants"}
	}
	refs := make([]crossplane.Reference, 0)
	for _, kind := range kinds {
		for _, ref := range bootstrapRefs(bs.Status.Execution.Selected) {
			if ref.Kind == kind {
				refs = append(refs, ref)
			}
		}
	}
	progress := make([]crossplane.Recreation, len(bs.Status.Crossplane.Recreating))
	for i, state := range bs.Status.Crossplane.Recreating {
		progress[i] = crossplane.Recreation{Reference: crossplane.Reference{Kind: state.Kind, Name: state.Name, UID: state.UID}, DeletionObserved: state.DeletionObserved, Complete: state.Complete}
	}
	next, done, err := cp.Recreate(ctx, refs, progress, crossplane.RecreateOptions{RemoveFinalizers: config.RemoveFinalizers, WaitForRecreation: config.WaitForRecreation})
	bs.Status.Crossplane.Recreating = make([]api.CrossplaneRecreation, len(next))
	for i, state := range next {
		bs.Status.Crossplane.Recreating[i] = api.CrossplaneRecreation{Kind: state.Kind, Name: state.Name, UID: state.UID, DeletionObserved: state.DeletionObserved, Complete: state.Complete}
		if state.Complete {
			bs.Status.Crossplane.Recreated = bootstrapMergeRefs(bs.Status.Crossplane.Recreated, []crossplane.Reference{state.Reference})
		}
	}
	if err != nil {
		r.bootstrapFail(bs, "CrossplaneRecreationFailed", "Managed resource recreation was rejected or failed")
		return bootstrapImmediate, nil
	}
	if !done {
		return bootstrapPoll, nil
	}
	r.bootstrapCondition(bs, bootstrapCrossplaneRecreated, metav1.ConditionTrue, "Recreated", "Configured managed resources completed recreation")
	r.bootstrapFinish(bs)
	return 0, nil
}

// Compensation never cancels a running restore or resumes a pause belonging to another owner.
func (r *BootstrapReconciler) bootstrapCompensate(ctx context.Context, bs *api.Bootstrap) (bool, error) {
	execution := bs.Status.Execution
	if execution == nil {
		return true, nil
	}
	for i, target := range bs.Status.Targets {
		if target.RestoreName == "" || target.CompletedAt != nil {
			continue
		}
		if i >= len(execution.Targets) {
			return false, errors.New("target execution checkpoint is incomplete")
		}
		_, restore, err := r.bootstrapReadRestore(ctx, bs, target, execution.Targets[i])
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		if err == nil && !restore.IsTerminal() {
			return false, nil
		}
		if execution.Targets[i].RestoreUID == "" {
			if err == nil {
				execution.Targets[i].RestoreUID = restore.UID
				return false, nil
			}
			if !execution.Targets[i].RestoreAttempted {
				continue
			}
		}
		ready, err := r.bootstrapUnpauseCluster(ctx, bs, target, execution.Targets[i])
		if err != nil || !ready {
			return false, err
		}
	}
	if len(execution.Selected) == 0 {
		return true, nil
	}
	if bs.Status.Crossplane == nil {
		bs.Status.Crossplane = &api.CrossplaneStatus{}
	}
	recovered, err := r.bootstrapRecoverPauses(ctx, bs)
	if err != nil {
		return false, err
	}
	if recovered {
		return false, nil
	}
	refs := bootstrapUnresumed(bs.Status.Crossplane)
	if len(refs) == 0 {
		return true, nil
	}
	present := make([]crossplane.Reference, 0, len(refs))
	for _, ref := range refs {
		object, err := r.bootstrapManagedResource(ctx, bs, ref)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if object.GetUID() != ref.UID {
			if object.GetAnnotations()[crossplane.OwnerAnnotation] == string(bs.UID) {
				return false, errors.New("replacement managed resource retains the pause owner token")
			}
			continue
		}
		present = append(present, crossplane.Reference{Kind: ref.Kind, Name: ref.Name, UID: ref.UID})
	}
	if len(present) == 0 {
		return true, nil
	}
	cp, err := r.bootstrapCrossplaneClient(bs)
	if err != nil {
		return false, err
	}
	resumed, err := cp.Resume(ctx, string(bs.UID), present)
	bs.Status.Crossplane.Resumed = bootstrapMergeRefs(bs.Status.Crossplane.Resumed, resumed)
	return err == nil, err
}

func (r *BootstrapReconciler) bootstrapManagedResource(ctx context.Context, bs *api.Bootstrap, ref api.CrossplaneObjectReference) (*unstructured.Unstructured, error) {
	if r.DynamicClient == nil {
		return nil, errors.New("crossplane dynamic client is unavailable")
	}
	group := schema.GroupVersion{Group: bs.Status.Execution.CrossplaneGroup, Version: bs.Status.Execution.CrossplaneVersion}
	return r.DynamicClient.Resource(group.WithResource(ref.Kind)).Get(ctx, ref.Name, metav1.GetOptions{})
}

func (r *BootstrapReconciler) bootstrapRecoverPauses(ctx context.Context, bs *api.Bootstrap) (bool, error) {
	changed := false
	for _, ref := range bs.Status.Execution.Selected {
		object, err := r.bootstrapManagedResource(ctx, bs, ref)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if object.GetAnnotations()[crossplane.OwnerAnnotation] != string(bs.UID) {
			continue
		}
		if object.GetUID() != ref.UID {
			return false, errors.New("pause ownership is attached to a replacement managed resource")
		}
		if !slices.Contains(bs.Status.Crossplane.Paused, ref) {
			bs.Status.Crossplane.Paused = append(bs.Status.Crossplane.Paused, ref)
			changed = true
		}
	}
	return changed, nil
}

func (r *BootstrapReconciler) bootstrapUnpauseCluster(ctx context.Context, bs *api.Bootstrap, target api.BootstrapTargetStatus, execution api.BootstrapTargetExecution) (bool, error) {
	if execution.UID == "" {
		return true, nil
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(pxc.ClusterGVK)
	err := r.bootstrapReader().Get(ctx, types.NamespacedName{Namespace: bs.Namespace, Name: target.PXCCluster}, cluster)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if cluster.GetUID() != execution.UID {
		return false, errors.New("cannot compensate a replacement target cluster")
	}
	paused, _, err := unstructured.NestedBool(cluster.Object, "spec", "pause")
	if err != nil {
		return false, err
	}
	if !paused {
		return true, nil
	}
	jobs := &batchv1.JobList{}
	if err := r.bootstrapReader().List(ctx, jobs, client.InNamespace(bs.Namespace)); err != nil {
		return false, err
	}
	for _, job := range jobs.Items {
		if job.Name == "restore-job-"+target.RestoreName+"-"+target.PXCCluster {
			return false, nil
		}
		for _, owner := range job.OwnerReferences {
			if owner.APIVersion == pxc.RestoreGVK.GroupVersion().String() && owner.Kind == pxc.RestoreGVK.Kind && owner.Name == target.RestoreName {
				return false, nil
			}
		}
	}
	base := cluster.DeepCopy()
	if err := unstructured.SetNestedField(cluster.Object, false, "spec", "pause"); err != nil {
		return false, err
	}
	if err := r.Patch(ctx, cluster, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return false, err
	}
	r.bootstrapEvent(bs, corev1.EventTypeNormal, "ClusterUnpaused", "Unpaused target cluster after the restore ended or vanished")
	return true, nil
}
