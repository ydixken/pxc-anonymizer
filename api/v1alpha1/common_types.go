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
)

// ObjectStorageSpec configures access to one S3-compatible bucket.
// +kubebuilder:validation:XValidation:rule="has(self.endpointURL) || has(self.credentialsSecretRef)",message="endpointURL or a credentials Secret carrying S3_ENDPOINT_URL is required"
type ObjectStorageSpec struct {
	// Bucket names the bucket containing backups or pointer objects.
	// +required
	// +kubebuilder:validation:Pattern="^[a-z0-9.-]{3,63}$"
	Bucket string `json:"bucket"`
	// Prefix scopes object keys within the bucket.
	// +optional
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('/')",message="prefix must not start with /"
	Prefix string `json:"prefix,omitempty"`
	// EndpointURL overrides the endpoint from the credentials Secret.
	// +optional
	EndpointURL string `json:"endpointURL,omitempty"`
	// Region supplies the S3 signing region.
	// +optional
	// +kubebuilder:default="auto"
	Region string `json:"region,omitempty"`
	// CredentialsSecretRef refers to a Secret in the resource's namespace.
	// +optional
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`
	// Keys remaps the default credentials and endpoint keys.
	// +optional
	Keys *ObjectStorageSecretKeys `json:"keys,omitempty"`
	// PathStyle leaves addressing-style selection automatic when unset.
	// +optional
	PathStyle *bool `json:"pathStyle,omitempty"`
	// TLS customizes certificate verification for this endpoint.
	// +optional
	TLS *ObjectStorageTLS `json:"tls,omitempty"`
}

// ObjectStorageSecretKeys names the keys read from a credentials Secret.
type ObjectStorageSecretKeys struct {
	// +optional
	// +kubebuilder:default="AWS_ACCESS_KEY_ID"
	AccessKeyID string `json:"accessKeyID,omitempty"`
	// +optional
	// +kubebuilder:default="AWS_SECRET_ACCESS_KEY"
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	// +optional
	// +kubebuilder:default="S3_ENDPOINT_URL"
	EndpointURL string `json:"endpointURL,omitempty"`
	// +optional
	// +kubebuilder:default="S3_PUBLIC_ENDPOINT_URL"
	PublicEndpointURL string `json:"publicEndpointURL,omitempty"`
}

// ObjectStorageTLS allows a custom CA without disabling verification by default.
type ObjectStorageTLS struct {
	// +optional
	// +kubebuilder:default=false
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
	// +optional
	CASecretRef *corev1.SecretKeySelector `json:"caSecretRef,omitempty"`
}

// PointerTarget identifies the object to publish and its optional public URL.
type PointerTarget struct {
	// +required
	ObjectStorage ObjectStorageSpec `json:"objectStorage"`
	// +required
	// +kubebuilder:validation:Pattern="^[A-Za-z0-9._/-]{1,512}$"
	Key string `json:"key"`
	// PublicURL is informational and is embedded in the published pointer.
	// +optional
	PublicURL string `json:"publicURL,omitempty"`
}

// PointerSource selects exactly one transport for reading a pointer.
// +kubebuilder:validation:XValidation:rule="(has(self.http) ? 1 : 0) + (has(self.s3) ? 1 : 0) == 1",message="set exactly one of http or s3"
type PointerSource struct {
	// +optional
	HTTP *HTTPPointerSource `json:"http,omitempty"`
	// +optional
	S3 *S3PointerSource `json:"s3,omitempty"`
}

// HTTPPointerSource configures a bounded HTTP request for a pointer.
// +kubebuilder:validation:XValidation:rule="self.url.startsWith('https://') || (has(self.allowInsecure) && self.allowInsecure && self.url.startsWith('http://'))",message="url must use https, or http with allowInsecure"
type HTTPPointerSource struct {
	// +required
	URL string `json:"url"`
	// +optional
	// +kubebuilder:default=false
	AllowInsecure bool `json:"allowInsecure,omitempty"`
	// +optional
	CASecretRef *corev1.SecretKeySelector `json:"caSecretRef,omitempty"`
	// +optional
	// +kubebuilder:default="30s"
	Timeout *metav1.Duration `json:"timeout,omitempty"`
	// +optional
	// +kubebuilder:default=65536
	MaxBytes int64 `json:"maxBytes,omitempty"`
}

// S3PointerSource identifies a pointer object read with S3 credentials.
type S3PointerSource struct {
	// +required
	ObjectStorage ObjectStorageSpec `json:"objectStorage"`
	// +required
	// +kubebuilder:validation:Pattern="^[A-Za-z0-9._/-]{1,512}$"
	Key string `json:"key"`
}

// RestoreS3Credentials supplies the inline S3 fields needed by a PXC restore.
type RestoreS3Credentials struct {
	// CredentialsSecret names a Secret in the resource's namespace with AWS credential keys.
	// +required
	CredentialsSecret string `json:"credentialsSecret"`
	// EndpointURL falls back to the endpoint carried in the pointer when unset.
	// +optional
	EndpointURL string `json:"endpointURL,omitempty"`
	// +optional
	// +kubebuilder:default="auto"
	Region string `json:"region,omitempty"`
	// +optional
	VerifyTLS *bool `json:"verifyTLS,omitempty"`
}

// XtrabackupContainerOptions keeps backup and restore arguments independently configurable.
type XtrabackupContainerOptions struct {
	// +optional
	Xbcloud []string `json:"xbcloud,omitempty"`
	// +optional
	Xbstream []string `json:"xbstream,omitempty"`
	// +optional
	Xtrabackup []string `json:"xtrabackup,omitempty"`
}

// RunnerPodSpec provides the pod settings shared by anonymization runs.
type RunnerPodSpec struct {
	// +optional
	Image string `json:"image,omitempty"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
}

// ResolvedSource records the source selected from a literal, backup, or pointer.
type ResolvedSource struct {
	// +optional
	BackupName string `json:"backupName,omitempty"`
	// +optional
	Destination string `json:"destination,omitempty"`
	// +optional
	SourceCluster string `json:"sourceCluster,omitempty"`
	// +optional
	PointerPublishedAt *metav1.Time `json:"pointerPublishedAt,omitempty"`
	// PointerSchemaVersion distinguishes legacy pointers without a publication timestamp.
	// +optional
	PointerSchemaVersion int32 `json:"pointerSchemaVersion,omitempty"`
}
