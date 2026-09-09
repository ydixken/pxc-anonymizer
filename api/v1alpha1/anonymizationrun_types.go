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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// RunSource selects one backup source and supplies its restore credentials.
// +kubebuilder:validation:XValidation:rule="(has(self.backupPointerRef)?1:0)+(has(self.pointer)?1:0)+(has(self.backupRef)?1:0)+(has(self.destination)?1:0) == 1",message="set exactly one source form"
type RunSource struct {
	// BackupPointerRef selects the current backup from a ready BackupPointer.
	// +optional
	BackupPointerRef *corev1.LocalObjectReference `json:"backupPointerRef,omitempty"`
	// Pointer reads a pointer document over HTTP or S3.
	// +optional
	Pointer *PointerSource `json:"pointer,omitempty"`
	// BackupRef selects a successful Percona backup in this namespace.
	// +optional
	BackupRef *corev1.LocalObjectReference `json:"backupRef,omitempty"`
	// Destination directly identifies the backup to restore.
	// +optional
	// +kubebuilder:validation:Pattern="^s3://"
	Destination string `json:"destination,omitempty"`
	// +required
	Restore RestoreS3Credentials `json:"restore"`
}

// TempClusterStorage prevents ambiguous default storage-class selection.
// +kubebuilder:validation:XValidation:rule="size(self.storageClassName) > 0",message="storageClassName is required"
type TempClusterStorage struct {
	// +required
	StorageClassName string `json:"storageClassName"`
	// +required
	Size resource.Quantity `json:"size"`
}

