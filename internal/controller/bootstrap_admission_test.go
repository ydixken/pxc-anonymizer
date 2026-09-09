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

func admissionBootstrapSpec() map[string]any {
	return map[string]any{
		admissionPointerField: map[string]any{admissionDestinationField: admissionDestination},
		admissionRestoreField: map[string]any{admissionCredentialsField: admissionCredentials},
		admissionTargetsField: []any{map[string]any{admissionClusterField: "example-target"}},
	}
}

var _ = Describe("Bootstrap API admission", Label("admission"), func() {
	It("defaults bounded restore stages without enabling Crossplane or maxAge", func() {
		obj := createAdmissionResource("Bootstrap", admissionBootstrapSpec())
		Expect(admissionField(obj, admissionSpecField, "timeouts")).To(Equal(map[string]any{
			"clustersReady": "0s", admissionPointerField: "15m", admissionRestoreField: "3h", admissionCrossplaneField: "15m",
		}))
		Expect(admissionField(obj, admissionSpecField)).NotTo(HaveKey(admissionCrossplaneField))
		Expect(admissionField(obj, admissionSpecField, admissionPointerField)).NotTo(HaveKey(admissionMaxAgeField))
	})

	It("defaults explicitly enabled Crossplane and recreation conservatively", func() {
		spec := admissionBootstrapSpec()
		spec[admissionCrossplaneField] = map[string]any{}
		obj := createAdmissionResource("Bootstrap", spec)
		crossplane := admissionField(obj, admissionSpecField, admissionCrossplaneField).(map[string]any)
		Expect(crossplane).To(HaveKeyWithValue("group", "mysql.sql.crossplane.io"))
		Expect(crossplane).To(HaveKeyWithValue("version", "v1alpha1"))
		Expect(crossplane).To(HaveKeyWithValue("kinds", []any{admissionDatabasesField, admissionUsers, "grants"}))
		Expect(crossplane).To(HaveKeyWithValue("pause", true))
		Expect(crossplane).To(HaveKeyWithValue("resume", true))
		Expect(crossplane).To(HaveKeyWithValue("readinessTimeout", "15m"))
		Expect(crossplane).NotTo(HaveKey(admissionRecreateField))
		setAdmissionField(obj, map[string]any{}, admissionSpecField, admissionCrossplaneField, admissionRecreateField)
		Expect(k8sClient.Update(ctx, obj)).To(Succeed())
		Expect(admissionField(obj, admissionSpecField, admissionCrossplaneField, admissionRecreateField)).To(Equal(map[string]any{
			"kinds": []any{admissionUsers, "grants"}, "removeFinalizers": false, "waitForRecreation": true,
		}))
	})

	DescribeTable("accepts each remote pointer transport", func(pointer map[string]any) {
		spec := admissionBootstrapSpec()
		pointer[admissionMaxAgeField] = "24h"
		spec[admissionPointerField] = pointer
		obj := createAdmissionResource("Bootstrap", spec)
		Expect(admissionField(obj, admissionSpecField, admissionPointerField, admissionMaxAgeField)).To(Equal("24h"))
	},
		Entry("HTTPS", map[string]any{admissionHTTPField: map[string]any{admissionUrlField: admissionPointerURL}}),
		Entry("S3", map[string]any{"s3": map[string]any{admissionStorageField: admissionStorage(), admissionKeyField: admissionPointerKey}}),
	)

	DescribeTable("rejects invalid pointer selections", func(pointer map[string]any, message string) {
		spec := admissionBootstrapSpec()
		spec[admissionPointerField] = pointer
		rejectAdmissionResource("Bootstrap", spec, message)
	},
		Entry("no pointer", map[string]any{}, "set exactly one of http, s3 or destination"),
		Entry("two forms", map[string]any{admissionDestinationField: admissionDestination, admissionHTTPField: map[string]any{admissionUrlField: admissionPointerURL}}, "set exactly one of http, s3 or destination"),
		Entry("non-S3 destination", map[string]any{admissionDestinationField: "https://example.com/backup"}, "spec.pointer.destination"),
	)

	It("enforces target identity uniqueness and a nonempty target list", func() {
		spec := admissionBootstrapSpec()
		spec[admissionTargetsField] = []any{}
		rejectAdmissionResource("Bootstrap", spec, "spec.targets")
		spec[admissionTargetsField] = []any{map[string]any{admissionClusterField: "same-target"}, map[string]any{admissionClusterField: "same-target"}}
		rejectAdmissionResource("Bootstrap", spec, "Duplicate value")
	})

	It("keeps the entire pointer immutable but permits a new trigger", func() {
		obj := createAdmissionResource("Bootstrap", admissionBootstrapSpec())
		setAdmissionField(obj, "1h", admissionSpecField, admissionPointerField, admissionMaxAgeField)
		Expect(k8sClient.Update(ctx, obj)).To(MatchError(ContainSubstring("pointer is immutable")))
		delete(admissionField(obj, admissionSpecField, admissionPointerField).(map[string]any), admissionMaxAgeField)
		setAdmissionField(obj, "second-run", admissionSpecField, "trigger")
		Expect(k8sClient.Update(ctx, obj)).To(Succeed())
	})
})
