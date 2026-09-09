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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // embed the IANA database so spec.timeZone resolves in minimal images

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/conditions"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
)

// Keys and tuning constants shared between a schedule and the runs it owns.
const (
	// scheduleNameLabel and scheduleOutputGroupLabel are authoritative on every child.
	scheduleNameLabel        = "pxc-anonymizer.io/schedule"
	scheduleOutputGroupLabel = pxc.LabelOutputGroup

	// scheduleRunNowAnnotation is a trigger token; presence alone triggers a manual run.
	scheduleRunNowAnnotation = "pxc-anonymizer.io/run-now"
	// scheduleRunNowHashAnnotation records the full token hash so short-hash collisions are detectable.
	scheduleRunNowHashAnnotation = "pxc-anonymizer.io/run-now-token-hash"

	scheduleValidConditionType = "ScheduleValid"
	scheduleReadyConditionType = "Ready"

	// A terminal run only settles once cleanup reported an outcome.
	scheduledRunCleanedUpCondition = "CleanedUp"
	scheduledRunRetainedCondition  = "Retained"

	anonymizationScheduleKind = "AnonymizationSchedule"

	scheduleRunTimeLayout   = "200601021504"
	scheduleMaxMissedTicks  = 100
	scheduleMaxNameLength   = 253
	scheduleShortHashLength = 6
	scheduleDeletionRequeue = 10 * time.Second
)

// scheduleCronParser accepts the standard five fields plus descriptors such as @daily.
var scheduleCronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// AnonymizationScheduleReconciler reconciles a AnonymizationSchedule object
type AnonymizationScheduleReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// now is injected by tests; production falls back to the wall clock.
	now func() time.Time
}

// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=anonymizationschedules,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=anonymizationschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=anonymizationschedules/finalizers,verbs=update
// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=anonymizationruns,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile retries conflicts against fresh state without reporting a failed operation.
func (r *AnonymizationScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	result, err := r.reconcileSchedule(ctx, req)
	if apierrors.IsConflict(err) {
		return scheduleRequeueOnConflict(err)
	}
	return result, err
}

