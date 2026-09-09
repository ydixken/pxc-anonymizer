// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

//nolint:goconst // Literal upstream field names keep the unstructured wire contract directly reviewable.
package pxc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kjson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/objectstore"
)

const annotationPrefix = "percona.com/"

// https://github.com/percona/percona-xtradb-cluster-operator/blob/v1.20.0/pkg/naming/naming.go#L9-L13
const (
	FinalizerDeleteSSL         = annotationPrefix + "delete-ssl"
	FinalizerDeleteProxysqlPvc = annotationPrefix + "delete-proxysql-pvc"
	FinalizerDeletePxcPvc      = annotationPrefix + "delete-pxc-pvc"
	FinalizerDeleteBackup      = annotationPrefix + "delete-backup"
)

// The default follows the pinned pxc-db 1.20.0 chart; overrides may choose another HAProxy image.
const defaultHAProxyImage = "percona/haproxy:2.8.18-1"

// RenderTempCluster confines the restore to owned storage with an explicit allocation ceiling.
func RenderTempCluster(run *api.AnonymizationRun, name string) (*unstructured.Unstructured, error) {
	if run == nil || run.UID == "" {
		return nil, errors.New("a persisted Run is required")
	}
	if err := validateRenderIdentity(run.Namespace, name, name); err != nil {
		return nil, err
	}
	template := run.Spec.TempCluster
	if template.SystemUsersSecretRef == nil || len(validation.IsDNS1123Subdomain(template.SystemUsersSecretRef.Name)) != 0 {
		return nil, errors.New("tempCluster.systemUsersSecretRef must name the source system-users Secret")
	}
	if template.CRVersion != "1.20.0" || !validRenderImage(template.Image) || !validRenderImage(template.BackupImage) {
		return nil, errors.New("temp cluster requires crVersion 1.20.0 and valid PXC and backup images")
	}
	if template.Storage.StorageClassName == "" || len(validation.IsDNS1123Subdomain(template.Storage.StorageClassName)) != 0 {
		return nil, errors.New("temp cluster storageClassName must be explicitly set")
	}
	size := template.Size
	if size == 0 {
		size = 1
	}
	if err := validateStorageBudget(int64(size), template.Storage.Size); err != nil {
		return nil, err
	}
	storage, err := renderOutputStorage(run.Spec.Output.ObjectStorage, run.Spec.Output.ContainerOptions)
	if err != nil {
		return nil, err
	}
	resources, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&template.Resources)
	if err != nil {
		return nil, fmt.Errorf("render PXC resources: %w", err)
	}
	backupTimeout := run.Spec.Timeouts.Backup.Duration
	if backupTimeout == 0 {
		backupTimeout = 3 * time.Hour
	}
	if backupTimeout < time.Second {
		return nil, errors.New("backup timeout must be at least one second")
	}
	haproxy := template.HAProxy == nil || *template.HAProxy
	secretName := name + "-secrets"
	spec := map[string]any{
		"crVersion": template.CRVersion, "secretsName": secretName,
		"tls":            map[string]any{"enabled": false},
		"unsafeFlags":    map[string]any{"pxcSize": true, "proxySize": true, "tls": true},
		"upgradeOptions": map[string]any{"apply": "disabled"},
		"pxc": map[string]any{
			"size": int64(size), "image": template.Image, "resources": resources,
			"configuration": template.Configuration,
			"affinity":      map[string]any{"antiAffinityTopologyKey": "none"},
			"volumeSpec": map[string]any{"persistentVolumeClaim": map[string]any{
				"accessModes": []any{"ReadWriteOnce"}, "storageClassName": template.Storage.StorageClassName,
				"resources": map[string]any{"requests": map[string]any{"storage": template.Storage.Size.String()}},
			}},
		},
		"haproxy": map[string]any{"enabled": haproxy, "size": int64(1), "image": defaultHAProxyImage,
			"affinity": map[string]any{"antiAffinityTopologyKey": "none"}},
		"proxysql":     map[string]any{"enabled": false},
		"logcollector": map[string]any{"enabled": false}, "pmm": map[string]any{"enabled": false},
		"backup": map[string]any{
			"image": template.BackupImage, "runningDeadlineSeconds": int64(backupTimeout / time.Second),
			"pitr": map[string]any{"enabled": false}, "schedule": []any{},
			"storages": map[string]any{outputStorageName: storage},
		},
	}
	if template.Overrides != nil {
		spec, err = applyClusterOverrides(spec, template.Overrides.Raw)
		if err != nil {
			return nil, err
		}
	}
	result := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	result.SetGroupVersionKind(ClusterGVK)
	result.SetName(name)
	result.SetNamespace(run.Namespace)
	result.SetLabels(maps.Clone(template.Labels))
	annotations := map[string]string{"argocd.argoproj.io/compare-options": "IgnoreExtraneous"}
	maps.Copy(annotations, template.Annotations)
	result.SetAnnotations(annotations)
	result.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(run, api.GroupVersion.WithKind("AnonymizationRun"))})
	result.SetFinalizers([]string{FinalizerDeletePxcPvc, FinalizerDeleteSSL, FinalizerDeleteProxysqlPvc})
	return result, nil
}

