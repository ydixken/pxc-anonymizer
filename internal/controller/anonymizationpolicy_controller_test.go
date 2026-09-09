// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

const policyRuntimeValueKey = "value"

var _ = Describe("AnonymizationPolicy Controller", func() {
	var policy *api.AnonymizationPolicy
	var reconciler *AnonymizationPolicyReconciler
	var objects []client.Object
	var testContext context.Context

	BeforeEach(func() {
		testContext = context.Background()
		policy = policyRuntimeFixture("policy-validation")
		policy.Namespace = "default"
		objects = nil
		reconciler = &AnonymizationPolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}
	})
	AfterEach(func() {
		for _, object := range objects {
			Expect(client.IgnoreNotFound(k8sClient.Delete(testContext, object))).To(Succeed())
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(testContext, policy))).To(Succeed())
	})

	reconcile := func() *api.AnonymizationPolicy {
		request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(policy)}
		result, err := reconciler.Reconcile(testContext, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(10 * time.Minute))
		current := &api.AnonymizationPolicy{}
		Expect(k8sClient.Get(testContext, client.ObjectKeyFromObject(policy), current)).To(Succeed())
		return current
	}

	It("persists validity only after every referenced key resolves and clears it after a Secret disappears", func() {
		Expect(k8sClient.Create(testContext, policy)).To(Succeed())
		for _, object := range policyRuntimeReferences(policy.Namespace) {
			current := reconcile()
			condition := meta.FindStatusCondition(current.Status.Conditions, conditionPolicyValid)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(api.ReasonStepRefMissing))
			Expect(condition.ObservedGeneration).To(Equal(current.Generation))
			Expect(current.Status.Hash).To(BeEmpty())
			Expect(k8sClient.Create(testContext, object)).To(Succeed())
			objects = append(objects, object)
		}
		current := reconcile()
		Expect(meta.IsStatusConditionTrue(current.Status.Conditions, conditionPolicyValid)).To(BeTrue())
		condition := meta.FindStatusCondition(current.Status.Conditions, conditionPolicyValid)
		Expect(condition.Reason).To(Equal(api.ReasonSpecValid))
		Expect(current.Status.Hash).To(MatchRegexp(`^sha256:[a-f0-9]{64}$`))

		Expect(k8sClient.Delete(testContext, objects[len(objects)-1])).To(Succeed())
		current = reconcile()
		Expect(meta.IsStatusConditionFalse(current.Status.Conditions, conditionPolicyValid)).To(BeTrue())
		Expect(current.Status.Hash).To(BeEmpty())
	})

	It("rejects malformed RE2 before validating referenced objects", func() {
		policy.Spec.Databases[0].Name = ""
		policy.Spec.Databases[0].NamePattern = "^[example$"
		Expect(k8sClient.Create(testContext, policy)).To(Succeed())
		current := reconcile()
		condition := meta.FindStatusCondition(current.Status.Conditions, conditionPolicyValid)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal(api.ReasonPatternInvalid))
		Expect(condition.ObservedGeneration).To(Equal(current.Generation))
		Expect(current.Status.Hash).To(BeEmpty())
	})
})

func policyRuntimeFixture(name string) *api.AnonymizationPolicy {
	return &api.AnonymizationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "policy-test"},
		Spec: api.AnonymizationPolicySpec{
			Determinism: api.DeterminismSpec{
				Mode: runFixedSeedMode, SeedSecretRef: policyRuntimeSecretRef("policy-seed", "seed"),
			},
			Steps: []api.SQLStep{
				{Name: "setup", ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "policy-sql"}, Key: "setup.sql",
				}},
				{Name: "private", SecretKeyRef: policyRuntimeSecretRef("policy-private", "private.sql")},
			},
			Databases: []api.DatabasePolicy{{
				Name: admissionExample, Pre: []api.StepRef{{Name: "setup"}}, Post: []api.StepRef{{Name: "private"}},
				Tables: []api.TablePolicy{{Name: "items", Action: api.TableActionAnonymize, Columns: []api.ColumnRule{{
					Name: policyRuntimeValueKey, Strategy: api.StrategyConstant,
					Params: &api.StrategyParams{ValueFrom: policyRuntimeSecretRef("policy-constant", policyRuntimeValueKey)},
				}}}},
			}},
		},
	}
}

func policyRuntimeSecretRef(name, key string) *corev1.SecretKeySelector {
	return &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key}
}

func policyRuntimeReferences(namespace string) []client.Object {
	return []client.Object{
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "policy-sql", Namespace: namespace},
			Data:       map[string]string{"setup.sql": "SELECT 1;"},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "policy-private", Namespace: namespace},
			Data:       map[string][]byte{"private.sql": []byte("SELECT 2;")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "policy-seed", Namespace: namespace},
			Data:       map[string][]byte{"seed": []byte("synthetic-seed-for-policy-test")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "policy-constant", Namespace: namespace},
			Data:       map[string][]byte{policyRuntimeValueKey: []byte("synthetic-constant")},
		},
	}
}
