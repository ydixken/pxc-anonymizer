// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func examplePolicy() api.AnonymizationPolicySpec {
	return api.AnonymizationPolicySpec{
		Steps:     []api.SQLStep{{Name: "prepare", SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "sql-source"}, Key: "prepare.sql"}}},
		Databases: []api.DatabasePolicy{{Name: "demo", Pre: []api.StepRef{{Name: "prepare"}}, Tables: []api.TablePolicy{{Name: "people", Columns: []api.ColumnRule{{Name: "email", Strategy: api.StrategyEmail}}}}}},
	}
}

func TestSnapshotHashAndIsolation(t *testing.T) {
	spec := examplePolicy()
	data, hash, err := Snapshot(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "sha256:") || len(hash) != 71 {
		t.Fatal("invalid policy hash")
	}
	wantHash, err := Hash(spec)
	if err != nil || hash != wantHash {
		t.Fatal("controller and snapshot hashes disagree")
	}
	spec.Databases[0].Tables[0].Columns[0].Strategy = api.StrategyNull
	decoded, decodedHash, err := Decode(data)
	if err != nil || decodedHash != hash || decoded.Databases[0].Tables[0].Columns[0].Strategy != api.StrategyEmail {
		t.Fatal("snapshot changed after policy edit:", err)
	}
	if decoded.Steps[0].SecretKeyRef.Name != "sql-source" || bytes.Contains(data, []byte("SELECT")) {
		t.Fatal("snapshot did not preserve reference-only SQL")
	}
	first := []byte(`{"databases":[{"name":"demo","tables":[{"name":"people","columns":[{"name":"email","strategy":"email"}]}]}]}`)
	second := []byte(`{"databases":[{"tables":[{"columns":[{"strategy":"email","name":"email"}],"name":"people"}],"name":"demo"}]}`)
	_, firstHash, err := Decode(first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondHash, err := Decode(second)
	if err != nil || firstHash != secondHash {
		t.Fatal("JSON key order changed canonical hash")
	}
}

func TestSnapshotRejectsUnsafeContracts(t *testing.T) {
	for _, data := range []string{`null`, `{}`, `{"databases":[]} {}`, `{"databases":[],"typo":true}`} {
		if _, _, err := Decode([]byte(data)); err == nil {
			t.Fatalf("accepted invalid snapshot %s", data)
		}
	}
	if _, _, err := Decode(bytes.Repeat([]byte(" "), (1<<20)+1)); err == nil {
		t.Fatal("oversized ConfigMap policy accepted")
	}
	spec := examplePolicy()
	spec.Databases[0].Name = ""
	spec.Databases[0].NamePattern = "demo_[0-9]+"
	if err := Validate(spec); err != nil {
		t.Fatal("required namePattern must not require optional:", err)
	}
	spec.Databases[0].NamePattern = "["
	if err := Validate(spec); !errors.Is(err, ErrPatternInvalid) {
		t.Fatalf("malformed pattern error category: %v", err)
	}
	spec = examplePolicy()
	spec.Databases[0].Pre[0].Name = "missing"
	if err := Validate(spec); !errors.Is(err, ErrStepRefMissing) {
		t.Fatalf("undefined step error category: %v", err)
	}
	spec = examplePolicy()
	spec.Steps[0].ConfigMapKeyRef = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "duplicate"}, Key: "step.sql"}
	if _, _, err := Snapshot(spec); !errors.Is(err, ErrStepRefMissing) {
		t.Fatal("Snapshot did not validate conflicting SQL sources")
	}
	spec = examplePolicy()
	spec.Determinism.Mode = "Fixed"
	if err := Validate(spec); !errors.Is(err, ErrStepRefMissing) {
		t.Fatal("missing fixed seed not rejected")
	}
}