// RenderSystemUsersSecret preserves mysql.user credentials without copying source ownership or unrelated data.
func RenderSystemUsersSecret(run *api.AnonymizationRun, clusterName string, source *corev1.Secret) (*corev1.Secret, error) {
	if run == nil || run.UID == "" || source == nil {
		return nil, errors.New("a persisted Run and resolved source system-users Secret are required")
	}
	if err := validateRenderIdentity(run.Namespace, clusterName+"-secrets", clusterName); err != nil {
		return nil, err
	}
	ref := run.Spec.TempCluster.SystemUsersSecretRef
	if ref == nil || ref.Name == "" || source.Name != ref.Name || source.Namespace != run.Namespace {
		return nil, errors.New("source system-users Secret must match the explicit same-namespace reference")
	}
	data := map[string][]byte{}
	for _, key := range []string{"root", "xtrabackup", "monitor", "proxyadmin", "operator", "replication"} {
		if len(source.Data[key]) == 0 {
			return nil, fmt.Errorf("source system-users Secret is missing key %s", key)
		}
		data[key] = bytes.Clone(source.Data[key])
	}
	if value := source.Data["xtrabackup-aes256-psk"]; len(value) > 0 {
		data["xtrabackup-aes256-psk"] = bytes.Clone(value)
	}
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: clusterName + "-secrets", Namespace: run.Namespace,
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(run, api.GroupVersion.WithKind("AnonymizationRun"))},
	}, Type: corev1.SecretTypeOpaque, Data: data}, nil
}

func renderOutputStorage(spec api.ObjectStorageSpec, options *api.XtrabackupContainerOptions) (map[string]any, error) {
	if spec.Bucket == "" || strings.HasPrefix(spec.Prefix, "/") ||
		spec.CredentialsSecretRef == nil || len(validation.IsDNS1123Subdomain(spec.CredentialsSecretRef.Name)) != 0 {
		return nil, errors.New("output storage requires a bucket, relative prefix and same-namespace credentials Secret")
	}
	if spec.Keys != nil && ((spec.Keys.AccessKeyID != "" && spec.Keys.AccessKeyID != "AWS_ACCESS_KEY_ID") ||
		(spec.Keys.SecretAccessKey != "" && spec.Keys.SecretAccessKey != "AWS_SECRET_ACCESS_KEY")) {
		return nil, errors.New("backup credentials require standard AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY keys")
	}
	if spec.EndpointURL != "" {
		if _, err := objectstore.ParseEndpoint(spec.EndpointURL); err != nil {
			return nil, errors.New("output storage endpoint is invalid")
		}
	}
	region := spec.Region
	if region == "" {
		region = "auto"
	}
	s3 := map[string]any{"bucket": spec.Bucket, "prefix": spec.Prefix,
		"credentialsSecret": spec.CredentialsSecretRef.Name, "region": region}
	if spec.EndpointURL != "" {
		s3["endpointUrl"] = spec.EndpointURL
	}
	if spec.PathStyle != nil {
		s3["forcePathStyle"] = *spec.PathStyle
	}
	storage := map[string]any{"type": "s3", "s3": s3}
	if spec.TLS != nil {
		storage["verifyTLS"] = !spec.TLS.InsecureSkipVerify
		if spec.TLS.CASecretRef != nil {
			ca := spec.TLS.CASecretRef
			if len(validation.IsDNS1123Subdomain(ca.Name)) != 0 || ca.Key == "" {
				return nil, errors.New("output storage CA must reference a same-namespace Secret key")
			}
			s3["caBundle"] = map[string]any{"name": ca.Name, "key": ca.Key}
		}
	}
	if options != nil {
		storage["containerOptions"] = renderContainerOptions(options, false)
	}
	return storage, nil
}

