// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

//nolint:goconst // Literal upstream field names keep the unstructured wire contract directly reviewable.
package pxc

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

const (
	LabelRun          = "pxc-anonymizer.io/run"
	LabelOutputGroup  = "pxc-anonymizer.io/output-group"
	LabelOutput       = "pxc-anonymizer.io/output"
	outputStorageName = "anonymized"
)

// RenderOutputBackup leaves the retained product outside the Run's garbage-collection tree.
func RenderOutputBackup(run *api.AnonymizationRun, clusterName, name, outputGroup string) (*unstructured.Unstructured, error) {
	if run == nil {
		return nil, errors.New("run is required")
	}
	if err := validateRenderIdentity(run.Namespace, name, clusterName); err != nil {
		return nil, err
	}
	if outputGroup == "" {
		outputGroup = run.Name
	}
	runLabel := LabelValue(run.Name)
	outputGroup = LabelValue(outputGroup)
	for _, value := range []string{runLabel, outputGroup} {
		if value == "" || len(validation.IsValidLabelValue(value)) != 0 {
			return nil, errors.New("run and output group must be valid nonempty label values")
		}
	}
	spec := map[string]any{"pxcCluster": clusterName, "storageName": outputStorageName}
	if run.Spec.Output.ContainerOptions != nil {
		spec["containerOptions"] = renderContainerOptions(run.Spec.Output.ContainerOptions, false)
	}
	backup := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": BackupGVK.GroupVersion().String(), "kind": BackupGVK.Kind,
		"metadata": map[string]any{
			"name": name, "namespace": run.Namespace,
			"finalizers": []any{FinalizerDeleteBackup},
			"labels":     map[string]any{LabelRun: runLabel, LabelOutputGroup: outputGroup, LabelOutput: "anonymized"},
		},
		"spec": spec,
	}}
	return backup, nil
}

func validateRenderIdentity(namespace, name, clusterName string) error {
	if len(validation.IsDNS1123Label(namespace)) != 0 {
		return errors.New("namespace must be a valid DNS label")
	}
	// PXC 1.20.0 reserves the remainder for pod and service suffixes.
	if len(clusterName) > 22 {
		return errors.New("PXC clusterName must not exceed 22 characters")
	}
	for field, value := range map[string]string{"name": name, "clusterName": clusterName} {
		if len(validation.IsDNS1123Subdomain(value)) != 0 {
			return fmt.Errorf("%s must be a valid DNS subdomain", field)
		}
	}
	return nil
}