func (r *AnonymizationScheduleReconciler) reconcileSchedule(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var schedule api.AnonymizationSchedule
	if err := r.Get(ctx, req.NamespacedName, &schedule); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !schedule.DeletionTimestamp.IsZero() {
		// Garbage collection removes the owned runs; nothing to schedule.
		return ctrl.Result{}, nil
	}

	now := r.clock()
	savedStatus := *schedule.Status.DeepCopy()

	owned, err := r.ownedRuns(ctx, &schedule)
	if err != nil {
		return ctrl.Result{}, err
	}
	active := scheduleActiveRuns(owned)

	schedule.Status.ObservedGeneration = schedule.Generation
	applyScheduleRunStatus(&schedule, owned, active)

	cronSchedule, location, parseErr := parseScheduleSpec(&schedule)
	if parseErr != nil {
		log.Info("schedule is not usable", "reason", parseErr.Error())
		conditions.Set(&schedule.Status.Conditions, schedule.Generation, now, metav1.Condition{
			Type:    scheduleValidConditionType,
			Status:  metav1.ConditionFalse,
			Reason:  api.ReasonInvalidCron,
			Message: parseErr.Error(),
		})
		setScheduleReady(&schedule, now, metav1.ConditionFalse, api.ReasonInvalidCron,
			"no run is created until spec.schedule and spec.timeZone are valid")
		schedule.Status.NextScheduleTime = nil
		if pruneErr := r.pruneHistory(ctx, &schedule, owned); pruneErr != nil {
			return ctrl.Result{}, pruneErr
		}
		if statusErr := r.persistStatus(ctx, &schedule, savedStatus); statusErr != nil {
			return scheduleRequeueOnConflict(statusErr)
		}
		return ctrl.Result{}, nil
	}
	conditions.Set(&schedule.Status.Conditions, schedule.Generation, now, metav1.Condition{
		Type:    scheduleValidConditionType,
		Status:  metav1.ConditionTrue,
		Reason:  api.ReasonParsed,
		Message: fmt.Sprintf("cron %q parsed in time zone %s", strings.TrimSpace(schedule.Spec.Schedule), location.String()),
	})

	// The next tick is reported even while suspended or blocked.
	next := cronSchedule.Next(now.In(location))
	if next.IsZero() {
		schedule.Status.NextScheduleTime = nil
	} else {
		schedule.Status.NextScheduleTime = &metav1.Time{Time: next.UTC()}
	}

	var (
		clearToken bool
		wait       time.Duration
	)
	token, manual := scheduleManualToken(&schedule)
	switch {
	case schedule.Spec.Suspend:
		// Suspension blocks cron and manual triggers alike; the token stays pending.
		setScheduleReady(&schedule, now, metav1.ConditionFalse, api.ReasonSuspended,
			"schedule is suspended; any run-now token stays pending")
	case manual:
		clearToken, wait, err = r.handleManualTrigger(ctx, &schedule, token, owned, active, now)
	default:
		wait, err = r.handleCronTick(ctx, &schedule, cronSchedule, location, owned, active, now)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.pruneHistory(ctx, &schedule, owned); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.persistStatus(ctx, &schedule, savedStatus); err != nil {
		return scheduleRequeueOnConflict(err)
	}
	if clearToken {
		if err := r.clearRunNowAnnotation(ctx, &schedule, token); err != nil {
			return scheduleRequeueOnConflict(err)
		}
	}

	result := ctrl.Result{}
	if !next.IsZero() {
		result.RequeueAfter = scheduleRequeueDelay(now, next)
	}
	if wait > 0 && (result.RequeueAfter == 0 || wait < result.RequeueAfter) {
		result.RequeueAfter = wait
	}
	return result, nil
}

// handleManualTrigger honours a run-now token ahead of the cron schedule and
// reports whether the annotation may be cleared.
func (r *AnonymizationScheduleReconciler) handleManualTrigger(
	ctx context.Context,
	schedule *api.AnonymizationSchedule,
	token string,
	owned []api.AnonymizationRun,
	active []*api.AnonymizationRun,
	now time.Time,
) (bool, time.Duration, error) {
	log := logf.FromContext(ctx)
	fullHash := scheduleTokenHash(token)
	name := scheduleChildName(schedule.Name, "manual-"+fullHash[:scheduleShortHashLength])

	// An earlier attempt may have created the run before the status or annotation
	// patch landed; recognise it before the concurrency policy blocks its own child.
	if existing := findScheduleRun(owned, name); existing != nil {
		if recorded := existing.Annotations[scheduleRunNowHashAnnotation]; recorded != fullHash {
			return false, 0, fmt.Errorf("run %s/%s exists for a different run-now token; refusing to reuse the name",
				existing.Namespace, existing.Name)
		}
		if !existing.DeletionTimestamp.IsZero() {
			setScheduleReady(schedule, now, metav1.ConditionFalse, api.ReasonBlocked,
				fmt.Sprintf("waiting for run %q to finish deleting before the manual run", name))
			return false, scheduleDeletionRequeue, nil
		}
		setScheduleReady(schedule, now, metav1.ConditionTrue, api.ReasonScheduled,
			fmt.Sprintf("manual run %q already exists", name))
		return true, 0, nil
	}

	switch schedule.Spec.ConcurrencyPolicy {
	case api.ConcurrencyPolicyAllow:
	case api.ConcurrencyPolicyReplace:
		if len(active) > 0 {
			if err := r.deleteRuns(ctx, active); err != nil {
				return false, 0, err
			}
			setScheduleReady(schedule, now, metav1.ConditionFalse, api.ReasonBlocked,
				fmt.Sprintf("replacing %d active run(s) before the manual run", len(active)))
			return false, scheduleDeletionRequeue, nil
		}
	default: // Forbid is the default and keeps the token pending.
		if len(active) > 0 {
			setScheduleReady(schedule, now, metav1.ConditionFalse, api.ReasonBlocked,
				fmt.Sprintf("manual run deferred while %d run(s) are active", len(active)))
			return false, 0, nil
		}
	}

	desired, err := r.desiredRun(schedule, name, map[string]string{scheduleRunNowHashAnnotation: fullHash})
	if err != nil {
		return false, 0, err
	}
	created, err := r.createRun(ctx, schedule, desired)
	if err != nil {
		return false, 0, err
	}
	recordScheduledRun(schedule, created)
	log.Info("manual run ready", "run", created.Name)
	setScheduleReady(schedule, now, metav1.ConditionTrue, api.ReasonScheduled,
		fmt.Sprintf("created manual run %q", created.Name))
	// A manual run never consumes or advances a cron tick.
	return true, 0, nil
}

// handleCronTick applies the CronJob missed-schedule algorithm and creates at
// most one run for the latest eligible tick.
func (r *AnonymizationScheduleReconciler) handleCronTick(
	ctx context.Context,
	schedule *api.AnonymizationSchedule,
	cronSchedule cron.Schedule,
	location *time.Location,
	owned []api.AnonymizationRun,
	active []*api.AnonymizationRun,
	now time.Time,
) (time.Duration, error) {
	log := logf.FromContext(ctx)

	// Never start before the schedule existed and never repeat a recorded tick.
	earliest := schedule.CreationTimestamp.Time
	if schedule.Status.LastScheduleTime != nil {
		earliest = schedule.Status.LastScheduleTime.Time
	}
	if deadline := schedule.Spec.StartingDeadlineSeconds; deadline != nil &&
		*deadline <= int64(now.Sub(earliest)/time.Second) {
		// Comparing seconds first avoids duration overflow for large configured deadlines.
		earliest = now.Add(-time.Duration(*deadline) * time.Second)
	}
	if earliest.After(now) {
		setScheduleReady(schedule, now, metav1.ConditionTrue, api.ReasonScheduled,
			"waiting for the next schedule")
		return 0, nil
	}

	tick, missed, tooMany := scheduleLatestMissedTick(cronSchedule, earliest.In(location), now)
	if tooMany {
		// A huge backlog means clock skew or a spec change; restart from now.
		r.event(schedule, corev1.EventTypeWarning, api.ReasonTooManyMissedTimes,
			fmt.Sprintf("more than %d missed schedules; resetting lastScheduleTime without creating a run", scheduleMaxMissedTicks))
		schedule.Status.LastScheduleTime = &metav1.Time{Time: now.UTC()}
		setScheduleReady(schedule, now, metav1.ConditionTrue, api.ReasonScheduled,
			fmt.Sprintf("skipped more than %d missed schedules and reset lastScheduleTime", scheduleMaxMissedTicks))
		return 0, nil
	}
	if tick == nil {
		setScheduleReady(schedule, now, metav1.ConditionTrue, api.ReasonScheduled,
			"no schedule is due")
		return 0, nil
	}

	name := scheduleChildName(schedule.Name, tick.UTC().Format(scheduleRunTimeLayout))
	// A successful create can outlive a failed status patch; recover the same tick
	// before concurrency handling mistakes its own child for a previous run.
	if existing := findScheduleRun(owned, name); existing != nil {
		if !existing.DeletionTimestamp.IsZero() {
			setScheduleReady(schedule, now, metav1.ConditionFalse, api.ReasonBlocked,
				fmt.Sprintf("waiting for run %q to finish deleting before reusing its schedule time", name))
			return scheduleDeletionRequeue, nil
		}
		recordScheduledRun(schedule, existing)
		schedule.Status.LastScheduleTime = &metav1.Time{Time: tick.UTC()}
		setScheduleReady(schedule, now, metav1.ConditionTrue, api.ReasonScheduled,
			fmt.Sprintf("run %q already exists for schedule time %s", name, tick.UTC().Format(time.RFC3339)))
		return 0, nil
	}

	switch schedule.Spec.ConcurrencyPolicy {
	case api.ConcurrencyPolicyAllow:
	case api.ConcurrencyPolicyReplace:
		if len(active) > 0 {
			// Wait for the real deletion of our own runs before the replacement.
			if err := r.deleteRuns(ctx, active); err != nil {
				return 0, err
			}
			setScheduleReady(schedule, now, metav1.ConditionFalse, api.ReasonBlocked,
				fmt.Sprintf("replacing %d active run(s) before the run due at %s", len(active), tick.UTC().Format(time.RFC3339)))
			return scheduleDeletionRequeue, nil
		}
	default: // Forbid keeps the tick pending until the active runs settle.
		if len(active) > 0 {
			setScheduleReady(schedule, now, metav1.ConditionFalse, api.ReasonBlocked,
				fmt.Sprintf("run due at %s deferred while %d run(s) are active", tick.UTC().Format(time.RFC3339), len(active)))
			return 0, nil
		}
	}

	desired, err := r.desiredRun(schedule, name, nil)
	if err != nil {
		return 0, err
	}
	created, err := r.createRun(ctx, schedule, desired)
	if err != nil {
		return 0, err
	}
	recordScheduledRun(schedule, created)
	log.Info("scheduled run ready", "run", created.Name, "scheduleTime", tick.UTC().Format(time.RFC3339), "missed", missed)

	schedule.Status.LastScheduleTime = &metav1.Time{Time: tick.UTC()}
	setScheduleReady(schedule, now, metav1.ConditionTrue, api.ReasonScheduled,
		fmt.Sprintf("created run %q for schedule time %s", created.Name, tick.UTC().Format(time.RFC3339)))
	return 0, nil
}

// desiredRun renders a run from the template without mutating the schedule.
func (r *AnonymizationScheduleReconciler) desiredRun(
	schedule *api.AnonymizationSchedule,
	name string,
	extraAnnotations map[string]string,
) (*api.AnonymizationRun, error) {
	labels := make(map[string]string, len(schedule.Spec.Template.Metadata.Labels)+2)
	maps.Copy(labels, schedule.Spec.Template.Metadata.Labels)
	// Ownership labels are authoritative and override conflicting template labels.
	labels[scheduleNameLabel] = pxc.LabelValue(schedule.Name)
	labels[scheduleOutputGroupLabel] = pxc.LabelValue(schedule.Name)

	annotations := make(map[string]string, len(schedule.Spec.Template.Metadata.Annotations)+len(extraAnnotations))
	maps.Copy(annotations, schedule.Spec.Template.Metadata.Annotations)
	maps.Copy(annotations, extraAnnotations)
	if len(annotations) == 0 {
		annotations = nil
	}

	run := &api.AnonymizationRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   schedule.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: *schedule.Spec.Template.Spec.DeepCopy(),
	}
	scheme := r.Scheme
	if scheme == nil {
		scheme = r.Client.Scheme()
	}
	if err := ctrl.SetControllerReference(schedule, run, scheme); err != nil {
		return nil, err
	}
	return run, nil
}