func applyClusterOverrides(spec map[string]any, patch []byte) (map[string]any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(patch, &fields); err != nil || fields == nil {
		return nil, errors.New("temp cluster overrides must be a JSON object")
	}
	original, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode temp cluster spec: %w", err)
	}
	merged, err := jsonpatch.MergePatch(original, patch)
	if err != nil {
		return nil, errors.New("temp cluster overrides are not a valid JSON merge patch")
	}
	var result map[string]any
	if err := kjson.Unmarshal(merged, &result); err != nil {
		return nil, fmt.Errorf("decode temp cluster overrides: %w", err)
	}
	protected := [][]string{
		{"crVersion"}, {"secretsName"}, {"tls"}, {"unsafeFlags"}, {"upgradeOptions", "apply"},
		{"pxc", "image"}, {"pxc", "volumeSpec", "persistentVolumeClaim", "storageClassName"},
		{"haproxy", "enabled"}, {"proxysql", "enabled"}, {"logcollector", "enabled"}, {"pmm", "enabled"},
		{"backup", "image"}, {"backup", "runningDeadlineSeconds"}, {"backup", "pitr"}, {"backup", "schedule"},
		{"backup", "storages", outputStorageName},
	}
	for _, path := range protected {
		before, _, _ := unstructured.NestedFieldNoCopy(spec, path...)
		after, found, fieldErr := unstructured.NestedFieldNoCopy(result, path...)
		if fieldErr != nil || !found || !reflect.DeepEqual(before, after) {
			return nil, fmt.Errorf("temp cluster overrides cannot change %s", strings.Join(path, "."))
		}
	}
	for _, field := range []string{"emptyDir", "hostPath"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(result, "pxc", "volumeSpec", field); found {
			return nil, errors.New("temp cluster overrides must retain PVC-only data storage")
		}
	}
	size, _, err := unstructured.NestedInt64(result, "pxc", "size")
	if err != nil {
		return nil, errors.New("temp cluster pxc.size must be an integer")
	}
	quantityText, _, err := unstructured.NestedString(result, "pxc", "volumeSpec", "persistentVolumeClaim", "resources", "requests", "storage")
	if err != nil {
		return nil, errors.New("temp cluster PVC storage request must be a quantity")
	}
	quantity, err := resource.ParseQuantity(quantityText)
	if err != nil {
		return nil, errors.New("temp cluster PVC storage request must be a quantity")
	}
	if err := validateStorageBudget(size, quantity); err != nil {
		return nil, err
	}
	image, _, err := unstructured.NestedString(result, "haproxy", "image")
	if err != nil || !validRenderImage(image) {
		return nil, errors.New("temp cluster HAProxy image must be valid and nonempty")
	}
	return result, nil
}

func validateStorageBudget(size int64, quantity resource.Quantity) error {
	limit := resource.MustParse("20Gi")
	if size < 1 || quantity.Sign() <= 0 || quantity.Cmp(limit) > 0 || quantity.Value() > limit.Value()/size {
		return errors.New("temporary PXC data storage must be positive and total at most 20Gi")
	}
	return nil
}

func validRenderImage(image string) bool {
	return image != "" && !strings.ContainsAny(image, " \t\r\n")
}
