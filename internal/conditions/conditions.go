// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

// Package conditions stamps status conditions consistently across reconciles.
package conditions

import (
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Set records the generation and uses the supplied clock only for status transitions.
func Set(current *[]metav1.Condition, generation int64, now time.Time, condition metav1.Condition) {
	condition.ObservedGeneration = generation
	condition.LastTransitionTime = metav1.NewTime(now)
	meta.SetStatusCondition(current, condition)
}
