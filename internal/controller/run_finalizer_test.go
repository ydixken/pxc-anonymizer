// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
)

func TestRunFinalizerCleansOwnedResourcesFromEveryPhase(t *testing.T) {
	phases := []api.RunPhase{api.RunPhasePending, api.RunPhaseResolvingSource, api.RunPhaseProvisioning, api.RunPhaseRestoring,
		api.RunPhaseAnonymizing, api.RunPhaseBackingUp, api.RunPhasePublishing, api.RunPhasePruning, api.RunPhaseCleaningUp,
		api.RunPhaseCompleted, api.RunPhaseFailed}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			s := newRunTest(t, nil)
			s.toProvisioning(t)
			s.step(t)
			cluster := runTestObject(pxc.ClusterGVK.Kind, s.run.Status.TempCluster.Name)
			if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster); err != nil {
				t.Fatal(err)
			}
			oldUID := cluster.GetUID()
			generated := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: cluster.GetName() + "-ssl", Namespace: runTestNamespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, pxc.ClusterGVK)}}}
			foreign := generated.DeepCopy()
			foreign.Name = cluster.GetName() + "-ssl-internal"
			foreign.OwnerReferences[0].UID = types.UID("foreign-cluster")
			for _, secret := range []*corev1.Secret{generated, foreign} {
				if err := s.reconciler.Create(t.Context(), secret); err != nil {
					t.Fatal(err)
				}
			}
			backup := runTestObject(pxc.BackupGVK.Kind, "retained-output")
			if err := s.reconciler.Create(t.Context(), backup); err != nil {
				t.Fatal(err)
			}
			s.run.Status.Phase = phase
			s.run.Status.TempCluster.UID = ""
			if err := s.reconciler.Status().Update(t.Context(), s.run); err != nil {
				t.Fatal(err)
			}
			if err := s.reconciler.Delete(t.Context(), s.run); err != nil {
				t.Fatal(err)
			}
			s.step(t)
			if s.run.Status.TempCluster.UID != oldUID {
				t.Fatal("cluster UID was not persisted before deletion")
			}
			if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster); err != nil {
				t.Fatal(err)
			}
			if !cluster.GetDeletionTimestamp().IsZero() {
				t.Fatal("cluster deletion preceded its UID checkpoint")
			}
			s.step(t)
			if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster); err != nil {
				t.Fatal(err)
			}
			if cluster.GetDeletionTimestamp().IsZero() || !controllerutil.ContainsFinalizer(s.run, runFinalizer) {
				t.Fatal("finalizer did not wait for PXC finalizers")
			}
			s.clock = s.clock.Add(31 * time.Minute)
			s.step(t)
			condition := meta.FindStatusCondition(s.run.Status.Conditions, runConditionCleaned)
			if condition == nil || condition.Status != metav1.ConditionFalse || !strings.Contains(condition.Message, "deadline exceeded") {
				t.Fatal("cleanup deadline concealed a live cluster")
			}
			// Simulate the Percona controller completing its own finalizers, without teaching Run to strip them.
			cluster.SetFinalizers(nil)
			if err := s.reconciler.Update(t.Context(), cluster); err != nil {
				t.Fatal(err)
			}
			removed := false
			for range 6 {
				_, err := s.reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s.run)})
				if err != nil {
					t.Fatal(err)
				}
				current := &api.AnonymizationRun{}
				err = s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(s.run), current)
				if apierrors.IsNotFound(err) {
					removed = true
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !removed {
				t.Fatal("Run finalizer remained after owned cleanup finished")
			}
			for _, name := range []string{s.run.Status.TempCluster.SecretName, generated.Name} {
				err := s.reconciler.APIReader.Get(t.Context(), client.ObjectKey{Namespace: runTestNamespace, Name: name}, &corev1.Secret{})
				if !apierrors.IsNotFound(err) {
					t.Fatalf("owned credentials remain: %s %v", name, err)
				}
			}
			for _, name := range []string{runTestSourceUsers, foreign.Name} {
				if err := s.reconciler.APIReader.Get(t.Context(), client.ObjectKey{Namespace: runTestNamespace, Name: name}, &corev1.Secret{}); err != nil {
					t.Fatal("source or foreign Secret was deleted")
				}
			}
			if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(backup), backup); err != nil {
				t.Fatal("Run deletion removed the unowned output backup")
			}
		})
	}
}

func TestRunRejectsForeignAndDeletingChildren(t *testing.T) {
	for _, scenario := range []string{"foreign", "deleting"} {
		t.Run(scenario, func(t *testing.T) {
			s := newRunTest(t, nil)
			s.toProvisioning(t)
			object := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: s.run.Status.TempCluster.SecretName, Namespace: runTestNamespace,
				OwnerReferences: []metav1.OwnerReference{runOwner(s.run)}}}
			if scenario == "foreign" {
				object.OwnerReferences[0].UID = "other-run"
			} else {
				object.Finalizers = []string{"example.com/hold"}
			}
			if err := s.reconciler.Create(t.Context(), object); err != nil {
				t.Fatal(err)
			}
			if scenario == "deleting" {
				if err := s.reconciler.Delete(t.Context(), object); err != nil {
					t.Fatal(err)
				}
			}
			count := s.creates
			if _, err := s.reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s.run)}); err == nil {
				t.Fatal("foreign or deleting child was accepted")
			}
			if s.creates != count {
				t.Fatal("unsafe child lookup created further resources")
			}
		})
	}
}

func TestRunRetainStopsFailureCleanupButNotDeletion(t *testing.T) {
	s := newRunTest(t, func(run *api.AnonymizationRun, _ []client.Object) { run.Spec.Cleanup.OnFailure = "Retain" })
	s.toProvisioning(t)
	s.step(t)
	if _, err := s.reconciler.failRun(t.Context(), s.run, runConditionRestored, api.ReasonRestoreFailed, "synthetic restore failure"); err != nil {
		t.Fatal(err)
	}
	s.step(t)
	if !meta.IsStatusConditionTrue(s.run.Status.Conditions, runConditionRetained) {
		t.Fatal("failure retention was not recorded")
	}
	cluster := runTestObject(pxc.ClusterGVK.Kind, s.run.Status.TempCluster.Name)
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster); err != nil || !cluster.GetDeletionTimestamp().IsZero() {
		t.Fatal("retention deleted the cluster")
	}
	if err := s.reconciler.Delete(t.Context(), s.run); err != nil {
		t.Fatal(err)
	}
	s.step(t)
	if err := s.reconciler.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster); err != nil || cluster.GetDeletionTimestamp().IsZero() {
		t.Fatal("retention prevented explicit deletion cleanup")
	}
}
