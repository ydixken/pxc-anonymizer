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

func admissionBackupPointerSpec() map[string]any {
	return map[string]any{
		admissionSourceField: map[string]any{admissionClusterField: "example-source"},
		admissionTargetField: map[string]any{admissionStorageField: admissionStorage(), admissionKeyField: admissionPointerKey},
	}
}

var _ = Describe("BackupPointer API admission", Label("admission"), func() {
	It("defaults freshness and storage while leaving optional helpers absent", func() {
		obj := createAdmissionResource("BackupPointer", admissionBackupPointerSpec())
		Expect(admissionField(obj, admissionSpecField, "staleAfter")).To(Equal("36h"))
		Expect(admissionField(obj, admissionSpecField, "verifyInterval")).To(Equal("1h"))
		storage := admissionField(obj, admissionSpecField, admissionTargetField, admissionStorageField).(map[string]any)
		Expect(storage).To(HaveKeyWithValue("region", "auto"))
		Expect(storage).NotTo(HaveKey(admissionKeysField))
		Expect(storage).NotTo(HaveKey(admissionTlsField))
		Expect(storage).NotTo(HaveKey("pathStyle"))
	})

	It("defaults explicitly enabled Secret key and TLS helpers", func() {
		spec := admissionBackupPointerSpec()
		storage := spec[admissionTargetField].(map[string]any)[admissionStorageField].(map[string]any)
		delete(storage, admissionEndpointField)
		storage["credentialsSecretRef"] = map[string]any{admissionNameField: admissionCredentials}
		storage[admissionKeysField] = map[string]any{}
		storage[admissionTlsField] = map[string]any{}
		obj := createAdmissionResource("BackupPointer", spec)
		Expect(admissionField(obj, admissionSpecField, admissionTargetField, admissionStorageField, admissionKeysField)).To(Equal(map[string]any{
			"accessKeyID": runAWSAccessKey, "secretAccessKey": runAWSSecretKey,
			admissionEndpointField: "S3_ENDPOINT_URL", "publicEndpointURL": "S3_PUBLIC_ENDPOINT_URL",
		}))
		Expect(admissionField(obj, admissionSpecField, admissionTargetField, admissionStorageField, admissionTlsField, "insecureSkipVerify")).To(BeFalse())
	})

	DescribeTable("rejects malformed publication configuration", func(field string, value any, message string) {
		spec := admissionBackupPointerSpec()
		target := spec[admissionTargetField].(map[string]any)
		storage := target[admissionStorageField].(map[string]any)
		switch field {
		case admissionSourceField:
			delete(spec, admissionSourceField)
		case admissionKeyField:
			target[admissionKeyField] = value
		case admissionEndpointField:
			delete(storage, admissionEndpointField)
		default:
			storage[field] = value
		}
		rejectAdmissionResource("BackupPointer", spec, message)
	},
		Entry("required source", admissionSourceField, nil, "spec.source: Required value"),
		Entry("bucket pattern", "bucket", "INVALID_BUCKET", "spec.target.objectStorage.bucket"),
		Entry("object key pattern", admissionKeyField, "invalid key.json", "spec.target.key"),
		Entry("prefix delimiter", "prefix", "/daily", "prefix must not start with /"),
		Entry("endpoint or Secret", admissionEndpointField, nil, "endpointURL or a credentials Secret carrying S3_ENDPOINT_URL is required"),
	)

	It("rejects source changes while accepting mutable publication settings", func() {
		obj := createAdmissionResource("BackupPointer", admissionBackupPointerSpec())
		setAdmissionField(obj, "example-other", admissionSpecField, admissionSourceField, admissionClusterField)
		Expect(k8sClient.Update(ctx, obj)).To(MatchError(ContainSubstring(admissionSourceImmutable)))
		setAdmissionField(obj, "example-source", admissionSpecField, admissionSourceField, admissionClusterField)
		setAdmissionField(obj, true, admissionSpecField, "suspend")
		Expect(k8sClient.Update(ctx, obj)).To(Succeed())
	})
})
