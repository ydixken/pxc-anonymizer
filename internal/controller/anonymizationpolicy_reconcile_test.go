// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

func TestAnonymizationPolicyMissingKeys(t *testing.T) {
	for missing := range 4 {
		t.Run([]string{"config-map", "sql-secret", "seed-secret", "constant-secret"}[missing], func(t *testing.T) {
			policy := policyRuntimeFixture("missing-key")
			policy.Generation = 7
			refs := policyRuntimeReferences(policy.Namespace)
			switch object := refs[missing].(type) {
			case *corev1.ConfigMap:
				object.Data = nil
			case *corev1.Secret:
				object.Data = nil
			}
			reconciler := policyRuntimeReconciler(t, policy, refs...)
			current := policyRuntimeReconcile(t, reconciler, policy)
			policyRuntimeCondition(t, current, metav1.ConditionFalse, api.ReasonStepRefMissing)
			if current.Status.Hash != "" {
				t.Fatal("missing reference key received a policy hash")
			}
		})
	}
}

func TestAnonymizationPolicyReferencesStayInNamespace(t *testing.T) {
	policy := policyRuntimeFixture("namespace-isolation")
	refs := policyRuntimeReferences("another-namespace")
	reconciler := policyRuntimeReconciler(t, policy, refs...)
	current := policyRuntimeReconcile(t, reconciler, policy)
	policyRuntimeCondition(t, current, metav1.ConditionFalse, api.ReasonStepRefMissing)
}

func TestAnonymizationPolicyValidationErrorClassification(t *testing.T) {
	for _, invalidRule := range []bool{true, false} {
		name := "api-transport-failure"
		if invalidRule {
			name = "unsupported-locale"
		}
		t.Run(name, func(t *testing.T) {
			policy := policyRuntimeFixture(name)
			policy.Status.Hash = "previous-spec-hash"
			if invalidRule {
				column := &policy.Spec.Databases[0].Tables[0].Columns[0]
				column.Strategy = api.StrategyFirstName
				column.Params = &api.StrategyParams{Locale: "fr"}
			}
			reconciler := policyRuntimeReconciler(t, policy)
			failure := apierrors.NewServiceUnavailable("synthetic policy read failure")
			reads := 0
			reconciler.APIReader = fake.NewClientBuilder().WithScheme(reconciler.Scheme).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						reads++
						return failure
					},
				}).Build()
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(policy)}
			result, err := reconciler.Reconcile(context.Background(), request)
			if invalidRule {
				if err != nil || reads != 0 {
					t.Fatalf("invalid rule performed API reads or requested error retry: reads=%d err=%v", reads, err)
				}
			} else if !errors.Is(err, failure) || reads != 1 {
				t.Fatalf("transport failure was not propagated: reads=%d err=%v", reads, err)
			}
			if result.RequeueAfter != policyRefreshInterval {
				t.Fatalf("validation refresh = %s, want 10m", result.RequeueAfter)
			}
			current := &api.AnonymizationPolicy{}
			if err := reconciler.Get(context.Background(), request.NamespacedName, current); err != nil {
				t.Fatal(err)
			}
			reason := api.ReasonStepRefMissing
			if invalidRule {
				reason = api.ReasonPatternInvalid
			}
			policyRuntimeCondition(t, current, metav1.ConditionFalse, reason)
			if current.Status.Hash != "" {
				t.Fatal("failed validation retained a stale hash")
			}
		})
	}
}

