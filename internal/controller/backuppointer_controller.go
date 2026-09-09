/*
Copyright 2026 pxc-anonymizer contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/conditions"
	"github.com/ydixken/pxc-anonymizer/internal/objectstore"
	"github.com/ydixken/pxc-anonymizer/internal/pointer"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
)

const (
	conditionBackupSelected = "BackupSelected"
	conditionPublished      = "Published"
	conditionFresh          = "Fresh"
	conditionReady          = "Ready"
	pointerRetryInterval    = 10 * time.Second
)

// BackupPointerReconciler publishes and verifies a pointer without owning the backup data.
type BackupPointerReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	APIReader client.Reader

	// Tests can exercise publication and elapsed time without external services.
	resolveStore func(context.Context, client.Reader, string, api.ObjectStorageSpec) (pointer.Store, error)
	now          func() time.Time
}

// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=backuppointers,verbs=get;list;watch
// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=backuppointers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusterbackups,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *BackupPointerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	result, err := r.reconcilePointer(ctx, req)
	if apierrors.IsConflict(err) {
		//nolint:staticcheck // Conflicts retry from fresh state without reporting a failed operation.
		return ctrl.Result{Requeue: true}, nil
	}
	return result, err
}

func (r *BackupPointerReconciler) reconcilePointer(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	bp := &api.BackupPointer{}
	if err := r.Get(ctx, req.NamespacedName, bp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !bp.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	base := bp.DeepCopy()
	if bp.Spec.Suspend {
		bp.Status.Phase = api.BackupPointerPhaseSuspended
		r.setPointerCondition(bp, conditionReady, metav1.ConditionFalse, api.ReasonSuspended, "pointer reconciliation is suspended")
		return ctrl.Result{}, r.patchPointerStatus(ctx, bp, base)
	}
	backups, err := r.pointerBackups(ctx, bp)
	if err != nil {
		bp.Status.Phase = api.BackupPointerPhaseError
		r.setPointerCondition(bp, conditionBackupSelected, metav1.ConditionFalse, api.ReasonNoSucceededBackup,
			"backup selection could not be completed")
		r.setPointerCondition(bp, conditionReady, metav1.ConditionFalse, api.ReasonPointerCurrent, "no backup selection is available")
		return ctrl.Result{}, errors.Join(err, r.patchPointerStatus(ctx, bp, base))
	}
	bp.Status.Backups = countPointerBackups(backups)
	candidates := make([]pxc.BackupView, 0, len(backups))
	for _, backup := range backups {
		if !backup.Succeeded() || strings.HasPrefix(backup.Status.Destination, "s3://") {
			candidates = append(candidates, backup)
		}
	}
	selection := pxc.LatestSucceeded(candidates)
	if selection.Backup == nil {
		r.noPointerCandidate(bp, backups, selection.MissingCompletion)
		return ctrl.Result{RequeueAfter: r.pointerStaleDelay(bp)}, r.patchPointerStatus(ctx, bp, base)
	}
	selected := selection.Backup
	reason, message := selectedPointerReason(backups, selected)
	r.setPointerCondition(bp, conditionBackupSelected, metav1.ConditionTrue, reason, message)

	current := bp.Status.Current
	needsPublish := current == nil || current.BackupName != selected.Name ||
		current.Destination != selected.Status.Destination || current.SchemaVersion != pointer.SchemaVersion ||
		base.Status.ObservedGeneration != bp.Generation || !meta.IsStatusConditionTrue(bp.Status.Conditions, conditionPublished)
	verifyDue := bp.Status.NextVerifyTime == nil || !r.pointerTime().Before(bp.Status.NextVerifyTime.Time)
	if needsPublish || verifyDue {
		storeContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		store, resolveErr := r.pointerStore(storeContext, bp)
		if resolveErr != nil {
			return r.pointerPublicationFailed(ctx, bp, base, resolveErr)
		}
		if !needsPublish {
			metadata, headErr := store.Head(storeContext, bp.Spec.Target.Key)
			if headErr != nil {
				return r.pointerPublicationFailed(ctx, bp, base, headErr)
			}
			needsPublish = !metadata.Found || metadata.ETag != current.ETag
		}
		if needsPublish {
			if publishErr := r.publishPointer(storeContext, bp, selected, store); publishErr != nil {
				return r.pointerPublicationFailed(ctx, bp, base, publishErr)
			}
		} else {
			r.setPointerCondition(bp, conditionPublished, metav1.ConditionTrue, api.ReasonVerified,
				"the published pointer object matches its recorded ETag")
		}
		nextVerify := metav1.NewTime(r.pointerTime().Add(pointerVerifyInterval(bp)))
		bp.Status.NextVerifyTime = &nextVerify
	}
	r.pointerFreshness(bp)
	bp.Status.Phase = api.BackupPointerPhasePublished
	if meta.IsStatusConditionFalse(bp.Status.Conditions, conditionFresh) {
		bp.Status.Phase = api.BackupPointerPhaseStale
	}
	r.setPointerCondition(bp, conditionReady, metav1.ConditionTrue, api.ReasonPointerCurrent,
		"the selected successful backup is published")
	return ctrl.Result{RequeueAfter: r.pointerNextDelay(bp)}, r.patchPointerStatus(ctx, bp, base)
}

func (r *BackupPointerReconciler) pointerBackups(ctx context.Context, bp *api.BackupPointer) ([]pxc.BackupView, error) {
	selector := labels.Everything()
	if bp.Spec.Source.Selector != nil {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(bp.Spec.Source.Selector)
		if err != nil {
			return nil, fmt.Errorf("parse backup selector: %w", err)
		}
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(pxc.BackupGVK.GroupVersion().WithKind(pxc.BackupGVK.Kind + "List"))
	if err := r.List(ctx, list, client.InNamespace(bp.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("list source backups: %w", err)
	}
	backups := make([]pxc.BackupView, 0, len(list.Items))
	for i := range list.Items {
		cluster, _, err := unstructured.NestedString(list.Items[i].Object, "spec", "pxcCluster")
		if err != nil || cluster != bp.Spec.Source.PXCCluster {
			continue
		}
		backup, err := pxc.DecodeBackup(&list.Items[i])
		if err != nil {
			return nil, fmt.Errorf("decode source backup: %w", err)
		}
		if len(bp.Spec.Source.StorageNames) != 0 && !slices.Contains(bp.Spec.Source.StorageNames, pointerStorageName(&backup)) {
			continue
		}
		backups = append(backups, backup)
	}
	return backups, nil
}

func countPointerBackups(backups []pxc.BackupView) api.BackupCounts {
	var counts api.BackupCounts
	for _, backup := range backups {
		switch backup.Status.State {
		case pxc.BackupSucceeded:
			counts.Succeeded++
		case pxc.BackupFailed:
			counts.Failed++
		case pxc.BackupRunning:
			counts.Running++
		case pxc.BackupStarting:
			counts.Starting++
		}
	}
	return counts
}

func selectedPointerReason(backups []pxc.BackupView, selected *pxc.BackupView) (string, string) {
	failed := 0
	var running *pxc.BackupView
	for i := range backups {
		backup := &backups[i]
		if !backup.CreationTimestamp.After(selected.CreationTimestamp.Time) {
			continue
		}
		switch backup.Status.State {
		case pxc.BackupFailed:
			failed++
		case pxc.BackupRunning, pxc.BackupStarting:
			if running == nil || backup.CreationTimestamp.After(running.CreationTimestamp.Time) ||
				(backup.CreationTimestamp.Equal(&running.CreationTimestamp) && backup.Name < running.Name) {
				running = backup
			}
		}
	}
	if failed > 0 {
		return api.ReasonNewerBackupFailed, fmt.Sprintf("%d newer backups failed; selected successful backup %s", failed, selected.Name)
	}
	if running != nil {
		return api.ReasonNewerBackupInProgress, fmt.Sprintf("newer backup %s is in progress; selected successful backup %s", running.Name, selected.Name)
	}
	return api.ReasonLatestSucceeded, "selected the latest successful backup " + selected.Name
}

func (r *BackupPointerReconciler) noPointerCandidate(bp *api.BackupPointer, backups []pxc.BackupView, missing int) {
	bp.Status.Phase = api.BackupPointerPhaseNoCandidate
	reason := api.ReasonNoSucceededBackup
	message := "no successful S3 backup with a completion timestamp is available"
	if missing > 0 {
		message = fmt.Sprintf("%d successful backups have no completion timestamp", missing)
	}
	if bp.Status.Current != nil && !slices.ContainsFunc(backups, func(backup pxc.BackupView) bool {
		return backup.Name == bp.Status.Current.BackupName
	}) {
		bp.Status.Phase = api.BackupPointerPhaseDangling
		reason = api.ReasonSelectedBackupDeleted
		message = "the selected backup was deleted and no replacement is available"
	}
	r.setPointerCondition(bp, conditionBackupSelected, metav1.ConditionFalse, reason, message)
	readyReason := api.ReasonPointerCurrent
	if bp.Status.Phase == api.BackupPointerPhaseDangling {
		readyReason = api.ReasonDangling
	}
	r.setPointerCondition(bp, conditionReady, metav1.ConditionFalse, readyReason, message)
	r.pointerFreshness(bp)
}

func (r *BackupPointerReconciler) publishPointer(
	ctx context.Context, bp *api.BackupPointer, selected *pxc.BackupView, store pointer.Store,
) error {
	publishedAt := r.pointerTime()
	document := pointer.Document{
		Name: selected.Name, Destination: selected.Status.Destination, SchemaVersion: pointer.SchemaVersion,
		PublishedAt:   &publishedAt,
		PublishedBy:   &pointer.Publisher{Kind: "BackupPointer", Namespace: bp.Namespace, Name: bp.Name, UID: string(bp.UID)},
		SourceCluster: &pointer.SourceCluster{Name: selected.Spec.PXCCluster, Namespace: selected.Namespace},
		Backup: &pointer.Backup{StorageName: pointerStorageName(selected), CompletedAt: &selected.Status.CompletedAt.Time,
			State: string(selected.Status.State)},
		PublicURL: bp.Spec.Target.PublicURL,
	}
	if selected.Status.S3 != nil {
		document.S3 = &pointer.S3{Bucket: selected.Status.S3.Bucket,
			EndpointURL: selected.Status.S3.EndpointURL, Region: selected.Status.S3.Region}
	}
	if document.S3 == nil || document.S3.Bucket == "" {
		sourceURL, err := url.Parse(selected.Status.Destination)
		if err != nil {
			return errors.New("backup destination is invalid")
		}
		if document.S3 == nil {
			document.S3 = &pointer.S3{}
		}
		document.S3.Bucket = sourceURL.Host
	}
	if err := document.Validate(); err != nil {
		return err
	}
	etag, err := store.PutJSON(ctx, bp.Spec.Target.Key, document)
	if err != nil {
		return err
	}
	if bp.Status.Current != nil {
		bp.Status.Previous = bp.Status.Current.DeepCopy()
	}
	now := metav1.NewTime(publishedAt)
	bp.Status.Current = &api.PublishedBackup{
		BackupName: selected.Name, Destination: selected.Status.Destination, StorageName: pointerStorageName(selected),
		CompletedAt: selected.Status.CompletedAt.DeepCopy(), PublishedAt: &now, ETag: etag, SchemaVersion: pointer.SchemaVersion,
	}
	r.setPointerCondition(bp, conditionPublished, metav1.ConditionTrue, api.ReasonUploaded, "the selected backup pointer was uploaded")
	return nil
}

func pointerStorageName(backup *pxc.BackupView) string {
	if backup.Spec.StorageName != "" {
		return backup.Spec.StorageName
	}
	return backup.Status.StorageName
}

func (r *BackupPointerReconciler) pointerPublicationFailed(
	ctx context.Context, bp, base *api.BackupPointer, err error,
) (ctrl.Result, error) {
	reason, message := api.ReasonUploadFailed, "pointer upload or verification failed"
	switch {
	case apierrors.IsNotFound(err):
		reason, message = api.ReasonCredentialsUnavailable, "an object storage credentials or CA Secret is unavailable"
	case errors.Is(err, objectstore.ErrCredentialsInvalid):
		reason, message = api.ReasonCredentialsInvalid, "object storage credentials are incomplete or were rejected"
	case errors.Is(err, objectstore.ErrEndpointUnreachable), errors.Is(err, context.DeadlineExceeded):
		reason, message = api.ReasonEndpointUnreachable, "the object storage endpoint is invalid or unreachable"
	}
	bp.Status.Phase = api.BackupPointerPhaseError
	r.setPointerCondition(bp, conditionPublished, metav1.ConditionFalse, reason, message)
	r.setPointerCondition(bp, conditionReady, metav1.ConditionFalse, api.ReasonPointerCurrent, message)
	r.pointerFreshness(bp)
	return ctrl.Result{RequeueAfter: pointerRetryInterval}, r.patchPointerStatus(ctx, bp, base)
}

func (r *BackupPointerReconciler) pointerFreshness(bp *api.BackupPointer) {
	if r.pointerStaleDelay(bp) == 0 {
		r.setPointerCondition(bp, conditionFresh, metav1.ConditionFalse, api.ReasonStaleBackup,
			"no published backup is within staleAfter")
		return
	}
	r.setPointerCondition(bp, conditionFresh, metav1.ConditionTrue, api.ReasonWithinStaleAfter,
		"the published backup completed within staleAfter")
}

func pointerVerifyInterval(bp *api.BackupPointer) time.Duration {
	if bp.Spec.VerifyInterval != nil && bp.Spec.VerifyInterval.Duration > 0 {
		return bp.Spec.VerifyInterval.Duration
	}
	return time.Hour
}

func (r *BackupPointerReconciler) pointerNextDelay(bp *api.BackupPointer) time.Duration {
	delay := bp.Status.NextVerifyTime.Sub(r.pointerTime())
	if staleIn := r.pointerStaleDelay(bp); staleIn > 0 && staleIn < delay {
		delay = staleIn
	}
	if delay <= 0 {
		return time.Nanosecond
	}
	return delay
}

func (r *BackupPointerReconciler) pointerStaleDelay(bp *api.BackupPointer) time.Duration {
	staleAfter := 36 * time.Hour
	if bp.Spec.StaleAfter != nil {
		staleAfter = bp.Spec.StaleAfter.Duration
	}
	if bp.Status.Current != nil && bp.Status.Current.CompletedAt != nil {
		if delay := bp.Status.Current.CompletedAt.Add(staleAfter).Sub(r.pointerTime()); delay > 0 {
			return delay
		}
	}
	return 0
}

func (r *BackupPointerReconciler) pointerStore(ctx context.Context, bp *api.BackupPointer) (pointer.Store, error) {
	if r.resolveStore != nil {
		return r.resolveStore(ctx, r.APIReader, bp.Namespace, bp.Spec.Target.ObjectStorage)
	}
	return objectstore.Resolve(ctx, r.APIReader, bp.Namespace, bp.Spec.Target.ObjectStorage)
}

func (r *BackupPointerReconciler) pointerTime() time.Time {
	if r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

func (r *BackupPointerReconciler) setPointerCondition(
	bp *api.BackupPointer, conditionType string, status metav1.ConditionStatus, reason, message string,
) {
	conditions.Set(&bp.Status.Conditions, bp.Generation, r.pointerTime(), metav1.Condition{
		Type: conditionType, Status: status, Reason: reason, Message: message,
	})
}

func (r *BackupPointerReconciler) patchPointerStatus(ctx context.Context, bp, base *api.BackupPointer) error {
	bp.Status.ObservedGeneration = bp.Generation
	if apiequality.Semantic.DeepEqual(bp.Status, base.Status) {
		return nil
	}
	return r.Status().Patch(ctx, bp, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func (r *BackupPointerReconciler) mapBackupToPointers(ctx context.Context, obj client.Object) []reconcile.Request {
	backup, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	cluster, _, err := unstructured.NestedString(backup.Object, "spec", "pxcCluster")
	if err != nil || cluster == "" {
		return nil
	}
	pointers := &api.BackupPointerList{}
	if err := r.List(ctx, pointers, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "map backup event to pointers")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(pointers.Items))
	for _, bp := range pointers.Items {
		if bp.Spec.Source.PXCCluster == cluster {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&bp)})
		}
	}
	return requests
}

func backupPointerBackupChanged(e event.UpdateEvent) bool {
	before, oldOK := e.ObjectOld.(*unstructured.Unstructured)
	after, newOK := e.ObjectNew.(*unstructured.Unstructured)
	if !oldOK || !newOK {
		return false
	}
	for _, field := range []string{"state", "completed", "destination"} {
		oldValue, _, oldErr := unstructured.NestedString(before.Object, "status", field)
		newValue, _, newErr := unstructured.NestedString(after.Object, "status", field)
		if oldErr != nil || newErr != nil || oldValue != newValue {
			return true
		}
	}
	return !apiequality.Semantic.DeepEqual(before.GetLabels(), after.GetLabels()) || before.GetGeneration() != after.GetGeneration()
}

func (r *BackupPointerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	backup := &unstructured.Unstructured{}
	backup.SetGroupVersionKind(pxc.BackupGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.BackupPointer{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(backup, handler.EnqueueRequestsFromMapFunc(r.mapBackupToPointers), builder.WithPredicates(predicate.Funcs{
			CreateFunc:  func(event.CreateEvent) bool { return true },
			UpdateFunc:  backupPointerBackupChanged,
			DeleteFunc:  func(event.DeleteEvent) bool { return true },
			GenericFunc: func(event.GenericEvent) bool { return false },
		})).
		Named("backuppointer").
		Complete(r)
}
