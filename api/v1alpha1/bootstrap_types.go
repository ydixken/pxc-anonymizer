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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// BootstrapPhase identifies the current stage of a bootstrap operation.
// +kubebuilder:validation:Enum=Pending;WaitingForClusters;ResolvingPointer;PausingCrossplane;Restoring;ResumingCrossplane;RecreatingCrossplane;Completed;Failed
type BootstrapPhase string

const (
	BootstrapPhasePending              BootstrapPhase = "Pending"
	BootstrapPhaseWaitingForClusters   BootstrapPhase = "WaitingForClusters"
	BootstrapPhaseResolvingPointer     BootstrapPhase = "ResolvingPointer"
	BootstrapPhasePausingCrossplane    BootstrapPhase = "PausingCrossplane"
	BootstrapPhaseRestoring            BootstrapPhase = "Restoring"
	BootstrapPhaseResumingCrossplane   BootstrapPhase = "ResumingCrossplane"
	BootstrapPhaseRecreatingCrossplane BootstrapPhase = "RecreatingCrossplane"
	BootstrapPhaseCompleted            BootstrapPhase = "Completed"
	BootstrapPhaseFailed               BootstrapPhase = "Failed"
)

// BootstrapSpec restores one immutable source into an ordered set of clusters.
type BootstrapSpec struct {
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="pointer is immutable"
	Pointer BootstrapSource `json:"pointer"`
	// Restore supplies credentials for inline backupSource.s3 restores.
	// +required
	Restore RestoreS3Credentials `json:"restore"`
	// Targets are restored in list order; duplicate cluster identities are invalid.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=pxcCluster
	Targets []BootstrapTarget `json:"targets"`
	// Crossplane is left untouched when this field is omitted.
	// +optional
	Crossplane *CrossplaneSpec `json:"crossplane,omitempty"`
	// +optional
	// +kubebuilder:default={}
	Timeouts BootstrapTimeouts `json:"timeouts,omitempty"`
	// Trigger re-arms a terminal Bootstrap when its value changes.
	// +optional
	Trigger string `json:"trigger,omitempty"`
}

// BootstrapSource selects one pointer transport or an inline backup destination.
// +kubebuilder:validation:XValidation:rule="(has(self.http) ? 1 : 0) + (has(self.s3) ? 1 : 0) + (has(self.destination) ? 1 : 0) == 1",message="set exactly one of http, s3 or destination"
type BootstrapSource struct {
	// +optional
	HTTP *HTTPPointerSource `json:"http,omitempty"`
	// +optional
	S3 *S3PointerSource `json:"s3,omitempty"`
	// +optional
	// +kubebuilder:validation:Pattern="^s3://.+$"
	Destination string `json:"destination,omitempty"`
	// MaxAge checks pointer publishedAt; nil disables the age check.
	// +optional
	MaxAge *metav1.Duration `json:"maxAge,omitempty"`
}

// BootstrapTarget configures one target cluster and optional restore overrides.
type BootstrapTarget struct {
	// +required
	// +kubebuilder:validation:MinLength=1
	PXCCluster string `json:"pxcCluster"`
	// +optional
	ContainerOptions *XtrabackupContainerOptions `json:"containerOptions,omitempty"`
	// Restore overrides the shared credentials for this target.
	// +optional
	Restore *RestoreS3Credentials `json:"restore,omitempty"`
}

// BootstrapTimeouts bounds each stage; a zero clustersReady duration is unbounded.
type BootstrapTimeouts struct {
	// +optional
	// +kubebuilder:default="0s"
	ClustersReady *metav1.Duration `json:"clustersReady,omitempty"`
	// +optional
	// +kubebuilder:default="15m"
	Pointer *metav1.Duration `json:"pointer,omitempty"`
	// Restore applies separately to each target.
	// +optional
	// +kubebuilder:default="3h"
	Restore *metav1.Duration `json:"restore,omitempty"`
	// +optional
	// +kubebuilder:default="15m"
	Crossplane *metav1.Duration `json:"crossplane,omitempty"`
}

// CrossplaneSpec selects managed resources to pause and resume around restores.
type CrossplaneSpec struct {
	// +optional
	// +kubebuilder:default="mysql.sql.crossplane.io"
	Group string `json:"group,omitempty"`
	// +optional
	// +kubebuilder:default="v1alpha1"
	Version string `json:"version,omitempty"`
	// Kinds contains plural resource names.
	// +optional
	// +listType=set
	// +kubebuilder:default={databases,users,grants}
	Kinds []string `json:"kinds,omitempty"`
	// A nil selector includes every cluster-scoped object of the selected kinds.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
	// +optional
	// +kubebuilder:default=true
	Pause *bool `json:"pause,omitempty"`
	// +optional
	// +kubebuilder:default=true
	Resume *bool `json:"resume,omitempty"`
	// A nil Recreate preserves the managed resources.
	// +optional
	Recreate *CrossplaneRecreate `json:"recreate,omitempty"`
	// ReadinessTimeout bounds waiting for both Ready and Synced on touched objects.
	// +optional
	// +kubebuilder:default="15m"
	ReadinessTimeout *metav1.Duration `json:"readinessTimeout,omitempty"`
}

