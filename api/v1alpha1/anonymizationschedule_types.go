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

// ConcurrencyPolicy controls overlapping runs created by a schedule.
// +kubebuilder:validation:Enum=Forbid;Allow;Replace
type ConcurrencyPolicy string

const (
	ConcurrencyPolicyForbid  ConcurrencyPolicy = "Forbid"
	ConcurrencyPolicyAllow   ConcurrencyPolicy = "Allow"
	ConcurrencyPolicyReplace ConcurrencyPolicy = "Replace"
)

// AnonymizationScheduleSpec schedules runs from a reusable template.
type AnonymizationScheduleSpec struct {
	// Schedule is parsed as cron by the controller, which reports ScheduleValid.
	// +required
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`
	// TimeZone is an IANA zone; UTC avoids daylight-saving ambiguity by default.
	// +optional
	// +kubebuilder:default="UTC"
	TimeZone *string `json:"timeZone,omitempty"`
	// +optional
	// +kubebuilder:default=false
	Suspend bool `json:"suspend,omitempty"`
	// Forbid avoids provisioning overlapping temporary clusters by default.
	// +optional
	// +kubebuilder:default="Forbid"
	ConcurrencyPolicy ConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`
	// StartingDeadlineSeconds limits how late a missed run may start.
	// +optional
	// +kubebuilder:validation:Minimum=0
	StartingDeadlineSeconds *int64 `json:"startingDeadlineSeconds,omitempty"`
	// Zero retains no successful runs; keep zero explicit when marshaling.
	// +optional
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=0
	SuccessfulRunsHistoryLimit int32 `json:"successfulRunsHistoryLimit"`
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	FailedRunsHistoryLimit int32 `json:"failedRunsHistoryLimit"`
	// +required
	Template AnonymizationRunTemplate `json:"template"`
}

// AnonymizationRunTemplate supplies the metadata and spec of each scheduled run.
type AnonymizationRunTemplate struct {
	// +optional
	Metadata RunTemplateMetadata `json:"metadata,omitempty"`
	// +required
	Spec AnonymizationRunSpec `json:"spec"`
}

// RunTemplateMetadata limits template metadata to labels and annotations.
type RunTemplateMetadata struct {
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// AnonymizationScheduleStatus records scheduling decisions and active runs.
type AnonymizationScheduleStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions include ScheduleValid and Ready.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	Active []string `json:"active,omitempty"`
	// ActiveCount is maintained with Active by the controller for the integer printer column.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ActiveCount int32 `json:"activeCount,omitempty"`
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`
	// +optional
	LastSuccessfulTime *metav1.Time `json:"lastSuccessfulTime,omitempty"`
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`
	// +optional
	LastRunName string `json:"lastRunName,omitempty"`
	// +optional
	LastRunPhase RunPhase `json:"lastRunPhase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=asched
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="Suspend",type=boolean,JSONPath=".spec.suspend"
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=".status.activeCount"
// +kubebuilder:printcolumn:name="Last Run",type=string,JSONPath=".status.lastRunName"
// +kubebuilder:printcolumn:name="Next",type=date,JSONPath=".status.nextScheduleTime"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// AnonymizationSchedule creates anonymization runs according to a cron schedule.
type AnonymizationSchedule struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec AnonymizationScheduleSpec `json:"spec"`
	// +optional
	Status AnonymizationScheduleStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AnonymizationScheduleList contains a list of AnonymizationSchedule.
type AnonymizationScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AnonymizationSchedule `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AnonymizationSchedule{}, &AnonymizationScheduleList{})
		return nil
	})
}
