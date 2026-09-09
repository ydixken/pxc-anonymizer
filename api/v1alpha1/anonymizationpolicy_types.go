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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// AnonymizationPolicySpec is snapshotted at Run start so edits cannot change an active Run.
type AnonymizationPolicySpec struct {
	// +optional
	// +kubebuilder:default={}
	Determinism DeterminismSpec `json:"determinism,omitempty"`
	// +optional
	// +kubebuilder:default={}
	Defaults *PolicyDefaults `json:"defaults,omitempty"`
	// SQL stays in referenced objects so credentials never need to be embedded in this policy.
	// +optional
	// +listType=map
	// +listMapKey=name
	Steps []SQLStep `json:"steps,omitempty"`
	// Pattern-only entries have no name to use as a required map key.
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	Databases []DatabasePolicy `json:"databases"`
}

// DeterminismSpec defaults to unlinkable identities between Runs.
// +kubebuilder:validation:XValidation:rule="!has(self.mode) || self.mode != 'Fixed' || has(self.seedSecretRef)",message="Fixed determinism requires seedSecretRef"
type DeterminismSpec struct {
	// Fixed mode reuses the referenced seed for stable identities across refreshes.
	// +optional
	// +kubebuilder:validation:Enum=PerRun;Fixed
	// +kubebuilder:default=PerRun
	Mode string `json:"mode,omitempty"`
	// +optional
	SeedSecretRef *corev1.SecretKeySelector `json:"seedSecretRef,omitempty"`
}

// PolicyDefaults supplies values inherited by tables and columns when they omit an override.
type PolicyDefaults struct {
	// +optional
	// +kubebuilder:default=5000
	// +kubebuilder:validation:Maximum=50000
	PageSize int32 `json:"pageSize,omitempty"`
	// +optional
	// +kubebuilder:default=Keep
	OnNull ValueHandling `json:"onNull,omitempty"`
	// +optional
	// +kubebuilder:default=Keep
	OnEmpty ValueHandling `json:"onEmpty,omitempty"`
	// +optional
	// +kubebuilder:default=en
	Locale string `json:"locale,omitempty"`
	// An omitted domain lets the generator select one.
	// +optional
	EmailDomain string `json:"emailDomain,omitempty"`
}

// SQLStep uses references so sensitive SQL can be carried by a Secret.
// +kubebuilder:validation:XValidation:rule="(has(self.configMapKeyRef) ? 1 : 0) + (has(self.secretKeyRef) ? 1 : 0) == 1",message="set exactly one of configMapKeyRef or secretKeyRef"
type SQLStep struct {
	// +required
	// +kubebuilder:validation:Pattern="^[a-z0-9-]{1,63}$"
	Name string `json:"name"`
	// +optional
	ConfigMapKeyRef *corev1.ConfigMapKeySelector `json:"configMapKeyRef,omitempty"`
	// +optional
	SecretKeyRef *corev1.SecretKeySelector `json:"secretKeyRef,omitempty"`
	// Failures stop the Run unless continuing was explicitly requested.
	// +optional
	ContinueOnError bool `json:"continueOnError,omitempty"`
}

// StepRef preserves the declared execution order of reusable SQL steps.
type StepRef struct {
	// +required
	Name string `json:"name"`
}

