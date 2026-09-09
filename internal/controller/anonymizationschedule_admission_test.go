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

func admissionScheduleSpec() map[string]any {
	return map[string]any{admissionScheduleField: "0 0 * * *", admissionTemplateField: map[string]any{admissionSpecField: admissionRunSpec()}}
}

var _ = Describe("AnonymizationSchedule API admission", Label("admission"), func() {
	It("defaults concurrency, timezone and history and preserves explicit zero history", func() {
		obj := createAdmissionResource("AnonymizationSchedule", admissionScheduleSpec())
		Expect(admissionField(obj, admissionSpecField, "timeZone")).To(Equal("UTC"))
		Expect(admissionField(obj, admissionSpecField, "concurrencyPolicy")).To(Equal("Forbid"))
		Expect(admissionField(obj, admissionSpecField, "suspend")).To(BeFalse())
		Expect(admissionField(obj, admissionSpecField, admissionSuccessfulRunsHistoryLimitField)).To(Equal(int64(3)))
		Expect(admissionField(obj, admissionSpecField, admissionFailedRunsHistoryLimitField)).To(Equal(int64(1)))
		Expect(admissionField(obj, admissionSpecField, admissionTemplateField, admissionSpecField, admissionRunnerField, admissionWorkersField)).To(Equal(int64(4)))
		setAdmissionField(obj, int64(0), admissionSpecField, admissionSuccessfulRunsHistoryLimitField)
		setAdmissionField(obj, int64(0), admissionSpecField, admissionFailedRunsHistoryLimitField)
		Expect(k8sClient.Update(ctx, obj)).To(Succeed())
		Expect(admissionField(obj, admissionSpecField, admissionSuccessfulRunsHistoryLimitField)).To(Equal(int64(0)))
		Expect(admissionField(obj, admissionSpecField, admissionFailedRunsHistoryLimitField)).To(Equal(int64(0)))
	})

	DescribeTable("rejects invalid scheduling fields", func(field string, value any, message string) {
		spec := admissionScheduleSpec()
		spec[field] = value
		rejectAdmissionResource("AnonymizationSchedule", spec, message)
	},
		Entry("empty schedule", admissionScheduleField, "", "spec.schedule"),
		Entry("unknown concurrency", "concurrencyPolicy", "Parallel", "spec.concurrencyPolicy"),
		Entry("negative deadline", "startingDeadlineSeconds", int64(-1), "spec.startingDeadlineSeconds"),
		Entry("missing Run inputs", admissionTemplateField, map[string]any{admissionSpecField: map[string]any{}}, "Required value"),
	)
})
