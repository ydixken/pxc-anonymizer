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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AnonymizationRun API admission", Label("admission"), func() {
	It("defaults execution, cleanup and retention without enabling pointer publication", func() {
		obj := createAdmissionResource("AnonymizationRun", admissionRunSpec())
		Expect(admissionField(obj, admissionSpecField, admissionRunnerField, admissionWorkersField)).To(Equal(int64(4)))
		Expect(admissionField(obj, admissionSpecField, admissionRunnerField, "pageSize")).To(Equal(int64(5000)))
		Expect(admissionField(obj, admissionSpecField, admissionRunnerField, "disableBinlog")).To(BeTrue())
		Expect(admissionField(obj, admissionSpecField, admissionCleanupField, "onFailure")).To(Equal("Delete"))
		Expect(admissionField(obj, admissionSpecField, admissionCleanupField, "holdTempClusterFor")).To(Equal("0s"))
		Expect(admissionField(obj, admissionSpecField, "timeouts")).To(Equal(map[string]any{
			"clusterReady": "30m", admissionRestoreField: "3h", "anonymize": "6h", "backup": "3h", "publish": "10m", admissionCleanupField: "30m",
		}))
		Expect(admissionField(obj, admissionSpecField, admissionOutputField, "retention", "keepSucceeded")).To(Equal(int64(2)))
		Expect(admissionField(obj, admissionSpecField, admissionOutputField, "retention", "deleteFailedAfter")).To(Equal("24h"))
		Expect(admissionField(obj, admissionSpecField, admissionOutputField)).NotTo(HaveKey(admissionPointerField))
		Expect(admissionField(obj, admissionSpecField, admissionTempClusterField, "haproxy")).To(BeTrue())
	})

	DescribeTable("accepts each source form independently", func(source map[string]any) {
		spec := admissionRunSpec()
		source[admissionRestoreField] = map[string]any{admissionCredentialsField: admissionCredentials}
		spec[admissionSourceField] = source
		createAdmissionResource("AnonymizationRun", spec)
	},
		Entry("BackupPointer reference", map[string]any{"backupPointerRef": map[string]any{admissionNameField: "example-pointer"}}),
		Entry("PXC backup reference", map[string]any{"backupRef": map[string]any{admissionNameField: "example-backup"}}),
		Entry("HTTPS pointer", map[string]any{admissionPointerField: map[string]any{admissionHTTPField: map[string]any{admissionUrlField: admissionPointerURL}}}),
		Entry("S3 pointer", map[string]any{admissionPointerField: map[string]any{"s3": map[string]any{admissionStorageField: admissionStorage(), admissionKeyField: admissionPointerKey}}}),
	)

	DescribeTable("rejects ambiguous or unsafe sources", func(source map[string]any, message string) {
		spec := admissionRunSpec()
		source[admissionRestoreField] = map[string]any{admissionCredentialsField: admissionCredentials}
		spec[admissionSourceField] = source
		rejectAdmissionResource("AnonymizationRun", spec, message)
	},
		Entry("no source", map[string]any{}, "set exactly one source form"),
		Entry("two sources", map[string]any{admissionDestinationField: admissionDestination, "backupRef": map[string]any{admissionNameField: "example-backup"}}, "set exactly one source form"),
		Entry("non-S3 destination", map[string]any{admissionDestinationField: "https://example.com/backup"}, "spec.source.destination"),
		Entry("empty pointer", map[string]any{admissionPointerField: map[string]any{}}, "set exactly one of http or s3"),
		Entry("two pointer transports", map[string]any{admissionPointerField: map[string]any{
			admissionHTTPField: map[string]any{admissionUrlField: admissionPointerURL},
			"s3":               map[string]any{admissionStorageField: admissionStorage(), admissionKeyField: admissionPointerKey},
		}}, "set exactly one of http or s3"),
		Entry("unapproved HTTP", map[string]any{admissionPointerField: map[string]any{admissionHTTPField: map[string]any{admissionUrlField: "http://example.com/latest.json"}}}, "url must use https, or http with allowInsecure"),
	)

	It("accepts explicit insecure HTTP and applies bounded fetch defaults", func() {
		spec := admissionRunSpec()
		source := spec[admissionSourceField].(map[string]any)
		delete(source, admissionDestinationField)
		source[admissionPointerField] = map[string]any{admissionHTTPField: map[string]any{admissionUrlField: "http://example.com/latest.json", "allowInsecure": true}}
		obj := createAdmissionResource("AnonymizationRun", spec)
		Expect(admissionField(obj, admissionSpecField, admissionSourceField, admissionPointerField, admissionHTTPField, "timeout")).To(Equal("30s"))
		Expect(admissionField(obj, admissionSpecField, admissionSourceField, admissionPointerField, admissionHTTPField, "maxBytes")).To(Equal(int64(65536)))
	})

	DescribeTable("rejects changes to immutable execution inputs", func(field, child, value, message string) {
		obj := createAdmissionResource("AnonymizationRun", admissionRunSpec())
		setAdmissionField(obj, value, admissionSpecField, field, child)
		Expect(k8sClient.Update(ctx, obj)).To(MatchError(ContainSubstring(message)))
	},
		Entry(admissionSourceField, admissionSourceField, admissionDestinationField, "s3://example-backups/other-full", admissionSourceImmutable),
		Entry("policy", "policyRef", admissionNameField, "other-policy", "policyRef is immutable"),
		Entry("temporary cluster", admissionTempClusterField, "image", "example.com/pxc:other", "tempCluster is immutable"),
	)

	It("requires an explicit storage class and bounds workers", func() {
		spec := admissionRunSpec()
		spec[admissionTempClusterField].(map[string]any)["storage"].(map[string]any)["storageClassName"] = ""
		rejectAdmissionResource("AnonymizationRun", spec, "storageClassName is required")
		spec = admissionRunSpec()
		spec[admissionRunnerField] = map[string]any{admissionWorkersField: int64(33)}
		rejectAdmissionResource("AnonymizationRun", spec, "spec.runner.workers")
	})
})
