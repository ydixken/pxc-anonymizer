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

import corev1 "k8s.io/api/core/v1"

// Strategy rejects misspelled transformations before a Run starts.
// +kubebuilder:validation:Enum=firstName;lastName;fullName;jobTitle;email;phone;date;address;streetAddress;streetSuffix;city;postalCode;state;country;company;companySuffix;vatId;iban;ipv4Public;url;uuid;alphanumeric;digits;number;text;constant;hash;mask;null
type Strategy string

const (
	StrategyFirstName     Strategy = "firstName"
	StrategyLastName      Strategy = "lastName"
	StrategyFullName      Strategy = "fullName"
	StrategyJobTitle      Strategy = "jobTitle"
	StrategyEmail         Strategy = "email"
	StrategyPhone         Strategy = "phone"
	StrategyDate          Strategy = "date"
	StrategyAddress       Strategy = "address"
	StrategyStreetAddress Strategy = "streetAddress"
	StrategyStreetSuffix  Strategy = "streetSuffix"
	StrategyCity          Strategy = "city"
	StrategyPostalCode    Strategy = "postalCode"
	StrategyState         Strategy = "state"
	StrategyCountry       Strategy = "country"
	StrategyCompany       Strategy = "company"
	StrategyCompanySuffix Strategy = "companySuffix"
	StrategyVATID         Strategy = "vatId"
	StrategyIBAN          Strategy = "iban"
	StrategyIPv4Public    Strategy = "ipv4Public"
	StrategyURL           Strategy = "url"
	StrategyUUID          Strategy = "uuid"
	StrategyAlphanumeric  Strategy = "alphanumeric"
	StrategyDigits        Strategy = "digits"
	StrategyNumber        Strategy = "number"
	StrategyText          Strategy = "text"
	StrategyConstant      Strategy = "constant"
	StrategyHash          Strategy = "hash"
	StrategyMask          Strategy = "mask"
	StrategyNull          Strategy = "null"
)

// StrategyParams keeps optional values distinguishable from explicit zero or empty values.
type StrategyParams struct {
	// Locale falls back to the policy default.
	// +optional
	Locale string `json:"locale,omitempty"`
	// Domain falls back to the policy default, then the email generator.
	// +optional
	Domain string `json:"domain,omitempty"`
	// +optional
	// +kubebuilder:default=true
	ASCII *bool `json:"ascii,omitempty"`
	// Digits limits phone numbers to their supported maximum length.
	// +optional
	// +kubebuilder:default=15
	// +kubebuilder:validation:Maximum=15
	Digits *int32 `json:"digits,omitempty"`
	// +optional
	// +kubebuilder:default="1950-01-01"
	From string `json:"from,omitempty"`
	// The date generator evaluates an omitted upper bound as today at execution time.
	// +optional
	To string `json:"to,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=date;datetime
	Format string `json:"format,omitempty"`
	// +optional
	// +kubebuilder:default=DE
	Country string `json:"country,omitempty"`
	// Hash uses 64 when omitted; alphanumeric and digits require an explicit value.
	// +optional
	Length *int32 `json:"length,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=Mixed;Upper;Lower
	Case string `json:"case,omitempty"`
	// Unique requests collision retries for the digits strategy.
	// +optional
	Unique bool `json:"unique,omitempty"`
	// +optional
	Min *int64 `json:"min,omitempty"`
	// +optional
	Max *int64 `json:"max,omitempty"`
	// +optional
	MaxLength *int32 `json:"maxLength,omitempty"`
	// +optional
	Sentences *int32 `json:"sentences,omitempty"`
	// A pointer preserves an intentionally empty constant.
	// +optional
	Value *string `json:"value,omitempty"`
	// ValueFrom keeps sensitive constants in a Secret in the policy's namespace.
	// +optional
	ValueFrom *corev1.SecretKeySelector `json:"valueFrom,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=hex;base64
	Encoding string `json:"encoding,omitempty"`
	// +optional
	KeepPrefix *int32 `json:"keepPrefix,omitempty"`
	// +optional
	KeepSuffix *int32 `json:"keepSuffix,omitempty"`
	// +optional
	MaskChar string `json:"maskChar,omitempty"`
}