// DatabasePolicy can match an exact database or an anchored RE2 expression.
// +kubebuilder:validation:XValidation:rule="(has(self.name) ? 1 : 0) + (has(self.namePattern) ? 1 : 0) == 1",message="set exactly one of name or namePattern"
type DatabasePolicy struct {
	// +optional
	// +kubebuilder:validation:Pattern="^[A-Za-z0-9_$]{1,64}$"
	Name string `json:"name,omitempty"`
	// Pattern compilation belongs to policy validation, before a Run starts.
	// +optional
	NamePattern string `json:"namePattern,omitempty"`
	// Missing databases or zero pattern matches fail unless this is true.
	// +optional
	Optional bool `json:"optional,omitempty"`
	// +optional
	Pre []StepRef `json:"pre,omitempty"`
	// +optional
	Post []StepRef `json:"post,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=name
	Tables []TablePolicy `json:"tables,omitempty"`
}

// TablePolicy allows a table to be truncated without carrying column rules.
// +kubebuilder:validation:XValidation:rule="self.action != 'Truncate' || !has(self.columns)",message="Truncate takes no columns"
// +kubebuilder:validation:XValidation:rule="self.action != 'Anonymize' || (has(self.columns) && size(self.columns) > 0)",message="Anonymize needs at least one column"
type TablePolicy struct {
	// +required
	Name string `json:"name"`
	// +optional
	// +kubebuilder:default=Anonymize
	Action TableAction `json:"action,omitempty"`
	// +optional
	Optional bool `json:"optional,omitempty"`
	// Omission selects the table's primary key; an override permits a unique index instead.
	// +optional
	PrimaryKey []string `json:"primaryKey,omitempty"`
	// +optional
	Ignore *IgnoreSpec `json:"ignore,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=name
	Columns []ColumnRule `json:"columns,omitempty"`
	// +optional
	// +kubebuilder:validation:Maximum=50000
	PageSize *int32 `json:"pageSize,omitempty"`
}

// TableAction separates row transformations from whole-table removal.
// +kubebuilder:validation:Enum=Anonymize;Truncate
type TableAction string

const (
	TableActionAnonymize TableAction = "Anonymize"
	TableActionTruncate  TableAction = "Truncate"
)

// IgnoreSpec excludes matching rows from a table's transformation.
type IgnoreSpec struct {
	// Supply a WHERE expression without the keyword or a statement delimiter.
	// +required
	// +kubebuilder:validation:MaxLength=4096
	// +kubebuilder:validation:Pattern="^[^;]*$"
	Where string `json:"where"`
}

// ColumnRule leaves unspecified behavior to policy and strategy defaults.
// +kubebuilder:validation:XValidation:rule="self.strategy in ['alphanumeric','digits'] ? (has(self.params) && has(self.params.length)) : true",message="alphanumeric and digits require params.length"
// +kubebuilder:validation:XValidation:rule="self.strategy == 'constant' ? (has(self.params) && (has(self.params.value) || has(self.params.valueFrom))) : true",message="constant requires params.value or params.valueFrom"
// +kubebuilder:validation:XValidation:rule="self.strategy in ['null','mask','constant'] ? !has(self.consistent) : true",message="null, mask and constant do not accept consistent"
// +kubebuilder:validation:XValidation:rule="!has(self.params) || !has(self.params.length) || self.params.length <= 4096",message="params.length must not exceed 4096"
type ColumnRule struct {
	// +required
	Name string `json:"name"`
	// +required
	Strategy Strategy `json:"strategy"`
	// +optional
	Params *StrategyParams `json:"params,omitempty"`
	// A true override makes equal inputs share outputs across tables within the Run.
	// +optional
	Consistent *bool `json:"consistent,omitempty"`
	// +optional
	OnNull ValueHandling `json:"onNull,omitempty"`
	// +optional
	OnEmpty ValueHandling `json:"onEmpty,omitempty"`
}

// ValueHandling preserves null or empty inputs unless generation is requested.
// +kubebuilder:validation:Enum=Keep;Generate
type ValueHandling string

const (
	ValueHandlingKeep     ValueHandling = "Keep"
	ValueHandlingGenerate ValueHandling = "Generate"
)

// AnonymizationPolicyStatus exposes validation before a Run snapshots the policy.
type AnonymizationPolicyStatus struct {
	// Hash is the SHA-256 of canonical policy JSON, also recorded on Runs.
	// +optional
	Hash string `json:"hash,omitempty"`
	// Valid reports SpecValid, StepRefMissing or PatternInvalid.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=apol

// AnonymizationPolicy defines reusable transformations without embedding SQL bodies.
type AnonymizationPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AnonymizationPolicy
	// +required
	Spec AnonymizationPolicySpec `json:"spec"`

	// status defines the observed state of AnonymizationPolicy
	// +optional
	Status AnonymizationPolicyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AnonymizationPolicyList contains a list of AnonymizationPolicy
type AnonymizationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AnonymizationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AnonymizationPolicy{}, &AnonymizationPolicyList{})
		return nil
	})
}