// createRun creates the deterministic child and tolerates a create that already
// succeeded in an earlier pass, without ever adopting a foreign object.
func (r *AnonymizationScheduleReconciler) createRun(
	ctx context.Context,
	schedule *api.AnonymizationSchedule,
	desired *api.AnonymizationRun,
) (*api.AnonymizationRun, error) {
	err := r.Create(ctx, desired)
	if err == nil {
		return desired, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, err
	}

	existing := &api.AnonymizationRun{}
	key := client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}
	if getErr := r.Get(ctx, key, existing); getErr != nil {
		return nil, getErr
	}
	if !ownedBySchedule(existing, schedule) {
		return nil, fmt.Errorf("run %s/%s already exists and is not controlled by this schedule", key.Namespace, key.Name)
	}
	if !existing.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("owned run %s/%s is being deleted; refusing to reuse its name", key.Namespace, key.Name)
	}
	if want := desired.Annotations[scheduleRunNowHashAnnotation]; want != "" {
		if recorded := existing.Annotations[scheduleRunNowHashAnnotation]; recorded != want {
			return nil, fmt.Errorf("run %s/%s carries a different run-now token hash; refusing to reuse the name",
				key.Namespace, key.Name)
		}
	}
	return existing, nil
}

// ownedRuns returns the strictly controller-owned runs in the schedule namespace,
// oldest first with a stable name tie-break.
func (r *AnonymizationScheduleReconciler) ownedRuns(
	ctx context.Context,
	schedule *api.AnonymizationSchedule,
) ([]api.AnonymizationRun, error) {
	var list api.AnonymizationRunList
	if err := r.List(ctx, &list, client.InNamespace(schedule.Namespace)); err != nil {
		return nil, err
	}
	owned := make([]api.AnonymizationRun, 0, len(list.Items))
	for i := range list.Items {
		if ownedBySchedule(&list.Items[i], schedule) {
			owned = append(owned, list.Items[i])
		}
	}
	slices.SortStableFunc(owned, func(a, b api.AnonymizationRun) int {
		if order := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	return owned, nil
}

// deleteRuns removes only this schedule's own runs with a UID precondition.
func (r *AnonymizationScheduleReconciler) deleteRuns(ctx context.Context, runs []*api.AnonymizationRun) error {
	for _, run := range runs {
		if err := r.deleteRun(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func (r *AnonymizationScheduleReconciler) deleteRun(ctx context.Context, run *api.AnonymizationRun) error {
	if !run.DeletionTimestamp.IsZero() {
		return nil
	}
	uid := run.UID
	if err := r.Delete(ctx, run, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// pruneHistory removes only the oldest settled terminal runs beyond the limits.
func (r *AnonymizationScheduleReconciler) pruneHistory(
	ctx context.Context,
	schedule *api.AnonymizationSchedule,
	owned []api.AnonymizationRun,
) error {
	var succeeded, failed []*api.AnonymizationRun
	for i := range owned {
		run := &owned[i]
		if !scheduleRunSettled(run) {
			continue
		}
		switch run.Status.Phase {
		case api.RunPhaseCompleted:
			succeeded = append(succeeded, run)
		case api.RunPhaseFailed:
			failed = append(failed, run)
		}
	}
	if err := r.pruneGroup(ctx, succeeded, int(schedule.Spec.SuccessfulRunsHistoryLimit)); err != nil {
		return err
	}
	return r.pruneGroup(ctx, failed, int(schedule.Spec.FailedRunsHistoryLimit))
}

func (r *AnonymizationScheduleReconciler) pruneGroup(
	ctx context.Context,
	runs []*api.AnonymizationRun,
	limit int,
) error {
	if limit < 0 {
		limit = 0
	}
	if len(runs) <= limit {
		return nil
	}
	slices.SortStableFunc(runs, scheduleRunCompletionOrder)
	for _, run := range runs[:len(runs)-limit] {
		if err := r.deleteRun(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

// persistStatus avoids needless patches and keeps the write on the subresource.
func (r *AnonymizationScheduleReconciler) persistStatus(
	ctx context.Context,
	schedule *api.AnonymizationSchedule,
	saved api.AnonymizationScheduleStatus,
) error {
	if equality.Semantic.DeepEqual(saved, schedule.Status) {
		return nil
	}
	base := schedule.DeepCopy()
	base.Status = saved
	return r.Status().Patch(ctx, schedule, client.MergeFrom(base))
}

// clearRunNowAnnotation drops the consumed token under an optimistic lock so a
// concurrently written token is never lost.
func (r *AnonymizationScheduleReconciler) clearRunNowAnnotation(
	ctx context.Context,
	schedule *api.AnonymizationSchedule,
	token string,
) error {
	current, present := schedule.Annotations[scheduleRunNowAnnotation]
	if !present || current != token {
		return nil
	}
	base := schedule.DeepCopy()
	delete(schedule.Annotations, scheduleRunNowAnnotation)
	return r.Patch(ctx, schedule, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func (r *AnonymizationScheduleReconciler) event(object runtime.Object, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(object, nil, eventType, reason, "Schedule", "%s", message)
}

// clock resolves the injected clock, defaulting to the wall clock.
func (r *AnonymizationScheduleReconciler) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// parseScheduleSpec keeps spec.timeZone authoritative over any embedded prefix.
func parseScheduleSpec(schedule *api.AnonymizationSchedule) (cron.Schedule, *time.Location, error) {
	spec := strings.TrimSpace(schedule.Spec.Schedule)
	if spec == "" {
		return nil, nil, fmt.Errorf("spec.schedule is empty")
	}
	upper := strings.ToUpper(spec)
	if strings.HasPrefix(upper, "TZ=") || strings.HasPrefix(upper, "CRON_TZ=") {
		return nil, nil, fmt.Errorf("embedded TZ= or CRON_TZ= prefixes are rejected; use spec.timeZone")
	}
	if strings.HasPrefix(strings.ToLower(spec), "@every") {
		return nil, nil, fmt.Errorf("@every intervals are not supported; use five-field cron or a calendar descriptor")
	}

	zone := "UTC"
	if schedule.Spec.TimeZone != nil && strings.TrimSpace(*schedule.Spec.TimeZone) != "" {
		zone = strings.TrimSpace(*schedule.Spec.TimeZone)
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid spec.timeZone %q: %w", zone, err)
	}

	parsed, err := scheduleCronParser.Parse(spec)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid spec.schedule %q: %w", spec, err)
	}
	return parsed, location, nil
}

// scheduleLatestMissedTick walks missed ticks like the CronJob controller and
// keeps only the newest one so at most one run is created per reconcile.
func scheduleLatestMissedTick(cronSchedule cron.Schedule, earliest, now time.Time) (*time.Time, int, bool) {
	var latest time.Time
	count := 0
	for tick := cronSchedule.Next(earliest); !tick.IsZero() && !tick.After(now); tick = cronSchedule.Next(tick) {
		latest = tick
		count++
		// Scan at most 101 ticks before declaring the backlog unusable.
		if count > scheduleMaxMissedTicks {
			return nil, count, true
		}
	}
	if count == 0 {
		return nil, 0, false
	}
	return &latest, count, false
}

// scheduleActiveRuns keeps cleanup-pending and deleting runs active so a failed
// cleanup never releases concurrency.
func scheduleActiveRuns(owned []api.AnonymizationRun) []*api.AnonymizationRun {
	active := make([]*api.AnonymizationRun, 0, len(owned))
	for i := range owned {
		if !scheduleRunSettled(&owned[i]) {
			active = append(active, &owned[i])
		}
	}
	return active
}

// scheduleRunSettled reports a terminal run whose cleanup outcome is recorded.
func scheduleRunSettled(run *api.AnonymizationRun) bool {
	if !run.DeletionTimestamp.IsZero() {
		return false
	}
	if run.Status.Phase != api.RunPhaseCompleted &&
		run.Status.Phase != api.RunPhaseFailed {
		return false
	}
	return meta.IsStatusConditionTrue(run.Status.Conditions, scheduledRunCleanedUpCondition) ||
		meta.IsStatusConditionTrue(run.Status.Conditions, scheduledRunRetainedCondition)
}

// applyScheduleRunStatus derives the run-facing status from the observed children.
func applyScheduleRunStatus(
	schedule *api.AnonymizationSchedule,
	owned []api.AnonymizationRun,
	active []*api.AnonymizationRun,
) {
	names := make([]string, 0, len(active))
	for _, run := range active {
		names = append(names, run.Name)
	}
	if len(names) == 0 {
		schedule.Status.Active = nil
	} else {
		slices.Sort(names)
		schedule.Status.Active = names
	}
	schedule.Status.ActiveCount = int32(len(names))

	if len(owned) > 0 {
		newest := &owned[len(owned)-1]
		schedule.Status.LastRunName = newest.Name
		schedule.Status.LastRunPhase = newest.Status.Phase
	}

	var latestSuccess *metav1.Time
	for i := range owned {
		run := &owned[i]
		if run.Status.Phase != api.RunPhaseCompleted || run.Status.CompletedAt == nil {
			continue
		}
		if latestSuccess == nil || latestSuccess.Before(run.Status.CompletedAt) {
			latestSuccess = run.Status.CompletedAt
		}
	}
	// Historical timestamps survive history pruning.
	if latestSuccess != nil &&
		(schedule.Status.LastSuccessfulTime == nil || schedule.Status.LastSuccessfulTime.Before(latestSuccess)) {
		schedule.Status.LastSuccessfulTime = latestSuccess.DeepCopy()
	}
}

// ownedBySchedule accepts only strict controller ownership by this exact object.
func ownedBySchedule(
	run *api.AnonymizationRun,
	schedule *api.AnonymizationSchedule,
) bool {
	ref := metav1.GetControllerOf(run)
	if run.Namespace != schedule.Namespace || schedule.UID == "" || ref == nil || ref.UID != schedule.UID || ref.Name != schedule.Name || ref.Kind != anonymizationScheduleKind {
		return false
	}
	groupVersion, err := schema.ParseGroupVersion(ref.APIVersion)
	return err == nil && groupVersion.Group == api.SchemeGroupVersion.Group
}

// Record the returned child immediately because the informer cache can lag a successful create.
func recordScheduledRun(schedule *api.AnonymizationSchedule, run *api.AnonymizationRun) {
	schedule.Status.LastRunName = run.Name
	schedule.Status.LastRunPhase = run.Status.Phase
	if !scheduleRunSettled(run) {
		if slices.Contains(schedule.Status.Active, run.Name) {
			return
		}
		schedule.Status.Active = append(schedule.Status.Active, run.Name)
		slices.Sort(schedule.Status.Active)
		schedule.Status.ActiveCount = int32(len(schedule.Status.Active))
	}
}

func findScheduleRun(runs []api.AnonymizationRun, name string) *api.AnonymizationRun {
	for i := range runs {
		if runs[i].Name == name {
			return &runs[i]
		}
	}
	return nil
}

func scheduleManualToken(schedule *api.AnonymizationSchedule) (string, bool) {
	// An empty value is still a present token.
	token, present := schedule.Annotations[scheduleRunNowAnnotation]
	return token, present
}

func scheduleTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// scheduleChildName keeps the documented names and falls back to a bounded,
// deterministic prefix instead of silently colliding on very long names.
func scheduleChildName(scheduleName, suffix string) string {
	name := scheduleName + "-" + suffix
	if len(name) <= scheduleMaxNameLength {
		return name
	}
	digest := scheduleTokenHash(scheduleName)[:8]
	keep := scheduleMaxNameLength - len(suffix) - len(digest) - 2
	keep = max(keep, 0)
	return strings.TrimRight(scheduleName[:keep], "-") + "-" + digest + "-" + suffix
}

func scheduleRunCompletionOrder(a, b *api.AnonymizationRun) int {
	left, right := scheduleRunCompletionTime(a), scheduleRunCompletionTime(b)
	if order := left.Compare(right); order != 0 {
		return order
	}
	return strings.Compare(a.Name, b.Name)
}

func scheduleRunCompletionTime(run *api.AnonymizationRun) time.Time {
	if run.Status.CompletedAt != nil {
		return run.Status.CompletedAt.Time
	}
	return run.CreationTimestamp.Time
}

func setScheduleReady(
	schedule *api.AnonymizationSchedule,
	now time.Time,
	status metav1.ConditionStatus,
	reason, message string,
) {
	conditions.Set(&schedule.Status.Conditions, schedule.Generation, now, metav1.Condition{
		Type:    scheduleReadyConditionType,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}

// scheduleRequeueDelay keeps the timer armed with a positive nanosecond minimum.
func scheduleRequeueDelay(now, next time.Time) time.Duration {
	if delay := next.Sub(now); delay > 0 {
		return delay
	}
	return time.Nanosecond
}

// scheduleRequeueOnConflict retries optimistic-concurrency conflicts on a fresh read.
func scheduleRequeueOnConflict(err error) (ctrl.Result, error) {
	if apierrors.IsConflict(err) {
		return ctrl.Result{Requeue: true}, nil //nolint:nilerr,staticcheck // conflicts are retried, not surfaced
	}
	return ctrl.Result{}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *AnonymizationScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorder("anonymizationschedule-controller")
	}
	if r.now == nil {
		r.now = time.Now
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.AnonymizationSchedule{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
		))).
		// Owned runs wake the schedule on status changes so concurrency is released promptly.
		Owns(&api.AnonymizationRun{}).
		Named("anonymizationschedule").
		Complete(r)
}