// CrossplaneRecreate controls recreation after restore; database deletion requires the runtime Orphan guard.
type CrossplaneRecreate struct {
	// +optional
	// +listType=set
	// +kubebuilder:default={users,grants}
	Kinds []string `json:"kinds,omitempty"`
	// +optional
	// +kubebuilder:default=false
	RemoveFinalizers bool `json:"removeFinalizers,omitempty"`
	// +optional
	// +kubebuilder:default=true
	WaitForRecreation *bool `json:"waitForRecreation,omitempty"`
}

// BootstrapTargetStatus records the restore state of one target cluster.
type BootstrapTargetStatus struct {
	// +required
	PXCCluster string `json:"pxcCluster"`
	// +optional
	RestoreName string `json:"restoreName,omitempty"`
	// State mirrors the PXC restore state without importing its Go API.
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// CrossplaneObjectReference identifies a cluster-scoped managed resource.
type CrossplaneObjectReference struct {
	// +required
	Kind string `json:"kind"`
	// +required
	Name string `json:"name"`
	// +optional
	UID types.UID `json:"uid,omitempty"`
}

// CrossplaneRecreation preserves the original UID so reconciliation never deletes a replacement.
type CrossplaneRecreation struct {
	// +required
	Kind string `json:"kind"`
	// +required
	Name string `json:"name"`
	// +required
	UID types.UID `json:"uid"`
	// +optional
	DeletionObserved bool `json:"deletionObserved,omitempty"`
	// +optional
	Complete bool `json:"complete,omitempty"`
}

// CrossplaneStatus records exactly which managed resources were touched or remain pending.
type CrossplaneStatus struct {
	// +optional
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=name
	Paused []CrossplaneObjectReference `json:"paused,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=name
	Resumed []CrossplaneObjectReference `json:"resumed,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=name
	Recreated []CrossplaneObjectReference `json:"recreated,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=name
	Pending []CrossplaneObjectReference `json:"pending,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=name
	Recreating []CrossplaneRecreation `json:"recreating,omitempty"`
}

// BootstrapTargetExecution pins identity and restore references across reconciliation restarts.
type BootstrapTargetExecution struct {
	// +required
	PXCCluster string `json:"pxcCluster"`
	// +optional
	UID types.UID `json:"uid,omitempty"`
	// +optional
	RestoreUID types.UID `json:"restoreUID,omitempty"`
	// RestoreAttempted prevents recreating a vanished restore after a lost status response.
	// +optional
	RestoreAttempted bool `json:"restoreAttempted,omitempty"`
	// +required
	Restore RestoreS3Credentials `json:"restore"`
}

// BootstrapExecutionStatus freezes non-secret execution inputs before external effects.
type BootstrapExecutionStatus struct {
	// ID separates repeated executions when trigger values are reused.
	// +optional
	ID string `json:"id,omitempty"`
	// +required
	SpecHash string `json:"specHash"`
	// +optional
	CrossplaneGroup string `json:"crossplaneGroup,omitempty"`
	// +optional
	CrossplaneVersion string `json:"crossplaneVersion,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=name
	Selected []CrossplaneObjectReference `json:"selected,omitempty"`
	// +required
	// +listType=map
	// +listMapKey=pxcCluster
	Targets []BootstrapTargetExecution `json:"targets"`
}

// BootstrapRunRecord retains the outcome of one completed trigger.
type BootstrapRunRecord struct {
	// +optional
	Trigger string `json:"trigger,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// +optional
	Outcome BootstrapPhase `json:"outcome,omitempty"`
	// +optional
	Destination string `json:"destination,omitempty"`
}

// BootstrapStatus records progress, touched resources and the last five outcomes.
type BootstrapStatus struct {
	// +optional
	Execution *BootstrapExecutionStatus `json:"execution,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	ObservedTrigger string `json:"observedTrigger,omitempty"`
	// +optional
	Phase BootstrapPhase `json:"phase,omitempty"`
	// Conditions include ClustersReady, PointerResolved, CrossplanePaused, Restored,
	// CrossplaneResumed, CrossplaneRecreated, Complete and Failed.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	Source *ResolvedSource `json:"source,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=pxcCluster
	Targets []BootstrapTargetStatus `json:"targets,omitempty"`
	// TargetsSummary contains the completed/total count used by the printer column.
	// +optional
	TargetsSummary string `json:"targetsSummary,omitempty"`
	// +optional
	Crossplane *CrossplaneStatus `json:"crossplane,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=5
	History []BootstrapRunRecord `json:"history,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=bs
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Destination",type=string,JSONPath=".status.source.destination"
// +kubebuilder:printcolumn:name="Targets",type=string,JSONPath=".status.targetsSummary"
// +kubebuilder:printcolumn:name="Complete",type=string,JSONPath=".status.conditions[?(@.type=='Complete')].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// Bootstrap restores a backup into existing PXC clusters in target order.
type Bootstrap struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec BootstrapSpec `json:"spec"`
	// +optional
	Status BootstrapStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// BootstrapList contains a list of Bootstrap.
type BootstrapList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Bootstrap `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Bootstrap{}, &BootstrapList{})
		return nil
	})
}
