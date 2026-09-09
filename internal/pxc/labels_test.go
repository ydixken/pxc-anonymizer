// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

//nolint:goconst // Synthetic names exercise label boundaries independently of production constants.
package pxc

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestLabelValuePreservesShortNames(t *testing.T) {
	for _, name := range []string{"example", "example.cluster", strings.Repeat("a", 63)} {
		if got := LabelValue(name); got != name {
			t.Fatalf("short name changed: %q", got)
		}
	}
}

func TestLabelValueLongNameIdentity(t *testing.T) {
	name := strings.Repeat("a", 64)
	got := LabelValue(name)
	want := strings.Repeat("a", 46) + "-ffe054fe7ae0cb6d"
	if got != want {
		t.Fatalf("long label=%q, want %q", got, want)
	}
	if len(got) != 63 || len(validation.IsValidLabelValue(got)) != 0 {
		t.Fatalf("invalid generated label %q", got)
	}
	if LabelValue(name) != got || LabelValue(got) != got {
		t.Fatal("label mapping is not stable and idempotent")
	}
	if LabelValue(strings.Repeat("a", 63)+"b") == got {
		t.Fatal("names sharing their prefix lost distinct identities")
	}
	maximum := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if len(maximum) != 253 || len(validation.IsDNS1123Subdomain(maximum)) != 0 {
		t.Fatal("maximum fixture is not a valid resource name")
	}
	if value := LabelValue(maximum); len(value) != 63 || len(validation.IsValidLabelValue(value)) != 0 {
		t.Fatal("maximum resource name produced an invalid label")
	}
}

func TestRenderOutputBackupLongMetadataLabels(t *testing.T) {
	run := renderRun()
	run.Name = strings.Repeat("example-", 20) + "run"
	before := run.DeepCopy()
	for _, group := range []string{"", strings.Repeat("schedule-", 12) + "group"} {
		backup, err := RenderOutputBackup(run, renderClusterName, "retained-output", group)
		if err != nil {
			t.Fatal(err)
		}
		labels := backup.GetLabels()
		wantGroup := group
		if wantGroup == "" {
			wantGroup = run.Name
		}
		if labels[LabelRun] != LabelValue(run.Name) || labels[LabelOutputGroup] != LabelValue(wantGroup) {
			t.Fatal("resource-derived output labels did not use the shared mapping")
		}
		for _, value := range labels {
			if len(validation.IsValidLabelValue(value)) != 0 {
				t.Fatalf("invalid label value %q", value)
			}
		}
		if backup.GetName() != "retained-output" || len(backup.GetOwnerReferences()) != 0 || !reflect.DeepEqual(run, before) {
			t.Fatal("label mapping changed resource identity, retention ownership or input")
		}
	}
}
