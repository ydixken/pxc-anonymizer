// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
)

const (
	scheduleTestDelete     = "delete"
	scheduleTestUTC        = "UTC"
	scheduleTestKept       = "kept"
	scheduleTestWrong      = "wrong"
	scheduleTestCron       = "* * * * *"
	scheduleTestForeignUID = "another-schedule"
	scheduleTestNamespace  = "schedule-test"
	scheduleTestName       = "nightly"
	scheduleTestToken      = "replay-one"
	scheduleTestFinalizer  = "example.invalid/cleanup"
)

var scheduleTestNow = time.Date(2026, time.January, 2, 12, 10, 30, 0, time.UTC)

func TestScheduleCronAndTimeZone(t *testing.T) {
	for _, tc := range []struct {
		name, expression, zone string
		valid                  bool
		next                   time.Time
	}{
		{"utc", "0 13 * * *", scheduleTestUTC, true, time.Date(2026, time.January, 2, 13, 0, 0, 0, time.UTC)},
		{"iana", "0 9 * * *", "America/New_York", true, time.Date(2026, time.January, 2, 14, 0, 0, 0, time.UTC)},
		{"descriptor", "@daily", scheduleTestUTC, true, time.Date(2026, time.January, 3, 0, 0, 0, 0, time.UTC)},
		{"invalid-cron", "not a cron", scheduleTestUTC, false, time.Time{}},
		{"invalid-zone", "0 9 * * *", "Invalid/Zone", false, time.Time{}},
		{"embedded-zone", "CRON_TZ=UTC 0 9 * * *", scheduleTestUTC, false, time.Time{}},
		{"interval", "@every 5s", scheduleTestUTC, false, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schedule := scheduleTestFixture()
			schedule.CreationTimestamp = metav1.NewTime(scheduleTestNow)
			schedule.Spec.Schedule = tc.expression
			schedule.Spec.TimeZone = &tc.zone
			r, c := scheduleTestReconciler(t, schedule, interceptor.Funcs{})
			result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schedule)})
			if err != nil {
				t.Fatal(err)
			}
			got := scheduleTestGet(t, c, schedule)
			condition := meta.FindStatusCondition(got.Status.Conditions, "ScheduleValid")
			if condition == nil || (condition.Status == metav1.ConditionTrue) != tc.valid {
				t.Fatalf("unexpected ScheduleValid condition: %#v", condition)
			}
			if got.Status.ObservedGeneration != schedule.Generation || condition.ObservedGeneration != schedule.Generation {
				t.Fatal("condition/status did not observe the current generation")
			}
			if tc.valid {
				if got.Status.NextScheduleTime == nil || !got.Status.NextScheduleTime.Time.Equal(tc.next) || result.RequeueAfter != tc.next.Sub(scheduleTestNow) {
					t.Fatalf("wrong next tick: %#v, result %#v", got.Status.NextScheduleTime, result)
				}
			} else if condition.Reason != api.ReasonInvalidCron || got.Status.NextScheduleTime != nil {
				t.Fatalf("invalid schedule was not reported: %#v", got.Status)
			}
			if len(scheduleTestRuns(t, c)) != 0 {
				t.Fatal("a future or invalid schedule created a Run")
			}
		})
	}
}

func TestScheduleLatestMissedTickAndCopiedTemplate(t *testing.T) {
	schedule := scheduleTestFixture()
	schedule.Spec.Template.Metadata.Labels = map[string]string{scheduleNameLabel: scheduleTestWrong, pxc.LabelOutputGroup: scheduleTestWrong, admissionExample: scheduleTestKept}
	schedule.Spec.Template.Metadata.Annotations = map[string]string{admissionExample: scheduleTestKept}
	r, c := scheduleTestReconciler(t, schedule, interceptor.Funcs{})
	scheduleTestReconcile(t, r, schedule)
	runs := scheduleTestRuns(t, c)
	if len(runs) != 1 || runs[0].Name != "nightly-202601021210" {
		t.Fatalf("expected one Run at the latest missed tick, got %#v", runs)
	}
	run := &runs[0]
	if !metav1.IsControlledBy(run, schedule) || run.Labels[scheduleNameLabel] != schedule.Name || run.Labels[pxc.LabelOutputGroup] != schedule.Name || run.Labels[admissionExample] != scheduleTestKept || run.Annotations[admissionExample] != scheduleTestKept {
		t.Fatalf("template or ownership metadata lost: %#v", run.ObjectMeta)
	}
	if run.Spec.Source.Destination != schedule.Spec.Template.Spec.Source.Destination {
		t.Fatal("template spec was not copied")
	}
	got := scheduleTestGet(t, c, schedule)
	if got.Spec.Template.Metadata.Labels[scheduleNameLabel] != scheduleTestWrong || got.Status.ActiveCount != 1 || len(got.Status.Active) != 1 || got.Status.LastRunName != run.Name {
		t.Fatalf("template mutated or immediate child status missing: %#v", got)
	}
	if got.Status.LastScheduleTime == nil || !got.Status.LastScheduleTime.Time.Equal(scheduleTestNow.Truncate(time.Minute)) {
		t.Fatalf("wrong last schedule time: %#v", got.Status.LastScheduleTime)
	}
	scheduleTestReconcile(t, r, schedule)
	if len(scheduleTestRuns(t, c)) != 1 {
		t.Fatal("same tick created a duplicate Run")
	}
}

