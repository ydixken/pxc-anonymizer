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

func admissionPolicySpec(column map[string]any) map[string]any {
	return map[string]any{admissionDatabasesField: []any{map[string]any{
		admissionNameField: "example_database",
		"tables":           []any{map[string]any{admissionNameField: admissionUsers, admissionColumnsField: []any{column}}},
	}}}
}

func admissionPolicyTable(spec map[string]any) map[string]any {
	return spec[admissionDatabasesField].([]any)[0].(map[string]any)["tables"].([]any)[0].(map[string]any)
}

var _ = Describe("AnonymizationPolicy API admission", Label("admission"), func() {
	It("defaults determinism and policy settings while preserving omitted strategy overrides", func() {
		obj := createAdmissionResource("AnonymizationPolicy", admissionPolicySpec(map[string]any{admissionNameField: admissionEmail, admissionStrategyField: admissionEmail}))
		Expect(admissionField(obj, admissionSpecField, admissionDeterminismField, admissionModeField)).To(Equal("PerRun"))
		Expect(admissionField(obj, admissionSpecField, "defaults")).To(Equal(map[string]any{
			"pageSize": int64(5000), "onNull": "Keep", "onEmpty": "Keep", "locale": "en",
		}))
		spec := admissionField(obj, admissionSpecField).(map[string]any)
		table := admissionPolicyTable(spec)
		Expect(table).To(HaveKeyWithValue(admissionActionField, "Anonymize"))
		column := table[admissionColumnsField].([]any)[0].(map[string]any)
		Expect(column).NotTo(HaveKey(admissionConsistentField))
		Expect(column).NotTo(HaveKey(admissionParamsField))
	})

	It("accepts Fixed determinism with a Secret and an intentionally empty constant", func() {
		spec := admissionPolicySpec(map[string]any{admissionNameField: "environment", admissionStrategyField: admissionConstant, admissionParamsField: map[string]any{admissionValueField: ""}})
		spec[admissionDeterminismField] = map[string]any{admissionModeField: "Fixed", "seedSecretRef": map[string]any{admissionNameField: "example-seed", admissionKeyField: "seed"}}
		spec["steps"] = []any{map[string]any{admissionNameField: "prepare", "configMapKeyRef": map[string]any{admissionNameField: "example-sql", admissionKeyField: admissionSQLKey}}}
		createAdmissionResource("AnonymizationPolicy", spec)
	})

	DescribeTable("rejects invalid strategy configuration", func(column map[string]any, message string) {
		column[admissionNameField] = admissionValueField
		rejectAdmissionResource("AnonymizationPolicy", admissionPolicySpec(column), message)
	},
		Entry("unknown enum", map[string]any{admissionStrategyField: "random_letters"}, "Unsupported value"),
		Entry("missing alphanumeric length", map[string]any{admissionStrategyField: "alphanumeric"}, "alphanumeric and digits require params.length"),
		Entry("missing digits length", map[string]any{admissionStrategyField: "digits"}, "alphanumeric and digits require params.length"),
		Entry("missing constant", map[string]any{admissionStrategyField: admissionConstant}, "constant requires params.value or params.valueFrom"),
		Entry("consistent constant", map[string]any{admissionStrategyField: admissionConstant, admissionParamsField: map[string]any{admissionValueField: "example"}, admissionConsistentField: false}, "null, mask and constant do not accept consistent"),
		Entry("consistent mask", map[string]any{admissionStrategyField: "mask", admissionConsistentField: true}, "null, mask and constant do not accept consistent"),
		Entry("consistent null", map[string]any{admissionStrategyField: "null", admissionConsistentField: false}, "null, mask and constant do not accept consistent"),
		Entry("oversized length", map[string]any{admissionStrategyField: "digits", admissionParamsField: map[string]any{"length": int64(4097)}}, "params.length must not exceed 4096"),
	)

	It("requires a seed for Fixed determinism and exactly one SQL reference", func() {
		spec := admissionPolicySpec(map[string]any{admissionNameField: admissionEmail, admissionStrategyField: admissionEmail})
		spec[admissionDeterminismField] = map[string]any{admissionModeField: "Fixed"}
		rejectAdmissionResource("AnonymizationPolicy", spec, "Fixed determinism requires seedSecretRef")
		delete(spec, admissionDeterminismField)
		step := map[string]any{admissionNameField: "prepare"}
		spec["steps"] = []any{step}
		rejectAdmissionResource("AnonymizationPolicy", spec, "set exactly one of configMapKeyRef or secretKeyRef")
		step["configMapKeyRef"] = map[string]any{admissionNameField: "example-sql", admissionKeyField: admissionSQLKey}
		step["secretKeyRef"] = map[string]any{admissionNameField: "example-private-sql", admissionKeyField: admissionSQLKey}
		rejectAdmissionResource("AnonymizationPolicy", spec, "set exactly one of configMapKeyRef or secretKeyRef")
	})

	It("requires exactly one database selector and accepts a pattern-only entry", func() {
		spec := admissionPolicySpec(map[string]any{admissionNameField: admissionEmail, admissionStrategyField: admissionEmail})
		database := spec[admissionDatabasesField].([]any)[0].(map[string]any)
		database["namePattern"] = "^example_.*$"
		rejectAdmissionResource("AnonymizationPolicy", spec, "set exactly one of name or namePattern")
		delete(database, admissionNameField)
		createAdmissionResource("AnonymizationPolicy", spec)
		delete(database, "namePattern")
		rejectAdmissionResource("AnonymizationPolicy", spec, "set exactly one of name or namePattern")
	})

	It("rejects statement delimiters and invalid action/column combinations", func() {
		spec := admissionPolicySpec(map[string]any{admissionNameField: admissionEmail, admissionStrategyField: admissionEmail})
		table := admissionPolicyTable(spec)
		table["ignore"] = map[string]any{"where": "id = 1; DELETE FROM users"}
		rejectAdmissionResource("AnonymizationPolicy", spec, "ignore.where")
		delete(table, "ignore")
		table[admissionActionField] = "Truncate"
		rejectAdmissionResource("AnonymizationPolicy", spec, "Truncate takes no columns")
		delete(table, admissionColumnsField)
		createAdmissionResource("AnonymizationPolicy", spec)
		table[admissionActionField] = "Anonymize"
		rejectAdmissionResource("AnonymizationPolicy", spec, "Anonymize needs at least one column")
	})
})
