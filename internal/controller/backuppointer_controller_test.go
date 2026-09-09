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
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/metrics"
	"github.com/ydixken/pxc-anonymizer/internal/pointer"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
)

const (
	pointerEnvDestination   = "s3://example-backups/daily/selected"
	pointerEnvClusterFilter = "cluster"
	pointerEnvStorageFilter = "storage"
	pointerEnvCredentials   = "storage-credentials"
)

var _ = Describe("BackupPointer reconciliation", Label("backuppointer-runtime"), func() {
	var (
		namespace  *corev1.Namespace
		bp         *api.BackupPointer
		reconciler *BackupPointerReconciler
		store      *reconcileStore
		now        time.Time
	)

	BeforeEach(func() {
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "pointer-envtest-"}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		bp = reconcilePointer()
		bp.Namespace, bp.UID, bp.Generation = namespace.Name, "", 0
		bp.Spec.Source.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{reconcileLabelKey: reconcileLabel}}
		Expect(k8sClient.Create(ctx, bp)).To(Succeed())
		now = reconcileTime()
		store = &reconcileStore{}
		reconciler = &BackupPointerReconciler{
			Client: k8sClient, APIReader: k8sClient, Scheme: k8sClient.Scheme(),
			now: func() time.Time { return now },
			resolveStore: func(context.Context, client.Reader, string, api.ObjectStorageSpec) (pointer.Store, error) {
				return store, nil
			},
		}
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &api.BackupPointer{}, client.InNamespace(namespace.Name))).To(Succeed())
		backups := &unstructured.Unstructured{}
		backups.SetGroupVersionKind(pxc.BackupGVK)
		Expect(k8sClient.DeleteAllOf(ctx, backups, client.InNamespace(namespace.Name))).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &corev1.Secret{}, client.InNamespace(namespace.Name))).To(Succeed())
		Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())
		metrics.ForgetBackupPointer(namespace.Name, bp.Name)
	})

	reconcile := func() ctrl.Result {
		GinkgoHelper()
		result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(bp)})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(bp), bp)).To(Succeed())
		return result
	}
	expectCondition := func(kind string, status metav1.ConditionStatus, reason string) {
		GinkgoHelper()
		condition := meta.FindStatusCondition(bp.Status.Conditions, kind)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(status))
		Expect(condition.Reason).To(Equal(reason))
		Expect(condition.ObservedGeneration).To(Equal(bp.Generation))
	}
	successfulBackup := func() *unstructured.Unstructured {
		GinkgoHelper()
		backup := fakePerconaBackup(namespace.Name, reconcileBackupName, reconcileSource, reconcileStorage)
		markBackupSucceeded(backup, pointerEnvDestination, now.Add(-10*time.Minute))
		return backup
	}
	waitForNewerCreation := func(backup *unstructured.Unstructured) {
		GinkgoHelper()
		// The API server owns creation timestamps at second precision.
		Eventually(time.Now, 2*time.Second, 10*time.Millisecond).
			Should(BeTemporally(">", backup.GetCreationTimestamp().Add(time.Second)))
	}

	It("publishes only matching successful backups and expires freshness with the injected clock", func() {
		selected := successfulBackup()
		for _, mismatch := range []string{pointerEnvClusterFilter, pointerEnvStorageFilter, "label"} {
			cluster, storage := reconcileSource, reconcileStorage
			if mismatch == pointerEnvClusterFilter {
				cluster = "other-cluster"
			}
			if mismatch == pointerEnvStorageFilter {
				storage = "other-storage"
			}
			backup := fakePerconaBackup(namespace.Name, "other-"+mismatch, cluster, storage)
			if mismatch == "label" {
				backup.SetLabels(map[string]string{reconcileLabelKey: "excluded"})
				Expect(k8sClient.Update(ctx, backup)).To(Succeed())
			}
			markBackupSucceeded(backup, "s3://example-backups/other/"+mismatch, now.Add(-time.Minute))
		}
		result := reconcile()
		Expect(bp.Status.Current).NotTo(BeNil())
		Expect(bp.Status.Current.BackupName).To(Equal(selected.GetName()))
		Expect(bp.Status.Current.SchemaVersion).To(Equal(int32(pointer.SchemaVersion)))
		Expect(bp.Status.Backups).To(Equal(api.BackupCounts{Succeeded: 1}))
		Expect(store.documents).To(HaveLen(1))
		Expect(store.documents[0].PublishedBy.UID).To(Equal(string(bp.UID)))
		Expect(store.documents[0].Destination).To(Equal(pointerEnvDestination))
		Expect(store.documents[0].SchemaVersion).To(Equal(pointer.SchemaVersion))
		Expect(result.RequeueAfter).To(Equal(time.Hour))
		expectCondition(conditionBackupSelected, metav1.ConditionTrue, api.ReasonLatestSucceeded)
		expectCondition(conditionPublished, metav1.ConditionTrue, api.ReasonUploaded)
		expectCondition(conditionReady, metav1.ConditionTrue, api.ReasonPointerCurrent)
		expectCondition(conditionFresh, metav1.ConditionTrue, api.ReasonWithinStaleAfter)
		now = now.Add(2 * time.Hour)
		reconcile()
		Expect(store.puts).To(Equal(1))
		expectCondition(conditionPublished, metav1.ConditionTrue, api.ReasonVerified)
		expectCondition(conditionFresh, metav1.ConditionFalse, api.ReasonStaleBackup)
	})

	It("publishes the previous success while a newer backup is running", func() {
		selected := successfulBackup()
		waitForNewerCreation(selected)
		running := fakePerconaBackup(namespace.Name, "newer-running", reconcileSource, reconcileStorage)
		markBackupState(running, map[string]any{runTestStateField: string(pxc.BackupRunning)})
		reconcile()
		Expect(bp.Status.Current.BackupName).To(Equal(selected.GetName()))
		Expect(bp.Status.Backups.Running).To(Equal(int32(1)))
		Expect(store.puts).To(Equal(1))
		expectCondition(conditionBackupSelected, metav1.ConditionTrue, api.ReasonNewerBackupInProgress)
		expectCondition(conditionReady, metav1.ConditionTrue, api.ReasonPointerCurrent)
	})

	It("retains the published pointer and reports the newer failed count", func() {
		selected := successfulBackup()
		reconcile()
		first := bp.Status.Current.DeepCopy()
		waitForNewerCreation(selected)
		failed := fakePerconaBackup(namespace.Name, "newer-failed", reconcileSource, reconcileStorage)
		markBackupFailed(failed, "running deadline seconds exceeded")
		reconcile()
		Expect(bp.Status.Current).To(Equal(first))
		Expect(bp.Status.Previous).To(BeNil())
		Expect(bp.Status.Backups.Failed).To(Equal(int32(1)))
		Expect(store.puts).To(Equal(1))
		expectCondition(conditionBackupSelected, metav1.ConditionTrue, api.ReasonNewerBackupFailed)
		Expect(meta.FindStatusCondition(bp.Status.Conditions, conditionBackupSelected).Message).
			To(ContainSubstring("1 newer backups failed"))
		expectCondition(conditionPublished, metav1.ConditionTrue, api.ReasonUploaded)
	})

	DescribeTable("distinguishes unavailable and invalid namespace-local credentials",
		func(createSecret bool, reason string) {
			successfulBackup()
			bp.Spec.Target.ObjectStorage.CredentialsSecretRef = &corev1.LocalObjectReference{Name: pointerEnvCredentials}
			Expect(k8sClient.Update(ctx, bp)).To(Succeed())
			if createSecret {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: pointerEnvCredentials, Namespace: namespace.Name},
					Data:       map[string][]byte{runAWSAccessKey: []byte("synthetic-access")},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			}
			reconciler.resolveStore = nil
			Expect(reconcile().RequeueAfter).To(Equal(pointerRetryInterval))
			Expect(bp.Status.Current).To(BeNil())
			expectCondition(conditionPublished, metav1.ConditionFalse, reason)
			expectCondition(conditionReady, metav1.ConditionFalse, api.ReasonPointerCurrent)
		},
		Entry("missing Secret", false, api.ReasonCredentialsUnavailable),
		Entry("missing secret-key data", true, api.ReasonCredentialsInvalid),
	)

	It("maps backup events and reconciles spec changes without feeding back status updates", func() {
		backup := fakePerconaBackup(namespace.Name, reconcileBackupName, reconcileSource, reconcileStorage)
		var primaryGets atomic.Int64
		key := client.ObjectKeyFromObject(bp)
		manager, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  k8sClient.Scheme(),
			Cache:   cache.Options{DefaultNamespaces: map[string]cache.Config{namespace.Name: {}}},
			Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
			NewClient: func(config *rest.Config, options client.Options) (client.Client, error) {
				base, clientErr := client.NewWithWatch(config, options)
				if clientErr != nil {
					return nil, clientErr
				}
				return interceptor.NewClient(base, interceptor.Funcs{
					Get: func(getCtx context.Context, c client.WithWatch, objectKey client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*api.BackupPointer); ok && objectKey == key {
							primaryGets.Add(1)
						}
						return c.Get(getCtx, objectKey, obj, opts...)
					},
				}), nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		reconciler.Client, reconciler.APIReader = manager.GetClient(), manager.GetAPIReader()
		Expect(reconciler.SetupWithManager(manager)).To(Succeed())
		managerCtx, stop := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() { stopped <- manager.Start(managerCtx) }()
		defer func() {
			stop()
			Eventually(stopped, 10*time.Second).Should(Receive(Succeed()))
		}()
		cacheCtx, stopCacheWait := context.WithTimeout(ctx, 10*time.Second)
		defer stopCacheWait()
		Expect(manager.GetCache().WaitForCacheSync(cacheCtx)).To(BeTrue())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, bp)).To(Succeed())
			g.Expect(bp.Status.Phase).To(Equal(api.BackupPointerPhaseNoCandidate))
		}, 10*time.Second, 20*time.Millisecond).Should(Succeed())

		markBackupSucceeded(backup, pointerEnvDestination, now.Add(-time.Minute))
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, bp)).To(Succeed())
			g.Expect(bp.Status.Current).NotTo(BeNil())
			g.Expect(bp.Status.Current.BackupName).To(Equal(backup.GetName()))
			g.Expect(meta.IsStatusConditionTrue(bp.Status.Conditions, conditionReady)).To(BeTrue())
		}, 10*time.Second, 20*time.Millisecond).Should(Succeed())
		calls := primaryGets.Load()
		bp.Status.Previous = bp.Status.Current.DeepCopy()
		Expect(k8sClient.Status().Update(ctx, bp)).To(Succeed())
		observedVersion := bp.ResourceVersion
		Eventually(func(g Gomega) {
			cached := &api.BackupPointer{}
			g.Expect(manager.GetCache().Get(ctx, key, cached)).To(Succeed())
			g.Expect(cached.ResourceVersion).To(Equal(observedVersion))
		}, 10*time.Second, 20*time.Millisecond).Should(Succeed())
		Consistently(primaryGets.Load, 300*time.Millisecond, 20*time.Millisecond).Should(Equal(calls))

		previousGeneration := bp.Generation
		bp.Spec.Suspend = true
		Expect(k8sClient.Update(ctx, bp)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, bp)).To(Succeed())
			g.Expect(bp.Generation).To(BeNumerically(">", previousGeneration))
			condition := meta.FindStatusCondition(bp.Status.Conditions, conditionReady)
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Reason).To(Equal(api.ReasonSuspended))
			g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(condition.ObservedGeneration).To(Equal(bp.Generation))
		}, 10*time.Second, 20*time.Millisecond).Should(Succeed())
		Expect(primaryGets.Load()).To(BeNumerically(">", calls))
	})
})
