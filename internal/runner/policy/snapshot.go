// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/runner/strategy"
)

var (
	ErrPatternInvalid = errors.New("invalid database pattern")
	ErrStepRefMissing = errors.New("missing policy reference")
)

// Hash excludes resolved Secret and SQL payloads so the snapshot retains references only.
func Hash(spec api.AnonymizationPolicySpec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("encode policy: %w", err)
	}
	return hash(data), nil
}

func Snapshot(spec api.AnonymizationPolicySpec) ([]byte, string, error) {
	if err := Validate(spec); err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, "", fmt.Errorf("encode policy: %w", err)
	}
	return data, hash(data), nil
}

func Decode(data []byte) (api.AnonymizationPolicySpec, string, error) {
	var spec api.AnonymizationPolicySpec
	if len(data) > 1<<20 {
		return spec, "", errors.New("policy exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, "", fmt.Errorf("decode policy: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return spec, "", errors.New("policy must contain one JSON document")
	}
	_, value, err := Snapshot(spec)
	return spec, value, err
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Validate is deliberately offline; controllers check namespace-local object and key existence.
func Validate(spec api.AnonymizationPolicySpec) error {
	if len(spec.Databases) == 0 {
		return errors.New("policy requires at least one database")
	}
	if spec.Determinism.Mode != "" && spec.Determinism.Mode != "PerRun" && spec.Determinism.Mode != "Fixed" {
		return errors.New("determinism mode must be PerRun or Fixed")
	}
	if spec.Determinism.Mode == "Fixed" && (spec.Determinism.SeedSecretRef == nil || spec.Determinism.SeedSecretRef.Name == "" || spec.Determinism.SeedSecretRef.Key == "") {
		return fmt.Errorf("%w: Fixed determinism requires a seed Secret key", ErrStepRefMissing)
	}
	defaults := api.PolicyDefaults{}
	if spec.Defaults != nil {
		defaults = *spec.Defaults
	}
	if defaults.PageSize < 0 || defaults.PageSize > 50000 {
		return errors.New("default pageSize must be between 1 and 50000 when set")
	}
	steps, err := validateSteps(spec.Steps)
	if err != nil {
		return err
	}
	for _, database := range spec.Databases {
		if err := validateDatabase(database, steps, defaults); err != nil {
			return err
		}
	}
	return nil
}

func validateSteps(definitions []api.SQLStep) (map[string]struct{}, error) {
	steps := make(map[string]struct{}, len(definitions))
	for _, step := range definitions {
		if step.Name == "" {
			return nil, fmt.Errorf("%w: SQL step name is empty", ErrStepRefMissing)
		}
		if _, exists := steps[step.Name]; exists {
			return nil, fmt.Errorf("duplicate SQL step %q", step.Name)
		}
		steps[step.Name] = struct{}{}
		if (step.ConfigMapKeyRef == nil) == (step.SecretKeyRef == nil) {
			return nil, fmt.Errorf("%w: step %q requires exactly one source", ErrStepRefMissing, step.Name)
		}
		if ref := step.ConfigMapKeyRef; ref != nil && (ref.Name == "" || ref.Key == "") {
			return nil, fmt.Errorf("%w: step %q requires a ConfigMap name and key", ErrStepRefMissing, step.Name)
		}
		if ref := step.SecretKeyRef; ref != nil && (ref.Name == "" || ref.Key == "") {
			return nil, fmt.Errorf("%w: step %q requires a Secret name and key", ErrStepRefMissing, step.Name)
		}
	}
	return steps, nil
}

func validateDatabase(database api.DatabasePolicy, steps map[string]struct{}, defaults api.PolicyDefaults) error {
	if (database.Name == "") == (database.NamePattern == "") {
		return errors.New("database requires exactly one name or namePattern")
	}
	if database.NamePattern != "" {
		if _, err := regexp.Compile("^(?:" + database.NamePattern + ")$"); err != nil {
			return fmt.Errorf("%w: cannot compile RE2 expression", ErrPatternInvalid)
		}
	}
	for _, refs := range [][]api.StepRef{database.Pre, database.Post} {
		for _, ref := range refs {
			if _, exists := steps[ref.Name]; !exists {
				return fmt.Errorf("%w: SQL step %q is not defined", ErrStepRefMissing, ref.Name)
			}
		}
	}
	tables := make(map[string]struct{}, len(database.Tables))
	for _, table := range database.Tables {
		if table.Name == "" {
			return errors.New("table name is required")
		}
		if _, exists := tables[table.Name]; exists {
			return fmt.Errorf("duplicate table %q", table.Name)
		}
		tables[table.Name] = struct{}{}
		if err := validateTable(table, defaults); err != nil {
			return err
		}
	}
	return nil
}

func validateTable(table api.TablePolicy, defaults api.PolicyDefaults) error {
	if table.PageSize != nil && (*table.PageSize < 1 || *table.PageSize > 50000) {
		return errors.New("table pageSize must be between 1 and 50000")
	}
	if table.Ignore != nil && (strings.TrimSpace(table.Ignore.Where) == "" || strings.Contains(table.Ignore.Where, ";") || len(table.Ignore.Where) > 4096) {
		return errors.New("ignore.where requires an expression without statement delimiters")
	}
	if table.Action == api.TableActionTruncate {
		if len(table.Columns) != 0 {
			return errors.New("truncate action takes no column rules")
		}
		return nil
	}
	if table.Action != "" && table.Action != api.TableActionAnonymize {
		return errors.New("table action must be Anonymize or Truncate")
	}
	if len(table.Columns) == 0 {
		return errors.New("anonymize action requires column rules")
	}
	columns := make(map[string]struct{}, len(table.Columns))
	for _, column := range table.Columns {
		if column.Name == "" {
			return errors.New("column name is required")
		}
		if _, exists := columns[column.Name]; exists {
			return fmt.Errorf("duplicate column %q", column.Name)
		}
		columns[column.Name] = struct{}{}
		if column.Params != nil && column.Params.ValueFrom != nil && (column.Params.ValueFrom.Name == "" || column.Params.ValueFrom.Key == "") {
			return fmt.Errorf("%w: constant requires a Secret name and key", ErrStepRefMissing)
		}
		if err := strategy.Validate(column, defaults); err != nil {
			return fmt.Errorf("column %q: %w", column.Name, err)
		}
	}
	return nil
}
