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
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/conditions"
	runnerpolicy "github.com/ydixken/pxc-anonymizer/internal/runner/policy"
)

const (
	conditionPolicyValid  = "Valid"
	policyRefreshInterval = 10 * time.Minute
)

// AnonymizationPolicyReconciler validates references before a Run can snapshot the policy.
type AnonymizationPolicyReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	APIReader client.Reader
	now       func() time.Time
}

// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=anonymizationpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=pxc-anonymizer.io,resources=anonymizationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *AnonymizationPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	result, err := r.reconcilePolicy(ctx, req)
	if apierrors.IsConflict(err) {
		//nolint:staticcheck // Retry conflicts from fresh state without marking validation failed.
		return ctrl.Result{Requeue: true}, nil
	}
	return result, err
}

func (r *AnonymizationPolicyReconciler) reconcilePolicy(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	policy := &api.AnonymizationPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !policy.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	base := policy.DeepCopy()
	policy.Status.Hash = ""
	reason, message := api.ReasonSpecValid, "policy and all referenced keys are valid"
	status := metav1.ConditionTrue
	validationErr := runnerpolicy.Validate(policy.Spec)
	var retryErr error
	if validationErr == nil {
		validationErr = r.validatePolicyReferences(ctx, policy)
		if validationErr != nil && !errors.Is(validationErr, runnerpolicy.ErrStepRefMissing) {
			retryErr = validationErr
		}
	}
	if validationErr != nil {
		status = metav1.ConditionFalse
		reason, message = api.ReasonPatternInvalid, "policy patterns or rules are invalid"
		if errors.Is(validationErr, runnerpolicy.ErrStepRefMissing) {
			reason, message = api.ReasonStepRefMissing, "a referenced step, object or key is unavailable"
		} else if retryErr != nil {
			reason, message = api.ReasonStepRefMissing, "referenced objects could not be checked"
		}
	} else {
		var err error
		policy.Status.Hash, err = runnerpolicy.Hash(policy.Spec)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	now := time.Now().UTC()
	if r.now != nil {
		now = r.now().UTC()
	}
	conditions.Set(&policy.Status.Conditions, policy.Generation, now, metav1.Condition{
		Type: conditionPolicyValid, Status: status, Reason: reason, Message: message,
	})
	if !apiequality.Semantic.DeepEqual(policy.Status, base.Status) {
		patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
		patchErr := r.Status().Patch(ctx, policy, patch)
		retryErr = errors.Join(retryErr, patchErr)
	}
	return ctrl.Result{RequeueAfter: policyRefreshInterval}, retryErr
}

func (r *AnonymizationPolicyReconciler) validatePolicyReferences(
	ctx context.Context, policy *api.AnonymizationPolicy,
) error {
	for _, step := range policy.Spec.Steps {
		if step.ConfigMapKeyRef != nil {
			if err := r.policyConfigMapKey(ctx, policy.Namespace, step.ConfigMapKeyRef); err != nil {
				return err
			}
		}
		if step.SecretKeyRef != nil {
			if err := r.policySecretKey(ctx, policy.Namespace, step.SecretKeyRef); err != nil {
				return err
			}
		}
	}
	if ref := policy.Spec.Determinism.SeedSecretRef; ref != nil {
		if err := r.policySecretKey(ctx, policy.Namespace, ref); err != nil {
			return err
		}
	}
	for _, database := range policy.Spec.Databases {
		for _, table := range database.Tables {
			for _, column := range table.Columns {
				if column.Params != nil && column.Params.ValueFrom != nil {
					if err := r.policySecretKey(ctx, policy.Namespace, column.Params.ValueFrom); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (r *AnonymizationPolicyReconciler) policyConfigMapKey(
	ctx context.Context, namespace string, ref *corev1.ConfigMapKeySelector,
) error {
	if r.APIReader == nil {
		return errors.New("policy reference validation requires an APIReader")
	}
	configMap := &corev1.ConfigMap{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: ConfigMap not found", runnerpolicy.ErrStepRefMissing)
		}
		return err
	}
	_, textKey := configMap.Data[ref.Key]
	_, binaryKey := configMap.BinaryData[ref.Key]
	if !textKey && !binaryKey {
		return fmt.Errorf("%w: ConfigMap key not found", runnerpolicy.ErrStepRefMissing)
	}
	return nil
}

func (r *AnonymizationPolicyReconciler) policySecretKey(
	ctx context.Context, namespace string, ref *corev1.SecretKeySelector,
) error {
	if r.APIReader == nil {
		return errors.New("policy reference validation requires an APIReader")
	}
	secret := &corev1.Secret{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: Secret not found", runnerpolicy.ErrStepRefMissing)
		}
		return err
	}
	if _, ok := secret.Data[ref.Key]; !ok {
		return fmt.Errorf("%w: Secret key not found", runnerpolicy.ErrStepRefMissing)
	}
	return nil
}

func (r *AnonymizationPolicyReconciler) mapConfigMapToPolicies(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	policies := &api.AnonymizationPolicyList{}
	if err := r.List(ctx, policies, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "map ConfigMap event to policies")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(policies.Items))
	for _, policy := range policies.Items {
		for _, step := range policy.Spec.Steps {
			if step.ConfigMapKeyRef != nil && step.ConfigMapKeyRef.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&policy)})
				break
			}
		}
	}
	return requests
}

func (r *AnonymizationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.AnonymizationPolicy{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.mapConfigMapToPolicies)).
		Named("anonymizationpolicy").
		Complete(r)
}