// TempClusterSpec configures the isolated PXC cluster used for one run.
type TempClusterSpec struct {
	// NamePrefix is combined with a stable suffix from the Run UID.
	// +optional
	// +kubebuilder:default="anon"
	// +kubebuilder:validation:MaxLength=20
	NamePrefix string `json:"namePrefix,omitempty"`
	// CRVersion must match the installed Percona API contract.
	// +required
	CRVersion string `json:"crVersion"`
	// Image must match the source database's major and minor version for restore compatibility.
	// +required
	Image string `json:"image"`
	// +required
	BackupImage string `json:"backupImage"`
	// +optional
	// +kubebuilder:default=1
	Size int32 `json:"size,omitempty"`
	// +required
	Storage TempClusterStorage `json:"storage"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// SystemUsersSecretRef preserves the source credentials and optional backup encryption key.
	// +optional
	SystemUsersSecretRef *corev1.LocalObjectReference `json:"systemUsersSecretRef,omitempty"`
	// +optional
	// +kubebuilder:default=true
	HAProxy *bool `json:"haproxy,omitempty"`
	// Configuration is a my.cnf fragment for the temporary cluster.
	// +optional
	Configuration string `json:"configuration,omitempty"`
	// Overrides is a JSON merge patch applied to the rendered PXC spec.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Overrides *apiextensionsv1.JSON `json:"overrides,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// OutputRetention bounds the successful and failed backups retained per output group.
type OutputRetention struct {
	// +optional
	// +kubebuilder:default=2
	KeepSucceeded int32 `json:"keepSucceeded,omitempty"`
	// +optional
	// +kubebuilder:default="24h"
	DeleteFailedAfter metav1.Duration `json:"deleteFailedAfter,omitzero"`
}

// OutputSpec configures the anonymized backup and optional pointer publication.
type OutputSpec struct {
	// +required
	ObjectStorage ObjectStorageSpec `json:"objectStorage"`
	// BackupNamePrefix defaults at reconciliation to the temporary cluster name plus "-anonymized".
	// +optional
	BackupNamePrefix string `json:"backupNamePrefix,omitempty"`
	// +optional
	ContainerOptions *XtrabackupContainerOptions `json:"containerOptions,omitempty"`
	// Pointer disables publication when omitted.
	// +optional
	Pointer *PointerTarget `json:"pointer,omitempty"`
	// +optional
	// +kubebuilder:default={}
	Retention OutputRetention `json:"retention,omitempty"`
}

// RunnerSpec combines pod placement with anonymization execution settings.
type RunnerSpec struct {
	RunnerPodSpec `json:",inline"`
	// +optional
	// +kubebuilder:default=4
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=32
	Workers int32 `json:"workers,omitempty"`
	// +optional
	// +kubebuilder:default=5000
	PageSize *int32 `json:"pageSize,omitempty"`
	// +optional
	// +kubebuilder:default=true
	DisableBinlog *bool `json:"disableBinlog,omitempty"`
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
}

// RunTimeouts bounds each stage independently.
type RunTimeouts struct {
	// +optional
	// +kubebuilder:default="30m"
	ClusterReady metav1.Duration `json:"clusterReady,omitzero"`
	// +optional
	// +kubebuilder:default="3h"
	Restore metav1.Duration `json:"restore,omitzero"`
	// +optional
	// +kubebuilder:default="6h"
	Anonymize metav1.Duration `json:"anonymize,omitzero"`
	// +optional
	// +kubebuilder:default="3h"
	Backup metav1.Duration `json:"backup,omitzero"`
	// +optional
	// +kubebuilder:default="10m"
	Publish metav1.Duration `json:"publish,omitzero"`
	// +optional
	// +kubebuilder:default="30m"
	Cleanup metav1.Duration `json:"cleanup,omitzero"`
}

// CleanupSpec controls retention of the temporary cluster after a run.
type CleanupSpec struct {
	// OnFailure permits manual inspection until the Run is deleted when set to Retain.
	// +optional
	// +kubebuilder:default="Delete"
	// +kubebuilder:validation:Enum=Delete;Retain
	OnFailure string `json:"onFailure,omitempty"`
	// HoldTempClusterFor delays cleanup after success.
	// +optional
	// +kubebuilder:default="0s"
	HoldTempClusterFor metav1.Duration `json:"holdTempClusterFor,omitzero"`
}

// AnonymizationRunSpec describes one immutable source, policy, and temporary cluster.
type AnonymizationRunSpec struct {
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="source is immutable"
	Source RunSource `json:"source"`
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="policyRef is immutable"
	PolicyRef corev1.LocalObjectReference `json:"policyRef"`
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="tempCluster is immutable"
	TempCluster TempClusterSpec `json:"tempCluster"`
	// +required
	Output OutputSpec `json:"output"`
	// +optional
	// +kubebuilder:default={}
	Runner RunnerSpec `json:"runner,omitempty"`
	// +optional
	// +kubebuilder:default={}
	Timeouts RunTimeouts `json:"timeouts,omitzero"`
	// +optional
	// +kubebuilder:default={}
	Cleanup CleanupSpec `json:"cleanup,omitzero"`
	// BackoffLimit applies only to runner attempts; restore and backup failures are not retried.
	// +optional
	// +kubebuilder:default=2
	BackoffLimit int32 `json:"backoffLimit,omitempty"`
	// TTLSecondsAfterFinished applies only to owned Jobs.
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// RunPhase follows the backup anonymization pipeline through cleanup.
// +kubebuilder:validation:Enum=Pending;ResolvingSource;Provisioning;Restoring;Anonymizing;BackingUp;Publishing;Pruning;CleaningUp;Completed;Failed
type RunPhase string

const (
	RunPhasePending         RunPhase = "Pending"
	RunPhaseResolvingSource RunPhase = "ResolvingSource"
	RunPhaseProvisioning    RunPhase = "Provisioning"
	RunPhaseRestoring       RunPhase = "Restoring"
	RunPhaseAnonymizing     RunPhase = "Anonymizing"
	RunPhaseBackingUp       RunPhase = "BackingUp"
	RunPhasePublishing      RunPhase = "Publishing"
	RunPhasePruning         RunPhase = "Pruning"
	RunPhaseCleaningUp      RunPhase = "CleaningUp"
	RunPhaseCompleted       RunPhase = "Completed"
	RunPhaseFailed          RunPhase = "Failed"
)

// TempClusterStatus records the lifetime of resources allocated for a run.
type TempClusterStatus struct {
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	SecretName string `json:"secretName,omitempty"`
	// +optional
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`
	// +optional
	ReadyAt *metav1.Time `json:"readyAt,omitempty"`
	// +optional
	DeletedAt *metav1.Time `json:"deletedAt,omitempty"`
}

// AnonymizeProgress is a summary; durable per-table checkpoints remain in the database.
type AnonymizeProgress struct {
	// +optional
	TablesTotal int32 `json:"tablesTotal,omitempty"`
	// +optional
	TablesDone int32 `json:"tablesDone,omitempty"`
	// +optional
	RowsTotal int64 `json:"rowsTotal,omitempty"`
	// +optional
	RowsDone int64 `json:"rowsDone,omitempty"`
	// +optional
	CurrentTable string `json:"currentTable,omitempty"`
	// +optional
	StepsDone int32 `json:"stepsDone,omitempty"`
	// +optional
	StepsTotal int32 `json:"stepsTotal,omitempty"`
}

// AnonymizeResult records the runner's final classification and message.
type AnonymizeResult struct {
	// +optional
	Class string `json:"class,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// AnonymizeStatus records the current attempt and its reported progress.
type AnonymizeStatus struct {
	// +optional
	JobName string `json:"jobName,omitempty"`
	// +optional
	Attempts int32 `json:"attempts,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// +optional
	Progress *AnonymizeProgress `json:"progress,omitempty"`
	// +optional
	LastResult *AnonymizeResult `json:"lastResult,omitempty"`
}

// AnonymizationRunStatus records source resolution, pipeline progress, and cleanup.
type AnonymizationRunStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Phase RunPhase `json:"phase,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	Source *ResolvedSource `json:"source,omitempty"`
	// +optional
	PolicyHash string `json:"policyHash,omitempty"`
	// +optional
	TempCluster *TempClusterStatus `json:"tempCluster,omitempty"`
	// +optional
	RestoreName string `json:"restoreName,omitempty"`
	// +optional
	Anonymize *AnonymizeStatus `json:"anonymize,omitempty"`
	// +optional
	Output *PublishedBackup `json:"output,omitempty"`
	// +optional
	PrunedBackups []string `json:"prunedBackups,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=arun
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=".status.source.backupName"
// +kubebuilder:printcolumn:name="Temp",type=string,JSONPath=".status.tempCluster.name"
// +kubebuilder:printcolumn:name="Output",type=string,JSONPath=".status.output.backupName"
// +kubebuilder:printcolumn:name="Complete",type=string,JSONPath=".status.conditions[?(@.type=='Complete')].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// AnonymizationRun executes one backup anonymization pipeline.
type AnonymizationRun struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec AnonymizationRunSpec `json:"spec"`
	// +optional
	Status AnonymizationRunStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AnonymizationRunList contains a list of AnonymizationRun resources.
type AnonymizationRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AnonymizationRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AnonymizationRun{}, &AnonymizationRunList{})
		return nil
	})
}
