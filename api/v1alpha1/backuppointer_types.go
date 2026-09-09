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
)

// BackupPointerSpec selects backups to publish and controls pointer freshness.
type BackupPointerSpec struct {
	// Source cannot change after creation because it defines the backup lineage.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="source is immutable"
	Source BackupPointerSource `json:"source"`
	// Target identifies where the selected backup's pointer is published.
	// +required
	Target PointerTarget `json:"target"`
	// StaleAfter measures freshness from the selected backup's completion time.
	// +optional
	// +kubebuilder:default="36h"
	StaleAfter *metav1.Duration `json:"staleAfter,omitempty"`
	// VerifyInterval controls checks for a missing or changed pointer object.
	// +optional
	// +kubebuilder:default="1h"
	VerifyInterval *metav1.Duration `json:"verifyInterval,omitempty"`
	// Suspend pauses pointer reconciliation.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// BackupPointerSource limits selection to backups from one cluster in this namespace.
type BackupPointerSource struct {
	// +required
	PXCCluster string `json:"pxcCluster"`
	// StorageNames permits any storage when the set is empty.
	// +optional
	// +listType=set
	StorageNames []string `json:"storageNames,omitempty"`
	// Selector further restricts backups by their labels.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// BackupPointerPhase summarizes selection, publication, and freshness.
// +kubebuilder:validation:Enum=Pending;Published;NoCandidate;Stale;Dangling;Error;Suspended
type BackupPointerPhase string

const (
	BackupPointerPhasePending     BackupPointerPhase = "Pending"
	BackupPointerPhasePublished   BackupPointerPhase = "Published"
	BackupPointerPhaseNoCandidate BackupPointerPhase = "NoCandidate"
	BackupPointerPhaseStale       BackupPointerPhase = "Stale"
	BackupPointerPhaseDangling    BackupPointerPhase = "Dangling"
	BackupPointerPhaseError       BackupPointerPhase = "Error"
	BackupPointerPhaseSuspended   BackupPointerPhase = "Suspended"
)

// PublishedBackup records the backup represented by a published pointer object.
type PublishedBackup struct {
	// +optional
	BackupName string `json:"backupName,omitempty"`
	// +optional
	Destination string `json:"destination,omitempty"`
	// +optional
	StorageName string `json:"storageName,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// +optional
	PublishedAt *metav1.Time `json:"publishedAt,omitempty"`
	// +optional
	ETag string `json:"etag,omitempty"`
	// +optional
	SchemaVersion int32 `json:"schemaVersion,omitempty"`
}

// BackupCounts exposes unsuccessful and unfinished backups alongside successful ones.
type BackupCounts struct {
	// +optional
	Succeeded int32 `json:"succeeded,omitempty"`
	// +optional
	Failed int32 `json:"failed,omitempty"`
	// +optional
	Running int32 `json:"running,omitempty"`
	// +optional
	Starting int32 `json:"starting,omitempty"`
}

// BackupPointerStatus records publication history and the current health conditions.
type BackupPointerStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Phase BackupPointerPhase `json:"phase,omitempty"`
	// Conditions use BackupSelected, Published, Fresh, and Ready with positive polarity.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	Current *PublishedBackup `json:"current,omitempty"`
	// +optional
	Previous *PublishedBackup `json:"previous,omitempty"`
	// +optional
	Backups BackupCounts `json:"backups,omitempty"`
	// +optional
	NextVerifyTime *metav1.Time `json:"nextVerifyTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=bp
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.source.pxcCluster"
// +kubebuilder:printcolumn:name="Backup",type=string,JSONPath=".status.current.backupName"
// +kubebuilder:printcolumn:name="Published",type=date,JSONPath=".status.current.publishedAt"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Fresh",type=string,JSONPath=".status.conditions[?(@.type=='Fresh')].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// BackupPointer publishes the latest successful backup as a reusable pointer.
type BackupPointer struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec BackupPointerSpec `json:"spec"`
	// +optional
	Status BackupPointerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// BackupPointerList contains a list of BackupPointer resources.
type BackupPointerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []BackupPointer `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &BackupPointer{}, &BackupPointerList{})
		return nil
	})
}
