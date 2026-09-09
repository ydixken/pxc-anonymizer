// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

//nolint:goconst // Literal upstream field names keep the unstructured wire contract directly reviewable.
package pxc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/objectstore"
	"github.com/ydixken/pxc-anonymizer/internal/pointer"
)

// Percona v1.20.0 pkg/naming/restore.go:7-16 adds a Job prefix and the cluster suffix.
const restoreJobNameOverhead = 13

// RestoreName reserves space for Percona's restore and prepare Job label values.
func RestoreName(candidate, clusterName string) (string, error) {
	if len(clusterName) > 22 || len(validation.IsDNS1123Subdomain(clusterName)) != 0 {
		return "", errors.New("restore clusterName must be a valid DNS subdomain of at most 22 bytes")
	}
	if len(validation.IsDNS1123Subdomain(candidate)) != 0 {
		return "", errors.New("restore name must be a valid DNS subdomain")
	}
	limit := 63 - restoreJobNameOverhead - len(clusterName)
	if len(candidate) <= limit {
		return candidate, nil
	}
	sum := sha256.Sum256([]byte(candidate))
	prefix := strings.TrimRight(candidate[:limit-17], "-.")
	return prefix + "-" + hex.EncodeToString(sum[:8]), nil
}

type RestoreRenderOptions struct {
	Namespace, Name, ClusterName, Destination string
	Credentials                               api.RestoreS3Credentials
	ContainerOptions                          *api.XtrabackupContainerOptions
	Owner                                     metav1.OwnerReference
}

// RenderRestore uses an inline source so Run and Bootstrap do not depend on target storage aliases.
func RenderRestore(options RestoreRenderOptions) (*unstructured.Unstructured, error) {
	if err := validateRenderIdentity(options.Namespace, options.Name, options.ClusterName); err != nil {
		return nil, err
	}
	if len(options.Name)+len(options.ClusterName)+restoreJobNameOverhead > 63 {
		return nil, errors.New("restore name exceeds the Percona Job label budget")
	}
	if options.Owner.APIVersion != api.GroupVersion.String() ||
		(options.Owner.Kind != "AnonymizationRun" && options.Owner.Kind != "Bootstrap") ||
		options.Owner.Name == "" || options.Owner.UID == "" ||
		options.Owner.Controller == nil || !*options.Owner.Controller ||
		options.Owner.BlockOwnerDeletion == nil || !*options.Owner.BlockOwnerDeletion {
		return nil, errors.New("restore requires a Run or Bootstrap controller owner reference")
	}
	if err := (&pointer.Document{Name: "restore", Destination: options.Destination, SchemaVersion: pointer.SchemaVersion}).Validate(); err != nil {
		return nil, errors.New("restore destination must identify an S3 backup prefix")
	}
	credentials := options.Credentials
	if len(validation.IsDNS1123Subdomain(credentials.CredentialsSecret)) != 0 {
		return nil, errors.New("restore credentialsSecret must name a same-namespace Secret")
	}
	if credentials.EndpointURL != "" {
		if _, err := objectstore.ParseEndpoint(credentials.EndpointURL); err != nil {
			return nil, errors.New("restore endpoint must be a valid HTTP or HTTPS endpoint")
		}
	}
	region := credentials.Region
	if region == "" {
		region = "auto"
	}
	source := map[string]any{
		"destination": options.Destination,
		"s3": map[string]any{
			"bucket":            strings.SplitN(strings.TrimPrefix(options.Destination, "s3://"), "/", 2)[0],
			"credentialsSecret": credentials.CredentialsSecret, "region": region,
		},
	}
	if credentials.EndpointURL != "" {
		source["s3"].(map[string]any)["endpointUrl"] = credentials.EndpointURL
	}
	if credentials.VerifyTLS != nil {
		source["verifyTLS"] = *credentials.VerifyTLS
	}
	result := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"pxcCluster": options.ClusterName, "backupSource": source,
			"containerOptions": renderContainerOptions(options.ContainerOptions, true),
		},
	}}
	result.SetGroupVersionKind(RestoreGVK)
	result.SetName(options.Name)
	result.SetNamespace(options.Namespace)
	result.SetOwnerReferences([]metav1.OwnerReference{options.Owner})
	return result, nil
}

func renderContainerOptions(options *api.XtrabackupContainerOptions, restoreDefaults bool) map[string]any {
	args := map[string]any{}
	if restoreDefaults {
		args["xbcloud"] = []any{"--parallel=256"}
	}
	if options != nil {
		for name, values := range map[string][]string{
			"xbcloud": options.Xbcloud, "xbstream": options.Xbstream, "xtrabackup": options.Xtrabackup,
		} {
			if values == nil {
				continue
			}
			copied := make([]any, len(values))
			for i, value := range values {
				copied[i] = value
			}
			args[name] = copied
		}
	}
	return map[string]any{"args": args}
}