func TestAnonymizationPolicyUncachedReferencesAndStableHash(t *testing.T) {
	policy := policyRuntimeFixture("uncached-references")
	refs := policyRuntimeReferences(policy.Namespace)
	reconciler := policyRuntimeReconciler(t, policy, refs...)
	current := policyRuntimeReconcile(t, reconciler, policy)
	policyRuntimeCondition(t, current, metav1.ConditionTrue, api.ReasonSpecValid)
	if current.Status.Hash == "" {
		t.Fatal("valid references did not receive a hash")
	}
	originalHash := current.Status.Hash
	secret := &corev1.Secret{}
	key := client.ObjectKeyFromObject(refs[3])
	if err := reconciler.APIReader.Get(context.Background(), key, secret); err != nil {
		t.Fatal(err)
	}
	secret.Data[policyRuntimeValueKey] = []byte("different-synthetic-constant")
	if err := reconciler.APIReader.(client.Client).Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	current = policyRuntimeReconcile(t, reconciler, policy)
	if current.Status.Hash != originalHash {
		t.Fatal("resolved Secret payload changed the public spec hash")
	}
	if err := reconciler.APIReader.(client.Client).Delete(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	current = policyRuntimeReconcile(t, reconciler, policy)
	policyRuntimeCondition(t, current, metav1.ConditionFalse, api.ReasonStepRefMissing)
	if current.Status.Hash != "" {
		t.Fatal("invalid policy retained its old hash")
	}
}

func TestAnonymizationPolicyHashIgnoresJSONKeyOrder(t *testing.T) {
	inputs := []string{
		`{"databases":[{"name":"example","tables":[{"name":"items","action":"Truncate"}]}],` +
			`"determinism":{"mode":"PerRun"}}`,
		`{"determinism":{"mode":"PerRun"},` +
			`"databases":[{"tables":[{"action":"Truncate","name":"items"}],"name":"example"}]}`,
	}
	var firstHash string
	for _, input := range inputs {
		policy := policyRuntimeFixture("ordered-json")
		policy.Spec = api.AnonymizationPolicySpec{}
		if err := json.Unmarshal([]byte(input), &policy.Spec); err != nil {
			t.Fatal(err)
		}
		reconciler := policyRuntimeReconciler(t, policy)
		current := policyRuntimeReconcile(t, reconciler, policy)
		policyRuntimeCondition(t, current, metav1.ConditionTrue, api.ReasonSpecValid)
		if current.Status.Hash == "" {
			t.Fatal("valid reordered policy received no hash")
		}
		if firstHash == "" {
			firstHash = current.Status.Hash
		} else if firstHash != current.Status.Hash {
			t.Fatalf("JSON key order changed hash: %s != %s", firstHash, current.Status.Hash)
		}
	}
}

func TestAnonymizationPolicyConfigMapMapping(t *testing.T) {
	policy := policyRuntimeFixture("referencing-policy")
	reconciler := policyRuntimeReconciler(t, policy)
	for _, other := range []*api.AnonymizationPolicy{
		policyRuntimeFixture("unrelated-policy"),
		policyRuntimeFixture("other-namespace-policy"),
	} {
		if other.Name == "unrelated-policy" {
			other.Spec.Steps[0].ConfigMapKeyRef.Name = "another-config-map"
		} else {
			other.Namespace = "another-namespace"
		}
		if err := reconciler.Create(context.Background(), other); err != nil {
			t.Fatal(err)
		}
	}
	requests := reconciler.mapConfigMapToPolicies(context.Background(), policyRuntimeReferences(policy.Namespace)[0])
	if len(requests) != 1 || requests[0].NamespacedName != client.ObjectKeyFromObject(policy) {
		t.Fatalf("ConfigMap event mapped outside its references: %#v", requests)
	}
}

func policyRuntimeReconciler(
	t *testing.T, policy *api.AnonymizationPolicy, references ...client.Object,
) *AnonymizationPolicyReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// References exist only in the APIReader, so a cached Secret lookup cannot pass.
	cached := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AnonymizationPolicy{}).
		WithObjects(policy).Build()
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(references...).Build()
	return &AnonymizationPolicyReconciler{
		Client: cached, Scheme: scheme, APIReader: reader,
		now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	}
}

func policyRuntimeReconcile(
	t *testing.T, reconciler *AnonymizationPolicyReconciler, policy *api.AnonymizationPolicy,
) *api.AnonymizationPolicy {
	t.Helper()
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(policy)}
	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != 10*time.Minute {
		t.Fatalf("Secret refresh delay = %s, want 10m", result.RequeueAfter)
	}
	current := &api.AnonymizationPolicy{}
	if err := reconciler.Get(context.Background(), client.ObjectKeyFromObject(policy), current); err != nil {
		t.Fatal(err)
	}
	return current
}

func policyRuntimeCondition(
	t *testing.T, policy *api.AnonymizationPolicy, status metav1.ConditionStatus, reason string,
) {
	t.Helper()
	condition := meta.FindStatusCondition(policy.Status.Conditions, conditionPolicyValid)
	if condition == nil || condition.Status != status || condition.Reason != reason ||
		condition.ObservedGeneration != policy.Generation {
		t.Fatalf("unexpected validation condition: %#v", condition)
	}
}