func TestScheduleLongNameLabelsAndCronRecovery(t *testing.T) {
	schedule := scheduleTestFixture()
	schedule.Name = strings.Repeat("a", 64)
	schedule.Spec.ConcurrencyPolicy = api.ConcurrencyPolicyReplace
	funcs := interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		for key, value := range obj.GetLabels() {
			if problems := validation.IsValidLabelValue(value); len(problems) > 0 {
				return fmt.Errorf("invalid generated label %s: %v", key, problems)
			}
		}
		obj.SetUID("long-name-run-uid")
		return c.Create(ctx, obj, opts...)
	}}
	r, c := scheduleTestReconciler(t, schedule, funcs)
	scheduleTestReconcile(t, r, schedule)
	before := scheduleTestRuns(t, c)
	if len(before) != 1 {
		t.Fatalf("expected one long-name child, got %d", len(before))
	}
	run := before[0]
	ref := metav1.GetControllerOf(&run)
	if run.Name != schedule.Name+"-202601021210" || ref == nil || ref.Name != schedule.Name || ref.UID != schedule.UID {
		t.Fatalf("full resource or owner identity changed: %#v", run.ObjectMeta)
	}
	for _, key := range []string{scheduleNameLabel, pxc.LabelOutputGroup} {
		if value := run.Labels[key]; len(value) != 63 || value != pxc.LabelValue(schedule.Name) {
			t.Fatalf("label %s did not use the shared name mapping", key)
		}
	}
	got := scheduleTestGet(t, c, schedule)
	got.Status.LastScheduleTime = nil
	if err := c.Status().Update(t.Context(), got); err != nil {
		t.Fatal(err)
	}
	scheduleTestReconcile(t, r, schedule)
	after := scheduleTestRuns(t, c)
	got = scheduleTestGet(t, c, schedule)
	if len(after) != 1 || after[0].UID != run.UID || !after[0].DeletionTimestamp.IsZero() || got.Status.LastScheduleTime == nil || !got.Status.LastScheduleTime.Time.Equal(scheduleTestNow.Truncate(time.Minute)) {
		t.Fatalf("long-name cron recovery did not preserve its owned child: %#v", got.Status)
	}
}

func TestScheduleStartingDeadlineAndMissCap(t *testing.T) {
	for _, tc := range []struct {
		name       string
		age        time.Duration
		deadline   *int64
		wantRuns   int
		wantReset  bool
		expression string
	}{
		{"deadline-expired", 3 * time.Hour, scheduleTestInt64(60), 0, false, "0 * * * *"},
		{"latest-within-deadline", 3 * time.Hour, scheduleTestInt64(60), 1, false, scheduleTestCron},
		{"large-deadline-does-not-overflow", 3 * time.Minute, scheduleTestInt64(math.MaxInt64), 1, false, scheduleTestCron},
		{"one-hundred-misses", 100 * time.Minute, nil, 1, false, scheduleTestCron},
		{"over-one-hundred-misses", 101 * time.Minute, nil, 0, true, scheduleTestCron},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schedule := scheduleTestFixture()
			schedule.CreationTimestamp = metav1.NewTime(scheduleTestNow.Add(-tc.age))
			schedule.Spec.Schedule = tc.expression
			schedule.Spec.StartingDeadlineSeconds = tc.deadline
			r, c := scheduleTestReconciler(t, schedule, interceptor.Funcs{})
			recorder := events.NewFakeRecorder(2)
			r.Recorder = recorder
			scheduleTestReconcile(t, r, schedule)
			if got := len(scheduleTestRuns(t, c)); got != tc.wantRuns {
				t.Fatalf("created %d Runs, want %d", got, tc.wantRuns)
			}
			if tc.wantReset {
				got := scheduleTestGet(t, c, schedule)
				if got.Status.LastScheduleTime == nil || !got.Status.LastScheduleTime.Time.Equal(scheduleTestNow) {
					t.Fatalf("miss cap did not reset its cursor: %#v", got.Status.LastScheduleTime)
				}
				select {
				case event := <-recorder.Events:
					if !strings.Contains(event, "Warning TooManyMissedTimes") {
						t.Fatalf("wrong event: %s", event)
					}
				default:
					t.Fatal("miss cap did not emit an event")
				}
			}
		})
	}
}

func TestScheduleConcurrencyAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  api.ConcurrencyPolicy
		phase   api.RunPhase
		settled bool
		wantNew bool
	}{
		{"forbid", api.ConcurrencyPolicyForbid, api.RunPhaseRestoring, false, false},
		{"allow", api.ConcurrencyPolicyAllow, api.RunPhaseRestoring, false, true},
		{"failed-cleanup-pending", api.ConcurrencyPolicyForbid, api.RunPhaseFailed, false, false},
		{"completed-cleanup-pending", api.ConcurrencyPolicyForbid, api.RunPhaseCompleted, false, false},
		{"settled-terminal", api.ConcurrencyPolicyForbid, api.RunPhaseCompleted, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schedule := scheduleTestFixture()
			schedule.Spec.ConcurrencyPolicy = tc.policy
			old := scheduleTestRun(schedule, "older", tc.phase, tc.settled, scheduleTestNow.Add(-time.Hour))
			r, c := scheduleTestReconciler(t, schedule, interceptor.Funcs{}, old)
			scheduleTestReconcile(t, r, schedule)
			want := 1
			if tc.wantNew {
				want++
			}
			if runs := scheduleTestRuns(t, c); len(runs) != want {
				t.Fatalf("got %d Runs, want %d", len(runs), want)
			}
			if !tc.wantNew {
				got := scheduleTestGet(t, c, schedule)
				ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
				if got.Status.ActiveCount != 1 || ready == nil || ready.Reason != api.ReasonBlocked || ready.Status != metav1.ConditionFalse {
					t.Fatalf("Forbid did not report the active Run: %#v", got.Status)
				}
			}
		})
	}
}

func TestScheduleReplaceWaitsForFinalizers(t *testing.T) {
	schedule := scheduleTestFixture()
	schedule.Spec.ConcurrencyPolicy = api.ConcurrencyPolicyReplace
	old := scheduleTestRun(schedule, "older", api.RunPhaseRestoring, false, scheduleTestNow.Add(-time.Hour))
	old.Finalizers = []string{scheduleTestFinalizer}
	r, c := scheduleTestReconciler(t, schedule, interceptor.Funcs{}, old)
	for range 2 {
		scheduleTestReconcile(t, r, schedule)
		runs := scheduleTestRuns(t, c)
		if len(runs) != 1 || runs[0].Name != old.Name || runs[0].DeletionTimestamp.IsZero() {
			t.Fatalf("replacement overlapped a deleting Run: %#v", runs)
		}
	}
	deleting := &api.AnonymizationRun{}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(old), deleting); err != nil {
		t.Fatal(err)
	}
	deleting.Finalizers = nil
	if err := c.Update(t.Context(), deleting); err != nil {
		t.Fatal(err)
	}
	scheduleTestReconcile(t, r, schedule)
	if runs := scheduleTestRuns(t, c); len(runs) != 1 || runs[0].Name == old.Name {
		t.Fatalf("replacement was not created after finalization: %#v", runs)
	}
}

