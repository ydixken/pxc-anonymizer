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
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	admissionExample                         = "example"
	admissionOutputBucket                    = "example-output"
	admissionAPIVersionField                 = "apiVersion"
	admissionKindField                       = "kind"
	admissionMetadataField                   = "metadata"
	admissionPointerURL                      = "https://example.com/latest.json"
	admissionPointerField                    = "pointer"
	admissionStrategyField                   = "strategy"
	admissionUrlField                        = "url"
	admissionTargetField                     = "target"
	admissionCrossplaneField                 = "crossplane"
	admissionRunnerField                     = "runner"
	admissionMaxAgeField                     = "maxAge"
	admissionOutputField                     = "output"
	admissionTempClusterField                = "tempCluster"
	admissionDatabasesField                  = "databases"
	admissionDeterminismField                = "determinism"
	admissionParamsField                     = "params"
	admissionWorkersField                    = "workers"
	admissionCleanupField                    = "cleanup"
	admissionKeysField                       = "keys"
	admissionTlsField                        = "tls"
	admissionTargetsField                    = "targets"
	admissionRecreateField                   = "recreate"
	admissionColumnsField                    = "columns"
	admissionModeField                       = "mode"
	admissionActionField                     = "action"
	admissionValueField                      = "value"
	admissionScheduleField                   = "schedule"
	admissionTemplateField                   = "template"
	admissionSuccessfulRunsHistoryLimitField = "successfulRunsHistoryLimit"
	admissionFailedRunsHistoryLimitField     = "failedRunsHistoryLimit"
	admissionNameField                       = "name"
	admissionKeyField                        = "key"
	admissionSpecField                       = "spec"
	admissionSourceField                     = "source"
	admissionStorageField                    = "objectStorage"
	admissionEndpointField                   = "endpointURL"
	admissionClusterField                    = "pxcCluster"
	admissionCredentialsField                = "credentialsSecret"
	admissionDestinationField                = "destination"
	admissionRestoreField                    = "restore"
	admissionHTTPField                       = "http"
	admissionConsistentField                 = "consistent"
	admissionUsers                           = "users"
	admissionEmail                           = "email"
	admissionConstant                        = "constant"
	admissionSQLKey                          = "prepare.sql"
	admissionPolicyName                      = "example-policy"
	admissionPXCVersion                      = "1.20.0"
	admissionStorageClass                    = "example-storage"
	admissionPXCImage                        = "example.com/pxc:8.4"
	admissionBackupImage                     = "example.com/xtrabackup:8.4"
	admissionPointerKey                      = "latest.json"
	admissionDestination                     = "s3://example-backups/source-full"
	admissionEndpoint                        = "https://s3.example.com"
	admissionCredentials                     = "example-credentials"
	admissionSourceImmutable                 = "source is immutable"
)

// Unstructured fixtures preserve omission so these tests exercise API-server defaulting.
func admissionResource(kind string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		admissionAPIVersionField: "pxc-anonymizer.io/v1alpha1",
		admissionKindField:       kind,
		admissionMetadataField:   map[string]any{"generateName": "admission-", "namespace": "default"},
		admissionSpecField:       spec,
	}}
}

func createAdmissionResource(kind string, spec map[string]any) *unstructured.Unstructured {
	GinkgoHelper()
	obj := admissionResource(kind, spec)
	Expect(k8sClient.Create(ctx, obj)).To(Succeed())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed())
	})
	return obj
}

func rejectAdmissionResource(kind string, spec map[string]any, message string) {
	GinkgoHelper()
	err := k8sClient.Create(ctx, admissionResource(kind, spec))
	Expect(err).To(HaveOccurred())
	Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected Kubernetes Invalid admission response: %v", err)
	Expect(err.Error()).To(ContainSubstring(message))
}

func admissionField(obj *unstructured.Unstructured, path ...string) any {
	GinkgoHelper()
	value, found, err := unstructured.NestedFieldNoCopy(obj.Object, path...)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue(), "missing admitted field %v", path)
	return value
}

func setAdmissionField(obj *unstructured.Unstructured, value any, path ...string) {
	GinkgoHelper()
	Expect(unstructured.SetNestedField(obj.Object, value, path...)).To(Succeed())
}

func admissionStorage() map[string]any {
	return map[string]any{"bucket": "example-backups", admissionEndpointField: admissionEndpoint}
}

func admissionRunSpec() map[string]any {
	return map[string]any{
		admissionSourceField: map[string]any{
			admissionDestinationField: admissionDestination,
			admissionRestoreField:     map[string]any{admissionCredentialsField: admissionCredentials},
		},
		"policyRef": map[string]any{admissionNameField: admissionPolicyName},
		admissionTempClusterField: map[string]any{
			"crVersion": admissionPXCVersion, "image": admissionPXCImage, "backupImage": admissionBackupImage,
			"storage": map[string]any{"storageClassName": admissionStorageClass, "size": "10Gi"},
		},
		admissionOutputField: map[string]any{admissionStorageField: admissionStorage()},
	}
}

var _ = Describe("Documented API examples", Label("admission"), func() {
	It("admits the five configuration guide manifests", func() {
		for _, guide := range []string{"backuppointer", "policy", "run", admissionScheduleField, "bootstrap"} {
			By("admitting the " + guide + " guide example")
			contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration", guide+".md"))
			Expect(err).NotTo(HaveOccurred())
			_, fenced, found := strings.Cut(string(contents), "```yaml\n")
			Expect(found).To(BeTrue(), "missing YAML example in %s", guide)
			manifest, _, found := strings.Cut(fenced, "\n```")
			Expect(found).To(BeTrue(), "unterminated YAML example in %s", guide)
			encoded, err := yaml.YAMLToJSON([]byte(manifest))
			Expect(err).NotTo(HaveOccurred())
			obj := &unstructured.Unstructured{}
			Expect(obj.UnmarshalJSON(encoded)).To(Succeed())
			createAdmissionResource(obj.GetKind(), admissionField(obj, admissionSpecField).(map[string]any))
		}
	})
})
