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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pxcanonymizeriov1alpha1 "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

var _ = Describe("AnonymizationSchedule Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		anonymizationschedule := &pxcanonymizeriov1alpha1.AnonymizationSchedule{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind AnonymizationSchedule")
			err := k8sClient.Get(ctx, typeNamespacedName, anonymizationschedule)
			if err != nil && errors.IsNotFound(err) {
				resource := &pxcanonymizeriov1alpha1.AnonymizationSchedule{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					Spec: pxcanonymizeriov1alpha1.AnonymizationScheduleSpec{
						Schedule: "0 0 * * *",
						Template: pxcanonymizeriov1alpha1.AnonymizationRunTemplate{
							Spec: pxcanonymizeriov1alpha1.AnonymizationRunSpec{
								Source: pxcanonymizeriov1alpha1.RunSource{
									Destination: admissionDestination,
									Restore: pxcanonymizeriov1alpha1.RestoreS3Credentials{
										CredentialsSecret: admissionCredentials,
									},
								},
								PolicyRef: corev1.LocalObjectReference{Name: admissionPolicyName},
								TempCluster: pxcanonymizeriov1alpha1.TempClusterSpec{
									CRVersion:   admissionPXCVersion,
									Image:       admissionPXCImage,
									BackupImage: admissionBackupImage,
									Storage: pxcanonymizeriov1alpha1.TempClusterStorage{
										StorageClassName: admissionStorageClass,
										Size:             resource.MustParse("10Gi"),
									},
								},
								Output: pxcanonymizeriov1alpha1.OutputSpec{
									ObjectStorage: pxcanonymizeriov1alpha1.ObjectStorageSpec{
										Bucket:      admissionOutputBucket,
										EndpointURL: admissionEndpoint,
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &pxcanonymizeriov1alpha1.AnonymizationSchedule{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance AnonymizationSchedule")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &AnonymizationScheduleReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