func TestSchedulePrunesOnlySettledOwnedHistory(t *testing.T) {
	schedule := scheduleTestFixture()
	schedule.Spec.Suspend = true
	schedule.Spec.SuccessfulRunsHistoryLimit = 1
	schedule.Spec.FailedRunsHistoryLimit = 0
	old := scheduleTestRun(schedule, "old-success", api.RunPhaseCompleted, true, scheduleTestNow.Add(-3*time.Hour))
	newer := scheduleTestRun(schedule, "new-success", api.RunPhaseCompleted, true, scheduleTestNow.Add(-time.Hour))
	failed := scheduleTestRun(schedule, "old-failure", api.RunPhaseFailed, true, scheduleTestNow.Add(-2*time.Hour))
	pending := scheduleTestRun(schedule, "cleanup-pending", api.RunPhaseFailed, false, scheduleTestNow.Add(-4*time.Hour))
	foreign := scheduleTestRun(schedule, "foreign", api.RunPhaseCompleted, true, scheduleTestNow.Add(-5*time.Hour))
	foreign.OwnerReferences[0].UID = scheduleTestForeignUID
	r, c := scheduleTestReconciler(t, schedule, interceptor.Funcs{}, old, newer, failed, pending, foreign)
	scheduleTestReconcile(t, r, schedule)
	runs := scheduleTestRuns(t, c)
	if len(runs) != 3 {
		t.Fatalf("unexpected history count: %d", len(runs))
	}
	for _, run := range runs {
		if run.Name == old.Name || run.Name == failed.Name {
			t.Fatalf("old settled history was retained: %s", run.Name)
		}
	}
	got := scheduleTestGet(t, c, schedule)
	if got.Status.ActiveCount != 1 || got.Status.LastSuccessfulTime == nil || !got.Status.LastSuccessfulTime.Equal(newer.Status.CompletedAt) {
		t.Fatalf("history status is incorrect: %#v", got.Status)
	}
}

func TestScheduleCronTriggerSurvivesStatusFailure(t *testing.T) {
	for _, policy := range []api.ConcurrencyPolicy{api.ConcurrencyPolicyReplace, api.ConcurrencyPolicyForbid} {
		t.Run(string(policy), func(t *testing.T) {
			schedule := scheduleTestFixture()
			schedule.Spec.ConcurrencyPolicy = policy
			failure := errors.New("injected cron status failure")
			fail := true
			creates, deletes := 0, 0
			funcs := interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					creates++
					obj.SetUID(types.UID(fmt.Sprintf("created-run-%d", creates)))
					return c.Create(ctx, obj, opts...)
				},
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deletes++
					return c.Delete(ctx, obj, opts...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if sub == runTestStatusField && fail {
						fail = false
						return failure
					}
					return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
				},
			}
			r, c := scheduleTestReconciler(t, schedule, funcs)
			_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schedule)})
			before := scheduleTestRuns(t, c)
			if !errors.Is(err, failure) || len(before) != 1 || before[0].UID == "" || scheduleTestGet(t, c, schedule).Status.LastScheduleTime != nil {
				t.Fatalf("expected a created child before a failed status patch: %v, %#v", err, before)
			}
			scheduleTestReconcile(t, r, schedule)
			after := scheduleTestRuns(t, c)
			got := scheduleTestGet(t, c, schedule)
			if len(after) != 1 || after[0].UID != before[0].UID || !after[0].DeletionTimestamp.IsZero() || creates != 1 || deletes != 0 {
				t.Fatalf("cron retry replaced its existing child: creates=%d deletes=%d runs=%#v", creates, deletes, after)
			}
			if got.Status.LastScheduleTime == nil || !got.Status.LastScheduleTime.Time.Equal(scheduleTestNow.Truncate(time.Minute)) || got.Status.LastRunName != before[0].Name || got.Status.ActiveCount != 1 {
				t.Fatalf("cron retry did not recover the consumed tick and child status: %#v", got.Status)
			}
		})
	}
}

func TestScheduleManualTriggerSurvivesStatusFailure(t *testing.T) {
	schedule := scheduleTestFixture()
	schedule.Annotations = map[string]string{scheduleRunNowAnnotation: scheduleTestToken}
	failure := errors.New("injected status failure")
	fail := true
	funcs := interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		if sub == runTestStatusField && fail {
			fail = false
			return failure
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}}
	r, c := scheduleTestReconciler(t, schedule, funcs)
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schedule)})
	if !errors.Is(err, failure) || len(scheduleTestRuns(t, c)) != 1 {
		t.Fatalf("expected a created child and propagated status failure: %v", err)
	}
	scheduleTestReconcile(t, r, schedule)
	got := scheduleTestGet(t, c, schedule)
	if _, present := got.Annotations[scheduleRunNowAnnotation]; present {
		t.Fatal("Forbid blocked retry from acknowledging its own manual Run")
	}
	sum := sha256.Sum256([]byte(scheduleTestToken))
	wantName := fmt.Sprintf("nightly-manual-%x", sum[:3])
	if runs := scheduleTestRuns(t, c); len(runs) != 1 || runs[0].Name != wantName || got.Status.LastScheduleTime != nil {
		t.Fatalf("manual retry duplicated a child or consumed a cron tick: %#v", got.Status)
	}
}

