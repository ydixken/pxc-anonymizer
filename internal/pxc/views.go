package pxc

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const GroupName = "pxc.percona.com"

var (
	BackupGVK  = schema.GroupVersionKind{Group: GroupName, Version: "v1", Kind: "PerconaXtraDBClusterBackup"}
	ClusterGVK = schema.GroupVersionKind{Group: GroupName, Version: "v1", Kind: "PerconaXtraDBCluster"}
	RestoreGVK = schema.GroupVersionKind{Group: GroupName, Version: "v1", Kind: "PerconaXtraDBClusterRestore"}
)

type BackupView struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BackupSpec   `json:"spec,omitempty"`
	Status            BackupStatus `json:"status,omitempty"`
}

type BackupSpec struct {
	PXCCluster  string `json:"pxcCluster"`
	StorageName string `json:"storageName,omitempty"`
}

type BackupStatus struct {
	State       BackupState  `json:"state,omitempty"`
	Destination string       `json:"destination,omitempty"`
	CompletedAt *metav1.Time `json:"completed,omitempty"`
	Error       string       `json:"error,omitempty"`
	StorageName string       `json:"storageName,omitempty"`
	S3          *BackupS3    `json:"s3,omitempty"`
}

// Pointer source metadata needs the backup endpoint, without its credential reference.
type BackupS3 struct {
	Bucket      string `json:"bucket,omitempty"`
	EndpointURL string `json:"endpointUrl,omitempty"`
	Region      string `json:"region,omitempty"`
}

type ClusterView struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterSpec   `json:"spec,omitempty"`
	Status            ClusterStatus `json:"status,omitempty"`
}

type ClusterSpec struct {
	CRVersion string         `json:"crVersion,omitempty"`
	Pause     bool           `json:"pause,omitempty"`
	PXC       ClusterPXCSpec `json:"pxc,omitempty"`
}

type ClusterPXCSpec struct {
	Size int32 `json:"size,omitempty"`
}

type ClusterStatus struct {
	State ClusterState     `json:"state,omitempty"`
	PXC   ClusterPXCStatus `json:"pxc,omitempty"`
}

type ClusterPXCStatus struct {
	State ClusterState `json:"status,omitempty"`
	Size  int32        `json:"size,omitempty"`
	Ready int32        `json:"ready,omitempty"`
}

type RestoreView struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RestoreSpec   `json:"spec,omitempty"`
	Status            RestoreStatus `json:"status,omitempty"`
}

type RestoreSpec struct {
	PXCCluster   string        `json:"pxcCluster"`
	BackupName   string        `json:"backupName,omitempty"`
	BackupSource *BackupStatus `json:"backupSource,omitempty"`
}

type RestoreStatus struct {
	State       RestoreState `json:"state,omitempty"`
	Comments    string       `json:"comments,omitempty"`
	CompletedAt *metav1.Time `json:"completed,omitempty"`
}

func DecodeBackup(object *unstructured.Unstructured) (BackupView, error) {
	var view BackupView
	err := decodeView(object, BackupGVK, &view)
	return view, err
}

func DecodeCluster(object *unstructured.Unstructured) (ClusterView, error) {
	var view ClusterView
	err := decodeView(object, ClusterGVK, &view)
	return view, err
}

func DecodeRestore(object *unstructured.Unstructured) (RestoreView, error) {
	var view RestoreView
	err := decodeView(object, RestoreGVK, &view)
	return view, err
}

func decodeView(object *unstructured.Unstructured, expected schema.GroupVersionKind, into any) error {
	if object == nil {
		return fmt.Errorf("decode %s: object is nil", expected.Kind)
	}
	if object.GroupVersionKind() != expected {
		return fmt.Errorf("decode %s: unexpected group/version/kind", expected.Kind)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, into); err != nil {
		return fmt.Errorf("decode %s: %w", expected.Kind, err)
	}
	return nil
}