func TestScheduleManualTokenRemainsPending(t *testing.T) {
	for _, suspended := range []bool{true, false} {
		t.Run(fmt.Sprintf("suspended=%t", suspended), func(t *testing.T) {
			schedule := scheduleTestFixture()
			schedule.Spec.Suspend = suspended
			schedule.Annotations = map[string]string{scheduleRunNowAnnotation: ""}
			old := scheduleTestRun(schedule, "older", api.RunPhaseRestoring, false, scheduleTestNow.Add(-time.Hour))
			r, c := scheduleTestReconciler(t, schedule, interceptor.Funcs{}, old)
			scheduleTestReconcile(t, r, schedule)
			got := scheduleTestGet(t, c, schedule)
			if _, present := got.Annotations[scheduleRunNowAnnotation]; !present || len(scheduleTestRuns(t, c)) != 1 {
				t.Fatal("blocked manual token was consumed or a Run was created")
			}
		})
	}
}

func TestScheduleDoesNotLoseConcurrentManualToken(t *testing.T) {
	schedule := scheduleTestFixture()
	schedule.Annotations = map[string]string{scheduleRunNowAnnotation: scheduleTestToken}
	funcs := interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*api.AnonymizationRun); ok {
			updated := &api.AnonymizationSchedule{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(schedule), updated); err != nil {
				return err
			}
			updated.Annotations[scheduleRunNowAnnotation] = "newer-token"
			if err := c.Update(ctx, updated); err != nil {
				return err
			}
		}
		return c.Create(ctx, obj, opts...)
	}}
	r, c := scheduleTestReconciler(t, schedule, funcs)
	scheduleTestReconcile(t, r, schedule)
	if got := scheduleTestGet(t, c, schedule); got.Annotations[scheduleRunNowAnnotation] != "newer-token" {
		t.Fatal("a concurrent manual token was lost")
	}
	if len(scheduleTestRuns(t, c)) != 1 {
		t.Fatal("more than one manual Run was created")
	}
}

func TestScheduleRefusesForeignNameCollision(t *testing.T) {
	schedule := scheduleTestFixture()
	foreign := scheduleTestRun(schedule, "nightly-202601021210", api.RunPhaseRestoring, false, scheduleTestNow)
	foreign.OwnerReferences[0].UID = scheduleTestForeignUID
	r, c := scheduleTestReconciler(t, schedule, interceptor.Funcs{}, foreign)
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schedule)})
	if err == nil || !strings.Contains(err.Error(), "not controlled") {
		t.Fatalf("foreign Run collision was not rejected: %v", err)
	}
	if runs := scheduleTestRuns(t, c); len(runs) != 1 || runs[0].OwnerReferences[0].UID != scheduleTestForeignUID {
		t.Fatal("foreign Run was adopted or deleted")
	}
}

func TestSchedulePropagatesChildAPIErrors(t *testing.T) {
	for _, operation := range []string{"list", "create", scheduleTestDelete} {
		t.Run(operation, func(t *testing.T) {
			schedule := scheduleTestFixture()
			failure := errors.New("injected child API failure")
			funcs := interceptor.Funcs{}
			var objects []client.Object
			switch operation {
			case "list":
				funcs.List = func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error { return failure }
			case "create":
				funcs.Create = func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error { return failure }
			case scheduleTestDelete:
				schedule.Spec.ConcurrencyPolicy = api.ConcurrencyPolicyReplace
				objects = append(objects, scheduleTestRun(schedule, "older", api.RunPhaseRestoring, false, scheduleTestNow))
				funcs.Delete = func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error { return failure }
			}
			r, _ := scheduleTestReconciler(t, schedule, funcs, objects...)
			_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schedule)})
			if !errors.Is(err, failure) {
				t.Fatalf("%s failure was swallowed: %v", operation, err)
			}
		})
	}
}

func TestScheduleConflictRequeues(t *testing.T) {
	schedule := scheduleTestFixture()
	funcs := interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
		return apierrors.NewConflict(api.SchemeGroupVersion.WithResource("anonymizationruns").GroupResource(), admissionExample, errors.New("changed"))
	}}
	r, _ := scheduleTestReconciler(t, schedule, funcs)
	result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schedule)})
	if err != nil || !result.Requeue { //nolint:staticcheck // The controller contract explicitly retries conflicts immediately.
		t.Fatalf("conflict did not request a fresh reconcile: %#v, %v", result, err)
	}
}

func scheduleTestFixture() *api.AnonymizationSchedule {
	return &api.AnonymizationSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: scheduleTestName, Namespace: scheduleTestNamespace, UID: "schedule-uid", Generation: 1, CreationTimestamp: metav1.NewTime(scheduleTestNow.Add(-3 * time.Minute))},
		Spec: api.AnonymizationScheduleSpec{
			Schedule: scheduleTestCron, ConcurrencyPolicy: api.ConcurrencyPolicyForbid, SuccessfulRunsHistoryLimit: 3, FailedRunsHistoryLimit: 1,
			Template: api.AnonymizationRunTemplate{Spec: api.AnonymizationRunSpec{
				Source:      api.RunSource{Destination: "s3://example-source/backup", Restore: api.RestoreS3Credentials{CredentialsSecret: "example-credentials"}},
				PolicyRef:   corev1.LocalObjectReference{Name: "example-policy"},
				TempCluster: api.TempClusterSpec{CRVersion: "1.20.0", Image: "example.invalid/database:v1", BackupImage: "example.invalid/backup:v1", Storage: api.TempClusterStorage{StorageClassName: "example-storage", Size: resource.MustParse("1Gi")}},
				Output:      api.OutputSpec{ObjectStorage: api.ObjectStorageSpec{Bucket: admissionOutputBucket, EndpointURL: "https://storage.example.invalid"}},
			}},
		},
	}
}

func scheduleTestRun(schedule *api.AnonymizationSchedule, name string, phase api.RunPhase, settled bool, completed time.Time) *api.AnonymizationRun {
	run := &api.AnonymizationRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: schedule.Namespace, UID: types.UID(name + "-uid"), CreationTimestamp: metav1.NewTime(completed.Add(-time.Hour)), OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(schedule, api.SchemeGroupVersion.WithKind("AnonymizationSchedule"))}},
		Spec:       *schedule.Spec.Template.Spec.DeepCopy(),
		Status:     api.AnonymizationRunStatus{Phase: phase, CompletedAt: &metav1.Time{Time: completed}},
	}
	if settled {
		run.Status.Conditions = []metav1.Condition{{Type: "CleanedUp", Status: metav1.ConditionTrue, Reason: "Deleted", Message: "Cleanup complete", LastTransitionTime: metav1.NewTime(completed)}}
	}
	return run
}

func scheduleTestReconciler(t *testing.T, schedule *api.AnonymizationSchedule, funcs interceptor.Funcs, objects ...client.Object) (*AnonymizationScheduleReconciler, client.WithWatch) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects = append(objects, schedule)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AnonymizationSchedule{}, &api.AnonymizationRun{}).WithObjects(objects...).WithInterceptorFuncs(funcs).Build()
	return &AnonymizationScheduleReconciler{Client: c, Scheme: scheme, now: func() time.Time { return scheduleTestNow }}, c
}

func scheduleTestReconcile(t *testing.T, r *AnonymizationScheduleReconciler, schedule *api.AnonymizationSchedule) {
	t.Helper()
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schedule)}); err != nil {
		t.Fatal(err)
	}
}

func scheduleTestGet(t *testing.T, c client.Client, schedule *api.AnonymizationSchedule) *api.AnonymizationSchedule {
	t.Helper()
	got := &api.AnonymizationSchedule{}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(schedule), got); err != nil {
		t.Fatal(err)
	}
	return got
}

func scheduleTestRuns(t *testing.T, c client.Client) []api.AnonymizationRun {
	t.Helper()
	list := &api.AnonymizationRunList{}
	if err := c.List(t.Context(), list, client.InNamespace(scheduleTestNamespace)); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func scheduleTestInt64(value int64) *int64 { return &value }
